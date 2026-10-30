import { afterEach, expect, spyOn, test } from "bun:test";
import command from "../src/commands/capture";
import { spoolFixture } from "./fixtures/capture";

type Args = Record<string, string | undefined>;
async function run(args: Args): Promise<{ output: unknown; exitCode: number }> {
  const log = spyOn(console, "log").mockImplementation(() => {});
  const previous = process.exitCode;
  process.exitCode = 0;
  try {
    // SAFETY: citty's run context is only read through `args` by this command.
    await command.run?.({ args, rawArgs: [], cmd: command } as never);
    const line = log.mock.calls.at(-1)?.[0];
    return {
      output: JSON.parse(String(line)),
      exitCode: Number(process.exitCode ?? 0),
    };
  } finally {
    log.mockRestore();
    process.exitCode = previous;
  }
}
const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const c of cleanups.splice(0)) await c();
});

test("status pause and resume work offline through the capture service", async () => {
  const f = await spoolFixture();
  cleanups.push(f.cleanup);
  const base = { profile: "default", spool: f.spool.path };
  expect(await run({ ...base, action: "status" })).toEqual({
    output: {
      ok: true,
      value: { paused: false, queued: 0, gaps: [], totalLosses: 0 },
    },
    exitCode: 0,
  });
  expect((await run({ ...base, action: "pause" })).output).toEqual({
    ok: true,
  });
  const paused = await run({ ...base, action: "status" });
  expect(paused.output).toMatchObject({ ok: true, value: { paused: true } });
  expect((await run({ ...base, action: "resume" })).exitCode).toBe(0);
  const resumed = await run({ ...base, action: "status" });
  expect(resumed.output).toMatchObject({ ok: true, value: { paused: false } });
});
test("invalid spool limit unknown action and missing endpoint fail without credential access", async () => {
  const f = await spoolFixture();
  cleanups.push(f.cleanup);
  const base = { profile: "default", spool: f.spool.path };
  for (const args of [
    { ...base, action: "status", "spool-limit-bytes": "0" },
    { ...base, action: "status", "spool-limit-bytes": "1e9" },
    { ...base, action: "explode" },
    { ...base, action: "flush" },
  ])
    expect(await run(args)).toEqual({
      output: { ok: false, error: { code: "invalid_input" } },
      exitCode: 1,
    });
});
test("status surfaces unsafe spool storage as an error status", async () => {
  const f = await spoolFixture();
  cleanups.push(f.cleanup);
  const { symlink, mkdir } = await import("node:fs/promises");
  const { join } = await import("node:path");
  await mkdir(join(f.parent, "target"), { mode: 0o700 });
  await symlink(join(f.parent, "target"), f.spool.path);
  expect(
    await run({ profile: "default", spool: f.spool.path, action: "status" }),
  ).toEqual({
    output: { ok: false, error: { code: "storage_unavailable" } },
    exitCode: 1,
  });
});
