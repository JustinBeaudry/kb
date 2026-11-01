// Explicit Docker regression: bun run deploy/tests/fixture-objects.integration.ts
// Not part of the Darwin-only client test suite, whose runners have no Docker.
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { parse } from "yaml";

const compose: {
  services: { objects: { image: string; entrypoint?: string[]; command: string[]; volumes: string[]; tmpfs?: string[] } };
} = parse(await readFile(resolve("deploy/compose.test.yaml"), "utf8"));
const objects = compose.services.objects;
const prefix = `kb-objects-permissions-${randomBytes(6).toString("hex")}`;
const source = `${prefix}-source`;
const data = `${prefix}-data`;
const server = `${prefix}-server`;
const names: string[] = [];
const volumes: string[] = [];

async function docker(args: string[]) {
  const child = Bun.spawn(["docker", ...args], { stdout: "pipe", stderr: "pipe", timeout: 30_000 });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited,
  ]);
  // Callers deliberately never print vendor stderr or raw container output.
  return { stdout, stderr, code };
}
async function requireDocker(args: string[], stage: string) {
  const result = await docker(args);
  assert.equal(result.code, 0, `${stage}: Docker command failed`);
  return result.stdout;
}
async function utility(stage: string, shell: string) {
  const name = `${prefix}-${stage}`;
  names.push(name);
  return requireDocker(["run", "--rm", "--name", name, "--network", "none", "--platform", "linux/amd64", "--user", "0:0", "--entrypoint", "/bin/sh", "--mount", `type=volume,src=${source},dst=/source`, objects.image, "-c", shell], stage);
}

try {
  for (const volume of [source, data]) {
    await requireDocker(["volume", "create", "--label", `kb.synthetic-permission-proof=${prefix}`, volume], "volume_create");
    volumes.push(volume);
  }
  // Deliberately emulate Linux runner UID1001; Docker Desktop host mapping
  // cannot hide this mismatch because ownership lives inside a Linux volume.
  await utility("seed", `umask 077; printf '%s\n' '{"identities":[{"name":"synthetic","credentials":[{"accessKey":"synthetic-access","secretKey":"synthetic-secret"}],"actions":["Read:synthetic","Write:synthetic","List:synthetic"]}]}' > /source/s3.json; chown 1001:1001 /source/s3.json; chmod 600 /source/s3.json`);
  const before = await utility("before", "stat -c '%a:%u:%g' /source/s3.json; sha256sum /source/s3.json");

  const args = ["run", "-d", "--name", server, "--network", "none", "--platform", "linux/amd64"];
  names.push(server);
  for (const mount of objects.volumes) {
    if (mount.startsWith("${KB_FIXTURE_DIRECTORY:")) {
      const target = mount.slice(mount.indexOf("}/s3.json:") + "}/s3.json:".length).split(":")[0];
      assert.ok(target === "/run/fixture-source/s3.json" || target === "/run/fixture/s3.json", "unexpected source mount");
      args.push("--mount", `type=volume,src=${source},dst=${target.slice(0, target.lastIndexOf("/"))},readonly`);
    } else if (mount === "objects:/data") {
      args.push("--mount", `type=volume,src=${data},dst=/data`);
    } else {
      assert.equal(mount, "./fixture-objects-entrypoint.sh:/run/fixture-objects-entrypoint.sh:ro", "unexpected fixture bind");
      args.push("--mount", `type=bind,src=${resolve("deploy/fixture-objects-entrypoint.sh")},dst=/run/fixture-objects-entrypoint.sh,readonly`);
    }
  }
  for (const mount of objects.tmpfs ?? []) args.push("--tmpfs", mount);
  if (objects.entrypoint) {
    const entrypoint = objects.entrypoint[0];
    assert.ok(entrypoint, "empty fixture entrypoint");
    args.push("--entrypoint", entrypoint);
  }
  args.push(objects.image, ...(objects.entrypoint?.slice(1) ?? []), ...objects.command);
  await requireDocker(args, "server_start");

  let ready = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    const running = (await requireDocker(["inspect", "--format", "{{.State.Running}}", server], "server_state")).trim();
    assert.equal(running, "true", "actual Seaweed server exited before S3 readiness");
    const response = await docker(["exec", server, "/bin/sh", "-c", "wget -S -O /dev/null -T 2 http://127.0.0.1:8333/"]);
    if (/HTTP\/[0-9.]+\s+403\b/.test(response.stdout + response.stderr)) { ready = true; break; }
    await Bun.sleep(500);
  }
  assert.ok(ready, "S3 did not reach expected authenticated boundary");
  const metadata = await requireDocker(["exec", server, "/bin/sh", "-c", "stat -c '%a:%u:%g' /run/fixture /run/fixture/s3.json; awk '/^Uid:/{print $2}' /proc/1/status; awk '$2==\"/run/fixture\"{print $3}' /proc/mounts"], "runtime_permissions");
  assert.equal(metadata.trim(), "700:1000:1000\n600:1000:1000\n1000\ntmpfs", "private runtime config or process identity mismatch");
  const sourceDigest = before.trim().split("\n")[1]?.split(/\s+/)[0];
  assert.ok(sourceDigest && /^[a-f0-9]{64}$/.test(sourceDigest), "missing source digest");
  const runtimeDigest = (await requireDocker(["exec", server, "sha256sum", "/run/fixture/s3.json"], "runtime_digest")).trim().split(/\s+/)[0];
  assert.equal(runtimeDigest, sourceDigest, "runtime config bytes differ from protected source");
  const denied = await docker(["exec", "--user", "1001:1001", server, "/bin/sh", "-c", "cat /run/fixture/s3.json >/dev/null"]);
  assert.notEqual(denied.code, 0, "unrelated UID could read runtime credentials");
  const after = await utility("after", "stat -c '%a:%u:%g' /source/s3.json; sha256sum /source/s3.json");
  assert.equal(after, before, "host-style source owner, permissions or bytes changed");
  assert.ok(after.startsWith("600:1001:1001\n"));
  console.log("fixture_objects_permissions_passed source600_uid1001 runtime600_uid1000 directory700_tmpfs process_uid1000 s3_status403");
} finally {
  for (const name of names) await docker(["rm", "-f", name]);
  for (const volume of volumes) await requireDocker(["volume", "rm", volume], "owned_volume_cleanup");
}
