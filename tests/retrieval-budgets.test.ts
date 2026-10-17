import { afterEach, describe, expect, it } from "bun:test";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseEnvelope, type Envelope } from "../src/lib/envelope";

const vaults: string[] = [];
const DEFAULT_BUDGET = 16_384;
const bytes = (value: string): number => new TextEncoder().encode(value).length;

afterEach(() => {
  for (const vault of vaults.splice(0)) rmSync(vault, { recursive: true, force: true });
});

function makeVault(): string {
  const vault = mkdtempSync(join(tmpdir(), "kb-retrieval-budget-"));
  mkdirSync(join(vault, "wiki"));
  mkdirSync(join(vault, ".kb"));
  vaults.push(vault);
  return vault;
}

function writePage(vault: string, name: string, lines: string[]): void {
  writeFileSync(join(vault, "wiki", `${name}.md`), lines.join("\n"));
}

async function run(vault: string, argv: string[], env: Record<string, string> = {}) {
  const proc = Bun.spawn(["bun", "src/cli.ts", ...argv], {
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      KB_VAULT: vault,
      KB_RETRIEVAL_BUDGET: "",
      KB_MAP_BUDGET: "",
      ...env,
    },
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  return { stdout, stderr, exitCode };
}

function continuationArgv(envelope: Envelope): string[] {
  const continuation = envelope.policy.continuation;
  expect(continuation).toBeDefined();
  if (
    continuation === null || typeof continuation !== "object" ||
    !("command" in continuation) || typeof continuation.command !== "string" ||
    !("args" in continuation) || !Array.isArray(continuation.args) ||
    !continuation.args.every((arg: unknown) => typeof arg === "string")
  ) {
    throw new Error("Expected a continuation with a command and string argv");
  }
  return [continuation.command, ...continuation.args];
}

const commandCases = [
  { label: "get", argv: ["get", "large"] },
  { label: "get-node page", argv: ["get-node", "wiki/large.md"] },
  { label: "get-node section", argv: ["get-node", "wiki/large.md#large"] },
  { label: "recall", argv: ["recall", "needle"] },
  { label: "list-topics", argv: ["list-topics"] },
  { label: "map", argv: ["map"] },
];

function makeLargeVault(): string {
  const vault = makeVault();
  const text = 'needle "quoted" \\ path\t雪🧭 '.repeat(5);
  writePage(vault, "large", ["# Large", ...Array.from({ length: 160 }, (_, i) => `${i}: ${text}`)]);
  writeFileSync(join(vault, "index.md"), Array.from({ length: 160 }, (_, i) => `## Topic ${i} ${text}`).join("\n"));
  for (let i = 0; i < 120; i++) {
    writePage(vault, `page-${String(i).padStart(3, "0")}`, [`# Topic ${i} 雪🧭`, "## Details", "body"]);
  }
  return vault;
}

describe("curated retrieval wire budgets", () => {
  for (const command of commandCases) {
    it(`${command.label} bounds the complete default response including JSON escaping and Unicode`, async () => {
      const vault = makeLargeVault();
      const { stdout, stderr, exitCode } = await run(vault, command.argv);
      expect(stderr).toBe("");
      expect(exitCode).toBe(0);
      expect(bytes(stdout)).toBeLessThanOrEqual(DEFAULT_BUDGET);
      const envelope = parseEnvelope(stdout);
      expect(envelope.chunks.length).toBeGreaterThan(0);
      expect(envelope.policy.truncated).toBe(true);
    });

    it(`${command.label} counts the complete wire against an explicit smaller budget`, async () => {
      const vault = makeLargeVault();
      const { stdout, stderr, exitCode } = await run(vault, [...command.argv, "--budget", "2048"]);
      expect(stderr).toBe("");
      expect(exitCode).toBe(0);
      expect(bytes(stdout)).toBeLessThanOrEqual(2048);
      expect(parseEnvelope(stdout).policy.truncated).toBe(true);
    });

    it(`${command.label} rejects invalid and impossible explicit budgets without emitting stdout`, async () => {
      const vault = makeVault();
      writePage(vault, "large", ["# Large", "needle"]);
      writeFileSync(join(vault, "index.md"), "## Small");
      for (const budget of ["0", "-1", "1.5", "123garbage", "Infinity", "1"]) {
        const { stdout, stderr, exitCode } = await run(vault, [...command.argv, "--budget", budget]);
        expect(exitCode).not.toBe(0);
        expect(stdout).toBe("");
        expect(stderr).toMatch(/budget/i);
      }
    });
  }

  it("uses KB_RETRIEVAL_BUDGET and allows an explicit flag to override it", async () => {
    const vault = makeLargeVault();
    const limited = await run(vault, ["get", "large"], { KB_RETRIEVAL_BUDGET: "1800" });
    expect(limited.exitCode).toBe(0);
    expect(bytes(limited.stdout)).toBeLessThanOrEqual(1800);
    const expanded = await run(vault, ["get", "large", "--budget", "1000000"], { KB_RETRIEVAL_BUDGET: "1800" });
    expect(expanded.exitCode).toBe(0);
    expect(bytes(expanded.stdout)).toBeGreaterThan(1800);
    expect(parseEnvelope(expanded.stdout).policy.truncated).toBeUndefined();
  });

  it("rejects an impossible budget on an empty map and a no-results recall", async () => {
    const vault = makeVault();
    for (const argv of [["map"], ["recall", "absent"]]) {
      const result = await run(vault, [...argv, "--budget", "1"]);
      expect(result.exitCode).not.toBe(0);
      expect(result.stdout).toBe("");
      expect(result.stderr).toMatch(/budget/i);
    }
  });

  it("keeps an explicit increase-budget remedy when a map item cannot fit at 420 bytes", async () => {
    const vault = makeVault();
    writePage(vault, "only", [`# ${"An unusually long page title ".repeat(8)}`, "## One section", "body"]);
    const { stdout, exitCode } = await run(vault, ["map", "--budget", "420"]);
    expect(exitCode).toBe(0);
    expect(bytes(stdout)).toBeLessThanOrEqual(420);
    const envelope = parseEnvelope(stdout);
    expect(envelope.policy.truncated).toBe(true);
    expect(JSON.stringify(envelope.policy.suggestions)).toMatch(/budget/);
  });
});

describe("retrieval continuation and provenance", () => {
  it("replays get continuations for a leading-dash page passed after --", async () => {
    const vault = makeVault();
    const lines = ["# Guide", ...Array.from({ length: 40 }, (_, i) => `line ${i}: needle --flag evidence`)];
    writePage(vault, "-guide", lines);
    let argv = ["get", "--budget", "800", "-p", vault, "--", "-guide"];
    const received: string[] = [];
    let completed = false;

    for (let call = 0; call < 20; call++) {
      const result = await run(vault, argv);
      expect(result.exitCode).toBe(0);
      expect(result.stderr).toBe("");
      expect(bytes(result.stdout)).toBeLessThanOrEqual(800);
      const envelope = parseEnvelope(result.stdout);
      expect(envelope.chunks).toHaveLength(1);
      const chunk = envelope.chunks[0]!;
      expect(chunk.source).toBe("wiki/-guide.md");
      expect(chunk.line_range[0]).toBe(received.length + 1);
      received.push(...chunk.text.split("\n"));
      if (envelope.policy.continuation === undefined) {
        completed = true;
        break;
      }
      argv = continuationArgv(envelope);
    }

    expect(completed).toBe(true);
    expect(received).toEqual(lines);
  });

  it("replays a leading-dash recall query in exact path and line order", async () => {
    const vault = makeVault();
    const lines = [
      "# Guide", "--flag first evidence", "context 3", "context 4", "context 5",
      "context 6", "context 7", "context 8", "--flag second evidence", "context 10",
      "context 11", "context 12",
    ];
    // Creation order intentionally differs from the promised path order.
    writePage(vault, "z-guide", lines);
    writePage(vault, "a-guide", lines);
    let argv = ["recall", "--budget", "700", "-p", vault, "--", "--flag"];
    const received: Array<{ source: string; line_range: [number, number]; text: string }> = [];
    let completed = false;
    let pages = 0;

    for (let call = 0; call < 10; call++) {
      const result = await run(vault, argv);
      expect(result.exitCode).toBe(0);
      expect(result.stderr).toBe("");
      expect(bytes(result.stdout)).toBeLessThanOrEqual(700);
      const envelope = parseEnvelope(result.stdout);
      pages++;
      if (call === 0) expect(envelope.policy.continuation).toBeDefined();
      received.push(...envelope.chunks.map(({ source, line_range, text }) => ({ source, line_range, text })));
      if (envelope.policy.continuation === undefined) {
        completed = true;
        break;
      }
      argv = continuationArgv(envelope);
    }

    expect(completed).toBe(true);
    expect(pages).toBeGreaterThan(1);
    expect(received).toEqual([
      { source: "wiki/a-guide.md", line_range: [1, 5], text: lines.slice(0, 5).join("\n") },
      { source: "wiki/a-guide.md", line_range: [6, 12], text: lines.slice(5, 12).join("\n") },
      { source: "wiki/z-guide.md", line_range: [1, 5], text: lines.slice(0, 5).join("\n") },
      { source: "wiki/z-guide.md", line_range: [6, 12], text: lines.slice(5, 12).join("\n") },
    ]);
  });

  for (const command of ["get", "get-node"]) {
    it(`${command} returns every original line through executable continuations`, async () => {
      const vault = makeVault();
      const lines = ["# Guide", ...Array.from({ length: 36 }, (_, i) => `line ${i}: "quoted" \\ 雪🧭 ${"evidence ".repeat(4)}`)];
      writePage(vault, "guide", lines);
      let argv = [command, command === "get" ? "guide" : "wiki/guide.md", "--budget", "1200"];
      const received: string[] = [];
      let completed = false;
      for (let call = 0; call < 20; call++) {
        const result = await run(vault, argv);
        expect(result.exitCode).toBe(0);
        expect(bytes(result.stdout)).toBeLessThanOrEqual(1200);
        const envelope = parseEnvelope(result.stdout);
        expect(envelope.chunks).toHaveLength(1);
        const chunk = envelope.chunks[0]!;
        expect(chunk.source).toBe("wiki/guide.md");
        expect(chunk.line_range[0]).toBe(received.length + 1);
        const chunkLines = chunk.text.split("\n");
        expect(chunk.line_range[1]).toBe(received.length + chunkLines.length);
        expect(chunk.text).toBe(lines.slice(chunk.line_range[0] - 1, chunk.line_range[1]).join("\n"));
        expect(chunk.text).not.toContain("�");
        received.push(...chunkLines);
        if (envelope.policy.truncated !== true) {
          completed = true;
          break;
        }
        argv = continuationArgv(envelope);
        expect(argv[0]).toBe(command);
        expect(argv).toContain("--start-line");
        expect(argv[argv.indexOf("-p") + 1]).toBe(vault);
      }
      expect(completed).toBe(true);
      expect(received).toEqual(lines);
    });
  }

  it("keeps section continuations inside the requested section using absolute source lines", async () => {
    const vault = makeVault();
    const lines = ["# Guide", "## Before", "prior fact", "## Target", ...Array.from({ length: 24 }, (_, i) => `target ${i} ${"detail ".repeat(8)}`), "## After", "later fact"];
    writePage(vault, "guide", lines);
    let argv = ["get-node", "wiki/guide.md#target", "--budget", "1200"];
    const received: string[] = [];
    for (let call = 0; call < 20; call++) {
      const result = await run(vault, argv);
      expect(result.exitCode).toBe(0);
      expect(bytes(result.stdout)).toBeLessThanOrEqual(1200);
      const envelope = parseEnvelope(result.stdout);
      const chunk = envelope.chunks[0]!;
      expect(chunk.node_id).toBe("wiki/guide.md#target");
      expect(chunk.heading_path).toEqual(["Guide", "Guide", "Target"]);
      expect(chunk.line_range[0]).toBe(4 + received.length);
      expect(chunk.line_range[1]).toBeLessThan(29);
      received.push(...chunk.text.split("\n"));
      if (envelope.policy.truncated !== true) break;
      argv = continuationArgv(envelope);
    }
    expect(received).toEqual(lines.slice(3, 28));
  });

  for (const command of ["get", "get-node"]) {
    it(`${command} gives an honest larger-budget remedy for a single indivisible line`, async () => {
      const vault = makeVault();
      writePage(vault, "huge", ["雪\"\\".repeat(2000)]);
      const target = command === "get" ? "huge" : "wiki/huge.md";
      const result = await run(vault, [command, target, "--budget", "1200"]);
      expect(result.exitCode).not.toBe(0);
      expect(result.stdout).toBe("");
      expect(result.stderr).toMatch(/line\s+1/i);
      expect(result.stderr).toMatch(/--budget/);
    });
  }

  it("gives primary section evidence priority over optional neighbors in the same budget", async () => {
    const vault = makeVault();
    writePage(vault, "guide", ["# Guide", "## Before", ...Array.from({ length: 20 }, () => "before ".repeat(40)), "## Target", "PRIMARY_FACT", "## After", ...Array.from({ length: 20 }, () => "after ".repeat(40))]);
    const result = await run(vault, ["get-node", "wiki/guide.md#target", "--neighbors", "--budget", "1200"]);
    expect(result.exitCode).toBe(0);
    expect(bytes(result.stdout)).toBeLessThanOrEqual(1200);
    const envelope = parseEnvelope(result.stdout);
    expect(envelope.chunks[0]!.node_id).toBe("wiki/guide.md#target");
    expect(envelope.chunks[0]!.text).toBe("## Target\nPRIMARY_FACT");
    expect(envelope.policy.truncated).toBe(true);
    expect(JSON.stringify(envelope.policy.suggestions)).toContain("get-node");
  });

  it("keeps optional wikilink previews inside the same wire budget", async () => {
    const vault = makeVault();
    writePage(vault, "guide", ["# Guide", "## Target", "PRIMARY_FACT [[linked]]"]);
    writePage(vault, "linked", ["# Linked", "雪🧭\"\\".repeat(200)]);
    const result = await run(vault, ["get-node", "wiki/guide.md#target", "--follow-wikilinks", "1", "--budget", "900"]);
    expect(result.exitCode).toBe(0);
    expect(bytes(result.stdout)).toBeLessThanOrEqual(900);
    const envelope = parseEnvelope(result.stdout);
    expect(envelope.chunks[0]!.text).toBe("## Target\nPRIMARY_FACT [[linked]]");
    expect(envelope.policy.truncated).toBe(true);
    expect(JSON.stringify(envelope.policy.suggestions)).toContain("get-node");
  });

  for (const command of ["recall", "list-topics"]) {
    it(`${command} continues to remaining ordered results without repeating or skipping them`, async () => {
      const vault = makeVault();
      const expected: string[] = [];
      const topics: string[] = [];
      for (let i = 0; i < 30; i++) {
        const name = `page-${String(i).padStart(2, "0")}`;
        writePage(vault, name, [`# Topic ${i}`, `needle evidence ${i}`]);
        expected.push(command === "list-topics" ? `Topic ${i}` : `wiki/${name}.md`);
        topics.push(`## Topic ${i}`);
      }
      writeFileSync(join(vault, "index.md"), topics.join("\n"));
      let argv = command === "recall" ? [command, "needle", "--budget", "1200"] : [command, "--budget", "1200"];
      const received: string[] = [];
      let completed = false;
      for (let call = 0; call < 30; call++) {
        const result = await run(vault, argv);
        expect(result.exitCode).toBe(0);
        expect(bytes(result.stdout)).toBeLessThanOrEqual(1200);
        const envelope = parseEnvelope(result.stdout);
        expect(envelope.chunks.length).toBeGreaterThan(0);
        received.push(...envelope.chunks.map((chunk) => command === "list-topics" ? chunk.text : chunk.source));
        if (envelope.policy.continuation === undefined) {
          completed = true;
          break;
        }
        expect(envelope.policy.truncated).toBe(true);
        argv = continuationArgv(envelope);
        expect(argv[0]).toBe(command);
        expect(argv).toContain("--offset");
        expect(argv[argv.indexOf("-p") + 1]).toBe(vault);
      }
      expect(completed).toBe(true);
      expect(received).toEqual(expected);
    });
  }

  it("preserves small response text, metadata, and line provenance without a truncation marker", async () => {
    const vault = makeVault();
    writePage(vault, "small", ["# Small", "## Evidence", 'needle "quoted" \\ 雪🧭']);
    writeFileSync(join(vault, "index.md"), "# Index\n\n## Evidence\n");
    const got = parseEnvelope((await run(vault, ["get", "small"])).stdout);
    expect(got.chunks).toEqual([{ source: "wiki/small.md", line_range: [1, 3], curation: "curated", text: '# Small\n## Evidence\nneedle "quoted" \\ 雪🧭' }]);
    expect(got.policy.truncated).toBeUndefined();
    const node = parseEnvelope((await run(vault, ["get-node", "wiki/small.md#evidence"])).stdout);
    expect(node.chunks[0]).toEqual({ source: "wiki/small.md", line_range: [2, 3], curation: "heading-section", text: '## Evidence\nneedle "quoted" \\ 雪🧭', node_id: "wiki/small.md#evidence", heading_path: ["Small", "Small", "Evidence"], node_kind: "section" });
    expect(node.policy.nav_trace).toEqual(["wiki/small.md#evidence"]);
    expect(node.policy.truncated).toBeUndefined();
    const recalled = parseEnvelope((await run(vault, ["recall", "needle"])).stdout);
    expect(recalled.chunks).toEqual(got.chunks);
    expect(recalled.policy.truncated).toBeUndefined();
    const topics = parseEnvelope((await run(vault, ["list-topics"])).stdout);
    expect(topics.chunks).toEqual([{ source: "index.md", line_range: [3, 3], curation: "curated", text: "Evidence" }]);
    expect(topics.policy.truncated).toBeUndefined();
  });
});
