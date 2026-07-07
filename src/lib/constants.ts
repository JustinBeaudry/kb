import { homedir } from "node:os";
import { join } from "node:path";
// package.json is inlined at build time (Bun JSON import), so compiled
// binaries carry the right version and it can never drift from the release.
import pkg from "../../package.json";

export const VERSION: string = pkg.version;
export const DEFAULT_VAULT_PATH = join(homedir(), "kb");
export const KB_DIR = ".kb";
export const STATE_FILE = ".kb/state.json";
export const CAPTURE_ERRORS_LOG = ".kb/capture-errors.log";
export const DEFAULT_BUDGET = 32768; // 32KB; sized for vaults with ~20KB index + recent session headroom
export const VAULT_DIRS = [
  "wiki",
  "raw",
  "sessions",
  "sessions/summaries",
  "sessions/.trash",
] as const;
export const VAULT_FILES = ["KB.md", "index.md", "log.md", "context.md"] as const;
export const ENTIRE_CHECKPOINT_BRANCH = "entire/checkpoints/v1";
