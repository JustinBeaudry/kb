import { existsSync, readFileSync, readdirSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { DEFAULT_VAULT_PATH, VAULT_DIRS, VAULT_FILES, VERSION } from "./constants";
import { getCairnMdTemplate, INDEX_MD_STUB, LOG_MD_STUB, CONTEXT_MD_STUB } from "./templates";

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

interface ScaffoldResult {
  created: string[];
  skipped: string[];
}

const FILE_CONTENT: Record<string, string> = {
  "CAIRN.md": getCairnMdTemplate(),
  "index.md": INDEX_MD_STUB,
  "log.md": LOG_MD_STUB,
  "context.md": CONTEXT_MD_STUB,
};

export function scaffoldVault(vaultPath: string): ScaffoldResult {
  const created: string[] = [];
  const skipped: string[] = [];

  if (!existsSync(vaultPath)) {
    mkdirSync(vaultPath, { recursive: true });
    created.push(vaultPath);
  }

  for (const dir of VAULT_DIRS) {
    const dirPath = join(vaultPath, dir);
    if (existsSync(dirPath)) {
      skipped.push(dir + "/");
    } else {
      mkdirSync(dirPath, { recursive: true });
      created.push(dir + "/");
    }
  }

  for (const file of VAULT_FILES) {
    const filePath = join(vaultPath, file);
    if (existsSync(filePath)) {
      skipped.push(file);
    } else {
      writeFileSync(filePath, FILE_CONTENT[file]!);
      created.push(file);
    }
  }

  const statePath = join(vaultPath, ".cairn", "state.json");
  const stateDir = join(vaultPath, ".cairn");
  if (existsSync(statePath)) {
    skipped.push(".cairn/state.json");
  } else {
    mkdirSync(stateDir, { recursive: true });
    writeFileSync(
      statePath,
      JSON.stringify(
        {
          version: VERSION,
          vaultPath,
          createdAt: new Date().toISOString(),
        },
        null,
        2
      )
    );
    created.push(".cairn/state.json");
  }

  // Fresh installs default to lazy inject mode so SessionStart stays
  // under the pointer-payload budget. Existing vaults (with a state
  // file already present above) are left alone to preserve behavior.
  const configPath = join(vaultPath, ".cairn", "config.json");
  if (existsSync(configPath)) {
    skipped.push(".cairn/config.json");
  } else {
    writeFileSync(
      configPath,
      JSON.stringify({ inject_mode: "lazy" }, null, 2) + "\n"
    );
    created.push(".cairn/config.json");
  }

  return { created, skipped };
}
