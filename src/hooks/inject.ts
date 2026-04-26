#!/usr/bin/env bun
import { existsSync } from "node:fs";
import { DEFAULT_BUDGET } from "../lib/constants";
import { buildEagerContext } from "../lib/inject/eager";
import { appendInjectLog } from "../lib/inject/log";
import { resolveInjectMode } from "../lib/inject/modes";
import { buildPointerPayload, byteLength } from "../lib/inject/pointer";
import { resolveVaultPath as resolveProjectVaultPath } from "../lib/vault";

function resolveVaultPath(argv: string[]): string {
  const arg = argv[0];
  if (arg) return arg;
  const env = process.env.CAIRN_VAULT;
  if (env) return env;
  return resolveProjectVaultPath(process.cwd());
}

function emitEmpty(): void {
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: {
        hookEventName: "SessionStart",
        additionalContext: "",
      },
    }) + "\n"
  );
}

function emitContext(context: string): void {
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: {
        hookEventName: "SessionStart",
        additionalContext: context,
      },
    }) + "\n"
  );
}

function countAdvertised(payload: string): number {
  const line = payload.split("\n").find((l) => l.startsWith("Topics:"));
  if (!line) return 0;
  return line
    .replace(/^Topics:\s*/, "")
    .replace(/\.\s*$/, "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean).length;
}

let emitted = false;

function emitOnce(context: string): void {
  if (emitted) return;
  emitted = true;
  emitContext(context);
}

function emitEmptyOnce(): void {
  if (emitted) return;
  emitted = true;
  emitEmpty();
}

async function main(): Promise<void> {
  const vaultPath = resolveVaultPath(process.argv.slice(2));
  const parsedBudget = Number(process.env.CAIRN_BUDGET ?? DEFAULT_BUDGET);
  const budget =
    Number.isFinite(parsedBudget) && parsedBudget > 0 ? parsedBudget : DEFAULT_BUDGET;

  if (!existsSync(vaultPath)) {
    emitEmptyOnce();
    return;
  }

  let mode: ReturnType<typeof resolveInjectMode>;
  try {
    mode = resolveInjectMode(vaultPath, process.env.CAIRN_INJECT_MODE);
  } catch {
    mode = "eager";
  }

  let context = "";
  let advertised = 0;
  try {
    if (mode === "off") {
      context = "";
    } else if (mode === "lazy") {
      context = buildPointerPayload({ vaultPath });
      advertised = countAdvertised(context);
    } else {
      context = buildEagerContext({ vaultPath, budget });
    }
  } catch {
    context = "";
  }

  emitOnce(context);

  try {
    await appendInjectLog(vaultPath, {
      timestamp: new Date().toISOString(),
      event: "inject",
      mode,
      bytes: byteLength(context),
      categories_advertised: advertised,
    });
  } catch {
    // logging must never fail the hook
  }
}

main().catch(() => {
  emitEmptyOnce();
  process.exit(0);
});
