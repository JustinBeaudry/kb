// Separate-process holder of the short state lock, for contention tests.
import { SecureSpoolDirectory } from "../../src/repositories/capture-spool.repository";
const [path, ms] = process.argv.slice(2);
if (!path || !ms) throw Error("fixture arguments");
const result = await new SecureSpoolDirectory(path).within(async () => {
  console.log("ready");
  await new Promise((r) => setTimeout(r, Number(ms)));
});
process.exit(result.ok ? 0 : 1);
