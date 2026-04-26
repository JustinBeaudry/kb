import { join } from "node:path";
import { appendMinimalJsonl } from "../log-writer";
import type { InjectMode } from "./modes";

export interface InjectLogEntry {
  timestamp: string;
  event: "inject";
  mode: InjectMode;
  bytes: number;
  categories_advertised: number;
}

export async function appendInjectLog(vaultPath: string, entry: InjectLogEntry): Promise<void> {
  await appendMinimalJsonl(entry as unknown as Record<string, unknown>, {
    logPath: join(vaultPath, ".cairn", "inject-log.jsonl"),
    lockPath: join(vaultPath, ".cairn", "inject-log.jsonl.lock"),
  });
}
