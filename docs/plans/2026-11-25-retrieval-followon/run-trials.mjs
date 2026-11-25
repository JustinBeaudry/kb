// Dispatch frozen solver trials: one fresh headless `claude -p --model haiku` process per trial, at most 4 at once.
// Usage: CLAUDE_BIN=/path/to/claude bun run-trials.mjs <evaluator-manifest.json> [trial-id ...]
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const only = new Set(process.argv.slice(3));
const trials = manifest.trials.filter(t => !only.size || only.has(t.id));
const bin = process.env.CLAUDE_BIN ?? 'claude';
const rawDir = join(manifest.root, 'raw');
mkdirSync(rawDir, { recursive: true });
mkdirSync(join(here, 'answers'), { recursive: true });
const CONCURRENCY = 4, TIMEOUT_MS = 600_000;

function extractAnswer(text) {
  if (typeof text !== 'string') return null;
  const fenced = text.match(/```(?:json)?\s*(\{[\s\S]*\})\s*```/);
  const candidate = fenced ? fenced[1] : text.slice(text.indexOf('{'), text.lastIndexOf('}') + 1);
  try { return JSON.parse(candidate); } catch { return null; }
}

async function runTrial(trial) {
  const args = ['-p', '--model', 'haiku', '--safe-mode', '--tools', 'Bash',
    '--allowedTools', `Bash(bun ${trial.directory}/tool.mjs:*)`, '--permission-mode', 'dontAsk',
    '--no-session-persistence', '--strict-mcp-config', '--output-format', 'stream-json', '--verbose', trial.prompt];
  const started = new Date();
  const proc = Bun.spawn([bin, ...args], { cwd: trial.workdir, stdout: 'pipe', stderr: 'pipe', env: process.env });
  const timer = setTimeout(() => proc.kill(), TIMEOUT_MS);
  const [stdout, stderr] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text()]);
  const exitCode = await proc.exited;
  clearTimeout(timer);
  const ended = new Date();
  writeFileSync(join(rawDir, `${trial.id}.stream.jsonl`), stdout);
  writeFileSync(join(rawDir, `${trial.id}.stderr.txt`), stderr);
  const events = stdout.split('\n').filter(Boolean).flatMap(line => { try { return [JSON.parse(line)]; } catch { return []; } });
  const result = events.findLast(e => e.type === 'result') ?? null;
  const init = events.find(e => e.type === 'system' && e.subtype === 'init') ?? null;
  const parsed = extractAnswer(result?.result);
  const record = { id: trial.id, started: started.toISOString(), ended: ended.toISOString(), wall_ms: ended - started,
    exit_code: exitCode, timed_out: ended - started >= TIMEOUT_MS, result_subtype: result?.subtype ?? null, is_error: result?.is_error ?? null,
    model: init?.model ?? null, tools_offered: init?.tools ?? null, num_turns: result?.num_turns ?? null,
    usage: result?.usage ?? null, model_usage: result?.modelUsage ?? null, cost_usd: result?.total_cost_usd ?? null,
    permission_denials: result?.permission_denials ?? null, final_text: result?.result ?? null, parsed_ok: parsed !== null, ...(parsed ?? {}) };
  writeFileSync(join(here, 'answers', `${trial.id}.json`), JSON.stringify(record, null, 2) + '\n');
  console.log(`${trial.id} ${trial.task} ${trial.arm} exit=${exitCode} parsed=${parsed !== null} ${(ended - started) / 1000}s`);
}

const queue = [...trials];
await Promise.all(Array.from({ length: CONCURRENCY }, async () => { while (queue.length) await runTrial(queue.shift()); }));
