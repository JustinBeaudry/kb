import { posix } from "node:path";
import {
  MAX_EVENT_BYTES,
  type CaptureEvent,
  type CapturePolicy,
  type FilterOutcome,
  type FilteredEvent,
  type Json,
} from "./types";

const credentialField =
  /^(?:password|passwd|secret|token|access_token|refresh_token|api[_-]?key|authorization|cookie|private[_-]?key)$/i;
const kinds = new Set([
  "message",
  "tool_call",
  "tool_result",
  "checkpoint",
  "artifact",
]);
export function canonicalSourcePath(path: string): boolean {
  return (
    path.length <= 4096 &&
    !path.includes("\\") &&
    !posix.isAbsolute(path) &&
    posix.normalize(path) === path &&
    path !== "." &&
    path !== ".." &&
    !path.startsWith("../")
  );
}
export function policyAvailable(
  policy: CapturePolicy | undefined,
): policy is CapturePolicy {
  return (
    !!policy &&
    typeof policy.revision === "string" &&
    policy.revision.length > 0 &&
    policy.revision.length <= 256 &&
    Number.isFinite(policy.expiresAt) &&
    policy.expiresAt > Date.now() &&
    Number.isSafeInteger(policy.spoolLimitBytes) &&
    policy.spoolLimitBytes >= 0
  );
}
export function captureMetadata(
  event: CaptureEvent,
  knownSecrets: readonly string[],
) {
  const metadata = {
    id: event.id,
    idempotencyKey: event.idempotencyKey,
    scope: event.scope,
    kind: event.kind,
    observedAt: event.observedAt,
    sequence: event.sequence,
    sourceLocator: event.sourceLocator,
    sourcePath: event.sourcePath,
  };
  if (
    !event.scope ||
    ![event.id, event.idempotencyKey, event.scope.projectID].every(
      (x) => typeof x === "string" && x.trim().length > 0 && x.length <= 256,
    ) ||
    !kinds.has(event.kind) ||
    !Number.isSafeInteger(event.sequence) ||
    event.sequence < 0 ||
    !Number.isFinite(Date.parse(event.observedAt)) ||
    typeof event.sourceLocator !== "string" ||
    !event.sourceLocator.trim() ||
    event.sourceLocator.length > 4096 ||
    (event.sourcePath !== undefined && !canonicalSourcePath(event.sourcePath))
  )
    return null;
  if (
    [event.scope.sessionID, event.scope.taskID].some(
      (x) =>
        x !== undefined &&
        (typeof x !== "string" || !x.trim() || x.length > 256),
    )
  )
    return null;
  const secrets = knownSecrets.filter((x) => x.length > 0);
  if (secrets.length > 256 || secrets.some((x) => x.length > 65536))
    return null;
  const meta = JSON.stringify(metadata);
  if (
    secrets.some(
      (secret) =>
        meta.includes(secret) ||
        meta.includes(JSON.stringify(secret).slice(1, -1)) ||
        Object.values(metadata).some(
          (x) => typeof x === "string" && x.includes(secret),
        ),
    )
  )
    return null;
  return metadata;
}
const eventKeys = new Set([
  "id",
  "idempotencyKey",
  "scope",
  "kind",
  "observedAt",
  "sequence",
  "sourceLocator",
  "sourcePath",
  "content",
  "policyRevision",
]);
const scopeKeys = new Set(["projectID", "taskID", "sessionID"]);
/** Complete structural check for a persisted filtered event read back from disk. */
export function isFilteredEvent(x: unknown): x is FilteredEvent {
  if (!x || typeof x !== "object" || Array.isArray(x)) return false;
  const keys = Object.keys(x);
  if (
    !keys.every((k) => eventKeys.has(k)) ||
    !("content" in x) ||
    !("policyRevision" in x) ||
    typeof x.policyRevision !== "string" ||
    !x.policyRevision ||
    x.policyRevision.length > 256 ||
    !("scope" in x) ||
    !x.scope ||
    typeof x.scope !== "object" ||
    !Object.keys(x.scope).every((k) => scopeKeys.has(k))
  )
    return false;
  // SAFETY: only known keys are present; captureMetadata validates every
  // metadata value at runtime and returns null on any mismatch.
  return captureMetadata(x as unknown as CaptureEvent, []) !== null;
}
/** Whole-event bounded filtering runs before chunking or durable/network effects.
 * Known secrets are exact strings; unidentified-secret detection is imperfect.
 */
export function filterEvent(
  event: CaptureEvent,
  policy: CapturePolicy | undefined,
  knownSecrets: readonly string[],
): FilterOutcome {
  const sequence =
    Number.isSafeInteger(event.sequence) && event.sequence >= 0
      ? event.sequence
      : 0;
  const withheld = (
    reason: "policy_unavailable" | "paused" | "invalid_input",
  ): FilterOutcome => ({ kind: "withheld", gap: { sequence, reason } });
  if (!policyAvailable(policy)) return withheld("policy_unavailable");
  if (policy.paused) return withheld("paused");
  const metadata = captureMetadata(event, knownSecrets);
  if (!metadata || knownSecrets.some((s) => s && policy.revision.includes(s)))
    return withheld("invalid_input");
  const secrets = knownSecrets.filter((x) => x.length > 0);
  const excluded = [
    ...policy.excludedPaths,
    ...(policy.localExcludedPaths ?? []),
  ];
  if (excluded.length && !event.sourcePath) return withheld("invalid_input");
  if (
    excluded.some((path) => {
      const e = path.replace(/\/$/, "");
      return (
        !e || event.sourcePath === e || event.sourcePath?.startsWith(e + "/")
      );
    })
  )
    return { kind: "excluded", gap: { sequence, reason: "excluded" } };
  const fields = new Set([
    ...policy.excludedFields,
    ...(policy.localExcludedFields ?? []),
  ]);
  let nodes = 0,
    characters = 0;
  const strings: { value: string; set: (value: string) => void }[] = [];
  function clone(value: Json, path: string, depth: number): Json {
    if (++nodes > 50_000 || depth > 32) throw Error("bounded");
    if (typeof value === "string") {
      characters += value.length;
      if (characters > MAX_EVENT_BYTES) throw Error("bounded");
      return value;
    }
    if (value === null || typeof value === "boolean") return value;
    if (typeof value === "number") {
      if (!Number.isFinite(value)) throw Error("number");
      return value;
    }
    if (typeof value !== "object") throw Error("json");
    if (Array.isArray(value)) {
      const out: Json[] = [];
      for (let i = 0; i < value.length; i++) {
        const v = clone(value[i] ?? null, `${path}.${i}`, depth + 1);
        out.push(v);
        if (typeof v === "string")
          strings.push({
            value: v,
            set: (s) => {
              out[i] = s;
            },
          });
      }
      return out;
    }
    if (
      Object.getPrototypeOf(value) !== Object.prototype &&
      Object.getPrototypeOf(value) !== null
    )
      throw Error("json");
    const out: { [key: string]: Json } = Object.create(null);
    for (const [key, v] of Object.entries(value)) {
      characters += key.length;
      if (characters > MAX_EVENT_BYTES) throw Error("bounded");
      const p = path ? `${path}.${key}` : key;
      if (
        fields.has(key) ||
        fields.has(p) ||
        fields.has("/" + p.replaceAll(".", "/")) ||
        credentialField.test(key) ||
        secrets.some((s) => key.includes(s))
      )
        continue;
      const copy = clone(v, p, depth + 1);
      out[key] = copy;
      if (typeof copy === "string")
        strings.push({
          value: copy,
          set: (s) => {
            out[key] = s;
          },
        });
    }
    return out;
  }
  try {
    let content = clone(event.content, "", 0);
    if (typeof content === "string")
      strings.push({
        value: content,
        set: (s) => {
          content = s;
        },
      });
    // Concatenation catches secrets split across tool-output fields/chunks. Mark
    // original UTF-16 offsets, replacing each intersecting segment independently.
    const joined = strings.map((s) => s.value).join("");
    const ranges: [number, number][] = [];
    for (const secret of secrets) {
      let start = joined.indexOf(secret);
      while (start !== -1) {
        ranges.push([start, start + secret.length]);
        if (ranges.length > 50_000) throw Error("bounded");
        start = joined.indexOf(secret, start + 1);
      }
    }
    // Merge ranges once, then sweep strings and ranges together: O(S + R log R)
    // rather than every range against every string.
    ranges.sort((a, b) => a[0] - b[0]);
    const merged: [number, number][] = [];
    for (const [from, to] of ranges) {
      const last = merged.at(-1);
      if (last && from <= last[1]) last[1] = Math.max(last[1], to);
      else merged.push([from, to]);
    }
    let offset = 0,
      r = 0;
    for (const entry of strings) {
      const end = offset + entry.value.length;
      while (r < merged.length && (merged[r]?.[1] ?? 0) <= offset) r++;
      let out = "",
        cursor = offset;
      for (let i = r; i < merged.length; i++) {
        const range = merged[i];
        if (!range || range[0] >= end) break;
        const from = Math.max(range[0], offset),
          to = Math.min(range[1], end);
        out +=
          entry.value.slice(cursor - offset, from - offset) +
          "*".repeat(to - from);
        cursor = to;
      }
      if (cursor !== offset)
        entry.set(out + entry.value.slice(cursor - offset));
      offset = end;
    }
    const filtered = { ...metadata, content, policyRevision: policy.revision };
    if (Buffer.byteLength(JSON.stringify(filtered)) > MAX_EVENT_BYTES)
      return withheld("invalid_input");
    return { kind: "accepted", event: filtered };
  } catch {
    return withheld("invalid_input");
  }
}
