import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { generateClient, SDK_OUTPUT_PATH } from "../scripts/generate-sdk";
import { SPEC_PATH } from "../scripts/generate-memory-api";
import { KBClient, KBError, canonicalOrigin } from "../sdk/typescript/src/kb";

type Seen = { url: string; init: RequestInit };
function fake(status: number, body: unknown, seen: Seen[] = [], contentType = "application/json") {
  return (async (url: URL | string, init?: RequestInit) => {
    seen.push({ url: String(url), init: init ?? {} });
    const text = body === undefined ? null : typeof body === "string" ? body : JSON.stringify(body);
    return new Response(text, { status, headers: { "Content-Type": contentType } });
  }) as typeof fetch;
}
const bearer = "kb_agent_test-bearer";

describe("generated TypeScript client", () => {
  test("is current with the OpenAPI contract", () => {
    const spec = JSON.parse(readFileSync(SPEC_PATH, "utf8"));
    expect(readFileSync(SDK_OUTPUT_PATH, "utf8")).toBe(generateClient(spec));
  });

  test("sends the bearer, JSON and an encoded path to the configured origin only", async () => {
    const seen: Seen[] = [];
    const kb = new KBClient({ origin: "https://memory.example.com", bearer, fetch: fake(200, { schema_version: "3" }, seen) });
    await kb.putCheckpoint("a/b?c", { scope: { project_id: "p", task_id: "t" }, objective: "x" } as never);
    const { url, init } = seen[0]!;
    expect(url).toBe("https://memory.example.com/v1/checkpoints/a%2Fb%3Fc");
    expect(init.method).toBe("PUT");
    expect(init.redirect).toBe("error");
    const headers = init.headers as Record<string, string>;
    expect(headers.Authorization).toBe(`Bearer ${bearer}`);
    expect(headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(String(init.body))).toMatchObject({ objective: "x" });
  });

  test("puts query parameters in the URL and sends no body on GET", async () => {
    const seen: Seen[] = [];
    const kb = new KBClient({ origin: "http://127.0.0.1:8080", bearer, fetch: fake(200, {}, seen) });
    await kb.fetchEvidence("art-1", { locator: "line 1-2", budget_bytes: 512, project_id: undefined });
    expect(seen[0]!.url).toBe("http://127.0.0.1:8080/v1/evidence/art-1?locator=line+1-2&budget_bytes=512");
    expect(seen[0]!.init.body).toBeUndefined();
    expect((seen[0]!.init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
  });

  test("turns a problem response into a KBError with kb's code", async () => {
    const kb = new KBClient({ origin: "https://memory.example.com", bearer, fetch: fake(403, { code: "forbidden", action: "Ask the owner.", request_id: "r1", status: 403 }, [], "application/problem+json") });
    const err = await kb.getStatus().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(KBError);
    expect(err).toMatchObject({ status: 403, code: "forbidden", detail: { action: "Ask the owner.", requestId: "r1" } });
    expect(String((err as Error).message)).not.toContain(bearer);
  });

  test("refuses an oversized or malformed response", async () => {
    const big = new KBClient({ origin: "https://memory.example.com", bearer, maxResponseBytes: 8, fetch: fake(200, { long: "x".repeat(64) }) });
    expect(await big.getStatus().catch((e: KBError) => e.code)).toBe("oversized_response");
    const bad = new KBClient({ origin: "https://memory.example.com", bearer, fetch: fake(200, "not json") });
    expect(await bad.getStatus().catch((e: KBError) => e.code)).toBe("invalid_response");
  });

  test("reports a network failure without the request", async () => {
    const kb = new KBClient({ origin: "https://memory.example.com", bearer, fetch: (async () => { throw new TypeError("redirect"); }) as unknown as typeof fetch });
    expect(await kb.getStatus().catch((e: KBError) => [e.status, e.code])).toEqual([0, "network"]);
  });

  test("never sends a bearer to plain http, a path, or credentials in the origin", () => {
    for (const origin of ["http://memory.example.com", "https://memory.example.com/api", "https://u:p@memory.example.com", "https://memory.example.com/?x=1", "http://localhost:8080", "ftp://x.example"]) {
      expect(canonicalOrigin(origin)).toBeNull();
      expect(() => new KBClient({ origin, bearer })).toThrow(KBError);
    }
    expect(canonicalOrigin("https://memory.example.com/")).toBe("https://memory.example.com");
    expect(() => new KBClient({ origin: "https://memory.example.com", bearer: "has space" })).toThrow(KBError);
  });

  test("refuses an empty ID as a rejected promise, before any request", async () => {
    const seen: Seen[] = [];
    const kb = new KBClient({ origin: "https://memory.example.com", bearer, fetch: fake(200, {}, seen) });
    expect(await kb.completeArtifact("", {} as never).catch((e: KBError) => e.code)).toBe("invalid_input");
    expect(seen).toHaveLength(0);
  });
});
