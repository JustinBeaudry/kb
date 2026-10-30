import type { CaptureEvent, CapturePolicy } from "../../src/capture/types";
export const event = (
  content: CaptureEvent["content"] = { text: "hello" },
): CaptureEvent => ({
  id: "event-1",
  idempotencyKey: "event-1",
  scope: { projectID: "demo", sessionID: "session" },
  kind: "tool_result",
  observedAt: "2026-10-29T00:00:00Z",
  sequence: 1,
  sourceLocator: "session#original:0-100",
  sourcePath: "src/file.ts",
  content,
});
export const policy = (): CapturePolicy => ({
  revision: "p1",
  paused: false,
  excludedPaths: [],
  excludedFields: [],
  allowedModelRoutes: [],
  spoolLimitBytes: 256 * 1024 * 1024,
  expiresAt: Date.now() + 60_000,
});

import { mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DiskSpool } from "../../src/capture/spool";
export async function spoolFixture(limit?: number) {
  const parent = await mkdtemp(join(tmpdir(), "kb-c1-"));
  const spool = new DiskSpool(join(parent, "spool"), limit);
  return {
    spool,
    parent,
    cleanup: () => rm(parent, { recursive: true, force: true }),
  };
}
export async function bytes(root: string): Promise<string> {
  let s = "";
  for (const name of await readdir(root)) {
    const path = join(root, name);
    if ((await stat(path)).isFile()) s += await readFile(path, "utf8");
  }
  return s;
}
