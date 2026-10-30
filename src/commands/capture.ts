import { defineCommand } from "citty";
import { join } from "node:path";
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
} from "../capture/types";
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
export default defineCommand({
  meta: {
    name: "capture",
    description: "Explicit filtered event capture with bounded local spooling",
  },
  args: {
    action: {
      type: "positional",
      required: true,
      description: "enqueue, flush, pause, resume, status, credential-set",
    },
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
  async run({ args }) {
    try {
      const spool = new DiskSpool(
        args.spool,
        parseSpoolLimit(args["spool-limit-bytes"]),
      );
      const connection = new CaptureConnectionService(
        new NativeCredentials(),
        spool,
        (token) =>
          new F4Uploader(args.endpoint ?? "", token, 3, {
            paths: args["exclude-paths"]?.split(","),
            fields: args["exclude-fields"]?.split(","),
          }),
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
      if (!["enqueue", "flush"].includes(args.action) || !args.endpoint)
        throw Error("command");
      const connected = await connection.connect(args.profile);
      if (!connected.ok) {
        console.log(JSON.stringify(connected));
        process.exitCode = 1;
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
