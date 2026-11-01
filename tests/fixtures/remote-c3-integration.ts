// Test-only executable: invoke the production citty remote command in a fresh
// process while substituting an in-memory native credential store. The synthetic
// bearer arrives over stdin, never through argv, environment, or a real keychain.
import { runCommand } from "citty";
import type { CaptureError, CredentialStore, Result } from "../../src/capture/types";
import { createRemoteCommand } from "../../src/commands/remote";
import { HTTPMemoryGateway } from "../../src/gateways/memory.gateway";
import { RemoteProfiles } from "../../src/services/remote.service";

class EphemeralCredentials implements CredentialStore {
  private readonly entries = new Map<string, string>();
  async get(account: string): Promise<Result<string, CaptureError>> {
    const token = this.entries.get(account);
    return token ? { ok: true, value: token } : { ok: false, error: { code: "credential_unavailable" } };
  }
  async set(account: string, token: string): Promise<Result<void, CaptureError>> {
    this.entries.set(account, token);
    return { ok: true, value: undefined };
  }
}

// SAFETY: only the test harness supplies this JSON; it controls every field.
const input = JSON.parse(await Bun.stdin.text()) as { origin: string; token: string; args: string[]; register: boolean };
const credentials = new EphemeralCredentials();
if (input.register) {
  const registration = await new RemoteProfiles(credentials).register("fixture", input.origin, input.token);
  if (!registration.ok) throw new Error("synthetic profile registration failed");
}
const command = createRemoteCommand({
  credentials,
  gateway: (origin, token) => new HTTPMemoryGateway(origin, token),
  readCredential: async () => { throw new Error("unexpected enrollment"); },
});
await runCommand(command, { rawArgs: [...input.args, "--profile", "fixture", "--endpoint", input.origin] });
