import {
  captureMetadata,
  filterEvent,
  policyAvailable,
} from "../capture/policy";
import type {
  CaptureError,
  CaptureStatus,
  CaptureEvent,
  CapturePolicy,
  CaptureSpool,
  CaptureUploadGateway,
  FilteredEvent,
  FlushReport,
  Receipt,
  Result,
} from "../capture/types";
const failure = (code: CaptureError["code"]): Result<never, CaptureError> => ({
  ok: false,
  error: { code },
});
/** Services own policy decisions; leaf ports own all disk and network effects. */
export class CaptureService {
  private paused = false;
  private controller: AbortController | undefined;
  private flushing = false;
  constructor(
    private readonly spool: CaptureSpool,
    private readonly uploader: CaptureUploadGateway,
    private readonly knownSecrets: readonly string[] = [],
  ) {}
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
        // Preserve unauthorized/forbidden (incl. revoked) and credential states;
        // only an absent, malformed, or stale policy is policy_unavailable.
        return [
          "unauthorized",
          "forbidden",
          "credential_unavailable",
          "cancelled",
        ].includes(result.error.code)
          ? result
          : failure("policy_unavailable");
      return policyAvailable(result.value)
        ? result
        : failure("policy_unavailable");
    } catch {
      return failure("policy_unavailable");
    }
  }
  async enqueue(event: CaptureEvent): Promise<Result<Receipt, CaptureError>> {
    try {
      if (!captureMetadata(event, this.knownSecrets)) {
        const gap = await this.spool.recordGap(event.sequence, "invalid_input");
        return gap.ok
          ? { ok: true, value: { id: "withheld", state: "withheld" } }
          : gap;
      }
      const batch = await this.spool.readBatch();
      if (!batch.ok) return batch;
      if (this.paused || batch.value.paused) {
        const gap = await this.spool.recordGap(event.sequence, "paused");
        return gap.ok
          ? { ok: true, value: { id: event.id, state: "withheld" } }
          : gap;
      }
      const p = await this.current(event.scope?.projectID);
      const outcome = filterEvent(
        event,
        p.ok ? p.value : undefined,
        this.knownSecrets,
      );
      if (outcome.kind !== "accepted") {
        const gap = await this.spool.recordGap(
          outcome.gap.sequence,
          outcome.gap.reason,
        );
        return gap.ok
          ? { ok: true, value: { id: event.id, state: outcome.kind } }
          : gap;
      }
      // A concurrent pause is checked again by the repository under its lock.
      return await this.spool.appendFiltered(
        outcome.event,
        p.ok ? p.value.spoolLimitBytes : 0,
      );
    } catch {
      return failure("invalid_input");
    }
  }
  /** Local, credential-free view of durable spool state. */
  async status(): Promise<Result<CaptureStatus, CaptureError>> {
    const batch = await this.spool.readBatch();
    if (!batch.ok) return batch;
    return {
      ok: true,
      value: {
        paused: this.paused || batch.value.paused,
        queued: batch.value.queued,
        gaps: batch.value.gaps,
        totalLosses: batch.value.totalLosses,
      },
    };
  }
  async pause(): Promise<Result<void, CaptureError>> {
    this.paused = true;
    this.controller?.abort();
    const saved = await this.spool.setPaused(true);
    if (!saved.ok) return saved;
    // A separate process cannot cancel an already leased request. Persist pause
    // first and report busy until its dispatch lease drains; never claim completion.
    for (let attempt = 0; attempt < 10; attempt++) {
      const drained = await this.spool.withDispatch(async () => ({
        ok: true,
        value: undefined,
      }));
      if (drained.ok || !this.flushing || drained.error.code !== "busy")
        return drained;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    return failure("busy");
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
    const batch = await this.spool.readBatch();
    if (!batch.ok) return batch;
    if (this.paused || batch.value.paused) return failure("paused");
    const p = await this.current(event.scope.projectID, signal);
    if (!p.ok) return p;
    // The policy read may be slow; another process may have paused meanwhile.
    // Re-read the durable marker after it and before any outbound payload.
    const after = await this.spool.readBatch();
    if (!after.ok) return after;
    if (this.paused || after.value.paused) return failure("paused");
    if (after.value.events[0]?.id !== event.id) return failure("stale");
    const filtered = filterEvent(event, p.value, this.knownSecrets);
    // A queued item is immutable. Changed policy/filter results purge its bytes;
    // metadata loss is explicit, rather than inventing a new version on replay.
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
          if (!checked.ok) {
            if (checked.error.code === "stale" && (await this.removed(e.id)))
              continue;
            return checked;
          }
          const send = await this.uploader.send(e, combined, () =>
            this.guard(e, combined),
          );
          if (!send.ok) {
            // Only a guard-purged item lets the loop advance. A server conflict
            // (HTTP 409) on an unchanged queued item stops with it retained.
            if (send.error.code === "stale" && (await this.removed(e.id)))
              continue;
            return send;
          }
          const ack = await this.spool.acknowledge(e.id);
          if (!ack.ok) return ack;
          sent++;
        }
        const batch = await this.spool.readBatch();
        return batch.ok
          ? {
              ok: true,
              value: {
                sent,
                gaps: batch.value.gaps,
                totalLosses: batch.value.totalLosses,
              },
            }
          : batch;
      });
    } catch {
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
    private readonly credentials: import("../capture/types").CredentialStore,
    private readonly spool: CaptureSpool,
    private readonly gateway: (bearer: string) => CaptureUploadGateway,
  ) {}
  async connect(
    profile: string,
  ): Promise<Result<CaptureService, CaptureError>> {
    const token = await this.credentials.get(profile);
    if (!token.ok) return token;
    try {
      return {
        ok: true,
        value: new CaptureService(this.spool, this.gateway(token.value), [
          token.value,
        ]),
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
