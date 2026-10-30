import { createHash } from "node:crypto";
import { isFilteredEvent } from "./policy";
import {
  SecureSpoolDirectory,
  type SpoolIO,
} from "../repositories/capture-spool.repository";
import {
  DEFAULT_SPOOL_BYTES,
  MAX_EVENT_BYTES,
  MAX_SPOOL_BYTES,
  type CaptureError,
  type CaptureSpool,
  type FilteredEvent,
  type Gap,
  type GapReason,
  type Receipt,
  type Result,
  type SpoolBatch,
} from "./types";
type Entry = { file: string; bytes: number; sequence: number };
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
    count(x.sequence)
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
    !x.entries.every(
      (e) => record(e) && entry(e) && only(e, ["file", "bytes", "sequence"]),
    ) ||
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
      only(p, ["file", "bytes", "sequence", "operation", "reason"]) &&
      (p.operation === "append" || p.operation === "remove") &&
      (p.reason === undefined || reasons.includes(p.reason as GapReason))
    )
  )
    throw Error("pending");
  // SAFETY: every key and value of the persisted state was validated above.
  return x as State;
}
export class DiskSpool implements CaptureSpool {
  private readonly directory: SecureSpoolDirectory;
  constructor(
    readonly path: string,
    private readonly limit = DEFAULT_SPOOL_BYTES,
  ) {
    if (!validLimit(limit)) throw Error("spool limit");
    this.directory = new SecureSpoolDirectory(path);
  }
  async withDispatch<T>(
    operation: () => Promise<Result<T, CaptureError>>,
  ): Promise<Result<T, CaptureError>> {
    const result = await this.directory.within(operation, "dispatch");
    return result.ok ? result.value : result;
  }
  private async transaction<T>(
    fn: (s: State, io: SpoolIO) => Promise<T>,
  ): Promise<Result<T, CaptureError>> {
    return this.directory.within(async (io) => {
      const s = state(await io.read("state", 2 * 1024 * 1024));
      if (s.pending) {
        const pending = s.pending;
        const raw = await io.read(pending.file, MAX_EVENT_BYTES);
        if (raw !== null) io.remove(pending.file);
        if (pending.operation === "remove") {
          s.entries = s.entries.filter((e) => e.file !== pending.file);
          if (pending.reason) loss(s, pending.sequence, pending.reason);
        } else {
          const temp = await io.read("event-next", MAX_EVENT_BYTES);
          if (temp !== null) io.remove("event-next");
          loss(s, pending.sequence, "invalid_input");
        }
        delete s.pending;
        await io.write("state", JSON.stringify(s));
      }
      return fn(s, io);
    });
  }
  async appendFiltered(
    event: FilteredEvent,
    limitBytes = this.limit,
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
    );
    return result.ok ? result.value : result;
  }
  async readBatch(): Promise<Result<SpoolBatch, CaptureError>> {
    return this.transaction(async (s, io) => {
      const events: FilteredEvent[] = [];
      for (const e of s.entries.slice(0, 1)) {
        const raw = await io.read(e.file, MAX_EVENT_BYTES);
        if (raw === null) throw Error("incomplete");
        const x: unknown = JSON.parse(raw);
        if (
          Buffer.byteLength(raw) !== e.bytes ||
          !isFilteredEvent(x) ||
          hash(x.id) !== e.file ||
          x.sequence !== e.sequence
        )
          throw Error("event");
        events.push(x);
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
      const raw = await io.read(file, MAX_EVENT_BYTES);
      if (raw !== null) io.remove(file);
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
  recordGap(sequence: number, reason: GapReason) {
    return this.transaction(async (s, io) => {
      loss(s, sequence, reason);
      await io.write("state", JSON.stringify(s));
    });
  }
  setPaused(paused: boolean) {
    return this.transaction(async (s, io) => {
      s.paused = paused;
      await io.write("state", JSON.stringify(s));
    });
  }
}
