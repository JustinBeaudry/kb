import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

export type InjectMode = "eager" | "lazy" | "off";

function parseMode(value: unknown): InjectMode | null {
  if (typeof value !== "string") return null;
  const normalized = value.trim().toLowerCase();
  return normalized === "eager" || normalized === "lazy" || normalized === "off" ? normalized : null;
}

export function resolveInjectMode(vaultPath: string, envValue: string | undefined): InjectMode {
  const fromEnv = parseMode(envValue);
  if (fromEnv) return fromEnv;

  const configPath = join(vaultPath, ".kb", "config.json");
  if (existsSync(configPath)) {
    try {
      const raw = readFileSync(configPath, "utf-8");
      const parsed: unknown = JSON.parse(raw);
      const fromConfig = parseMode(
        parsed !== null && typeof parsed === "object" && "inject_mode" in parsed
          ? parsed.inject_mode
          : undefined
      );
      if (fromConfig) return fromConfig;
    } catch {
      // fall through to default
    }
  }

  return "lazy";
}
