import { defineCommand } from "citty";
import { RemoteCredentials } from "../gateways/remote-credentials";
import type { CredentialStore } from "../capture/types";
import { HTTPMemoryGateway } from "../gateways/memory.gateway";
import { renderRemoteFrame, type Applicability } from "../lib/remote-envelope";
import { launchRemoteMCP } from "../services/remote-mcp.service";
import { RemoteProfiles, type MemoryGateway, type RemoteError, type RemoteResult, type RetrievalInput } from "../services/remote.service";

const actions: Record<RemoteError["code"], string> = {
  invalid_input: "Check the command arguments and explicit profile and origin.",
  credential_unavailable: "Register this profile and origin using credential-set with the bearer on stdin.",
  unauthorized: "Credential rejected or revoked; enroll and register a current client credential.",
  forbidden: "This client lacks the requested scope or capability.",
  stale: "Repeat the original request without its continuation.",
  conflict: "Refresh state before retrying.",
  deleted: "The requested evidence was deleted.",
  incomplete: "Evidence is incomplete; retry after capture is complete.",
  budget: "The complete output cannot fit; increase the byte budget within the command limit.",
  unavailable: "Remote service or response unavailable; no local fallback was used.",
};
function report(error: RemoteError): void {
  process.stderr.write(JSON.stringify({ error: { code: error.code, action: actions[error.code] } }) + "\n");
  process.exitCode = 1;
}
function integer(value: string | undefined, fallback: number | undefined): number | undefined {
  if (value === undefined) return fallback;
  if (!/^(0|[1-9][0-9]*)$/.test(value)) throw Error("integer");
  const n = Number(value);
  if (!Number.isSafeInteger(n)) throw Error("integer");
  return n;
}
async function readCredential(): Promise<string> {
  const reader = Bun.stdin.stream().getReader();
  const parts: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const p = await reader.read();
      if (p.done) break;
      size += p.value.byteLength;
      if (size > 66) throw Error("credential input");
      parts.push(p.value);
    }
    return new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(parts)).replace(/\r?\n$/, "");
  } finally { await reader.cancel().catch(() => {}); }
}
function outcome<T>(result: RemoteResult<T>): result is { ok: true; value: T } {
  if (!result.ok) { report(result.error); return false; }
  return true;
}
export function createRemoteCommand(dependencies: {
  credentials: CredentialStore;
  gateway: (origin: string, token: string) => MemoryGateway;
  readCredential: () => Promise<string>;
} = { credentials: new RemoteCredentials(), gateway: (origin, token) => new HTTPMemoryGateway(origin, token), readCredential }) {
  return defineCommand({
  meta: { name: "remote", description: "Explicit authenticated remote memory; never falls back to the local vault" },
  args: {
    action: { type: "positional", required: false, description: "Required: credential-set (64-hex bearer on stdin), orient, search, fetch, status, or mcp" },
    profile: { type: "string", required: false, description: "Required: explicit profile name" },
    endpoint: { type: "string", required: false, description: "Required: registered HTTPS origin; HTTP only on 127.0.0.1 or [::1]" },
    project: { type: "string", description: "Project ID (required for orient/search and working fetch)" },
    task: { type: "string", description: "Task ID (required for working mode)" },
    session: { type: "string", description: "Optional session ID" },
    query: { type: "string", description: "Retrieval query" },
    entities: { type: "string", description: "Comma-separated entity ID hints" },
    applicability: { type: "string", description: "JSON applicability constraints" },
    mode: { type: "string", description: "orient/search: orientation, evidence, working; fetch: publication, working" },
    id: { type: "string", description: "Fetch artifact ID" },
    locator: { type: "string", description: "Exact base locator of the cited artifact" },
    budget: { type: "string", description: "Complete framed output byte limit (orient/status <=4096; search/fetch <=16384)" },
    depth: { type: "string", description: "Maximum graph depth, 0..2" },
    visited: { type: "string", description: "Maximum visited records, 1..50" },
    start: { type: "string", description: "Fetch inclusive stored-artifact byte offset" },
    end: { type: "string", description: "Fetch exclusive stored-artifact byte offset" },
    continuation: { type: "string", description: "Opaque CLI continuation; repeat arguments and budget (fetch: omit --start)" },
  },
  async run({ args, rawArgs }) {
    try {
      const action = args.action;
      const known = new Set(["_", "action", "profile", "endpoint", "project", "task", "session", "query", "entities", "applicability", "mode", "id", "locator", "budget", "depth", "visited", "start", "end", "continuation"]);
      if (!action || !["credential-set", "orient", "search", "fetch", "status", "mcp"].includes(action) || !args.profile || !args.endpoint || args._.length !== 1 || Object.keys(args).some((key) => !known.has(key))) throw Error("command");
      if (action === "mcp") {
        // Citty normalizes aliases and accepts repeated or negated flags. Inspect
        // the original argv so no additional option can slip through unnoticed.
        const options = new Set<string>();
        for (let i = 1; i < rawArgs.length; i++) {
          const match = /^--(profile|endpoint)(?:=(.*))?$/.exec(rawArgs[i]!);
          if (!match || options.has(match[1]!)) throw Error("mcp options");
          options.add(match[1]!);
          if (match[2] === undefined) {
            const value = rawArgs[++i];
            if (!value || value.startsWith("-")) throw Error("mcp option value");
          } else if (!match[2]) throw Error("mcp option value");
        }
        if (rawArgs[0] !== "mcp" || options.size !== 2 || typeof args.profile !== "string" || typeof args.endpoint !== "string") throw Error("mcp options");
        const result = await launchRemoteMCP({ profile: args.profile, endpoint: args.endpoint, credentials: dependencies.credentials });
        if (outcome(result)) process.exitCode = result.value;
        return;
      }
      const profiles = new RemoteProfiles(dependencies.credentials);
      if (action === "credential-set") {
        if (Object.keys(args).some((key) => !["_", "action", "profile", "endpoint"].includes(key)) || args._.length !== 1) throw Error("registration options");
        const result = await profiles.register(args.profile, args.endpoint, await dependencies.readCredential());
        if (outcome(result)) process.stdout.write(JSON.stringify({ ok: true }) + "\n");
        return;
      }
      // Validate action-specific options before touching the credential store.
      const has = (...keys: (keyof typeof args)[]) => keys.some((k) => args[k] !== undefined);
      if (action === "status" && has("project", "task", "session", "query", "entities", "applicability", "mode", "id", "locator", "depth", "visited", "start", "end", "continuation")) throw Error("status options");
      if (action === "fetch" && (!args.id || !args.locator || has("query", "entities", "applicability", "depth", "visited"))) throw Error("fetch options");
      if ((action === "orient" || action === "search") && (!args.project || has("id", "locator", "start", "end"))) throw Error("retrieval options");
      const budget = integer(args.budget, action === "orient" || action === "status" ? 4096 : 16384)!;
      if (budget < 1 || budget > (action === "orient" || action === "status" ? 4096 : 16384)) throw Error("budget");
      if (action === "fetch" && ((args.mode !== undefined && args.mode !== "publication" && args.mode !== "working") || (args.mode === "working" ? !args.project || !args.task : has("project", "task", "session")))) throw Error("fetch mode");
      if ((action === "orient" || action === "search") && ((args.mode !== undefined && args.mode !== "orientation" && args.mode !== "evidence" && args.mode !== "working") || (args.mode === "working" && !args.task))) throw Error("retrieval mode");
      const depth = integer(args.depth, undefined), visited = integer(args.visited, undefined);
      const start = integer(args.start, undefined), end = integer(args.end, undefined);
      if ((depth !== undefined && depth > 2) || (visited !== undefined && (visited < 1 || visited > 50)) || (start !== undefined && start > 8 * 1024 * 1024) || (end !== undefined && (end > 8 * 1024 * 1024 || end < (start ?? 0))) || (args.continuation !== undefined && (start ?? 0) !== 0)) throw Error("bounds");
      let applicability: Applicability | undefined;
      if (args.applicability !== undefined) {
        const parsed: unknown = JSON.parse(args.applicability);
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw Error("applicability");
        // SAFETY: RemoteService validates every applicability field before use.
        applicability = parsed as Applicability;
      }
      const connected = await profiles.connect(args.profile, args.endpoint, dependencies.gateway);
      if (!outcome(connected)) return;
      const service = connected.value;
      if (action === "status") {
        const result = await service.status(budget);
        if (outcome(result)) process.stdout.write(renderRemoteFrame(result.value));
      } else if (action === "fetch") {
        if (args.mode !== undefined && args.mode !== "publication" && args.mode !== "working") throw Error("fetch mode");
        const result = await service.fetch(args.id!, args.locator!, budget, { mode: args.mode, project_id: args.project, task_id: args.task, session_id: args.session, start_byte: start, end_byte: end, continuation: args.continuation });
        if (outcome(result)) process.stdout.write(renderRemoteFrame(result.value));
      } else {
        if (args.mode !== undefined && args.mode !== "orientation" && args.mode !== "evidence" && args.mode !== "working") throw Error("retrieval mode");
        const input: RetrievalInput = { scope: { project_id: args.project!, task_id: args.task, session_id: args.session }, query: args.query, entity_ids: args.entities?.split(","), applicability, budget_bytes: budget, max_depth: depth, max_visited: visited, continuation: args.continuation, mode: args.mode };
        const result = action === "orient" ? await service.orient(input) : await service.search(input);
        if (outcome(result)) process.stdout.write(renderRemoteFrame(result.value));
      }
    } catch { report({ code: "invalid_input" }); }
  },
  });
}
export default createRemoteCommand();
