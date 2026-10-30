// Synthetic-only driver for the real Go/Neo4j/S3 integration. Bearer arrives on
// stdin; never argv, environment, persistent profile, diagnostics, or snapshots.
import { CaptureService } from "../../src/services/capture.service";
import { DiskSpool } from "../../src/capture/spool";
import { F4Uploader } from "../../src/capture/uploader";
const [url, path, action] = process.argv.slice(2);
if (!url || !path) throw Error("fixture arguments");
const bearer = (await Bun.stdin.text()).trim();
const spool = new DiskSpool(path);
const service = new CaptureService(spool, new F4Uploader(url, bearer, 1), [
  bearer,
  "SYNTHETIC-C1-SECRET",
]);
if (action === "enqueue-flush") {
  const result = await service.enqueue({
    id: "stable-logical-event",
    idempotencyKey: "stable-logical-event",
    scope: { projectID: "demo", sessionID: "synthetic-session" },
    kind: "tool_result",
    sequence: 7,
    observedAt: "2026-10-29T00:00:00Z",
    sourceLocator: "synthetic.jsonl#bytes=13-85",
    sourcePath: "synthetic.jsonl",
    content: {
      output: ["SYNTHETIC-C1-", "SECRET"],
      text: "SYNTHETIC-C1-SECRET",
      safe: "synthetic result",
    },
  });
  if (!result.ok || result.value.state !== "queued") {
    console.log(JSON.stringify(result));
    process.exit(1);
  }
}
const result = await service.flush(AbortSignal.timeout(30_000));
console.log(JSON.stringify(result));
if (!result.ok) process.exitCode = 1;
