import { test, expect } from "bun:test";
import { spawn } from "node:child_process";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import type { CaptureError, CredentialStore, Result } from "../src/capture/types";
import { launchRemoteMCP } from "../src/services/remote-mcp.service";
import { RemoteProfiles, profileAccount } from "../src/services/remote.service";

const bearer = "a".repeat(64); // synthetic only
class Store implements CredentialStore {
  values = new Map<string, string>();
  reads = 0;
  async get(name: string): Promise<Result<string, CaptureError>> {
    this.reads++;
    const value = this.values.get(name);
    return value ? { ok: true, value } : { ok: false, error: { code: "credential_unavailable" } };
  }
  async set(name: string, value: string): Promise<Result<void, CaptureError>> {
    this.values.set(name, value);
    return { ok: true, value: undefined };
  }
}

test("invalid profile/origin and missing or wrong-origin credential fail before launch", async () => {
  const store = new Store();
  for (const [profile, endpoint] of [["", "https://example.test"], ["good", "http://example.test"], ["good", "https://example.test/path"], ["good", "https://u:p@example.test"]] as const) {
    expect(await launchRemoteMCP({ profile, endpoint, credentials: store })).toEqual({ ok: false, error: { code: "invalid_input" } });
  }
  expect(store.reads).toBe(0);
  expect((await new RemoteProfiles(store).register("p", "https://example.test", bearer)).ok).toBe(true);
  expect(await launchRemoteMCP({ profile: "p", endpoint: "https://other.test", credentials: store })).toEqual({ ok: false, error: { code: "credential_unavailable" } });
  expect(await launchRemoteMCP({ profile: "other", endpoint: "https://example.test", credentials: store })).toEqual({ ok: false, error: { code: "credential_unavailable" } });
});

test("spawn failure is sanitized", async () => {
  const store = new Store();
  await new RemoteProfiles(store).register("p", "https://example.test", bearer);
  expect(await launchRemoteMCP({ profile: "p", endpoint: "https://example.test", credentials: store, executable: "/definitely/not/a/kb-mcp" })).toEqual({ ok: false, error: { code: "unavailable" } });
});

test("real child inherits protocol streams and private FD, never bearer argv or env; exit propagates", async () => {
  const dir = await mkdtemp(join(tmpdir(), "kb-mcp-test-"));
  try {
    const bridge = join(dir, "synthetic-mcp");
    const parent = join(dir, "parent.ts");
    // The bearer is deliberately not embedded in either child script or its environment.
    await writeFile(bridge, `#!${process.execPath}\nimport { readFileSync, fstatSync } from 'node:fs';\nconst token = readFileSync(3, 'utf8');\nconst valid = token === 'a'.repeat(64) && (fstatSync(3).isSocket() || fstatSync(3).isFIFO());\nconst clean = !JSON.stringify(process.argv).includes(token) && !JSON.stringify(process.env).includes(token);\nconst args = JSON.stringify(process.argv.slice(2)) === JSON.stringify(['--endpoint','https://example.test','--credential-fd','3']);\nconst input = readFileSync(0, 'utf8');\nprocess.stdout.write(JSON.stringify({jsonrpc:'2.0', id:1, result:{valid, clean, args, input}})+'\\n');\nprocess.stderr.write('synthetic diagnostic\\n');\nprocess.exit(valid && clean && args ? 23 : 42);\n`);
    await chmod(bridge, 0o700);
    await writeFile(parent, `import {launchRemoteMCP} from ${JSON.stringify(resolve(import.meta.dir, "../src/services/remote-mcp.service.ts"))};\nimport {RemoteProfiles} from ${JSON.stringify(resolve(import.meta.dir, "../src/services/remote.service.ts"))};\nconst values = new Map();\nconst store = {get: async (n) => values.has(n) ? {ok:true,value:values.get(n)} : {ok:false,error:{code:'credential_unavailable'}}, set: async (n,v) => {values.set(n,v);return {ok:true,value:undefined}}};\nawait new RemoteProfiles(store).register('p','https://EXAMPLE.test:443','a'.repeat(64));\nconst r = await launchRemoteMCP({profile:'p',endpoint:'https://EXAMPLE.test:443',credentials:store,executable:${JSON.stringify(bridge)}});\nprocess.exit(r.ok ? r.value : 99);\n`);
    const result = await new Promise<{ out: string; err: string; code: number | null }>((resolveResult, reject) => {
      const p = spawn(process.execPath, [parent], { stdio: ["pipe", "pipe", "pipe"] });
      let out = "", err = "";
      p.stdout.setEncoding("utf8").on("data", (s) => { out += s; });
      p.stderr.setEncoding("utf8").on("data", (s) => { err += s; });
      p.on("error", reject);
      p.on("close", (code) => resolveResult({ out, err, code }));
      p.stdin.end('{"jsonrpc":"2.0","id":1}\n');
    });
    expect(result.code).toBe(23);
    expect(result.err).toBe("synthetic diagnostic\n");
    expect(JSON.parse(result.out)).toEqual({ jsonrpc: "2.0", id: 1, result: { valid: true, clean: true, args: true, input: '{"jsonrpc":"2.0","id":1}\n' } });
    expect(result.out + result.err).not.toContain(bearer);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("launcher honors the default RemoteCredentials native namespace", async () => {
  const module = await import("../src/gateways/remote-credentials");
  const prototype = module.RemoteCredentials.prototype;
  const originalGet = prototype.get;
  const account = profileAccount("p", "https://example.test")!;
  let observed = "";
  prototype.get = async function (name) {
    observed = name;
    return { ok: true, value: bearer };
  };
  try {
    // No executable exists at this path: unavailable proves the class supplied
    // the credential and execution reached spawn, without touching OS secrets.
    expect(await launchRemoteMCP({ profile: "p", endpoint: "https://example.test", executable: "/definitely/not/a/kb-mcp" })).toEqual({ ok: false, error: { code: "unavailable" } });
    expect(observed).toBe(account);
  } finally { prototype.get = originalGet; }
});

test("malformed stored bearer and throwing credential backend fail closed", async () => {
  const store = new Store();
  const account = profileAccount("p", "https://example.test")!;
  store.values.set(account, "not-a-bearer");
  expect(await launchRemoteMCP({ profile: "p", endpoint: "https://example.test", credentials: store })).toEqual({ ok: false, error: { code: "credential_unavailable" } });
  const throwing: CredentialStore = { get: async () => { throw Error(bearer); }, set: async () => { throw Error(bearer); } };
  expect(await launchRemoteMCP({ profile: "p", endpoint: "https://example.test", credentials: throwing })).toEqual({ ok: false, error: { code: "credential_unavailable" } });
});
