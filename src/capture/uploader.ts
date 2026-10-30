import { createHash, randomUUID } from "node:crypto";
import {
  MAX_CHUNK_BYTES,
  MAX_EVENT_BYTES,
  POLICY_FRESHNESS_MS,
  type CaptureError,
  type CapturePolicy,
  type CaptureUploadGateway,
  type FilteredEvent,
  type Result,
} from "./types";
const fail = (code: CaptureError["code"]): Result<never, CaptureError> => ({
  ok: false,
  error: { code },
});
const digest = (bytes: Uint8Array | string) =>
  createHash("sha256").update(bytes).digest("hex");
function object(x: unknown): x is Record<string, unknown> {
  return !!x && typeof x === "object" && !Array.isArray(x);
}
function strings(x: unknown): x is string[] {
  return (
    Array.isArray(x) &&
    x.length <= 256 &&
    x.every((s) => typeof s === "string" && s.length <= 4096)
  );
}
function endpoint(value: string) {
  const u = new URL(value);
  if (
    u.username ||
    u.password ||
    u.search ||
    u.hash ||
    u.pathname !== "/" ||
    !(
      u.protocol === "https:" ||
      (u.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(u.hostname))
    )
  )
    throw Error("endpoint");
  return u.origin;
}
async function boundedJSON(response: Response): Promise<unknown> {
  if (!response.body) throw Error("response");
  const reader = response.body.getReader();
  let size = 0;
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 65536) throw Error("response budget");
      chunks.push(value);
    }
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } finally {
    await reader.cancel().catch(() => {});
  }
}
async function wait(ms: number, signal: AbortSignal): Promise<boolean> {
  if (signal.aborted) return false;
  return new Promise((resolve) => {
    const aborted = () => {
      clearTimeout(timer);
      resolve(false);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", aborted);
      resolve(true);
    }, ms);
    signal.addEventListener("abort", aborted, { once: true });
  });
}
/** F4-only gateway. C3 will supply a common client; no local-success fallback. */
export class F4Uploader implements CaptureUploadGateway {
  private readonly origin: string;
  constructor(
    baseURL: string,
    private readonly bearer: string,
    private readonly attempts = 3,
    private readonly localExclusions: {
      paths?: readonly string[];
      fields?: readonly string[];
    } = {},
  ) {
    this.origin = endpoint(baseURL);
    if (
      !/^[a-f0-9]{64}$/.test(bearer) ||
      !Number.isInteger(attempts) ||
      attempts < 1 ||
      attempts > 5
    )
      throw Error("configuration");
  }
  private async request(
    path: string,
    body: unknown,
    signal: AbortSignal,
    guard?: () => Promise<Result<void, CaptureError>>,
  ): Promise<Result<unknown, CaptureError>> {
    for (let attempt = 0; attempt < this.attempts; attempt++) {
      if (signal.aborted) return fail("cancelled");
      if (guard) {
        const allowed = await guard();
        if (!allowed.ok) return allowed;
      }
      let result: Result<unknown, CaptureError>;
      try {
        const response = await fetch(this.origin + path, {
          method: body === undefined ? "GET" : "POST",
          headers: {
            Authorization: "Bearer " + this.bearer,
            "Content-Type": "application/json",
            "X-Request-Id": randomUUID(),
          },
          body: body === undefined ? undefined : JSON.stringify(body),
          signal: AbortSignal.any([signal, AbortSignal.timeout(5000)]),
          redirect: "error",
        });
        if (response.ok) {
          const value = await boundedJSON(response);
          return object(value) && value.schema_version === "3"
            ? { ok: true, value }
            : fail("unavailable");
        }
        // Do not relay remote errors or response bodies (which may contain secrets).
        await response.body?.cancel();
        if (response.status === 401) return fail("unauthorized");
        if (response.status === 403) return fail("forbidden");
        if (response.status === 409) return fail("stale");
        if (response.status === 400 || response.status === 413)
          return fail("invalid_input");
        result = fail("unavailable");
      } catch {
        result = fail(signal.aborted ? "cancelled" : "unavailable");
      }
      if (attempt + 1 === this.attempts) return result;
      if (!(await wait(Math.min(2000, 100 * 2 ** attempt), signal)))
        return fail("cancelled");
    }
    return fail("unavailable");
  }
  async policy(
    projectID: string,
    signal = AbortSignal.timeout(2000),
  ): Promise<Result<CapturePolicy, CaptureError>> {
    const response = await this.request(
      "/v1/policies/" + encodeURIComponent(projectID),
      undefined,
      signal,
    );
    if (!response.ok) return response;
    const x = object(response.value) ? response.value.policy : undefined;
    if (
      !object(x) ||
      typeof x.revision !== "string" ||
      !x.revision ||
      typeof x.paused !== "boolean" ||
      !strings(x.excluded_paths) ||
      !strings(x.excluded_fields) ||
      !strings(x.allowed_model_routes) ||
      typeof x.spool_limit_bytes !== "number" ||
      !Number.isSafeInteger(x.spool_limit_bytes) ||
      x.spool_limit_bytes < 0
    )
      return fail("policy_unavailable");
    return {
      ok: true,
      value: {
        revision: x.revision,
        paused: x.paused,
        excludedPaths: x.excluded_paths,
        excludedFields: x.excluded_fields,
        allowedModelRoutes: x.allowed_model_routes,
        spoolLimitBytes: x.spool_limit_bytes,
        expiresAt: Date.now() + POLICY_FRESHNESS_MS,
        localExcludedPaths: this.localExclusions.paths,
        localExcludedFields: this.localExclusions.fields,
      },
    };
  }
  async send(
    event: FilteredEvent,
    signal: AbortSignal,
    guard: () => Promise<Result<void, CaptureError>>,
  ): Promise<Result<void, CaptureError>> {
    // Preserve logical identity and original locator in a versioned filtered JSON
    // representation. Ranges index UTF-8 bytes of this representation, never raw
    // source offsets after redaction. SourceLocator carries both relationships.
    const data = Buffer.from(
      JSON.stringify({
        schema_version: "capture-1",
        logical_event_id: event.id,
        source_locator: event.sourceLocator,
        content: event.content,
      }),
    );
    if (data.length > MAX_EVENT_BYTES) return fail("invalid_input");
    const scope = {
      project_id: event.scope.projectID,
      task_id: event.scope.taskID,
      session_id: event.scope.sessionID,
    };
    const total = Math.ceil(data.length / MAX_CHUNK_BYTES);
    for (let i = 0; i < total; i++) {
      const start = i * MAX_CHUNK_BYTES,
        end = Math.min(start + MAX_CHUNK_BYTES, data.length);
      const chunk = data.subarray(start, end);
      const d = digest(chunk);
      const locator = JSON.stringify({
        v: 1,
        logical_event_id: event.id,
        source_locator: event.sourceLocator,
        filtered_json_utf8: { start, end, total: data.length },
      });
      if (Buffer.byteLength(locator) > 4096) return fail("invalid_input");
      const stage = await this.request(
        "/v1/artifacts",
        {
          scope,
          digest: d,
          bytes: chunk.length,
          policy_revision: event.policyRevision,
          source_locator: locator,
          source_path: event.sourcePath,
          content_base64: chunk.toString("base64"),
        },
        signal,
        guard,
      );
      if (!stage.ok) return stage;
      const artifact = object(stage.value) ? stage.value.artifact : undefined;
      if (
        !object(artifact) ||
        typeof artifact.id !== "string" ||
        !artifact.id ||
        artifact.digest !== d ||
        artifact.policy_revision !== event.policyRevision
      )
        return fail("unavailable");
      const complete = await this.request(
        "/v1/artifacts/" + encodeURIComponent(artifact.id) + ":complete",
        { digest: d },
        signal,
        guard,
      );
      if (!complete.ok) return complete;
      const ready = object(complete.value) ? complete.value.receipt : undefined;
      if (!object(ready) || ready.id !== artifact.id || ready.state !== "ready")
        return fail("unavailable");
      const identity = digest(
        JSON.stringify(["capture-1", event.id, i, total]),
      );
      const key = digest(
        JSON.stringify(["capture-1", event.idempotencyKey, i, total]),
      );
      const append = await this.request(
        "/v1/events:append",
        {
          events: [
            {
              id: identity,
              idempotency_key: key,
              digest: d,
              scope,
              kind: event.kind,
              observed_at: event.observedAt,
              payload_artifact_id: artifact.id,
              sequence: event.sequence,
              completeness: total === 1 ? "complete" : "partial",
            },
          ],
        },
        signal,
        guard,
      );
      if (!append.ok) return append;
      const receipts = object(append.value) ? append.value.receipts : undefined;
      if (
        !Array.isArray(receipts) ||
        receipts.length !== 1 ||
        !object(receipts[0]) ||
        receipts[0].state !== "submitted" ||
        typeof receipts[0].id !== "string"
      )
        return fail("unavailable");
    }
    return { ok: true, value: undefined };
  }
}
