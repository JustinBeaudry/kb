import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { DEFAULT_VAULT_PATH } from "./constants";

export type VaultState = "empty" | "cairn" | "obsidian" | "occupied";

export function resolveVaultPath(projectDir?: string): string {
  const envPath = process.env.CAIRN_VAULT;
  if (envPath) return envPath;

  if (projectDir) {
    const dotCairn = join(projectDir, ".cairn");
    if (existsSync(dotCairn)) {
      try {
        const content = readFileSync(dotCairn, "utf-8").trim();
        if (content) return content;
      } catch {
        // Fall through to default
      }
    }
  }

  return DEFAULT_VAULT_PATH;
}

export function checkVaultState(vaultPath: string): VaultState {
  if (!existsSync(vaultPath)) return "empty";
  if (existsSync(join(vaultPath, ".cairn", "state.json"))) return "cairn";
  if (existsSync(join(vaultPath, ".obsidian"))) return "obsidian";
  const entries = readdirSync(vaultPath);
  if (entries.length === 0) return "empty";
  return "occupied";
}
