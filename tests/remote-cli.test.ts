import { expect, test } from "bun:test";
import { runCommand } from "citty";
import { spawn } from "node:child_process";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createRemoteCommand } from "../src/commands/remote";
import { HTTPMemoryGateway } from "../src/gateways/memory.gateway";
import type { CredentialStore, CaptureError, Result } from "../src/capture/types";
import { buildEnvelope } from "../src/lib/envelope";

class Credentials implements CredentialStore {
  entries = new Map<string, string>();
  calls = 0;
  async get(profile: string): Promise<Result<string, CaptureError>> {
    this.calls++;
    const token = this.entries.get(profile);
    return token ? { ok: true, value: token } : { ok: false, error: { code: "credential_unavailable" } };
  }
  async set(profile: string, token: string): Promise<Result<void, CaptureError>> {
    this.entries.set(profile, token);
    return { ok: true, value: undefined };
  }
}
const token = "a".repeat(64);
// Project-only orientation returns verified knowledge; the server emits task
// candidates only in task-scoped working mode.
const data = { schema_version: "3", chunks: [{ record_id: "claim", revision: "r", text: "synthetic 猫", claim_status: "verified", origin_trust: "owner_authenticated", evidence: [], warnings: [] }], truncated: false, visited: 1, no_results: false };
const readFrame = (frame: string) => {
  const split = frame.indexOf("\n");
  const body = frame.slice(split + 1);
  expect(Number(frame.slice(0, split))).toBe(Buffer.byteLength(body));
  return JSON.parse(body);
};

test("actual CLI framing, origin binding and failures never read local vault", async () => {
  const credentials = new Credentials();
  let calls = 0;
  let authorization = "";
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
    calls++;
    authorization = req.headers.get("Authorization") ?? "";
    if (new URL(req.url).pathname === "/v1/retrieval:orient") return Response.json(data);
    return Response.json({ code: "forbidden", message: "private error text" }, { status: 403 });
  } });
  const origin = `http://127.0.0.1:${server.port}`;
  const command = createRemoteCommand({ credentials, readCredential: async () => token, gateway: (url, bearer) => new HTTPMemoryGateway(url, bearer) });
  let out = "", err = "";
  const originalOut = process.stdout.write, originalErr = process.stderr.write, previousExit = process.exitCode;
  // SAFETY: these test-only replacements capture only strings written by this command.
  process.stdout.write = ((chunk: string) => { out += chunk; return true; }) as typeof process.stdout.write;
  // SAFETY: same test-only write capture for sanitized diagnostics.
  process.stderr.write = ((chunk: string) => { err += chunk; return true; }) as typeof process.stderr.write;
  const run = async (args: string[]) => { out = ""; err = ""; process.exitCode = 0; await runCommand(command, { rawArgs: args }); };
  try {
    await run(["credential-set", "--profile", "demo", "--endpoint", origin]);
    expect(process.exitCode).toBe(0);
    expect(out).not.toContain(token);
    expect(credentials.entries.size).toBe(1);
    await run(["orient", "--profile", "demo", "--endpoint", origin, "--project", "p", "--budget", "1000"]);
    expect(process.exitCode).toBe(0);
    expect(readFrame(out).schema_version).toBe("3");
    expect(Buffer.byteLength(out)).toBeLessThanOrEqual(1000);
    expect(authorization).toBe(`Bearer ${token}`);
    expect(calls).toBe(1);
    await run(["search", "--profile", "demo", "--endpoint", origin, "--project", "p"]);
    expect(process.exitCode).toBe(1);
    expect(out).toBe("");
    expect(JSON.parse(err).error.code).toBe("forbidden");
    expect(err).not.toContain("private error text");
    await run(["orient", "--profile", "demo", "--endpoint", "https://other.example", "--project", "p"]);
    expect(JSON.parse(err).error.code).toBe("credential_unavailable");
    expect(calls).toBe(2);
    expect(buildEnvelope({ policy: {}, chunks: [] }).schema_version).toBe("2");
  } finally {
    process.stdout.write = originalOut; process.stderr.write = originalErr; process.exitCode = previousExit ?? 0;
    server.stop(true);
  }
});

test("invalid flags and extra positionals do not read the credential store", async () => {
  const credentials = new Credentials();
  const command = createRemoteCommand({ credentials, readCredential: async () => token, gateway: (url, bearer) => new HTTPMemoryGateway(url, bearer) });
  const original = process.stderr.write, previous = process.exitCode;
  // SAFETY: no diagnostic content is needed in this test.
  process.stderr.write = (() => true) as typeof process.stderr.write;
  try {
    for (const args of [
      ["credential-set", "--profile", "p", "--endpoint", "https://x.example", "--query", "secret"],
      ["orient", "extra", "--profile", "p", "--endpoint", "https://x.example", "--project", "p"],
      ["orient", "--profile", "p", "--endpoint", "https://x.example", "--project", "p", "--typo", "value"],
      ["mcp", "extra", "--profile", "p", "--endpoint", "https://x.example"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--project", "p"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--typo"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--profile", "q"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--no-profile"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--"],
      ["mcp", "-profile", "p", "--endpoint", "https://x.example"],
      ["mcp", "--profile", "p", "--endpoint", "https://x.example", "--action", "mcp"],
      ["mcp", "--profile", "p", "--endpoint"],
      ["--profile", "p", "mcp", "--endpoint", "https://x.example"],
    ]) {
      process.exitCode = 0;
      await runCommand(command, { rawArgs: args });
      expect(process.exitCode).toBe(1);
    }
    expect(credentials.calls).toBe(0);
    expect(credentials.entries.size).toBe(0);
  } finally { process.stderr.write = original; process.exitCode = previous ?? 0; }
});

test("real CLI required argument failures write only sanitized stderr", async () => {
  for (const args of [[], ["orient"], ["orient", "--profile", "p"], ["orient", "--endpoint", "https://example.com"], ["mcp"], ["mcp", "--profile", "p"], ["mcp", "extra", "--profile", "p", "--endpoint", "https://example.com"], ["mcp", "--profile", "p", "--endpoint", "https://example.com", "--budget", "10"]]) {
    const proc = Bun.spawn([process.execPath, "run", "src/cli.ts", "remote", ...args], { stdout: "pipe", stderr: "pipe" });
    const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    expect(code).toBe(1);
    expect(out).toBe("");
    expect(JSON.parse(err).error.code).toBe("invalid_input");
  }
});

test.skipIf(!Bun.which("go"))("real CLI bridges JSON-RPC through compiled Go MCP with injected remote credentials", async () => {
  const dir = await mkdtemp(join(tmpdir(), "kb-remote-cli-"));
  const root = resolve(import.meta.dir, "..");
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
    requests.push({ method: req.method, path: new URL(req.url).pathname, authorization: req.headers.get("authorization") });
    return Response.json({ schema_version: "3", connection_state: "available", authorized_project_ids: ["demo"], authorized_capabilities: ["status:read"], capability_gaps: ["recovery_unavailable"] });
  } });
  const requests: { method: string; path: string; authorization: string | null }[] = [];
  const origin = `http://127.0.0.1:${server.port}`;
  try {
    const binDir = join(dir, "bin"), realDir = join(dir, "real-bin"), mockDir = join(dir, "mock-bin"), brokenDir = join(dir, "broken-bin"), signalDir = join(dir, "signal-bin");
    await mkdir(binDir); await mkdir(realDir); await mkdir(mockDir); await mkdir(brokenDir); await mkdir(signalDir);
    // Put the actual Go binary in a temporary directory, never in the worktree.
    const binary = join(realDir, "kb-mcp");
    const build = Bun.spawn([Bun.which("go")!, "build", "-o", binary, "./cmd/kb-mcp"], { cwd: join(root, "server"), stdout: "pipe", stderr: "pipe" });
    const [buildOut, buildErr, buildCode] = await Promise.all([new Response(build.stdout).text(), new Response(build.stderr).text(), build.exited]);
    expect({ code: buildCode, stdout: buildOut, stderr: buildErr }).toEqual({ code: 0, stdout: "", stderr: "" });

    // Preload a test-native backend into the real RemoteCredentials class, then
    // inject that instance into the unmodified CLI entrypoint. No real OS store
    // is read or written; the synthetic bearer is constructed inside the child.
    const preload = join(dir, "preload.ts");
    await writeFile(preload, `import { mock } from 'bun:test';
const path = ${JSON.stringify(resolve(root, "src/gateways/remote-credentials.ts"))};
const { RemoteCredentials } = await import(path);
const { RemoteProfiles } = await import(${JSON.stringify(resolve(root, "src/services/remote.service.ts"))});
const entries = new Map();
const backend = {
  get: async ({service, name}) => entries.get(JSON.stringify([service, name])) ?? null,
  set: async ({service, name, value}) => { entries.set(JSON.stringify([service, name]), value); },
};
const credentials = new RemoteCredentials(backend);
const endpoint = process.argv[process.argv.indexOf('--endpoint') + 1];
const registration = await new RemoteProfiles(credentials).register('demo', endpoint, 'a'.repeat(64));
if (!registration.ok) throw Error('synthetic registration failed');
mock.module(path, () => ({ RemoteCredentials: class { constructor() { return credentials; } } }));
`);
    // Observe the precise argv and environment inherited by the child, then
    // exec the real bridge with the original descriptors unchanged.
    await writeFile(join(binDir, "kb-mcp"), `#!/bin/sh\nprintf '%s\\n' "$@" > ${JSON.stringify(join(dir, "bridge-argv"))}\n/usr/bin/env > ${JSON.stringify(join(dir, "bridge-env"))}\nexec ${JSON.stringify(binary)} "$@"\n`);
    await chmod(join(binDir, "kb-mcp"), 0o700);
    // A separate fake executable tests exit propagation (the protocol assertion
    // below is against the real Go binary, not this helper).
    await writeFile(join(mockDir, "kb-mcp"), `#!${process.execPath}\nimport { readFileSync } from 'node:fs';\nconst bearer = readFileSync(3, 'utf8');\nconst clean = !JSON.stringify(process.argv).includes(bearer) && !JSON.stringify(process.env).includes(bearer);\nprocess.exit(bearer === 'a'.repeat(64) && clean ? 23 : 42);\n`);
    await chmod(join(mockDir, "kb-mcp"), 0o700);
    await writeFile(join(brokenDir, "kb-mcp"), "#!/definitely/not/a/real/interpreter\n");
    await chmod(join(brokenDir, "kb-mcp"), 0o700);
    await writeFile(join(signalDir, "kb-mcp"), `#!${process.execPath}\nimport { readFileSync } from 'node:fs';\nreadFileSync(3);\nprocess.kill(process.pid, 'SIGTERM');\n`);
    await chmod(join(signalDir, "kb-mcp"), 0o700);
    const run = (path: string, input: string) => new Promise<{ out: string; err: string; code: number | null }>((done, reject) => {
      const child = spawn(process.execPath, ["--preload", preload, join(root, "src/cli.ts"), "remote", "mcp", "--profile", "demo", "--endpoint", origin], {
        cwd: root, env: { PATH: path }, stdio: ["pipe", "pipe", "pipe"],
      });
      let out = "", err = "";
      child.stdout.setEncoding("utf8").on("data", (s) => { out += s; });
      child.stderr.setEncoding("utf8").on("data", (s) => { err += s; });
      child.on("error", reject);
      child.on("close", (code) => done({ out, err, code }));
      child.stdin.on("error", () => {});
      child.stdin.end(input);
    });
    const input = [
      { jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "test", version: "1" } } },
      { jsonrpc: "2.0", method: "notifications/initialized" },
      { jsonrpc: "2.0", id: 2, method: "tools/list" },
      { jsonrpc: "2.0", id: 3, method: "tools/call", params: { name: "memory_status", arguments: {} } },
    ].map((frame) => JSON.stringify(frame)).join("\n") + "\n";
    const result = await run(binDir, input);
    expect(result.code).toBe(0);
    expect(result.err).toBe("");
    expect(result.out.endsWith("\n")).toBe(true);
    const lines = result.out.trimEnd().split("\n");
    expect(lines).toHaveLength(3); // notifications and the launcher write no stdout
    const [init, list, status] = lines.map((line) => JSON.parse(line));
    expect(init).toMatchObject({ jsonrpc: "2.0", id: 1, result: { serverInfo: { name: "kb-mcp" } } });
    expect(list).toMatchObject({ jsonrpc: "2.0", id: 2 });
    expect(list.result.tools.map((tool: { name: string }) => tool.name)).toContain("memory_status");
    expect(status).toMatchObject({ jsonrpc: "2.0", id: 3, result: { isError: false } });
    expect(JSON.parse(status.result.content[0].text)).toMatchObject({ schema_version: "3", connection_state: "available", authorized_project_ids: ["demo"] });
    expect(requests).toEqual([{ method: "GET", path: "/v1/status", authorization: `Bearer ${token}` }]);
    const bridgeArgs = await Bun.file(join(dir, "bridge-argv")).text();
    const bridgeEnv = await Bun.file(join(dir, "bridge-env")).text();
    expect(bridgeArgs.split("\n")).toEqual(["--endpoint", origin, "--credential-fd", "3", ""]);
    expect(result.out + result.err + bridgeArgs + bridgeEnv).not.toContain(token);
    const nonzero = await run(mockDir, "");
    expect(nonzero).toEqual({ code: 23, out: "", err: "" });
    const signaled = await run(signalDir, "");
    expect(signaled).toEqual({ code: 143, out: "", err: "" });
    const missing = await run(brokenDir, "");
    expect(missing.code).toBe(1);
    expect(missing.out).toBe("");
    expect(JSON.parse(missing.err).error.code).toBe("unavailable");
  } finally {
    server.stop(true);
    await rm(dir, { recursive: true, force: true });
  }
}, 120_000);
