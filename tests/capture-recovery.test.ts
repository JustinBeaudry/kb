import { expect, spyOn, test } from "bun:test";
import { join } from "node:path";
import { readdir, writeFile } from "node:fs/promises";
import { CaptureService } from "../src/services/capture.service";
import { DiskSpool } from "../src/capture/spool";
import { F4Uploader } from "../src/capture/uploader";
import {
  ENQUEUE_DEADLINE_MS,
  MIN_STRIKE_INTERVAL_MS,
  type CaptureError,
  type CapturePolicy,
  type CaptureUploadGateway,
  type FilteredEvent,
} from "../src/capture/types";
import { event, policy, spoolFixture, bytes } from "./fixtures/capture";

const signal = () => new AbortController().signal;
const second = () => ({
  ...event({ text: "second" }),
  id: "event-2",
  idempotencyKey: "event-2",
  sequence: 2,
});
function online(clock: () => number = Date.now) {
  let current: CapturePolicy | CaptureError["code"] = policy();
  let reads = 0;
  const sent: FilteredEvent[] = [];
  let sendResult: CaptureError["code"] | undefined;
  const gateway: CaptureUploadGateway = {
    policy: async () => {
      reads++;
      return typeof current === "string"
        ? { ok: false, error: { code: current } }
        : { ok: true, value: { ...current, expiresAt: clock() + 60_000 } };
    },
    send: async (e, _s, guard) => {
      const allowed = await guard();
      if (!allowed.ok) return allowed;
      if (sendResult) return { ok: false, error: { code: sendResult } };
      sent.push(e);
      return { ok: true, value: undefined };
    },
  };
  return {
    gateway,
    sent,
    reads: () => reads,
    set: (p: CapturePolicy | CaptureError["code"]) => {
      current = p;
    },
    failSends: (code: CaptureError["code"] | undefined) => {
      sendResult = code;
    },
  };
}

test("unknown scope keys are dropped before spooling and never wedge the queue", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    const poisoned = {
      ...event(),
      // SAFETY: deliberately malformed external input from a harness.
      scope: { projectID: "demo", repo: "r" } as never,
    };
    const r = await s.enqueue(poisoned);
    expect(r.ok && r.value.state).toBe("queued");
    expect((await s.enqueue(second())).ok).toBe(true);
    expect((await s.status()).ok).toBe(true);
    const flushed = await s.flush(signal());
    expect(flushed.ok && flushed.value.sent).toBe(2);
    expect(JSON.stringify(g.sent)).not.toContain("repo");
  } finally {
    await f.cleanup();
  }
});
test("a corrupted head payload is quarantined with a metadata-only gap", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    await s.enqueue(second());
    const { createHash } = await import("node:crypto");
    const head = createHash("sha256").update("event-1").digest("hex") + ".json";
    await writeFile(join(f.spool.path, head), "{not json", { mode: 0o600 });
    const status = await s.status();
    expect(status.ok && status.value.queued).toBe(2);
    const flushed = await s.flush(signal());
    expect(flushed.ok && flushed.value.sent).toBe(1);
    expect(flushed.ok && flushed.value.gaps).toEqual([
      { sequence: 1, reason: "corrupt" },
    ]);
    expect(g.sent.map((e) => e.id)).toEqual(["event-2"]);
    expect(await readdir(f.spool.path)).not.toContain(head);
  } finally {
    await f.cleanup();
  }
});
async function hold(path: string, ms: number) {
  const proc = Bun.spawn(
    [
      process.execPath,
      join(import.meta.dir, "fixtures/capture-lock-hold.ts"),
      path,
      String(ms),
    ],
    { stdout: "pipe", stderr: "pipe", stdin: "ignore" },
  );
  const reader = proc.stdout.getReader();
  const ready = await reader.read();
  expect(ready.value && Buffer.from(ready.value).toString()).toContain("ready");
  reader.releaseLock();
  return proc;
}
test("pause and enqueue wait out a separate process holding the state lock", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    expect((await s.status()).ok).toBe(true);
    let holder = await hold(f.spool.path, 300);
    const queued = await s.enqueue(event());
    expect(queued.ok && queued.value.state).toBe("queued");
    await holder.exited;
    holder = await hold(f.spool.path, 300);
    expect(await new CaptureService(f.spool, g.gateway).pause()).toEqual({
      ok: true,
      value: "paused",
    });
    await holder.exited;
    const status = await new DiskSpool(f.spool.path).status();
    expect(status.ok && status.value.paused).toBe(true);
  } finally {
    await f.cleanup();
  }
});
test("server conflict and deleted heads are discarded with a gap and flush continues", async () => {
  for (const code of ["conflict", "deleted", "invalid_input"] as const) {
    const f = await spoolFixture();
    const g = online();
    try {
      let calls = 0;
      const s = new CaptureService(f.spool, {
        ...g.gateway,
        send: async (e, sig, guard) => {
          if (calls++ === 0) return { ok: false, error: { code } };
          return g.gateway.send(e, sig, guard);
        },
      });
      await s.enqueue(event());
      await s.enqueue(second());
      const r = await s.flush(signal());
      expect(r.ok && r.value.sent).toBe(1);
      expect(r.ok && r.value.gaps).toEqual([
        { sequence: 1, reason: "rejected" },
      ]);
      // The dropped item's server error is surfaced, not hidden behind ok.
      expect(r.ok && r.value.lastError).toBe(code);
      expect(await bytes(f.spool.path)).not.toContain("hello");
    } finally {
      await f.cleanup();
    }
  }
});
test("item-level stale strikes count only once per interval and drop the head after the bound", async () => {
  const f = await spoolFixture();
  let t = Date.now();
  const clock = () => t;
  const g = online(clock);
  try {
    const s = new CaptureService(f.spool, g.gateway, [], { now: clock });
    await s.enqueue(event());
    await s.enqueue(second());
    g.failSends("stale");
    // Rapid flushes inside one interval count as a single strike.
    for (let i = 0; i < 10; i++)
      expect(await s.flush(signal())).toEqual({
        ok: false,
        error: { code: "stale" },
      });
    for (let i = 0; i < 3; i++) {
      t += MIN_STRIKE_INTERVAL_MS;
      expect((await s.flush(signal())).ok).toBe(false);
    }
    expect(g.sent).toHaveLength(0);
    let status = await s.status();
    expect(status.ok && status.value.queued).toBe(2);
    t += MIN_STRIKE_INTERVAL_MS;
    const stuck = new CaptureService(
      f.spool,
      {
        ...g.gateway,
        send: async (e, sig, guard) => {
          const allowed = await guard();
          if (!allowed.ok) return allowed;
          return e.id === "event-1"
            ? { ok: false, error: { code: "stale" } }
            : { ok: true, value: undefined };
        },
      },
      [],
      { now: clock },
    );
    const r = await stuck.flush(signal());
    expect(r).toEqual({
      ok: true,
      value: {
        sent: 1,
        gaps: [{ sequence: 1, reason: "undeliverable" }],
        totalLosses: 1,
        lastError: "stale",
      },
    });
    status = await s.status();
    expect(status.ok && status.value.queued).toBe(0);
  } finally {
    await f.cleanup();
  }
});
test("a project-level 403 from the policy read keeps the queue and never strikes", async () => {
  for (const code of ["forbidden", "unauthorized"] as const) {
    const f = await spoolFixture();
    let t = Date.now();
    const clock = () => t;
    const g = online(clock);
    try {
      const s = new CaptureService(f.spool, g.gateway, [], { now: clock });
      await s.enqueue(event());
      g.set(code);
      for (let i = 0; i < 8; i++) {
        t += MIN_STRIKE_INTERVAL_MS;
        expect(await s.flush(signal())).toEqual({
          ok: false,
          error: { code },
        });
      }
      const status = await s.status();
      expect(status.ok && status.value).toMatchObject({
        queued: 1,
        totalLosses: 0,
      });
      g.set(policy());
      const sent = await s.flush(signal());
      expect(sent.ok && sent.value.sent).toBe(1);
    } finally {
      await f.cleanup();
    }
  }
});
test("item-level forbidden from the item's own response strikes only across the time window", async () => {
  const f = await spoolFixture();
  let t = Date.now();
  const clock = () => t;
  const g = online(clock);
  try {
    const s = new CaptureService(f.spool, g.gateway, [], { now: clock });
    await s.enqueue(event());
    g.failSends("forbidden");
    for (let i = 0; i < 20; i++)
      expect((await s.flush(signal())).ok).toBe(false);
    let status = await s.status();
    expect(status.ok && status.value.queued).toBe(1);
    for (let i = 0; i < 4; i++) {
      t += MIN_STRIKE_INTERVAL_MS;
      await s.flush(signal());
    }
    status = await s.status();
    expect(status.ok && status.value).toMatchObject({
      queued: 0,
      gaps: [{ sequence: 1, reason: "undeliverable" }],
    });
  } finally {
    await f.cleanup();
  }
});
test("a transient read error on the head keeps the payload and reports storage_unavailable", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    const original = Bun.file;
    // Within readBatch, Bun.file call 1 reads the state journal and call 2 the
    // head payload. Only call 2 fails (simulated EIO after a successful open);
    // every other call, including writes, gets the real BunFile.
    let calls = 0;
    const spy = spyOn(Bun, "file").mockImplementation(((
      ...args: Parameters<typeof Bun.file>
    ) =>
      ++calls === 2
        ? {
            text: async () => {
              throw Error("EIO");
            },
          }
        : original(...args)) as typeof Bun.file); // SAFETY: test double; only text() is used on call 2.
    try {
      expect(await f.spool.readBatch()).toEqual({
        ok: false,
        error: { code: "storage_unavailable" },
      });
      expect(calls).toBeGreaterThanOrEqual(2);
    } finally {
      spy.mockRestore();
    }
    const batch = await f.spool.readBatch();
    expect(batch.ok && batch.value.events.map((e) => e.id)).toEqual([
      "event-1",
    ]);
    expect(batch.ok && batch.value.totalLosses).toBe(0);
  } finally {
    await f.cleanup();
  }
});
test("unchanged server policy does not rewrite the policy cache on every read", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    const { stat } = await import("node:fs/promises");
    const before = await stat(join(f.spool.path, "policy"));
    await s.flush(signal());
    await s.enqueue(second());
    await s.flush(signal());
    const after = await stat(join(f.spool.path, "policy"));
    expect(after.ino).toBe(before.ino);
    g.set({ ...policy(), revision: "p2" });
    // A queued item makes flush read the (changed) server policy.
    await s.enqueue({
      ...event(),
      id: "event-3",
      idempotencyKey: "event-3",
      sequence: 3,
    });
    await s.flush(signal());
    expect((await stat(join(f.spool.path, "policy"))).ino).not.toBe(before.ino);
  } finally {
    await f.cleanup();
  }
});
test("enqueue has one overall deadline across lock waits and refresh", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    expect((await s.status()).ok).toBe(true);
    const holder = await hold(f.spool.path, 6000);
    const started = performance.now();
    const r = await s.enqueue(event());
    const elapsed = performance.now() - started;
    expect(r).toEqual({ ok: false, error: { code: "busy" } });
    expect(elapsed).toBeLessThan(ENQUEUE_DEADLINE_MS + 500);
    holder.kill();
    await holder.exited;
  } finally {
    await f.cleanup();
  }
}, 15_000);
test("generic server unavailability never quarantines queued data", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    g.failSends("unavailable");
    for (let i = 0; i < 8; i++)
      expect((await s.flush(signal())).ok).toBe(false);
    const status = await s.status();
    expect(status.ok && status.value).toMatchObject({
      queued: 1,
      totalLosses: 0,
    });
  } finally {
    await f.cleanup();
  }
});
test("offline enqueue uses the persisted last policy and a later online flush sends", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const first = new CaptureService(f.spool, g.gateway);
    expect((await first.enqueue(event())).ok).toBe(true);
    g.set("unavailable");
    const restarted = new CaptureService(
      new DiskSpool(f.spool.path),
      g.gateway,
      ["offline-secret"],
    );
    const r = await restarted.enqueue({
      ...second(),
      content: { text: "second offline-secret", private: "drop me" },
    });
    expect(r.ok && r.value.state).toBe("queued");
    const spooled = await bytes(f.spool.path);
    expect(spooled).not.toContain("offline-secret");
    expect(await restarted.flush(signal())).toEqual({
      ok: false,
      error: { code: "policy_unavailable" },
    });
    expect(g.sent).toHaveLength(0);
    g.set(policy());
    const sent = await restarted.flush(signal());
    expect(sent.ok && sent.value.sent).toBe(2);
  } finally {
    await f.cleanup();
  }
});
test("offline local exclusions still apply to cached policy", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    await new CaptureService(f.spool, g.gateway).enqueue(second());
    g.set("unavailable");
    const s = new CaptureService(f.spool, g.gateway, [], {
      localExcludedPaths: ["src"],
    });
    const r = await s.enqueue(event());
    expect(r.ok && r.value.state).toBe("excluded");
  } finally {
    await f.cleanup();
  }
});
test("cached policy older than 24 hours or a denied refresh withholds offline enqueue", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    await new CaptureService(f.spool, g.gateway).enqueue(second());
    g.set("unavailable");
    const later = new CaptureService(f.spool, g.gateway, [], {
      now: () => Date.now() + 24 * 60 * 60 * 1000 + 1000,
    });
    const stale = await later.enqueue(event());
    expect(stale.ok && stale.value.state).toBe("withheld");
    g.set("unauthorized");
    const denied = await new CaptureService(f.spool, g.gateway, [], {
      now: () => Date.now() + 120_000,
    }).enqueue({
      ...event(),
      id: "event-3",
      idempotencyKey: "event-3",
      sequence: 3,
    });
    expect(denied.ok && denied.value.state).toBe("withheld");
    const status = await later.status();
    expect(status.ok && status.value.gaps.map((x) => x.reason)).toEqual([
      "policy_unavailable",
      "policy_unavailable",
    ]);
    expect(await bytes(f.spool.path)).not.toContain("hello");
  } finally {
    await f.cleanup();
  }
});
test("fresh cached policy avoids a network read on enqueue", async () => {
  const f = await spoolFixture();
  const g = online();
  try {
    const s = new CaptureService(f.spool, g.gateway);
    await s.enqueue(event());
    const before = g.reads();
    await s.enqueue(second());
    expect(g.reads()).toBe(before);
  } finally {
    await f.cleanup();
  }
});
test("F4 gateway reads the closed problem code to separate permanent from retryable failures", async () => {
  for (const [status, code, expected, calls] of [
    [409, "conflict", "conflict", 1],
    [409, "stale", "stale", 1],
    [410, "deleted", "deleted", 1],
    [503, "incomplete", "incomplete", 1],
    [503, "unavailable", "unavailable", 2],
    [409, "unlisted-code", "stale", 1],
  ] as const) {
    let seen = 0;
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch() {
        seen++;
        return Response.json({ schema_version: "3", code }, { status });
      },
    });
    try {
      const r = await new F4Uploader(server.url.origin, "a".repeat(64), 2).send(
        { ...event(), policyRevision: "p1" },
        signal(),
        async () => ({ ok: true, value: undefined }),
      );
      expect(r).toEqual({ ok: false, error: { code: expected } });
      expect(seen).toBe(calls);
    } finally {
      await server.stop(true);
    }
  }
});
test("a lock wait plus a hanging policy refresh still finish within the enqueue deadline", async () => {
  const f = await spoolFixture();
  try {
    const s = new CaptureService(f.spool, {
      policy: (_p, sig) =>
        new Promise((resolve) =>
          sig?.addEventListener("abort", () =>
            resolve({ ok: false, error: { code: "cancelled" } }),
          ),
        ),
      send: async () => ({ ok: true, value: undefined }),
    });
    expect((await s.status()).ok).toBe(true);
    const holder = await hold(f.spool.path, 1900);
    const started = performance.now();
    const r = await s.enqueue(event());
    const elapsed = performance.now() - started;
    expect(r.ok && r.value.state).toBe("withheld");
    expect(elapsed).toBeLessThan(ENQUEUE_DEADLINE_MS + 250);
    await holder.exited;
  } finally {
    await f.cleanup();
  }
}, 15_000);
