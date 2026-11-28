/**
 * Transport for the generated kb client. Dependency-free: it needs only the
 * standard fetch, URL and AbortSignal, so it runs in Bun, Node 20+, Deno and
 * edge runtimes.
 */

export type KBClientOptions = {
  /** The kb origin, such as https://memory.example.com. Plain http is accepted only on 127.0.0.1 or [::1]. */
  origin: string;
  /** An enrolled agent bearer. Read it from the environment or a secret manager, never from source. */
  bearer: string;
  /** Replaces the global fetch, for tests or a custom agent. */
  fetch?: typeof fetch;
  /** Abort a request after this many milliseconds. Default 30000. */
  timeoutMs?: number;
  /** Refuse a response body larger than this. Default 16 MiB. */
  maxResponseBytes?: number;
};

export type RequestOptions = { signal?: AbortSignal };

/** A refused or failed request. code is kb's problem code when kb sent one. */
export class KBError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly detail: { action?: string; field?: string; requestId?: string; retryAfterSeconds?: number } = {},
  ) {
    super(status === 0 ? `kb request failed: ${code}` : `kb answered ${status} ${code}`);
    this.name = "KBError";
  }
}

const DEFAULT_TIMEOUT_MS = 30_000;
const DEFAULT_MAX_RESPONSE_BYTES = 16 << 20;

/** The origin kb is reached at, or null when it is not one the client may send a bearer to. */
export function canonicalOrigin(value: string): string | null {
  try {
    const u = new URL(value);
    if (u.username || u.password || u.search || u.hash || u.pathname !== "/") return null;
    if (u.protocol === "https:") return u.origin;
    if (u.protocol === "http:" && (u.hostname === "127.0.0.1" || u.hostname === "[::1]")) return u.origin;
    return null;
  } catch {
    return null;
  }
}

const validBearer = (b: string) => b.length > 0 && b.length <= 4096 && /^[\x21-\x7e]+$/.test(b);

/** Encodes one path segment, so an ID can never change the path it is sent to. */
export const segment = (value: string) => {
  if (typeof value !== "string" || value === "") throw new KBError(0, "invalid_input");
  return encodeURIComponent(value);
};

export class Transport {
  private readonly origin: string;
  private readonly send: typeof fetch;
  private readonly timeoutMs: number;
  private readonly maxBytes: number;

  constructor(private readonly options: KBClientOptions) {
    const origin = canonicalOrigin(options.origin);
    if (!origin) throw new KBError(0, "invalid_origin");
    if (!validBearer(options.bearer)) throw new KBError(0, "invalid_bearer");
    this.origin = origin;
    this.send = options.fetch ?? fetch;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.maxBytes = options.maxResponseBytes ?? DEFAULT_MAX_RESPONSE_BYTES;
  }

  async request<T>(method: string, path: string, body: unknown, query: Record<string, unknown> | undefined, options: RequestOptions = {}): Promise<T> {
    const url = new URL(this.origin + path);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined && value !== null) url.searchParams.set(key, String(value));
    }
    const headers: Record<string, string> = { Authorization: `Bearer ${this.options.bearer}`, Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const timeout = AbortSignal.timeout(this.timeoutMs);
    const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout;
    let response: Response;
    try {
      // A redirect is refused rather than followed, so the bearer only ever goes to the configured origin.
      response = await this.send(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: "error", signal });
    } catch (err) {
      throw new KBError(0, signal.aborted ? "timeout" : "network", {});
    }
    const value = await this.read(response);
    if (response.ok) return value as T;
    throw problem(response.status, value);
  }

  private async read(response: Response): Promise<unknown> {
    if (!response.body) return undefined;
    const reader = response.body.getReader();
    const parts: Uint8Array[] = [];
    let total = 0;
    try {
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        total += part.value.byteLength;
        if (total > this.maxBytes) throw new KBError(response.status, "oversized_response");
        parts.push(part.value);
      }
    } finally {
      await reader.cancel().catch(() => {});
    }
    if (total === 0) return undefined;
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const p of parts) {
      bytes.set(p, offset);
      offset += p.byteLength;
    }
    try {
      return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    } catch {
      if (response.ok) throw new KBError(response.status, "invalid_response");
      return undefined;
    }
  }
}

function problem(status: number, value: unknown): KBError {
  const p = value !== null && typeof value === "object" ? (value as Record<string, unknown>) : {};
  const text = (k: string) => (typeof p[k] === "string" ? (p[k] as string) : undefined);
  const code = text("code") ?? `http_${status}`;
  const retry = typeof p.retry_after_seconds === "number" ? p.retry_after_seconds : undefined;
  return new KBError(status, code, { action: text("action"), field: text("field"), requestId: text("request_id"), retryAfterSeconds: retry });
}
