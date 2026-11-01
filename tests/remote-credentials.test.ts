import { expect, test } from "bun:test";
import { NativeCredentials } from "../src/capture/credentials";
import { RemoteCredentials } from "../src/gateways/remote-credentials";
import { RemoteProfiles, profileAccount } from "../src/services/remote.service";
import { HTTPMemoryGateway } from "../src/gateways/memory.gateway";

test("native secret services isolate origin-bound remote bearer from capture profiles", async () => {
  const entries = new Map<string, string>();
  const calls: { service: string; name: string }[] = [];
  // Implements the Bun.secrets get/set interface, keyed by both OS service and name.
  const secrets = {
    async get(options: { service: string; name: string }) { calls.push(options); return entries.get(JSON.stringify([options.service, options.name])) ?? null; },
    async set(options: { service: string; name: string; value: string }) { calls.push(options); entries.set(JSON.stringify([options.service, options.name]), options.value); },
  };
  const capture = new NativeCredentials(secrets);
  const remote = new RemoteProfiles(new RemoteCredentials(secrets));
  const token = "a".repeat(64), origin = "https://example.com";
  const account = profileAccount("p", origin)!;
  expect((await remote.register("p", origin, token)).ok).toBe(true);
  expect(await capture.get(account)).toMatchObject({ error: { code: "credential_unavailable" } });
  expect((await capture.set(account, "b".repeat(64))).ok).toBe(true);
  let received = "";
  const connect = (url: string, bearer: string) => { received = bearer; return new HTTPMemoryGateway(url, bearer); };
  expect((await remote.connect("p", origin, connect)).ok).toBe(true);
  expect(received).toBe(token);
  expect((await remote.connect("p", "https://evil.example", connect)).ok).toBe(false);
  expect(calls.map((c) => c.service)).toContain("com.beaudry.kb.capture");
  expect(calls.map((c) => c.service)).toContain("com.beaudry.kb.remote");
});
