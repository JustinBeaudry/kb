import { SecureSpoolDirectory } from "../../src/repositories/capture-spool.repository";
import { createHash } from "node:crypto";
const [path, action] = process.argv.slice(2);
if (!path) throw Error("fixture path");
const result = await new SecureSpoolDirectory(path).within(
  async (io) => {
    if (action === "dispatch") {
      console.log("ready");
      await new Promise(() => {});
      return;
    }
    const file =
      createHash("sha256").update("interrupted").digest("hex") + ".json";
    await io.write(
      "state",
      JSON.stringify({
        version: 1,
        paused: false,
        entries: [],
        gaps: [],
        totalLosses: 0,
        pending: { file, bytes: 18, sequence: 42, operation: "append" },
      }),
    );
    await io.write(file, "synthetic-filtered", "event-next");
    console.log("ready");
    await new Promise(() => {});
  },
  action === "dispatch" ? "dispatch" : "lock",
);
if (!result.ok) process.exit(1);
