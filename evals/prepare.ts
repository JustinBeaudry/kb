import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import scenarios from "./scenarios.json";
import { addSession, createFixture, knowledgeSnapshot, writeFixtureFile, type TranscriptKind } from "./fixtures";

const args = process.argv.slice(2);
const option = (name: string): string | undefined => {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
};
if (args.includes("--help") || !option("--scenario")) {
  console.log("bun evals/prepare.ts --scenario A01-known-fact [--dir NEW_DIRECTORY] [--cli-root PATH]\nCreates an isolated fixture and task.json for an actual agent trial. Does not run an agent.");
  process.exit(args.includes("--help") ? 0 : 2);
}
const scenario = scenarios.scenarios.find((item) => item.id === option("--scenario"));
if (!scenario) throw new Error(`Unknown scenario: ${option("--scenario")}`);
const destination = option("--dir");
if (destination && existsSync(resolve(destination))) throw new Error("--dir must not already exist; each trial needs a fresh fixture.");
if (destination) mkdirSync(resolve(destination), { recursive: true });
const fixture = createFixture(destination ? resolve(destination) : undefined);
for (const kind of ["legacy", "claude-user", "codex", "generic", "unsupported"] satisfies TranscriptKind[]) addSession(fixture, kind);
writeFixtureFile(fixture.root, "inputs/retry-note.md", "# Retry note\n\nRetry failed deliveries at most 3 times with exponential backoff.\n");
writeFixtureFile(fixture.root, "inputs/escalation-note.md", "# Escalation decision\n\nAfter delivery retries are exhausted, escalate to Noor. Approved October 16, 2026.\n");
writeFixtureFile(fixture.vault, "sessions/summaries/2026-10-15T12-00-00-legacy.md", "---\nmanifest_hash: fixture-pinned\ntranscript_hash: null\ngenerated_at: '2026-10-15T13:00:00Z'\nuser_edited: true\n---\n## Summary\n\nThe team confirmed the delivery retry policy.\n\n## Extraction Candidates\n\nRetry failed deliveries at most 3 times with exponential backoff.\n");
if (scenario.id === "A19-missing-summarizer") fixture.env.KB_SUMMARIZE_COMMAND = join(fixture.bin, "missing-provider");
if (scenario.id === "A20-failed-refresh-preserves-summary") fixture.env.KB_EVAL_PROVIDER_MODE = "fail";
const cliRoot = resolve(option("--cli-root") ?? join(import.meta.dir, ".."));
const task = {
  schema_version: 1,
  scenario,
  root: fixture.root,
  vault: fixture.vault,
  cli: [process.execPath, join(cliRoot, "src/cli.ts")],
  environment: fixture.env,
  skill_path: join(cliRoot, "skills/kb/SKILL.md"),
  before_knowledge_sha256: knowledgeSnapshot(fixture.vault),
  recording: { answer: "answer.md", commands: "commands.jsonl", assessment: "assessment.json", after_snapshot: "after-knowledge.json" },
  instructions: "Give the agent only the prompt, fixture location, selected version's skill and CLI instructions. Keep expected outcomes out of the agent context. Save the actual answer, command trace and after snapshot; then assess grounding, abstention and unauthorized writes separately. Never substitute a deterministic contract result for an agent outcome.",
};
writeFileSync(join(fixture.root, "task.json"), JSON.stringify(task, null, 2) + "\n");
writeFileSync(join(fixture.root, "prompt.txt"), scenario.prompt + "\n");
console.log(JSON.stringify({ task: join(fixture.root, "task.json"), prompt: join(fixture.root, "prompt.txt"), vault: fixture.vault, skill: task.skill_path }, null, 2));
