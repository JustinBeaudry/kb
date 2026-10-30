import { expect, test } from "bun:test";
import { CaptureService } from "../src/services/capture.service";
import type { CaptureUploadGateway, CapturePolicy } from "../src/capture/types";
import { event, policy, spoolFixture, bytes } from "./fixtures/capture";
const signal = () => new AbortController().signal;
function gateway() {
  let p: CapturePolicy | undefined = policy();
  const sentPayloads: unknown[] = [];
  const gateway: CaptureUploadGateway = {
    policy: async () =>
      p
        ? { ok: true, value: p }
        : { ok: false, error: { code: "policy_unavailable" } },
    send: async (e) => {
      sentPayloads.push(e);
      return { ok: true, value: undefined };
    },
  };
  return {
    gateway,
    sentPayloads,
    setPolicy: (x: CapturePolicy | undefined) => {
      p = x;
    },
  };
}
test("pause suppresses queued transmission", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    const queued = await s.enqueue(event());
    expect(queued.ok && queued.value.state).toBe("queued");
    expect(await s.pause()).toEqual({ ok: true, value: "paused" });
    await s.flush(signal());
    expect(g.sentPayloads).toHaveLength(0);
    expect(await bytes(f.spool.path)).toContain("hello");
  } finally {
    await f.cleanup();
  }
});
test("resume does not backfill pause", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    expect((await s.pause()).ok).toBe(true);
    const withheld = await s.enqueue(event({ text: "paused-secret-content" }));
    expect(withheld.ok && withheld.value.state).toBe("withheld");
    await s.resume();
    await s.flush(signal());
    expect(g.sentPayloads).toHaveLength(0);
    expect(await bytes(f.spool.path)).not.toContain("paused-secret-content");
  } finally {
    await f.cleanup();
  }
});
test("new exclusions purge queued payload", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    const queued = await s.enqueue(event());
    expect(queued.ok && queued.value.state).toBe("queued");
    g.setPolicy({ ...policy(), excludedPaths: ["src"] });
    await s.flush(signal());
    expect(g.sentPayloads).toHaveLength(0);
    expect(await bytes(f.spool.path)).not.toContain("hello");
  } finally {
    await f.cleanup();
  }
});
test("missing policy blocks upload", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    const queued = await s.enqueue(event());
    expect(queued.ok && queued.value.state).toBe("queued");
    g.setPolicy(undefined);
    expect(await s.flush(signal())).toEqual({
      ok: false,
      error: { code: "policy_unavailable" },
    });
    expect(g.sentPayloads).toHaveLength(0);
  } finally {
    await f.cleanup();
  }
});
test("secret is absent from durable bytes and gateway payload", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const secret = "synthetic-private-key";
    const s = new CaptureService(f.spool, g.gateway, [secret]);
    await s.enqueue(
      event({ outputs: ["synthetic-", "private-key"], nested: [secret] }),
    );
    const spoolBytes = await bytes(f.spool.path);
    expect(spoolBytes).not.toContain(secret);
    await s.flush(signal());
    expect(g.sentPayloads).toHaveLength(1);
    expect(JSON.stringify(g.sentPayloads)).not.toContain(secret);
  } finally {
    await f.cleanup();
  }
});
test("policy is checked before each individual gateway request", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    let checked = 0;
    const service = new CaptureService(f.spool, {
      ...g.gateway,
      send: async (_e, _signal, guard) => {
        checked++;
        g.setPolicy({ ...policy(), paused: true });
        const allowed = await guard();
        expect(allowed.ok).toBe(false);
        return allowed;
      },
    });
    expect((await service.enqueue(event())).ok).toBe(true);
    expect((await service.flush(signal())).ok).toBe(false);
    expect(checked).toBe(1);
  } finally {
    await f.cleanup();
  }
});
test("pause cancels in-flight request and persists across service restart", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    let started!: () => void;
    const begin = new Promise<void>((r) => {
      started = r;
    });
    const service = new CaptureService(f.spool, {
      ...g.gateway,
      send: async (_e, signal) => {
        started();
        await new Promise<void>((resolve) =>
          signal.addEventListener("abort", () => resolve(), { once: true }),
        );
        return { ok: false, error: { code: "cancelled" } };
      },
    });
    await service.enqueue(event());
    const flushing = service.flush(signal());
    await begin;
    expect(await service.pause()).toEqual({ ok: true, value: "paused" });
    expect((await flushing).ok).toBe(false);
    const restarted = new CaptureService(f.spool, g.gateway);
    expect((await restarted.flush(signal())).ok).toBe(true);
    expect(g.sentPayloads).toHaveLength(0);
  } finally {
    await f.cleanup();
  }
});
test("policy revision and field exclusions purge immutable queued payload", async () => {
  for (const p of [
    { ...policy(), revision: "p2" },
    { ...policy(), excludedFields: ["text"] },
  ]) {
    const f = await spoolFixture();
    const g = gateway();
    try {
      const s = new CaptureService(f.spool, g.gateway);
      await s.enqueue(event());
      g.setPolicy(p);
      const result = await s.flush(signal());
      expect(result.ok && result.value.totalLosses).toBe(1);
      expect(g.sentPayloads).toHaveLength(0);
      expect(await bytes(f.spool.path)).not.toContain("hello");
    } finally {
      await f.cleanup();
    }
  }
});
test("offline enqueue records only bounded loss metadata", async () => {
  const f = await spoolFixture();
  const g = gateway();
  g.setPolicy(undefined);
  try {
    const service = new CaptureService(f.spool, g.gateway);
    // Never-fetched policy: no cached policy exists, so nothing is spooled.
    const r = await service.enqueue(event({ text: "never-persist-this" }));
    expect(r.ok && r.value.state).toBe("withheld");
    const status = await service.status();
    expect(status.ok && status.value.gaps).toEqual([
      { sequence: 1, reason: "policy_unavailable" },
    ]);
    expect(await bytes(f.spool.path)).not.toContain("never-persist-this");
  } finally {
    await f.cleanup();
  }
});

import { F4Uploader } from "../src/capture/uploader";
import { NativeCredentials } from "../src/capture/credentials";
import { CaptureConnectionService } from "../src/services/capture.service";
import { MAX_CHUNK_BYTES } from "../src/capture/types";
test("native credential failures surface sanitized status without fallback", async () => {
  const store = new NativeCredentials({
    get: async () => {
      throw Error("SECRET-ERROR");
    },
    set: async () => {
      throw Error("SECRET-ERROR");
    },
  });
  for (const r of [
    await store.get("default"),
    await store.set("default", "a".repeat(64)),
    await store.get("../bad"),
  ])
    expect(r).toEqual({ ok: false, error: { code: "credential_unavailable" } });
  let memory: string | null = null;
  const good = new NativeCredentials({
    get: async () => memory,
    set: async (o) => {
      memory = o.value;
    },
  });
  expect((await good.get("profile")).ok).toBe(false);
  expect((await good.set("profile", "b".repeat(64))).ok).toBe(true);
  expect(await good.get("profile")).toEqual({
    ok: true,
    value: "b".repeat(64),
  });
  expect((await good.set("profile", "wrong")).ok).toBe(false);
});
test("connection orchestration redacts its protected bearer", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const token = "c".repeat(64);
    const service = new CaptureConnectionService(
      {
        get: async () => ({ ok: true, value: token }),
        set: async () => ({ ok: true, value: undefined }),
      },
      f.spool,
      () => g.gateway,
    );
    const connection = await service.connect("synthetic");
    if (!connection.ok) throw Error("fixture");
    await connection.value.enqueue(event({ text: token }));
    expect(await bytes(f.spool.path)).not.toContain(token);
    expect((await service.setCredential("synthetic", token)).ok).toBe(true);
    const fail = new CaptureConnectionService(
      {
        get: async () => ({
          ok: false,
          error: { code: "credential_unavailable" },
        }),
        set: async () => ({
          ok: false,
          error: { code: "credential_unavailable" },
        }),
      },
      f.spool,
      () => g.gateway,
    );
    expect((await fail.connect("missing")).ok).toBe(false);
  } finally {
    await f.cleanup();
  }
});
test("F4 gateway caps chunks and preserves original identity and exact filtered byte offsets", async () => {
  const chunks: Record<string, unknown>[] = [];
  const events: unknown[] = [];
  let checks = 0;
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    async fetch(req) {
      const path = new URL(req.url).pathname;
      const body: unknown = await req.json();
      if (
        path === "/v1/artifacts" &&
        body &&
        typeof body === "object" &&
        "digest" in body &&
        "policy_revision" in body
      ) {
        const b = body as Record<string, unknown>; // SAFETY: synthetic fixture body is validated structurally here and by assertions below.
        chunks.push(b);
        return Response.json({
          schema_version: "3",
          artifact: {
            id: "artifact-" + chunks.length,
            digest: b.digest,
            policy_revision: b.policy_revision,
          },
        });
      }
      if (path.endsWith(":complete"))
        return Response.json({
          schema_version: "3",
          receipt: {
            id: path.split("/").at(-1)?.split(":")[0],
            state: "ready",
          },
        });
      events.push(body);
      return Response.json({
        schema_version: "3",
        receipts: [{ id: "receipt", state: "submitted" }],
      });
    },
  });
  try {
    const p = {
      ...event({ text: "😀".repeat(MAX_CHUNK_BYTES / 4) }),
      policyRevision: "p1",
    };
    const u = new F4Uploader(server.url.origin, "d".repeat(64));
    const sent = await u.send(p, signal(), async () => {
      checks++;
      return { ok: true, value: undefined };
    });
    expect(sent.ok).toBe(true);
    expect(chunks).toHaveLength(2);
    expect(events).toHaveLength(2);
    expect(checks).toBe(6);
    let offset = 0;
    for (const chunk of chunks) {
      const data = Buffer.from(String(chunk.content_base64), "base64");
      expect(data.length).toBeLessThanOrEqual(MAX_CHUNK_BYTES);
      const locator = JSON.parse(String(chunk.source_locator));
      expect(locator.logical_event_id).toBe(p.id);
      expect(locator.source_locator).toBe(p.sourceLocator);
      expect(locator.filtered_json_utf8.start).toBe(offset);
      offset += data.length;
      expect(locator.filtered_json_utf8.end).toBe(offset);
    }
    expect(offset).toBeGreaterThan(MAX_CHUNK_BYTES);
  } finally {
    await server.stop(true);
  }
});
test("retry is bounded cancellation aware and rechecks guard before every attempt", async () => {
  let calls = 0,
    guards = 0;
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch() {
      calls++;
      return new Response("", { status: 503 });
    },
  });
  try {
    const up = new F4Uploader(server.url.origin, "a".repeat(64), 3);
    const result = await up.send(
      { ...event(), policyRevision: "p1" },
      signal(),
      async () => {
        guards++;
        return { ok: true, value: undefined };
      },
    );
    expect(result).toEqual({ ok: false, error: { code: "unavailable" } });
    expect(calls).toBe(3);
    expect(guards).toBe(3);
    const controller = new AbortController();
    const sending = up.send(
      { ...event(), policyRevision: "p1" },
      controller.signal,
      async () => ({ ok: true, value: undefined }),
    );
    setTimeout(() => controller.abort(), 20);
    expect(await sending).toEqual({ ok: false, error: { code: "cancelled" } });
  } finally {
    await server.stop(true);
  }
});
test("gateway rejects redirects auth failures oversized replies and insecure origins", async () => {
  expect(() => new F4Uploader("http://example.org", "a".repeat(64))).toThrow();
  expect(
    () => new F4Uploader("https://user:secret@example.org", "a".repeat(64)),
  ).toThrow();
  for (const status of [401, 403, 409, 400, 413, 302, 200]) {
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch() {
        return new Response(status === 200 ? "x".repeat(65537) : "", {
          status,
          headers: status === 302 ? { location: "https://example.org" } : {},
        });
      },
    });
    try {
      const result = await new F4Uploader(
        server.url.origin,
        "a".repeat(64),
        1,
      ).policy("demo");
      expect(result.ok).toBe(false);
    } finally {
      await server.stop(true);
    }
  }
});
test("cross-process pause saves the marker and reports paused_draining while a dispatch is leased", async () => {
  const f = await spoolFixture();
  const g = gateway();
  let release!: () => void;
  let started!: () => void;
  const begin = new Promise<void>((r) => {
    started = r;
  });
  try {
    const first = new CaptureService(f.spool, {
      ...g.gateway,
      send: async (e) => {
        started();
        await new Promise<void>((r) => {
          release = r;
        });
        g.sentPayloads.push(e);
        return { ok: true, value: undefined };
      },
    });
    for (const e of [
      event(),
      { ...event(), id: "event-2", idempotencyKey: "event-2", sequence: 2 },
    ]) {
      const queued = await first.enqueue(e);
      expect(queued.ok && queued.value.state).toBe("queued");
    }
    const flushing = first.flush(signal());
    await begin;
    const second = new CaptureService(f.spool, g.gateway);
    expect(await second.pause()).toEqual({
      ok: true,
      value: "paused_draining",
    });
    const during = await second.status();
    expect(during.ok && during.value.paused).toBe(true);
    release();
    await flushing;
    // Only the request already leased before pause completed; nothing after it.
    expect(g.sentPayloads).toHaveLength(1);
    expect(await second.pause()).toEqual({ ok: true, value: "paused" });
    const after = await second.status();
    expect(after.ok && after.value).toMatchObject({ paused: true, queued: 1 });
  } finally {
    release?.();
    await f.cleanup();
  }
});
test("known secrets in metadata are withheld before even a policy request", async () => {
  const f = await spoolFixture();
  let reads = 0;
  try {
    const service = new CaptureService(
      f.spool,
      {
        policy: async () => {
          reads++;
          return { ok: true, value: policy() };
        },
        send: async () => ({ ok: true, value: undefined }),
      },
      ["sentinel-project"],
    );
    const result = await service.enqueue({
      ...event(),
      scope: { projectID: "sentinel-project" },
    });
    expect(result.ok && result.value.state).toBe("withheld");
    expect(reads).toBe(0);
    expect(await bytes(f.spool.path)).not.toContain("sentinel-project");
  } finally {
    await f.cleanup();
  }
});
test("persistent server conflict on an unchanged queued item stops flush and retains it", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    let sends = 0;
    const s = new CaptureService(f.spool, {
      ...g.gateway,
      send: async () => {
        sends++;
        return { ok: false, error: { code: "stale" } };
      },
    });
    await s.enqueue(event());
    expect(await s.flush(signal())).toEqual({
      ok: false,
      error: { code: "stale" },
    });
    expect(sends).toBe(1);
    const batch = await f.spool.readBatch();
    expect(batch.ok && batch.value.events.map((e) => e.id)).toEqual([
      "event-1",
    ]);
  } finally {
    await f.cleanup();
  }
});
test("guard-purged item continues to the next queued event", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue({ ...event(), sourcePath: "private/a.ts" });
    await s.enqueue({
      ...event(),
      id: "event-2",
      idempotencyKey: "event-2",
      sequence: 2,
    });
    g.setPolicy({ ...policy(), excludedPaths: ["private"] });
    const result = await s.flush(signal());
    expect(result.ok && result.value.sent).toBe(1);
    expect(g.sentPayloads).toHaveLength(1);
  } finally {
    await f.cleanup();
  }
});
test("unauthorized revoked and forbidden policy reads keep their status and the queue", async () => {
  for (const code of [
    "unauthorized",
    "forbidden",
    "credential_unavailable",
  ] as const) {
    const f = await spoolFixture();
    const g = gateway();
    try {
      const s = new CaptureService(f.spool, g.gateway);
      await s.enqueue(event());
      const denied = new CaptureService(f.spool, {
        ...g.gateway,
        policy: async () => ({ ok: false, error: { code } }),
      });
      expect(await denied.flush(signal())).toEqual({
        ok: false,
        error: { code },
      });
      expect(g.sentPayloads).toHaveLength(0);
      const batch = await f.spool.readBatch();
      expect(batch.ok && batch.value.events).toHaveLength(1);
    } finally {
      await f.cleanup();
    }
  }
});
test("pause written by another process during the policy read stops the payload", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const other = new DiskSpool(f.spool.path);
    let reads = 0;
    const s = new CaptureService(f.spool, {
      ...g.gateway,
      policy: async (project, sig) => {
        reads++;
        if (reads === 2) await other.setPaused(true);
        return g.gateway.policy(project, sig);
      },
    });
    await s.enqueue(event());
    const result = await s.flush(signal());
    expect(result.ok ? "ok" : result.error.code).toBe("paused");
    expect(g.sentPayloads).toHaveLength(0);
    const batch = await f.spool.readBatch();
    expect(batch.ok && batch.value.events).toHaveLength(1);
  } finally {
    await f.cleanup();
  }
});
test("expired collector policy freshness withholds sending and retains the queue for a later fresh read", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    g.setPolicy({ ...policy(), expiresAt: Date.now() - 1 });
    expect(await s.flush(signal())).toEqual({
      ok: false,
      error: { code: "policy_unavailable" },
    });
    expect(g.sentPayloads).toHaveLength(0);
    expect(await bytes(f.spool.path)).toContain("hello");
    g.setPolicy(policy());
    const sent = await s.flush(signal());
    expect(sent.ok && sent.value.sent).toBe(1);
  } finally {
    await f.cleanup();
  }
});
test("status is served by the capture service without credentials or network", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    await s.enqueue({
      ...event(),
      id: "event-2",
      idempotencyKey: "event-2",
      sequence: 2,
    });
    await s.pause();
    expect(await s.status()).toEqual({
      ok: true,
      value: { paused: true, queued: 2, gaps: [], totalLosses: 0 },
    });
  } finally {
    await f.cleanup();
  }
});
test("re-enqueue of an event ID with different content fails explicitly", async () => {
  const f = await spoolFixture();
  const g = gateway();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    expect((await s.enqueue(event())).ok).toBe(true);
    const same = await s.enqueue(event());
    expect(same.ok && same.value.state).toBe("duplicate");
    expect(await s.enqueue(event({ text: "different" }))).toEqual({
      ok: false,
      error: { code: "conflict" },
    });
    expect(
      await s.enqueue({
        ...event(),
        scope: { projectID: "demo", sessionID: "other" },
      }),
    ).toEqual({ ok: false, error: { code: "conflict" } });
  } finally {
    await f.cleanup();
  }
});
import { DiskSpool } from "../src/capture/spool";
import { parseSpoolLimit } from "../src/commands/capture";
test("CLI spool byte limit is explicit and validated", () => {
  expect(parseSpoolLimit(undefined)).toBe(256 * 1024 * 1024);
  expect(parseSpoolLimit("1048576")).toBe(1048576);
  for (const bad of [
    "",
    "0",
    "-1",
    "1.5",
    "1e9",
    "abc",
    "17179869185",
    "9007199254740993",
  ])
    expect(() => parseSpoolLimit(bad)).toThrow();
  expect(() => new DiskSpool("/tmp/unused", Number.NaN)).toThrow();
  expect(() => new DiskSpool("/tmp/unused", -1)).toThrow();
});
