import { defineCommand } from "citty";
import { join, resolve } from "node:path";
import { homedir } from "node:os";
import {
  CaptureConnectionService,
  CaptureService,
} from "../services/capture.service";
import { NativeCredentials } from "../capture/credentials";
import { DiskSpool } from "../capture/spool";
import { F4Uploader } from "../capture/uploader";
import {
  DEFAULT_SPOOL_BYTES,
  MAX_EVENT_BYTES,
  MAX_SPOOL_BYTES,
  type CaptureEvent,
  type CaptureUploadGateway,
  type CredentialStore,
} from "../capture/types";
import { claudeAdapter } from "../adapters/claude";
import { codexAdapter } from "../adapters/codex";
import { genericAdapter } from "../adapters/generic";
import type { HarnessAdapter } from "../adapters/types";
import { FileCursorStore, FsSourceReader } from "../capture/cursor";
import { CaptureWatcher } from "../capture/watcher";

const adapters: Record<string, HarnessAdapter> = {
  codex: codexAdapter,
  claude: claudeAdapter,
  generic: genericAdapter,
};
/** The poll action requires the profile to be named on the command line. */
const explicitProfile = (rawArgs: readonly string[]) =>
  rawArgs.some((a) => a === "--profile" || a.startsWith("--profile="));

export type CaptureCommandDependencies = {
  credentials: CredentialStore;
  uploader: (endpoint: string, token: string) => CaptureUploadGateway;
};
/** Explicit decimal byte count; no exponent, sign, or fraction. */
export function parseSpoolLimit(value: string | undefined): number {
  if (value === undefined) return DEFAULT_SPOOL_BYTES;
  const n = /^[1-9][0-9]{0,15}$/.test(value) ? Number(value) : Number.NaN;
  if (!Number.isSafeInteger(n) || n > MAX_SPOOL_BYTES)
    throw Error("spool limit");
  return n;
}
/** Local-only actions never read credentials or reach the server. */
const offline: CaptureUploadGateway = {
  policy: async () => ({ ok: false, error: { code: "policy_unavailable" } }),
  send: async () => ({ ok: false, error: { code: "unavailable" } }),
};
async function input(max: number): Promise<string> {
  const reader = Bun.stdin.stream().getReader();
  let size = 0;
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const x = await reader.read();
      if (x.done) break;
      size += x.value.length;
      if (size > max) throw Error("input limit");
      chunks.push(x.value);
    }
    return Buffer.concat(chunks).toString("utf8");
  } finally {
    await reader.cancel();
  }
}
export function createCaptureCommand(
  dependencies: CaptureCommandDependencies = {
    credentials: new NativeCredentials(),
    uploader: (endpoint, token) => new F4Uploader(endpoint, token, 3),
  },
) {
  return defineCommand({
  meta: {
    name: "capture",
    description: "Explicit filtered event capture with bounded local spooling",
  },
  args: {
    action: {
      type: "positional",
      required: true,
      description: "enqueue, flush, poll, pause, resume, status, credential-set",
    },
    adapter: {
      type: "string",
      description: "poll: harness adapter (codex, claude, generic)",
    },
    source: {
      type: "string",
      description:
        "poll: the one explicitly selected transcript file; new records only, never a directory",
    },
    project: { type: "string", description: "poll: project ID for captured events" },
    task: { type: "string", description: "poll: optional task ID" },
    session: { type: "string", description: "poll: session ID for captured events" },
    endpoint: {
      type: "string",
      description:
        "HTTPS F4 server origin (HTTP permitted only on numeric loopback)",
    },
    profile: { type: "string", default: "default" },
    spool: {
      type: "string",
      default: join(homedir(), ".kb-capture"),
      description: "Private collector spool directory",
    },
    "spool-limit-bytes": {
      type: "string",
      description:
        "Local spool byte limit (default 268435456); the server policy may only lower it",
    },
    "exclude-paths": {
      type: "string",
      description:
        "Comma-separated additional exact/directory source exclusions",
    },
    "exclude-fields": {
      type: "string",
      description: "Comma-separated additional field exclusions",
    },
  },
  async run({ args, rawArgs }) {
    try {
      const spool = new DiskSpool(
        args.spool,
        parseSpoolLimit(args["spool-limit-bytes"]),
      );
      const options = {
        localExcludedPaths: args["exclude-paths"]?.split(","),
        localExcludedFields: args["exclude-fields"]?.split(","),
      };
      const connection = new CaptureConnectionService(
        dependencies.credentials,
        spool,
        (token) => dependencies.uploader(args.endpoint ?? "", token),
        options,
      );
      // Pause/resume/status need no credential access or server availability.
      if (args.action === "status") {
        const result = await new CaptureService(spool, offline).status();
        console.log(JSON.stringify(result));
        if (!result.ok) process.exitCode = 1;
        return;
      }
      if (args.action === "credential-set") {
        const result = await connection.setCredential(
          args.profile,
          (await input(128)).trim(),
        );
        console.log(JSON.stringify(result));
        if (!result.ok) process.exitCode = 1;
        return;
      }
      if (args.action === "pause" || args.action === "resume") {
        const local = new CaptureService(spool, offline);
        const result =
          args.action === "pause" ? await local.pause() : await local.resume();
        console.log(JSON.stringify(result));
        if (!result.ok) process.exitCode = 1;
        return;
      }
      if (!["enqueue", "flush", "poll"].includes(args.action) || !args.endpoint)
        throw Error("command");
      const adapter =
        args.adapter && Object.hasOwn(adapters, args.adapter)
          ? adapters[args.adapter]
          : undefined;
      // Validate the whole poll selection before touching credentials.
      if (
        args.action === "poll" &&
        (!explicitProfile(rawArgs ?? []) ||
          !adapter ||
          !args.source ||
          args.source.includes("\0") ||
          !args.project ||
          !args.session)
      )
        throw Error("poll selection");
      const connected = await connection.connect(args.profile);
      if (!connected.ok) {
        console.log(JSON.stringify(connected));
        process.exitCode = 1;
        return;
      }
      if (args.action === "poll" && adapter && args.source && args.project && args.session) {
        const watcher = new CaptureWatcher({
          sources: [
            {
              path: resolve(args.source),
              adapter,
              clientID: args.profile,
              sessionID: args.session,
              scope: args.task
                ? { projectID: args.project, taskID: args.task }
                : { projectID: args.project },
            },
          ],
          cursors: new FileCursorStore(join(args.spool, "cursors")),
          reader: new FsSourceReader(),
          sink: connected.value,
        });
        const result = await watcher.poll(AbortSignal.timeout(60_000));
        console.log(JSON.stringify(result));
        if (!result.ok) process.exitCode = 1;
        return;
      }
      if (args.action === "flush") {
        const result = await connected.value.flush(AbortSignal.timeout(60_000));
        console.log(JSON.stringify(result));
        if (!result.ok) process.exitCode = 1;
        return;
      }
      const parsed: unknown = JSON.parse(await input(MAX_EVENT_BYTES));
      if (
        !parsed ||
        typeof parsed !== "object" ||
        !("scope" in parsed) ||
        !("content" in parsed) ||
        !("sequence" in parsed)
      )
        throw Error("event");
      // SAFETY: the service performs complete bounded validation/filtering before any write.
      const result = await connected.value.enqueue(parsed as CaptureEvent);
      console.log(JSON.stringify(result));
      if (!result.ok) process.exitCode = 1;
    } catch {
      console.log(
        JSON.stringify({ ok: false, error: { code: "invalid_input" } }),
      );
      process.exitCode = 1;
    }
  },
});
}
export default createCaptureCommand();
