import {
  captureMetadata,
  filterEvent,
  isFilteredEvent,
  policyAvailable,
} from "../capture/policy";
import {
  MAX_HEAD_FAILURES,
  POLICY_CACHE_MAX_AGE_MS,
  POLICY_FRESHNESS_MS,
  type CachedPolicy,
  type CaptureError,
  type CaptureEvent,
  type CaptureOptions,
  type CapturePolicy,
  type CaptureSpool,
  type CaptureStatus,
  type CaptureUploadGateway,
  type CredentialStore,
  type FilteredEvent,
  type FlushReport,
  type GapReason,
  type PauseState,
  type Receipt,
  type Result,
} from "../capture/types";
const failure = (code: CaptureError["code"]): Result<never, CaptureError> => ({
  ok: false,
  error: { code },
});
/** Statuses the server or credential store reported about the caller itself;
 * these are surfaced as-is and never replaced by a cached policy. */
const denied: readonly CaptureError["code"][] = [
  "unauthorized",
  "forbidden",
  "credential_unavailable",
];
/** Item-level failures the server reported for this event; counted per queued
 * item and quarantined after MAX_HEAD_FAILURES. Generic unavailability and
 * transport failures are never counted, so an outage never discards data. */
const itemFailures: readonly CaptureError["code"][] = [
  "stale",
  "incomplete",
  "forbidden",
];
/** Permanent server refusals: retrying cannot succeed. */
const rejected: readonly CaptureError["code"][] = [
  "conflict",
  "deleted",
  "invalid_input",
];
/** Services own policy decisions; leaf ports own all disk and network effects. */
export class CaptureService {
  private paused = false;
  private controller: AbortController | undefined;
  private flushing = false;
  private readonly now: () => number;
  constructor(
    private readonly spool: CaptureSpool,
    private readonly uploader: CaptureUploadGateway,
    private readonly knownSecrets: readonly string[] = [],
    private readonly options: CaptureOptions = {},
  ) {
    this.now = options.now ?? Date.now;
  }
  /** Local exclusions always narrow whichever server policy is in force. */
  private local(p: CapturePolicy): CapturePolicy {
    return {
      ...p,
      localExcludedPaths: this.options.localExcludedPaths,
      localExcludedFields: this.options.localExcludedFields,
    };
  }
  /** A fresh server read; persisted as the last known policy on success. */
  private async current(
    project: string,
    signal?: AbortSignal,
  ): Promise<Result<CapturePolicy, CaptureError>> {
    try {
      const result = await this.uploader.policy(
        project,
        signal ?? AbortSignal.timeout(2000),
      );
      if (!result.ok)
        return [...denied, "cancelled"].includes(result.error.code)
          ? result
          : failure("policy_unavailable");
      if (!policyAvailable(result.value, this.now()))
        return failure("policy_unavailable");
      const p = result.value;
      // Opportunistic: a cache write failure never blocks capture or sending.
      await this.spool.cachePolicy(project, {
        revision: p.revision,
        paused: p.paused,
        excludedPaths: [...p.excludedPaths],
        excludedFields: [...p.excludedFields],
        allowedModelRoutes: [...p.allowedModelRoutes],
        spoolLimitBytes: p.spoolLimitBytes,
        fetchedAt: this.now(),
      });
      return { ok: true, value: this.local(p) };
    } catch {
      return failure("policy_unavailable");
    }
  }
  private cached(c: CachedPolicy, maxAge: number): CapturePolicy | undefined {
    const age = this.now() - c.fetchedAt;
    if (age < 0 || age > maxAge) return undefined;
    return this.local({ ...c, expiresAt: c.fetchedAt + maxAge + 1 });
  }
  /** Enqueue-time policy: a fresh cached policy, else one short refresh, else
   * the persisted last server policy up to POLICY_CACHE_MAX_AGE_MS old. A
   * denied refresh or a missing/too-old cache withholds (never permissive). */
  private async enqueuePolicy(
    project: string,
  ): Promise<Result<CapturePolicy, CaptureError>> {
    const stored = await this.spool.cachedPolicy(project);
    const c = stored.ok ? stored.value : undefined;
    const fresh = c && this.cached(c, POLICY_FRESHNESS_MS);
    if (fresh) return { ok: true, value: fresh };
    const read = await this.current(project, AbortSignal.timeout(1000));
    if (read.ok || denied.includes(read.error.code)) return read;
    const fallback = c && this.cached(c, POLICY_CACHE_MAX_AGE_MS);
    return fallback ? { ok: true, value: fallback } : read;
  }
  private async withheld(
    event: CaptureEvent,
    reason: GapReason,
    state: Receipt["state"],
    id = event.id,
  ): Promise<Result<Receipt, CaptureError>> {
    const gap = await this.spool.recordGap(event.sequence, reason);
    return gap.ok ? { ok: true, value: { id, state } } : gap;
  }
  async enqueue(event: CaptureEvent): Promise<Result<Receipt, CaptureError>> {
    try {
      if (!captureMetadata(event, this.knownSecrets))
        return await this.withheld(
          event,
          "invalid_input",
          "withheld",
          "withheld",
        );
      const status = await this.spool.status();
      if (!status.ok) return status;
      if (this.paused || status.value.paused)
        return await this.withheld(event, "paused", "withheld");
      const p = await this.enqueuePolicy(event.scope.projectID);
      const outcome = filterEvent(
        event,
        p.ok ? p.value : undefined,
        this.knownSecrets,
        this.now(),
      );
      if (outcome.kind !== "accepted")
        return await this.withheld(event, outcome.gap.reason, outcome.kind);
      // Never persist anything readBatch would later reject.
      if (!isFilteredEvent(outcome.event))
        return await this.withheld(event, "invalid_input", "withheld");
      // A concurrent pause is checked again by the repository under its lock.
      return await this.spool.appendFiltered(
        outcome.event,
        p.ok ? p.value.spoolLimitBytes : 0,
      );
    } catch {
      // Unexpected exceptions still leave an explicit metadata-only loss.
      return await this.withheld(event, "invalid_input", "withheld").catch(() =>
        failure("invalid_input"),
      );
    }
  }
  /** Local, credential-free, metadata-only view of durable spool state. */
  async status(): Promise<Result<CaptureStatus, CaptureError>> {
    const s = await this.spool.status();
    if (!s.ok) return s;
    return {
      ok: true,
      value: { ...s.value, paused: this.paused || s.value.paused },
    };
  }
  /** Ok means the pause marker is durably saved. "paused_draining" means a
   * dispatch lease (possibly another process) still has a request in flight;
   * no further request starts after it. Any error means NOT saved. */
  async pause(): Promise<Result<PauseState, CaptureError>> {
    this.paused = true;
    this.controller?.abort();
    const saved = await this.spool.setPaused(true);
    if (!saved.ok) return saved;
    for (let attempt = 0; attempt < 10; attempt++) {
      const drained = await this.spool.withDispatch(async () => ({
        ok: true,
        value: undefined,
      }));
      if (drained.ok) return { ok: true, value: "paused" };
      if (drained.error.code !== "busy") return drained;
      if (!this.flushing) break;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    return { ok: true, value: "paused_draining" };
  }
  async resume(): Promise<Result<void, CaptureError>> {
    const result = await this.spool.setPaused(false);
    if (result.ok) this.paused = false;
    return result;
  }
  private async guard(
    event: FilteredEvent,
    signal: AbortSignal,
  ): Promise<Result<void, CaptureError>> {
    if (signal.aborted) return failure("cancelled");
    const before = await this.spool.status();
    if (!before.ok) return before;
    if (this.paused || before.value.paused) return failure("paused");
    const p = await this.current(event.scope.projectID, signal);
    if (!p.ok) return p;
    // The policy read may be slow; another process may have paused meanwhile.
    // Re-read the durable marker after it and before any outbound payload.
    const after = await this.spool.readBatch();
    if (!after.ok) return after;
    if (this.paused || after.value.paused) return failure("paused");
    if (after.value.events[0]?.id !== event.id) return failure("stale");
    const filtered = filterEvent(event, p.value, this.knownSecrets, this.now());
    // A queued item is immutable. Any policy revision change or changed filter
    // result purges its bytes (conservative: a revision change purges every
    // queued item), with an explicit metadata-only loss.
    if (
      filtered.kind !== "accepted" ||
      event.policyRevision !== p.value.revision ||
      JSON.stringify(filtered.event) !== JSON.stringify(event)
    ) {
      if (
        filtered.kind === "withheld" &&
        filtered.gap.reason === "policy_unavailable"
      )
        return failure("policy_unavailable");
      if (filtered.kind === "withheld" && filtered.gap.reason === "paused")
        return failure("paused");
      const dropped = await this.spool.discard(event.id, "policy_changed");
      return dropped.ok ? failure("stale") : dropped;
    }
    if (signal.aborted || this.paused) return failure("cancelled");
    return { ok: true, value: undefined };
  }
  private async removed(id: string): Promise<boolean> {
    const batch = await this.spool.readBatch();
    return batch.ok && batch.value.events[0]?.id !== id;
  }
  /** Decide whether a failed head lets flush continue, or stops it. */
  private async headFailed(
    e: FilteredEvent,
    error: CaptureError,
  ): Promise<Result<"continue", CaptureError>> {
    if (error.code === "stale" && (await this.removed(e.id)))
      return { ok: true, value: "continue" };
    let reason: GapReason | undefined;
    if (rejected.includes(error.code)) reason = "rejected";
    else if (itemFailures.includes(error.code)) {
      const failures = await this.spool.recordFailure(e.id);
      if (!failures.ok) return failures;
      if (failures.value >= MAX_HEAD_FAILURES) reason = "undeliverable";
    }
    if (!reason) return { ok: false, error };
    const dropped = await this.spool.discard(e.id, reason);
    return dropped.ok ? { ok: true, value: "continue" } : dropped;
  }
  async flush(signal: AbortSignal): Promise<Result<FlushReport, CaptureError>> {
    if (this.flushing) return failure("busy");
    this.flushing = true;
    this.controller = new AbortController();
    const combined = AbortSignal.any([signal, this.controller.signal]);
    let sent = 0;
    try {
      return await this.spool.withDispatch(async () => {
        // Bound one invocation even when another producer continuously adds events.
        for (let examined = 0; examined < 4096; examined++) {
          const batch = await this.spool.readBatch();
          if (!batch.ok) return batch;
          const report = {
            sent,
            gaps: batch.value.gaps,
            totalLosses: batch.value.totalLosses,
          };
          if (this.paused || batch.value.paused || !batch.value.events.length)
            return { ok: true, value: report };
          if (combined.aborted) return failure("cancelled");
          const e = batch.value.events[0];
          if (!e) return { ok: true, value: report };
          const checked = await this.guard(e, combined);
          const send = checked.ok
            ? await this.uploader.send(e, combined, () =>
                this.guard(e, combined),
              )
            : checked;
          if (!send.ok) {
            const next = await this.headFailed(e, send.error);
            if (next.ok) continue;
            return next;
          }
          const ack = await this.spool.acknowledge(e.id);
          if (!ack.ok) return ack;
          sent++;
        }
        const s = await this.spool.status();
        return s.ok
          ? {
              ok: true,
              value: {
                sent,
                gaps: s.value.gaps,
                totalLosses: s.value.totalLosses,
              },
            }
          : s;
      });
    } catch {
      // Storage and gateway failures arrive as Results; only a programming
      // error or a throwing third-party gateway reaches this point.
      return failure("unavailable");
    } finally {
      this.flushing = false;
      this.controller = undefined;
    }
  }
}

/** Connection orchestration keeps OS credential reads below the command layer. */
export class CaptureConnectionService {
  constructor(
    private readonly credentials: CredentialStore,
    private readonly spool: CaptureSpool,
    private readonly gateway: (bearer: string) => CaptureUploadGateway,
    private readonly options: CaptureOptions = {},
  ) {}
  async connect(
    profile: string,
  ): Promise<Result<CaptureService, CaptureError>> {
    const token = await this.credentials.get(profile);
    if (!token.ok) return token;
    try {
      return {
        ok: true,
        value: new CaptureService(
          this.spool,
          this.gateway(token.value),
          [token.value],
          this.options,
        ),
      };
    } catch {
      return failure("invalid_input");
    }
  }
  setCredential(
    profile: string,
    bearer: string,
  ): Promise<Result<void, CaptureError>> {
    return this.credentials.set(profile, bearer);
  }
}
