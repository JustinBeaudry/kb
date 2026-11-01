import { spawn } from "node:child_process";
import { constants } from "node:os";
import { validBearer } from "../capture/credentials";
import type { CredentialStore } from "../capture/types";
import { canonicalOrigin } from "../gateways/memory.gateway";
import { RemoteCredentials } from "../gateways/remote-credentials";
import { profileAccount, type RemoteResult } from "./remote.service";

export type RemoteMCPOptions = {
  profile: string;
  endpoint: string;
  /** Injection for tests; production defaults to native credentials. */
  credentials?: CredentialStore;
  /** Path to the installed kb-mcp executable, or a trusted test executable. */
  executable?: string;
};

/**
 * Launch the stdio MCP bridge. FDs 0–2 are inherited without interpretation;
 * FD 3 is a private anonymous child-process pipe containing only the bearer.
 * No protocol bytes or credentials are emitted by this launcher. The caller
 * reports errors on stderr and adopts the returned exit code.
 */
export async function launchRemoteMCP(options: RemoteMCPOptions): Promise<RemoteResult<number>> {
  const account = profileAccount(options.profile, options.endpoint);
  const origin = canonicalOrigin(options.endpoint);
  if (!account || !origin) return { ok: false, error: { code: "invalid_input" } };

  let token: string;
  try {
    const result = await (options.credentials ?? new RemoteCredentials()).get(account);
    if (!result.ok || !validBearer(result.value)) return { ok: false, error: { code: "credential_unavailable" } };
    token = result.value;
  } catch {
    return { ok: false, error: { code: "credential_unavailable" } };
  }

  return new Promise((resolve) => {
    let child: ReturnType<typeof spawn>;
    try {
      child = spawn(options.executable ?? "kb-mcp", ["--endpoint", origin, "--credential-fd", "3"], {
        stdio: ["inherit", "inherit", "inherit", "pipe"],
      });
    } catch {
      resolve({ ok: false, error: { code: "unavailable" } });
      return;
    }
    let failed = false;
    let spawnFailed = false;
    const pipe = child.stdio[3];
    // A missing executable, an early child exit, and a broken pipe are all
    // sanitized: never expose OS errors that might contain sensitive data.
    child.on("error", () => { failed = true; spawnFailed = true; });
    if (!pipe || !("end" in pipe)) {
      failed = true;
      child.kill();
    } else {
      pipe.on("error", () => { failed = true; });
      pipe.end(token);
    }
    const onInterrupt = () => { child.kill("SIGINT"); };
    const onTerminate = () => { child.kill("SIGTERM"); };
    process.on("SIGINT", onInterrupt);
    process.on("SIGTERM", onTerminate);
    child.on("close", (code, signal) => {
      process.removeListener("SIGINT", onInterrupt);
      process.removeListener("SIGTERM", onTerminate);
      // Preserve a child's nonzero exit (or signal), even if it closed the
      // credential pipe early. A spawn failure has neither an exit nor signal.
      if (spawnFailed) resolve({ ok: false, error: { code: "unavailable" } });
      else if (code !== null && code !== 0) resolve({ ok: true, value: code });
      else if (signal && signal in constants.signals) resolve({ ok: true, value: 128 + constants.signals[signal as keyof typeof constants.signals] });
      else if (!failed && code === 0) resolve({ ok: true, value: 0 });
      else resolve({ ok: false, error: { code: "unavailable" } });
    });
  });
}
