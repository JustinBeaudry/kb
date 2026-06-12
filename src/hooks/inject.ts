#!/usr/bin/env bun
import { existsSync } from "node:fs";
import { DEFAULT_BUDGET } from "../lib/constants";
import { buildEagerContext } from "../lib/inject/eager";
import { appendInjectLog } from "../lib/inject/log";
import { resolveInjectMode } from "../lib/inject/modes";
import { buildPointerPayload } from "../lib/inject/pointer";
import { byteLength } from "../lib/bytes";
import { buildNudgeLine } from "../lib/session-state";
import { resolveVaultPath as resolveProjectVaultPath } from "../lib/vault";

function resolveVaultPath(argv: string[]): string {
  const arg = argv[0];
  if (arg) return arg;
  const env = process.env.KB_VAULT;
  if (env) return env;
  return resolveProjectVaultPath(process.cwd());
}

const DEFAULT_EVENT_NAME = "SessionStart";
// Pin the contract to the events this hook is registered for (hooks.json);
// anything else on stdin is treated as garbage.
const ALLOWED_EVENT_NAMES = new Set(["SessionStart", "PostCompact"]);

// Remembered for the top-level fail-soft path so a crash after event-name
// resolution still reports the real firing event.
let resolvedEventName = DEFAULT_EVENT_NAME;

/**
 * Claude Code hooks receive a JSON payload on stdin whose hook_event_name
 * identifies the firing event (SessionStart or PostCompact here). Tolerant
 * and non-blocking: TTY, absent, slow, or garbled stdin falls back to
 * SessionStart. The timer is cleared after the race so it never holds the
 * event loop open past emit.
 */
async function readHookEventName(): Promise<string> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    if (process.stdin.isTTY) return DEFAULT_EVENT_NAME;
    const text = await Promise.race([
      Bun.stdin.text(),
      new Promise<string>((resolve) => {
        timer = setTimeout(() => resolve(""), 250);
      }),
    ]);
    const parsed = JSON.parse(text) as { hook_event_name?: unknown };
    return typeof parsed.hook_event_name === "string" &&
      ALLOWED_EVENT_NAMES.has(parsed.hook_event_name)
      ? parsed.hook_event_name
      : DEFAULT_EVENT_NAME;
  } catch {
    return DEFAULT_EVENT_NAME;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

function emitEmpty(eventName: string): void {
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: {
        hookEventName: eventName,
        additionalContext: "",
      },
    }) + "\n"
  );
}

function emitContext(eventName: string, context: string): void {
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: {
        hookEventName: eventName,
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

function emitOnce(eventName: string, context: string): void {
  if (emitted) return;
  emitted = true;
  emitContext(eventName, context);
}

function emitEmptyOnce(eventName: string): void {
  if (emitted) return;
  emitted = true;
  emitEmpty(eventName);
}

async function main(): Promise<void> {
  const eventName = await readHookEventName();
  resolvedEventName = eventName;
  const vaultPath = resolveVaultPath(process.argv.slice(2));
  const parsedBudget = Number(process.env.KB_BUDGET ?? DEFAULT_BUDGET);
  const budget =
    Number.isFinite(parsedBudget) && parsedBudget > 0 ? parsedBudget : DEFAULT_BUDGET;

  if (!existsSync(vaultPath)) {
    emitEmptyOnce(eventName);
    return;
  }

  let mode: ReturnType<typeof resolveInjectMode>;
  try {
    mode = resolveInjectMode(vaultPath, process.env.KB_INJECT_MODE);
  } catch {
    mode = "eager";
  }

  let context = "";
  let advertised = 0;
  try {
    if (mode === "off") {
      context = "";
    } else {
      const nudge = buildNudgeLine(vaultPath);
      if (mode === "lazy") {
        context = buildPointerPayload({ vaultPath, nudge });
        advertised = countAdvertised(context);
      } else {
        context = buildEagerContext({ vaultPath, budget, nudge });
      }
    }
  } catch {
    context = "";
  }

  emitOnce(eventName, context);

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
  emitEmptyOnce(resolvedEventName);
  process.exit(0);
});
