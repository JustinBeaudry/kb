import { expect, test } from "bun:test";
import {
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
  symlink,
  mkdir,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DiskSpool } from "../src/capture/spool";
import { filterEvent } from "../src/capture/policy";
import { event, policy, spoolFixture, bytes } from "./fixtures/capture";
test("secure durable spool contains only filtered payload across restart", async () => {
  const f = await spoolFixture();
  try {
    const secret = "sentinel-secret";
    const out = filterEvent(event({ text: secret }), policy(), [secret]);
    if (out.kind !== "accepted") throw Error("fixture rejected");
    expect((await f.spool.appendFiltered(out.event)).ok).toBe(true);
    const spoolBytes = await bytes(f.spool.path);
    expect(spoolBytes).not.toContain(secret);
    const restarted = new DiskSpool(f.spool.path);
    const read = await restarted.readBatch();
    expect(read.ok && read.value.events).toHaveLength(1);
    expect((await stat(f.spool.path)).mode & 0o777).toBe(0o700);
    for (const name of await readdir(f.spool.path))
      expect((await stat(join(f.spool.path, name))).mode & 0o777).toBe(0o600);
  } finally {
    await f.cleanup();
  }
});
test("quota reports dropped event and bounded metadata-only losses", async () => {
  const f = await spoolFixture(1);
  try {
    const out = filterEvent(event(), policy(), []);
    if (out.kind !== "accepted") throw Error("fixture");
    await f.spool.appendFiltered(out.event);
    const report = await f.spool.readBatch();
    if (!report.ok) throw Error("fixture");
    expect(report.value.gaps).toHaveLength(1);
    expect(report.value.events).toHaveLength(0);
    expect(await bytes(f.spool.path)).not.toContain("hello");
    for (let i = 0; i < 150; i++) await f.spool.recordGap(i, "quota");
    const bounded = await f.spool.readBatch();
    expect(bounded.ok && bounded.value.gaps.length).toBeLessThanOrEqual(128);
  } finally {
    await f.cleanup();
  }
});
test("symlink spool directory and symlink payload are rejected", async () => {
  const f = await spoolFixture();
  try {
    await mkdir(join(f.parent, "target"), { mode: 0o700 });
    await symlink(join(f.parent, "target"), f.spool.path);
    expect((await f.spool.readBatch()).ok).toBe(false);
  } finally {
    await f.cleanup();
  }
});
test("payload and state symlinks and permissive modes fail closed", async () => {
  for (const which of ["payload", "state", "mode"]) {
    const f = await spoolFixture();
    try {
      const out = filterEvent(event(), policy(), []);
      if (out.kind !== "accepted") throw Error("fixture");
      expect((await f.spool.appendFiltered(out.event)).ok).toBe(true);
      const files = await readdir(f.spool.path);
      const target =
        which === "state" ? "state" : files.find((x) => x.endsWith(".json"));
      if (!target) throw Error("fixture");
      const { chmod, unlink, writeFile } = await import("node:fs/promises");
      if (which === "mode") await chmod(join(f.spool.path, target), 0o644);
      else {
        await writeFile(join(f.parent, "outside"), "SYNTHETIC-OUTSIDE");
        await unlink(join(f.spool.path, target));
        await symlink(join(f.parent, "outside"), join(f.spool.path, target));
      }
      expect((await f.spool.readBatch()).ok).toBe(false);
      expect(
        await readFile(join(f.parent, "outside"), "utf8").catch(() => ""),
      ).toBe(which === "mode" ? "" : "SYNTHETIC-OUTSIDE");
    } finally {
      await f.cleanup();
    }
  }
});
test("quota refuses newest and preserves older stable event", async () => {
  const f = await spoolFixture(1000);
  try {
    const out = filterEvent(event(), policy(), []);
    if (out.kind !== "accepted") throw Error("fixture");
    expect((await f.spool.appendFiltered(out.event)).ok).toBe(true);
    expect((await f.spool.appendFiltered(out.event)).ok).toBe(true);
    await f.spool.appendFiltered({
      ...out.event,
      id: "event-2",
      sequence: 2,
      content: "x".repeat(1200),
    });
    const batch = await f.spool.readBatch();
    expect(batch.ok && batch.value.events[0]?.id).toBe("event-1");
    expect(batch.ok && batch.value.gaps).toEqual([
      { sequence: 2, reason: "quota" },
    ]);
    expect((await f.spool.acknowledge("event-1")).ok).toBe(true);
    expect((await f.spool.readBatch()).ok).toBe(true);
  } finally {
    await f.cleanup();
  }
});
import { SecureSpoolDirectory } from "../src/repositories/capture-spool.repository";
test("directory substitution cannot redirect anchored payload writes", async () => {
  const f = await spoolFixture();
  try {
    const { rename } = await import("node:fs/promises");
    await mkdir(join(f.parent, "outside"), { mode: 0o700 });
    const result = await new SecureSpoolDirectory(f.spool.path).within(
      async (io) => {
        await rename(f.spool.path, join(f.parent, "original"));
        await symlink(join(f.parent, "outside"), f.spool.path);
        await io.write("state", "synthetic-filtered-data");
      },
    );
    expect(result.ok).toBe(true);
    expect(await readFile(join(f.parent, "original", "state"), "utf8")).toBe(
      "synthetic-filtered-data",
    );
    expect(await readdir(join(f.parent, "outside"))).toEqual([]);
    expect((await f.spool.readBatch()).ok).toBe(false);
  } finally {
    await f.cleanup();
  }
});
test("kill releases native lock and restart removes interrupted append bytes with explicit gap", async () => {
  const f = await spoolFixture();
  const proc = Bun.spawn(
    [
      process.execPath,
      join(import.meta.dir, "fixtures/capture-spool-crash.ts"),
      f.spool.path,
    ],
    { stdout: "pipe", stderr: "pipe", stdin: "ignore" },
  );
  try {
    const reader = proc.stdout.getReader();
    const ready = await Promise.race([
      reader.read(),
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(Error("fixture timeout")), 5000),
      ),
    ]);
    expect(ready.value && Buffer.from(ready.value).toString()).toContain(
      "ready",
    );
    expect(await f.spool.readBatch()).toEqual({
      ok: false,
      error: { code: "busy" },
    });
    proc.kill();
    await proc.exited;
    await reader.cancel();
    const restarted = await new DiskSpool(f.spool.path).readBatch();
    expect(restarted.ok && restarted.value.events).toEqual([]);
    expect(restarted.ok && restarted.value.gaps).toEqual([
      { sequence: 42, reason: "invalid_input" },
    ]);
    expect(await bytes(f.spool.path)).not.toContain("synthetic-filtered");
  } finally {
    proc.kill();
    await proc.exited;
    await f.cleanup();
  }
});
test("separate-process dispatch lease drains after kill while durable pause survives restart", async () => {
  const f = await spoolFixture();
  const proc = Bun.spawn(
    [
      process.execPath,
      join(import.meta.dir, "fixtures/capture-spool-crash.ts"),
      f.spool.path,
      "dispatch",
    ],
    { stdout: "pipe", stderr: "pipe", stdin: "ignore" },
  );
  try {
    const reader = proc.stdout.getReader();
    const ready = await reader.read();
    expect(ready.value && Buffer.from(ready.value).toString()).toContain(
      "ready",
    );
    expect((await f.spool.setPaused(true)).ok).toBe(true);
    expect(
      await f.spool.withDispatch(async () => ({ ok: true, value: undefined })),
    ).toEqual({ ok: false, error: { code: "busy" } });
    proc.kill();
    await proc.exited;
    await reader.cancel();
    const restarted = new DiskSpool(f.spool.path);
    expect(
      (
        await restarted.withDispatch(async () => ({
          ok: true,
          value: undefined,
        }))
      ).ok,
    ).toBe(true);
    const status = await restarted.readBatch();
    expect(status.ok && status.value.paused).toBe(true);
  } finally {
    proc.kill();
    await proc.exited;
    await f.cleanup();
  }
});
test("invalid policy quota parameter drops instead of accepting", async () => {
  const f = await spoolFixture();
  try {
    const out = filterEvent(event(), policy(), []);
    if (out.kind !== "accepted") throw Error("fixture");
    for (const limit of [Number.NaN, -1, 1.5, Number.POSITIVE_INFINITY])
      expect(await f.spool.appendFiltered(out.event, limit)).toEqual({
        ok: true,
        value: { id: "event-1", state: "dropped" },
      });
    expect(await bytes(f.spool.path)).not.toContain("hello");
  } finally {
    await f.cleanup();
  }
});
test("persisted state with invalid counters or journal fields fails closed", async () => {
  const { writeFile } = await import("node:fs/promises");
  const entry = { file: "a".repeat(64) + ".json", bytes: 1, sequence: 1 };
  for (const corrupt of [
    { version: 1, paused: false, entries: [], gaps: [], totalLosses: -1 },
    {
      version: 1,
      paused: false,
      entries: [entry, entry],
      gaps: [],
      totalLosses: 0,
    },
    {
      version: 1,
      paused: false,
      entries: [],
      gaps: [],
      totalLosses: 0,
      pending: { ...entry, operation: "remove", reason: "bogus" },
    },
    {
      version: 1,
      paused: false,
      entries: [],
      gaps: [],
      totalLosses: 0,
      extra: "x",
    },
  ]) {
    const f = await spoolFixture();
    try {
      expect((await f.spool.readBatch()).ok).toBe(true);
      await writeFile(join(f.spool.path, "state"), JSON.stringify(corrupt), {
        mode: 0o600,
      });
      expect(await f.spool.readBatch()).toEqual({
        ok: false,
        error: { code: "storage_unavailable" },
      });
    } finally {
      await f.cleanup();
    }
  }
});
test("queued payload with tampered metadata fails closed on read", async () => {
  const { writeFile } = await import("node:fs/promises");
  const f = await spoolFixture();
  try {
    const out = filterEvent(event(), policy(), []);
    if (out.kind !== "accepted") throw Error("fixture");
    await f.spool.appendFiltered(out.event);
    const file = (await readdir(f.spool.path)).find((x) => x.endsWith(".json"));
    if (!file) throw Error("fixture");
    await writeFile(
      join(f.spool.path, file),
      JSON.stringify({ ...out.event, scope: undefined, sequence: -4 }),
      { mode: 0o600 },
    );
    expect((await f.spool.readBatch()).ok).toBe(false);
  } finally {
    await f.cleanup();
  }
});
