import { existsSync, readFileSync, readdirSync, statSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath, checkVaultState } from "../lib/vault";
import { isQmdOnPath, isVaultRegistered, QMD_INSTALL_HINT } from "../lib/qmd";
import { isEntireOnPath } from "../lib/entire";
import { CAPTURE_ERRORS_LOG, MIGRATION_JOURNAL, VERSION } from "../lib/constants";
import { parseFrontmatter } from "../lib/frontmatter";
import { validateManifest } from "../lib/manifest";

type Status = "ok" | "warn" | "error";

const GLYPH: Record<Status, string> = {
  ok: "✓",
  warn: "!",
  error: "✗",
};

function line(status: Status, label: string, detail?: string): string {
  const suffix = detail ? ` — ${detail}` : "";
  return `  ${GLYPH[status]} ${label}${suffix}`;
}

export default defineCommand({
  meta: { name: "doctor", description: "Report cairn vault + dependency health" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const lines: string[] = [];
    let warnings = 0;
    let errors = 0;

    lines.push(`cairn doctor — v${VERSION}`);
    lines.push("");
    lines.push("Vault");

    const state = checkVaultState(vaultPath);
    if (state === "cairn") {
      lines.push(line("ok", "vault initialized", vaultPath));

      const statePath = join(vaultPath, ".cairn", "state.json");
      try {
        const parsed = JSON.parse(readFileSync(statePath, "utf-8"));
        lines.push(line("ok", "state.json readable", `created ${parsed.createdAt ?? "unknown"}`));
      } catch (err) {
        errors++;
        lines.push(line("error", "state.json unreadable", String(err)));
      }

      const sessionsDir = join(vaultPath, "sessions");
      if (existsSync(sessionsDir)) {
        const newestMd = findNewestMarkdown(vaultPath);
        if (newestMd) {
          const days = Math.floor((Date.now() - newestMd.mtimeMs) / 86400000);
          lines.push(
            line(
              days > 30 ? "warn" : "ok",
              "newest wiki page",
              `${newestMd.path} (${days}d ago)`
            )
          );
          if (days > 30) warnings++;
        }
      }

      const sessionHealth = await collectSessionHealth(vaultPath);
      warnings += sessionHealth.warnings;
      errors += sessionHealth.errors;
      lines.push(...sessionHealth.lines);
    } else {
      errors++;
      lines.push(line("error", `vault state: ${state}`, `run 'cairn init' at ${vaultPath}`));
    }

    lines.push("");
    lines.push("qmd (optional hybrid search)");

    if (isQmdOnPath()) {
      lines.push(line("ok", "qmd binary on PATH"));
      const registered = await isVaultRegistered(vaultPath);
      if (registered) {
        lines.push(line("ok", "vault registered as qmd collection"));
      } else {
        warnings++;
        lines.push(
          line(
            "warn",
            "vault not registered",
            `run 'qmd collection add ${vaultPath} --name cairn --mask "**/*.md" && qmd embed'`
          )
        );
      }
    } else {
      warnings++;
      lines.push(line("warn", "qmd not installed", "hybrid search disabled, fallback to index.md walk"));
    }

    lines.push("");
    lines.push("entire (session capture)");

    if (await isEntireOnPath()) {
      lines.push(line("ok", "entire binary on PATH"));
    } else {
      lines.push(line("ok", "entire not installed", "optional, cairn falls back to manifest-only sessions"));
    }

    lines.push("");
    if (errors > 0) {
      lines.push(`${errors} error(s), ${warnings} warning(s). Fix errors first.`);
    } else if (warnings > 0) {
      lines.push(`${warnings} warning(s). Vault is usable; address warnings to unlock full functionality.`);
      if (!isQmdOnPath()) {
        lines.push("");
        lines.push(QMD_INSTALL_HINT);
      }
    } else {
      lines.push("All checks passed.");
    }

    console.log(lines.join("\n"));
    if (errors > 0) process.exit(1);
  },
});

function findNewestMarkdown(vaultPath: string): { path: string; mtimeMs: number } | null {
  const roots = ["wiki", "sessions"];
  const skipDirs = new Set([
    join(vaultPath, "sessions", "summaries"),
    join(vaultPath, "sessions", ".trash"),
    join(vaultPath, ".cairn"),
  ]);
  let newest: { path: string; mtimeMs: number } | null = null;
  for (const root of roots) {
    const dir = join(vaultPath, root);
    if (!existsSync(dir)) continue;
    walkForMarkdown(dir, (file) => {
      const stat = statSync(file);
      if (!newest || stat.mtimeMs > newest.mtimeMs) {
        newest = { path: file.replace(`${vaultPath}/`, ""), mtimeMs: stat.mtimeMs };
      }
    }, skipDirs);
  }
  return newest;
}

function walkForMarkdown(
  dir: string,
  onFile: (file: string) => void,
  skipDirs: Set<string> = new Set()
): void {
  for (const entry of readdirSync(dir)) {
    if (entry.startsWith(".")) continue;
    const full = join(dir, entry);
    if (skipDirs.has(full)) continue;
    const stat = statSync(full);
    if (stat.isDirectory()) {
      walkForMarkdown(full, onFile, skipDirs);
    } else if (entry.endsWith(".md")) {
      onFile(full);
    }
  }
}

async function collectSessionHealth(vaultPath: string): Promise<{
  lines: string[];
  warnings: number;
  errors: number;
}> {
  const lines: string[] = [];
  let warnings = 0;
  let errors = 0;

  const sessionsDir = join(vaultPath, "sessions");
  const summariesDir = join(sessionsDir, "summaries");
  const trashDir = join(sessionsDir, ".trash");

  const recentErrors = countRecentCaptureErrors(join(vaultPath, CAPTURE_ERRORS_LOG));
  if (recentErrors > 0) {
    warnings++;
    lines.push(line("warn", `${recentErrors} capture errors in last 7 days`, CAPTURE_ERRORS_LOG));
  }

  if (existsSync(join(vaultPath, MIGRATION_JOURNAL))) {
    warnings++;
    lines.push(
      line("warn", "migration in progress", "run 'cairn migrate-sessions --apply' to resume or delete journal")
    );
  }

  if (!bunResolvableFromHookPath()) {
    warnings++;
    lines.push(line("warn", "bun not found on hook PATH", "Stop hook will fail"));
  }

  const removedLocks = removeStaleSessionLocks(join(vaultPath, ".cairn", "sessions"));
  if (removedLocks > 0) {
    lines.push(line("ok", "removed stale session lockfiles", String(removedLocks)));
  }

  const sessionFiles = existsSync(sessionsDir)
    ? readdirSync(sessionsDir).filter((entry) => entry.endsWith(".md"))
    : [];
  let manifests = 0;
  let legacy = 0;
  let malformed = 0;

  for (const file of sessionFiles) {
    const path = join(sessionsDir, file);
    try {
      const content = readFileSync(path, "utf-8");
      const { data } = parseFrontmatter<Record<string, unknown>>(content);
      if ("manifest_hash" in data) {
        manifests++;
        try {
          validateManifest(data);
        } catch {
          malformed++;
        }
      } else {
        legacy++;
      }
    } catch {
      malformed++;
    }
  }

  const summaries = existsSync(summariesDir)
    ? readdirSync(summariesDir).filter((entry) => entry.endsWith(".md")).length
    : 0;
  const trashed = countMarkdownFiles(trashDir);

  lines.push(line("ok", "session manifests", String(manifests)));
  lines.push(line("ok", "cached summaries", String(summaries)));
  lines.push(line("ok", "trashed sessions", String(trashed)));

  if (legacy > 0) {
    warnings++;
    lines.push(line("warn", `${legacy} legacy session files detected`, "run 'cairn migrate-sessions' to migrate"));
  }
  if (malformed > 0) {
    warnings++;
    lines.push(line("warn", "manifest missing required fields", `${malformed} file(s)`));
  }

  return { lines, warnings, errors };
}

function countRecentCaptureErrors(path: string): number {
  if (!existsSync(path)) return 0;
  const cutoff = Date.now() - 7 * 24 * 60 * 60 * 1000;
  let count = 0;
  for (const lineText of readFileSync(path, "utf-8").split("\n")) {
    if (lineText.trim().length === 0) continue;
    try {
      const parsed = JSON.parse(lineText) as { ts?: string };
      const ts = parsed.ts ? Date.parse(parsed.ts) : NaN;
      if (Number.isFinite(ts) && ts >= cutoff) count++;
    } catch {
      count++;
    }
  }
  return count;
}

function bunResolvableFromHookPath(): boolean {
  const path = `/usr/bin:/bin:/usr/local/bin:${process.env.HOME ?? ""}/.bun/bin`;
  const oldPath = process.env.PATH;
  try {
    process.env.PATH = path;
    return Bun.which("bun") !== null;
  } catch {
    return false;
  } finally {
    process.env.PATH = oldPath;
  }
}

function removeStaleSessionLocks(lockDir: string): number {
  if (!existsSync(lockDir)) return 0;
  const cutoff = Date.now() - 24 * 60 * 60 * 1000;
  let removed = 0;
  for (const entry of readdirSync(lockDir)) {
    if (!entry.endsWith(".lock")) continue;
    const path = join(lockDir, entry);
    if (lockCreatedAtMs(path) >= cutoff) continue;
    unlinkSync(path);
    removed++;
  }
  return removed;
}

function lockCreatedAtMs(path: string): number {
  try {
    const parsed = JSON.parse(readFileSync(path, "utf-8")) as { createdAt?: string };
    const createdAt = parsed.createdAt ? Date.parse(parsed.createdAt) : NaN;
    if (Number.isFinite(createdAt)) return createdAt;
  } catch {
    // Fall back to mtime for malformed lockfiles.
  }
  return statSync(path).mtimeMs;
}

function countMarkdownFiles(dir: string): number {
  if (!existsSync(dir)) return 0;
  let count = 0;
  walkForMarkdown(dir, () => count++);
  return count;
}
