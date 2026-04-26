import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

export type InjectMode = "eager" | "lazy" | "off";

const VALID_MODES: ReadonlySet<InjectMode> = new Set(["eager", "lazy", "off"]);

function parseMode(value: unknown): InjectMode | null {
  if (typeof value !== "string") return null;
  const normalized = value.trim().toLowerCase();
  return VALID_MODES.has(normalized as InjectMode) ? (normalized as InjectMode) : null;
}

export function resolveInjectMode(vaultPath: string, envValue: string | undefined): InjectMode {
  const fromEnv = parseMode(envValue);
  if (fromEnv) return fromEnv;

  const configPath = join(vaultPath, ".cairn", "config.json");
  if (existsSync(configPath)) {
    try {
      const raw = readFileSync(configPath, "utf-8");
      const parsed = JSON.parse(raw) as { inject_mode?: unknown };
      const fromConfig = parseMode(parsed.inject_mode);
      if (fromConfig) return fromConfig;
    } catch {
      // fall through to default
    }
  }

  return "eager";
}
