import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join, relative, resolve } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath, checkVaultState } from "../lib/vault";
import { isQmdOnPath, isVaultRegistered, QMD_INSTALL_HINT } from "../lib/qmd";
import { isEntireOnPath } from "../lib/entire";
import { VERSION } from "../lib/constants";

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
      lines.push(line("ok", "entire not installed", "optional, cairn falls back to summary-only sessions"));
    }

    lines.push("");
    lines.push("Trust boundary (best-effort + detection)");
    lines.push(
      line(
        "warn",
        "run the security self-test manually",
        "bash <plugin>/hooks/security-self-test — host-level deny rules are not simulatable from a CLI"
      )
    );
    lines.push(
      line(
        "ok",
        "sanctioned retrieval paths",
        "cairn recall / cairn get / cairn list-topics (curated), cairn read-raw / cairn read-session (ask-gated)"
      )
    );

    if (!vaultMatchesDefaultDenyGlobs(vaultPath)) {
      warnings++;
      lines.push(
        line(
          "warn",
          "vault path outside default deny globs",
          `${vaultPath} does not match **/cairn/** or ~/cairn/** — shipped .claude/settings.json deny rules will not fire. Add project-scoped deny entries or move the vault under ~/cairn.`
        )
      );
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
  let newest: { path: string; mtimeMs: number } | null = null;
  for (const root of roots) {
    const dir = join(vaultPath, root);
    if (!existsSync(dir)) continue;
    walkForMarkdown(dir, (file) => {
      const stat = statSync(file);
      if (!newest || stat.mtimeMs > newest.mtimeMs) {
        newest = { path: file.replace(`${vaultPath}/`, ""), mtimeMs: stat.mtimeMs };
      }
    });
  }
  return newest;
}

function vaultMatchesDefaultDenyGlobs(vaultPath: string): boolean {
  // Deny rules are shipped for any path containing /cairn/ as a segment
  // and for ~/cairn/**. If the resolved vault path satisfies neither,
  // the host-level enforcement is silently inactive.
  //
  // Uses path.relative to compare against the default ~/cairn root in a
  // separator-aware way (works on POSIX and Windows). For the segment
  // check, splits on both "/" and the platform separator to catch both
  // normalized and non-normalized inputs.
  const resolvedVault = resolve(vaultPath);
  const defaultRoot = resolve(join(homedir(), "cairn"));
  const rel = relative(defaultRoot, resolvedVault);
  if (rel === "" || (!rel.startsWith("..") && !isAbsolute(rel))) {
    return true;
  }
  const segments = resolvedVault.split(/[\\/]/).filter(Boolean);
  return segments.includes("cairn");
}

function walkForMarkdown(dir: string, onFile: (file: string) => void): void {
  for (const entry of readdirSync(dir)) {
    if (entry.startsWith(".")) continue;
    const full = join(dir, entry);
    const stat = statSync(full);
    if (stat.isDirectory()) {
      walkForMarkdown(full, onFile);
    } else if (entry.endsWith(".md")) {
      onFile(full);
    }
  }
}
