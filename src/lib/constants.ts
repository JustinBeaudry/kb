import { homedir } from "node:os";
import { join } from "node:path";

export const VERSION = "0.4.0";
export const DEFAULT_VAULT_PATH = join(homedir(), "cairn");
export const STATE_FILE = ".cairn/state.json";
export const DEFAULT_BUDGET = 2048; // 2KB in bytes
export const VAULT_DIRS = ["wiki", "raw", "sessions"] as const;
export const VAULT_FILES = ["CAIRN.md", "index.md", "log.md", "context.md"] as const;
export const ENTIRE_CHECKPOINT_BRANCH = "entire/checkpoints/v1";
