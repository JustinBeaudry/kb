import { defineCommand } from "citty";
import { readFileSync, writeFileSync } from "node:fs";
import type { CredentialStore } from "../capture/types";
import { HTTPTransferGateway } from "../gateways/memory.gateway";
import { RemoteCredentials } from "../gateways/remote-credentials";
import { FileBundleWriter, FileVaultRepository } from "../repositories/vault.repository";
import { profileAccount } from "../services/remote.service";
import { TransferService, type ImportManifest, type TransferError, type TransferGateway } from "../services/transfer.service";

const actions: Record<TransferError["code"], string> = {
  invalid_input: "Check the command arguments: explicit vault, project, task and selected file IDs.",
  approval_stale: "The manifest, selected sources or policy changed since approval. Run preview again and approve the new digest.",
  policy_paused: "Capture policy is paused for this project; nothing was read or uploaded.",
  vault_unavailable: "The vault path is missing or unreadable.",
  output_exists: "Choose a new, empty output path, or pass --resume to continue an interrupted export.",
  credential_unavailable: "Register both profiles for this origin using kb remote credential-set.",
  unauthorized: "Credential rejected or revoked; enroll and register a current client credential.",
  forbidden: "This client lacks the required capability or project grant.",
  stale: "Authorization or the export cursor changed; restart without --resume.",
  conflict: "Refresh state before retrying.",
  deleted: "The requested record was deleted.",
  incomplete: "Evidence is incomplete; retry later.",
  budget: "The response exceeded its bound.",
  unavailable: "Remote service or response unavailable; no local fallback was used. Retry to resume.",
};
function report(code: TransferError["code"]): void {
  process.stderr.write(JSON.stringify({ error: { code, action: actions[code] } }) + "\n");
  process.exitCode = 1;
}
function emit(value: unknown): void {
  process.stdout.write(JSON.stringify(value, null, 2) + "\n");
}

export type TransferDependencies = {
  credentials: CredentialStore;
  gateway: (origin: string, agentToken: string, evidenceToken: string | undefined) => TransferGateway;
};

async function token(credentials: CredentialStore, profile: string | undefined, endpoint: string): Promise<string | undefined> {
  if (!profile) return undefined;
  const account = profileAccount(profile, endpoint);
  if (!account) return undefined;
  const result = await credentials.get(account);
  return result.ok ? result.value : undefined;
}

export function createTransferCommand(dependencies: TransferDependencies = { credentials: new RemoteCredentials(), gateway: (o, a, e) => new HTTPTransferGateway(o, a, e) }) {
  return defineCommand({
    meta: { name: "transfer", description: "Selected import of local vault files as remote candidates, and portable export. Never rewrites vault originals." },
    args: {
      action: { type: "positional", required: true, description: "inventory, preview, apply, or export" },
      vault: { type: "string", description: "inventory/preview: explicit vault path" },
      project: { type: "string", description: "Project binding" },
      task: { type: "string", description: "Task binding (required for preview: imports are task-scoped candidates)" },
      session: { type: "string", description: "Optional session binding" },
      select: { type: "string", description: "preview: comma-separated vault file IDs from inventory" },
      "include-raw": { type: "boolean", description: "preview: allow selected raw/ files (default off)" },
      "include-sessions": { type: "boolean", description: "preview: allow selected sessions/ files (default off)" },
      manifest: { type: "string", description: "preview: new manifest output file; apply: approved manifest" },
      approve: { type: "string", description: "apply: approval digest printed by preview" },
      out: { type: "string", description: "export: new output directory" },
      resume: { type: "boolean", description: "export: continue an interrupted export in --out" },
      profile: { type: "string", description: "Agent profile (policy:read, candidate:write, evidence:read, memory:export)" },
      "evidence-profile": { type: "string", description: "apply: collector profile with artifacts:write" },
      endpoint: { type: "string", description: "Registered HTTPS origin" },
    },
    async run({ args }) {
      try {
        const action = args.action;
        if (action === "inventory") {
          if (!args.vault) return report("invalid_input");
          const vault = FileVaultRepository.open(args.vault);
          if (!vault.ok) return report("vault_unavailable");
          const inventory = await vault.value.inventory();
          if (!inventory.ok) return report("vault_unavailable");
          return emit({ vault: vault.value.root, entries: inventory.value });
        }
        if (!["preview", "apply", "export"].includes(action) || !args.profile || !args.endpoint) return report("invalid_input");
        const agent = await token(dependencies.credentials, args.profile, args.endpoint);
        const evidence = await token(dependencies.credentials, args["evidence-profile"], args.endpoint);
        if (!agent || (action === "apply" && !evidence)) return report("credential_unavailable");
        const service = new TransferService(dependencies.gateway(args.endpoint, agent, evidence), (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));
        if (action === "preview") {
          if (!args.vault || !args.project || !args.task || !args.select || !args.manifest) return report("invalid_input");
          const result = await service.preview({ vaultPath: args.vault, scope: { project_id: args.project, task_id: args.task, ...(args.session ? { session_id: args.session } : {}) }, fileIds: args.select.split(","), includeRaw: !!args["include-raw"], includeSessions: !!args["include-sessions"] });
          if (!result.ok) return report(result.error.code);
          writeFileSync(args.manifest, JSON.stringify(result.value, null, 2) + "\n", { flag: "wx", mode: 0o600 });
          return emit({ approval_digest: result.value.digest, items: result.value.items.map((i) => ({ file_id: i.file_id, kind: i.kind, digest: i.digest, bytes: i.bytes })), omissions: result.value.omissions });
        }
        if (action === "apply") {
          if (!args.manifest || !args.approve) return report("invalid_input");
          // SAFETY: TransferService.apply validates shape and recomputes the digest.
          const manifest = JSON.parse(readFileSync(args.manifest, "utf8")) as ImportManifest;
          const result = await service.apply(manifest, args.approve);
          if (!result.ok) return report(result.error.code);
          emit(result.value);
          if (!result.value.complete) process.exitCode = 2;
          return;
        }
        if (!args.project || !args.out) return report("invalid_input");
        const result = await service.export({ project_id: args.project, ...(args.task ? { task_id: args.task } : {}), ...(args.session ? { session_id: args.session } : {}) }, args.out, { resume: !!args.resume });
        if (!result.ok) return report(result.error.code);
        emit(result.value);
      } catch {
        report("invalid_input");
      }
    },
  });
}

export default createTransferCommand();
