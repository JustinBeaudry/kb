import { afterEach, expect, spyOn, test } from "bun:test";
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createCaptureCommand } from "../src/commands/capture";
import type {
  CaptureError,
  CaptureUploadGateway,
  CredentialStore,
  Result,
} from "../src/capture/types";
import { bytes, policy, spoolFixture } from "./fixtures/capture";

const TOKEN = "a".repeat(64);

/** Credential-store boundary fake: records every read. */
class FakeCredentials implements CredentialStore {
  readonly reads: string[] = [];
  async get(profile: string): Promise<Result<string, CaptureError>> {
    this.reads.push(profile);
    return { ok: true, value: TOKEN };
  }
  async set(): Promise<Result<void, CaptureError>> {
    return { ok: true, value: undefined };
  }
}
/** Upload gateway fake: serves a policy and never sends anything in poll. */
const uploader = (): CaptureUploadGateway => ({
  policy: async () => ({ ok: true, value: policy() }),
  send: async () => ({ ok: false, error: { code: "unavailable" } }),
});

type Args = Record<string, string | undefined>;
async function run(
  credentials: FakeCredentials,
  args: Args,
  rawArgs: string[],
): Promise<{ output: unknown; exitCode: number }> {
  const command = createCaptureCommand({ credentials, uploader: () => uploader() });
  const log = spyOn(console, "log").mockImplementation(() => {});
  const previous = process.exitCode;
  process.exitCode = 0;
  try {
    // SAFETY: citty's run context is only read through `args`/`rawArgs` by this command.
    await command.run?.({ args, rawArgs, cmd: command } as never);
    return {
      output: JSON.parse(String(log.mock.calls.at(-1)?.[0])),
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

async function setup() {
  const f = await spoolFixture();
  cleanups.push(f.cleanup);
  const source = join(f.parent, "rollout.jsonl");
  writeFileSync(source, "");
  const args = {
    action: "poll",
    profile: "work",
    endpoint: "https://memory.example.com",
    spool: f.spool.path,
    adapter: "codex",
    source,
    project: "synthetic-project",
    session: "synthetic-session",
  };
  return { f, source, args, raw: ["poll", "--profile", "work"] };
}

test("capture poll enqueues new records from the selected file and reports JSON", async () => {
  const { f, source, args, raw } = await setup();
  const credentials = new FakeCredentials();
  writeFileSync(join(f.parent, "unselected.jsonl"), JSON.stringify({ role: "user", content: "UNSELECTED" }) + "\n");
  await run(credentials, args, raw);
  appendFileSync(source, readFileSync(join(import.meta.dir, "fixtures", "capture", "codex.jsonl"), "utf8"));

  const result = await run(credentials, args, raw);

  expect(result.exitCode).toBe(0);
  expect(result.output).toMatchObject({ ok: true, value: { events: 4, completeness: "complete" } });
  const spooled = await bytes(f.spool.path);
  expect(spooled).toContain("Run the synthetic tests");
  expect(spooled).not.toContain("UNSELECTED");
  expect(credentials.reads).toEqual(["work", "work"]);
});

test("capture poll refuses without an explicit profile before credential access", async () => {
  const { args } = await setup();
  const credentials = new FakeCredentials();

  const result = await run(credentials, args, ["poll"]);

  expect(result).toEqual({ output: { ok: false, error: { code: "invalid_input" } }, exitCode: 1 });
  expect(credentials.reads).toEqual([]);
});

test("capture poll refuses missing or unknown selections before credential access", async () => {
  const { args, raw } = await setup();
  const credentials = new FakeCredentials();

  const outputs = [];
  for (const bad of [
    { ...args, adapter: "hermes" },
    { ...args, adapter: undefined },
    { ...args, source: undefined },
    { ...args, endpoint: undefined },
    { ...args, project: undefined },
    { ...args, session: undefined },
  ])
    outputs.push(await run(credentials, bad, raw));

  for (const output of outputs)
    expect(output).toEqual({ output: { ok: false, error: { code: "invalid_input" } }, exitCode: 1 });
  expect(credentials.reads).toEqual([]);
});

test("capture poll reports a directory source as invalid without reading it", async () => {
  const { f, args, raw } = await setup();

  const result = await run(new FakeCredentials(), { ...args, source: f.parent }, raw);

  expect(result.output).toMatchObject({
    ok: true,
    value: { completeness: "partial", sources: [{ state: "invalid", events: 0 }] },
  });
});
