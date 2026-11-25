// Dispatches one fresh headless Claude Code process per trial (4 concurrent).
// Usage: bun run.mjs TRIAL_ROOT [CLAUDE_BIN]
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

const [root, claude = `${process.env.HOME}/.local/bin/claude`] = process.argv.slice(2);
const manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'));
const only = process.env.O3_ONLY ? new Set(process.env.O3_ONLY.split(',')) : null;

async function runTrial(t) {
  const out = join(t.dir, 'result.json');
  if (existsSync(out) && !only) return;
  const prompt = readFileSync(join(t.dir, 'prompt.txt'), 'utf8');
  const tools = t.arm === 'none' ? ['--tools', ''] : ['--tools', 'Bash', '--allowedTools', `Bash(bun ${join(t.dir, 'tool.mjs')}:*)`];
  const argv = [claude, '-p', '--model', 'haiku', '--setting-sources', '', '--strict-mcp-config', '--output-format', 'json', ...tools, '--', prompt];
  const started = Date.now();
  const proc = Bun.spawn(argv, { cwd: t.dir, stdout: 'pipe', stderr: 'pipe', env: { ...process.env, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1' } });
  const timer = setTimeout(() => proc.kill(), 10 * 60 * 1000);
  const [stdout, stderr] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text()]);
  const exitCode = await proc.exited;
  clearTimeout(timer);
  writeFileSync(out, JSON.stringify({ trial: t.id, task: t.task, arm: t.arm, exit_code: exitCode, wall_ms: Date.now() - started, stdout, stderr: stderr.slice(0, 4000) }, null, 2));
  console.log(`${t.id} ${t.task} ${t.arm} exit=${exitCode} ${(Date.now() - started) / 1000}s`);
}
const queue = manifest.trials.filter((t) => !only || only.has(t.id));
const workers = Array.from({ length: 4 }, async () => { while (queue.length) await runTrial(queue.shift()); });
await Promise.all(workers);
