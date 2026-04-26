import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, rmSync, existsSync, readFileSync } from "node:fs";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `cairn-sec-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  vaults.push(dir);
  return dir;
}

async function runSelfTest(vault: string): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bash", "hooks/security-self-test", vault], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("security-self-test hook", () => {
  it("exits 0 on success", async () => {
    const vault = makeVault();
    const { exitCode } = await runSelfTest(vault);
    expect(exitCode).toBe(0);
  });

  it("creates sentinel files in raw/, sessions/, and .cairn/", async () => {
    const vault = makeVault();
    await runSelfTest(vault);
    expect(existsSync(join(vault, "raw", ".security-probe-sentinel"))).toBe(true);
    expect(existsSync(join(vault, "sessions", ".security-probe-sentinel"))).toBe(true);
    expect(existsSync(join(vault, ".cairn", ".security-probe-sentinel.jsonl"))).toBe(true);
  });

  it("writes unique sentinel tokens so agent can confirm read escape", async () => {
    const vault = makeVault();
    await runSelfTest(vault);
    const raw = readFileSync(join(vault, "raw", ".security-probe-sentinel"), "utf-8");
    expect(raw).toMatch(/CAIRN-SEC-PROBE-[0-9a-f]+/);
  });

  it("output lists expected-deny probes for Read/Grep/Glob/Bash", async () => {
    const vault = makeVault();
    const { stdout } = await runSelfTest(vault);
    expect(stdout).toMatch(/Read\(/);
    expect(stdout).toMatch(/Grep\(/);
    expect(stdout).toMatch(/Glob\(/);
    expect(stdout).toMatch(/Bash\(/);
    expect(stdout).toMatch(/raw\//);
    expect(stdout).toMatch(/sessions\//);
    expect(stdout).toMatch(/\.cairn\//);
  });

  it("output includes sentinel paths for verification", async () => {
    const vault = makeVault();
    const { stdout } = await runSelfTest(vault);
    expect(stdout).toContain(".security-probe-sentinel");
  });

  it("output marks itself as best-effort", async () => {
    const vault = makeVault();
    const { stdout } = await runSelfTest(vault);
    expect(stdout.toLowerCase()).toMatch(/best-effort|regression detector|not a guarantee/);
  });
});

describe("plugin .claude/settings.json", () => {
  it("ships deny rules for raw/, sessions/, and .cairn/ logs", () => {
    const settingsPath = join(process.cwd(), ".claude", "settings.json");
    expect(existsSync(settingsPath)).toBe(true);
    const raw = readFileSync(settingsPath, "utf-8");
    const parsed = JSON.parse(raw);
    const denies: string[] = parsed?.permissions?.deny ?? [];
    expect(denies.length).toBeGreaterThan(0);
    const joined = denies.join("\n");
    expect(joined).toMatch(/raw/);
    expect(joined).toMatch(/sessions/);
    expect(joined).toMatch(/\.cairn/);
    expect(joined).toMatch(/Read\(/);
    expect(joined).toMatch(/Grep\(/);
    expect(joined).toMatch(/Glob\(/);
  });

  it("covers more than one Bash reader (cat, head, tail, less, grep, awk, sed, python at minimum)", () => {
    const settingsPath = join(process.cwd(), ".claude", "settings.json");
    const parsed = JSON.parse(readFileSync(settingsPath, "utf-8"));
    const denies: string[] = parsed?.permissions?.deny ?? [];
    const bashEntries = denies.filter((d) => d.startsWith("Bash("));
    const readers = ["cat", "head", "tail", "less", "grep", "awk", "sed", "python"];
    for (const reader of readers) {
      // Patterns may use `Bash(reader ...)` or the more permissive
      // `Bash(reader* ...)` form (which also matches options like `cat -n …`).
      const re = new RegExp(`\\(${reader}\\*? `);
      expect(
        bashEntries.some((entry) => re.test(entry)),
        `expected at least one Bash(${reader} ...) or Bash(${reader}* ...) pattern`
      ).toBe(true);
    }
  });

  it("bash denies cover both raw/ and sessions/ at minimum", () => {
    const settingsPath = join(process.cwd(), ".claude", "settings.json");
    const parsed = JSON.parse(readFileSync(settingsPath, "utf-8"));
    const denies: string[] = parsed?.permissions?.deny ?? [];
    const bashEntries = denies.filter((d) => d.startsWith("Bash("));
    expect(bashEntries.some((e) => e.includes("raw/"))).toBe(true);
    expect(bashEntries.some((e) => e.includes("sessions/"))).toBe(true);
  });
});
