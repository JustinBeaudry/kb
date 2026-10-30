import { createHash } from "node:crypto";
import { isFilteredEvent } from "./policy";
import {
  SecureSpoolDirectory,
  BoundedRead,
  UnsafeStorage,
  type SpoolIO,
} from "../repositories/capture-spool.repository";
import {
  DEFAULT_SPOOL_BYTES,
  MAX_EVENT_BYTES,
  MAX_SPOOL_BYTES,
  POLICY_FRESHNESS_MS,
  type CachedPolicy,
  type CaptureError,
  type CaptureSpool,
  type CaptureStatus,
  type FilteredEvent,
  type Gap,
  type GapReason,
  type Receipt,
  type Result,
  type SpoolBatch,
} from "./types";
type Entry = {
  file: string;
  bytes: number;
  sequence: number;
  /** Item-level delivery failures recorded for this queued event. */
  failures?: number;
  /** Collector time (ms) of the last counted failure. */
  struckAt?: number;
};
const entryKeys = ["file", "bytes", "sequence", "failures", "struckAt"];
type Pending = Entry & { operation: "append" | "remove"; reason?: GapReason };
type State = {
  version: 1;
  paused: boolean;
  entries: Entry[];
  gaps: Gap[];
  totalLosses: number;
  pending?: Pending;
};
const hash = (id: string) =>
  createHash("sha256").update(id).digest("hex") + ".json";
const validLimit = (n: number) =>
  Number.isSafeInteger(n) && n >= 0 && n <= MAX_SPOOL_BYTES;
const empty = (): State => ({
  version: 1,
  paused: false,
  entries: [],
  gaps: [],
  totalLosses: 0,
});
function loss(s: State, sequence: number, reason: GapReason) {
  s.gaps.push({
    sequence: Number.isSafeInteger(sequence) && sequence >= 0 ? sequence : 0,
    reason,
  });
  s.gaps = s.gaps.slice(-128);
  s.totalLosses = Math.min(Number.MAX_SAFE_INTEGER, s.totalLosses + 1);
}
const reasons: readonly GapReason[] = [
  "quota",
  "paused",
  "excluded",
  "policy_changed",
  "policy_unavailable",
  "invalid_input",
  "rejected",
  "undeliverable",
  "interrupted",
  "corrupt",
];
const record = (x: unknown): x is Record<string, unknown> =>
  !!x && typeof x === "object" && !Array.isArray(x);
const only = (x: Record<string, unknown>, keys: readonly string[]) =>
  Object.keys(x).every((k) => keys.includes(k));
const count = (x: unknown) => Number.isSafeInteger(x) && Number(x) >= 0;
function entry(x: unknown): boolean {
  return (
    record(x) &&
    typeof x.file === "string" &&
    /^[a-f0-9]{64}\.json$/.test(x.file) &&
    count(x.bytes) &&
    Number(x.bytes) <= MAX_EVENT_BYTES &&
    count(x.sequence) &&
    (x.failures === undefined || count(x.failures)) &&
    (x.struckAt === undefined || count(x.struckAt))
  );
}
/** The journal is our own durable format: every field and key is validated
 * before use, and anything unexpected fails closed as storage_unavailable. */
function state(raw: string | null): State {
  if (raw === null) return empty();
  const x: unknown = JSON.parse(raw);
  if (
    !record(x) ||
    !only(x, [
      "version",
      "paused",
      "entries",
      "gaps",
      "totalLosses",
      "pending",
    ]) ||
    x.version !== 1 ||
    typeof x.paused !== "boolean" ||
    !Array.isArray(x.entries) ||
    x.entries.length > 4096 ||
    !x.entries.every((e) => record(e) && entry(e) && only(e, entryKeys)) ||
    new Set(x.entries.map((e) => (record(e) ? e.file : undefined))).size !==
      x.entries.length ||
    !Array.isArray(x.gaps) ||
    x.gaps.length > 128 ||
    !x.gaps.every(
      (g) =>
        record(g) &&
        only(g, ["sequence", "reason"]) &&
        count(g.sequence) &&
        reasons.includes(g.reason as GapReason),
    ) ||
    !count(x.totalLosses)
  )
    throw Error("state");
  const p = x.pending;
  if (
    p !== undefined &&
    !(
      record(p) &&
      entry(p) &&
      only(p, [...entryKeys, "operation", "reason"]) &&
      (p.operation === "append" || p.operation === "remove") &&
      (p.reason === undefined || reasons.includes(p.reason as GapReason))
    )
  )
    throw Error("pending");
  // SAFETY: every key and value of the persisted state was validated above.
  return x as State;
}
const POLICY_CACHE_BYTES = 4 * 1024 * 1024;
/** Unchanged policy is re-persisted at most once per freshness window: often
 * enough that enqueue reads the server at most once per POLICY_FRESHNESS_MS,
 * and never on every guard read (bounds fsyncs to one per minute per project). */
const POLICY_CACHE_REWRITE_MS = POLICY_FRESHNESS_MS;
const sameServerPolicy = (a: CachedPolicy, b: CachedPolicy) =>
  JSON.stringify({ ...a, fetchedAt: 0 }) ===
  JSON.stringify({ ...b, fetchedAt: 0 });
const POLICY_CACHE_PROJECTS = 16;
const strings = (x: unknown): x is string[] =>
  Array.isArray(x) &&
  x.length <= 256 &&
  x.every((v) => typeof v === "string" && v.length <= 4096);
function cachedPolicy(x: unknown): x is CachedPolicy {
  return (
    record(x) &&
    only(x, [
      "revision",
      "paused",
      "excludedPaths",
      "excludedFields",
      "allowedModelRoutes",
      "spoolLimitBytes",
      "fetchedAt",
    ]) &&
    typeof x.revision === "string" &&
    x.revision.length > 0 &&
    x.revision.length <= 256 &&
    typeof x.paused === "boolean" &&
    strings(x.excludedPaths) &&
    strings(x.excludedFields) &&
    strings(x.allowedModelRoutes) &&
    count(x.spoolLimitBytes) &&
    count(x.fetchedAt)
  );
}
/** Corrupt or unparseable cache content is ignored (treated as never fetched);
 * an unsafe cache file still fails closed through UnsafeStorage. */
async function policies(io: SpoolIO): Promise<Record<string, CachedPolicy>> {
  try {
    const raw = await io.read("policy", POLICY_CACHE_BYTES);
    if (raw === null) return {};
    const x: unknown = JSON.parse(raw);
    if (!record(x)) return {};
    const out: Record<string, CachedPolicy> = Object.create(null);
    for (const [k, v] of Object.entries(x)) if (cachedPolicy(v)) out[k] = v;
    return out;
  } catch (e) {
    if (e instanceof UnsafeStorage) throw e;
    return {};
  }
}
export class DiskSpool implements CaptureSpool {
  private readonly directory: SecureSpoolDirectory;
  constructor(
    readonly path: string,
    private readonly limit = DEFAULT_SPOOL_BYTES,
    lockWaitMs?: number,
  ) {
    if (!validLimit(limit)) throw Error("spool limit");
    this.directory = new SecureSpoolDirectory(path, lockWaitMs);
  }
  async withDispatch<T>(
    operation: () => Promise<Result<T, CaptureError>>,
  ): Promise<Result<T, CaptureError>> {
    const result = await this.directory.within(operation, "dispatch");
    return result.ok ? result.value : result;
  }
  private async transaction<T>(
    fn: (s: State, io: SpoolIO) => Promise<T>,
    deadlineAt?: number,
  ): Promise<Result<T, CaptureError>> {
    return this.directory.within(
      async (io) => {
        const s = state(await io.read("state", 2 * 1024 * 1024));
        if (s.pending) {
          const pending = s.pending;
          await io.removeIfPresent(pending.file);
          if (pending.operation === "remove") {
            s.entries = s.entries.filter((e) => e.file !== pending.file);
            if (pending.reason) loss(s, pending.sequence, pending.reason);
          } else {
            await io.removeIfPresent("event-next");
            loss(s, pending.sequence, "interrupted");
          }
          delete s.pending;
          await io.write("state", JSON.stringify(s));
        }
        return fn(s, io);
      },
      "lock",
      deadlineAt,
    );
  }
  async appendFiltered(
    event: FilteredEvent,
    limitBytes = this.limit,
    deadlineAt?: number,
  ): Promise<Result<Receipt, CaptureError>> {
    const result = await this.transaction(
      async (s, io): Promise<Result<Receipt, CaptureError>> => {
        const file = hash(event.id);
        const data = JSON.stringify(event),
          size = Buffer.byteLength(data);
        if (s.entries.some((e) => e.file === file)) {
          // Stable IDs are only idempotent for identical filtered content and scope.
          const existing = await io.read(file, MAX_EVENT_BYTES);
          return existing === data
            ? { ok: true, value: { id: event.id, state: "duplicate" } }
            : { ok: false, error: { code: "conflict" } };
        }
        const reason = s.paused
          ? "paused"
          : size > MAX_EVENT_BYTES ||
              !validLimit(limitBytes) ||
              s.entries.length >= 4096 ||
              s.entries.reduce((n, e) => n + e.bytes, 0) + size >
                Math.min(this.limit, limitBytes)
            ? "quota"
            : undefined;
        if (reason) {
          loss(s, event.sequence, reason);
          await io.write("state", JSON.stringify(s));
          return { ok: true, value: { id: event.id, state: "dropped" } };
        }
        const entry = { file, bytes: size, sequence: event.sequence };
        s.pending = { ...entry, operation: "append" };
        await io.write("state", JSON.stringify(s));
        await io.write(file, data, "event-next");
        s.entries.push(entry);
        delete s.pending;
        await io.write("state", JSON.stringify(s));
        return { ok: true, value: { id: event.id, state: "queued" } };
      },
      deadlineAt,
    );
    return result.ok ? result.value : result;
  }
  async readBatch(): Promise<Result<SpoolBatch, CaptureError>> {
    return this.transaction(async (s, io) => {
      const events: FilteredEvent[] = [];
      // A head that cannot be read back intact is quarantined with a metadata-
      // only gap so one bad entry can never wedge enqueue, status, or flush.
      while (s.entries.length && !events.length) {
        const e = s.entries[0];
        if (!e) break;
        let valid: FilteredEvent | undefined;
        try {
          const raw = await io.read(e.file, MAX_EVENT_BYTES);
          const x: unknown = raw === null ? undefined : JSON.parse(raw);
          if (
            raw !== null &&
            Buffer.byteLength(raw) === e.bytes &&
            isFilteredEvent(x) &&
            hash(x.id) === e.file &&
            x.sequence === e.sequence
          )
            valid = x;
        } catch (error) {
          // Only invalid content is quarantined. Unsafe entries fail closed, and
          // transient I/O errors (fstat/read after open) keep the payload and
          // surface as storage_unavailable.
          if (!(error instanceof BoundedRead || error instanceof SyntaxError))
            throw error;
        }
        if (valid) {
          events.push(valid);
          break;
        }
        s.pending = { ...e, operation: "remove", reason: "corrupt" };
        await io.write("state", JSON.stringify(s));
        await io.removeIfPresent(e.file);
        s.entries.shift();
        loss(s, e.sequence, "corrupt");
        delete s.pending;
        await io.write("state", JSON.stringify(s));
      }
      return {
        events,
        queued: s.entries.length,
        gaps: s.gaps,
        totalLosses: s.totalLosses,
        paused: s.paused,
      };
    });
  }
  async status(
    deadlineAt?: number,
  ): Promise<Result<CaptureStatus, CaptureError>> {
    return this.transaction(
      async (s) => ({
        paused: s.paused,
        queued: s.entries.length,
        gaps: s.gaps,
        totalLosses: s.totalLosses,
      }),
      deadlineAt,
    );
  }
  async recordFailure(
    id: string,
    at: number,
    minIntervalMs: number,
  ): Promise<Result<number, CaptureError>> {
    return this.transaction(async (s, io) => {
      const e = s.entries.find((x) => x.file === hash(id));
      if (!e) return 0;
      // Strikes inside the interval are not counted (nor persisted).
      if (e.struckAt !== undefined && at - e.struckAt < minIntervalMs)
        return e.failures ?? 0;
      e.failures = Math.min((e.failures ?? 0) + 1, 1_000_000);
      e.struckAt = Math.max(0, Math.floor(at));
      await io.write("state", JSON.stringify(s));
      return e.failures;
    });
  }
  async cachedPolicy(
    projectID: string,
    deadlineAt?: number,
  ): Promise<Result<CachedPolicy | undefined, CaptureError>> {
    return this.transaction(
      async (_s, io) => (await policies(io))[projectID],
      deadlineAt,
    );
  }
  async cachePolicy(
    projectID: string,
    policy: CachedPolicy,
    deadlineAt?: number,
  ): Promise<Result<void, CaptureError>> {
    return this.transaction(async (_s, io) => {
      if (!cachedPolicy(policy)) return;
      const all = await policies(io);
      const previous = all[projectID];
      // Rewrite (with its fsyncs) only when contents change, or to advance a
      // fetchedAt older than POLICY_CACHE_REWRITE_MS; not on every guard read.
      if (
        previous &&
        sameServerPolicy(previous, policy) &&
        policy.fetchedAt - previous.fetchedAt < POLICY_CACHE_REWRITE_MS &&
        policy.fetchedAt >= previous.fetchedAt
      )
        return;
      all[projectID] = policy;
      // Bounded: keep the most recently fetched projects that fit the budget.
      let kept = Object.entries(all)
        .sort((a, b) => b[1].fetchedAt - a[1].fetchedAt)
        .slice(0, POLICY_CACHE_PROJECTS);
      let data = JSON.stringify(Object.fromEntries(kept));
      while (kept.length > 1 && Buffer.byteLength(data) > POLICY_CACHE_BYTES) {
        kept = kept.slice(0, -1);
        data = JSON.stringify(Object.fromEntries(kept));
      }
      if (Buffer.byteLength(data) > POLICY_CACHE_BYTES) return;
      await io.write("policy", data, "policy-next");
    }, deadlineAt);
  }
  private async remove(
    id: string,
    reason?: GapReason,
  ): Promise<Result<void, CaptureError>> {
    return this.transaction(async (s, io) => {
      const file = hash(id),
        entry = s.entries.find((e) => e.file === file);
      if (!entry) return;
      s.pending = { ...entry, operation: "remove", reason };
      await io.write("state", JSON.stringify(s));
      await io.removeIfPresent(file);
      s.entries = s.entries.filter((e) => e.file !== file);
      if (reason) loss(s, entry.sequence, reason);
      delete s.pending;
      await io.write("state", JSON.stringify(s));
    });
  }
  acknowledge(id: string) {
    return this.remove(id);
  }
  discard(id: string, reason: GapReason) {
    return this.remove(id, reason);
  }
  recordGap(sequence: number, reason: GapReason, deadlineAt?: number) {
    return this.transaction(async (s, io) => {
      loss(s, sequence, reason);
      await io.write("state", JSON.stringify(s));
    }, deadlineAt);
  }
  setPaused(paused: boolean) {
    return this.transaction(async (s, io) => {
      s.paused = paused;
      await io.write("state", JSON.stringify(s));
    });
  }
}
