// Mechanical measurement of every trial: adapter traces, transcript audit, evidence exposure, corpus integrity.
// Usage: bun measure.mjs <evaluator-manifest.json>   -> writes measurements.json next to this script.
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const sha = value => createHash('sha256').update(value).digest('hex');
const walk = dir => readdirSync(dir).flatMap(name => {
  const path = join(dir, name);
  return statSync(path).isDirectory() ? walk(path) : [path];
});
const tasks = Object.fromEntries(manifest.tasks.map(t => [t.id, t]));
const out = [];
for (const trial of manifest.trials) {
  const trace = readFileSync(join(trial.directory, 'trace.jsonl'), 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l));
  const vault = join(trial.directory, 'vault');
  const files = walk(vault).map(p => relative(vault, p));
  const knowledge = files.filter(f => !f.startsWith('.kb/'));
  const changed = knowledge.filter(f => manifest.corpus_hashes[f] !== sha(readFileSync(join(vault, f))));
  const missing = Object.keys(manifest.corpus_hashes).filter(f => !existsSync(join(vault, f)));
  const evidence = trace.filter(e => e.op !== 'guide').map(e => e.visible).join('\n');
  const gold = tasks[trial.task].gold;
  const exposed = gold.filter(g => evidence.includes(g.text)).map(g => g.text);
  // Transcript audit: every tool use must be a Bash call to this trial's adapter.
  const streamPath = join(manifest.root, 'raw', `${trial.id}.stream.jsonl`);
  const events = existsSync(streamPath) ? readFileSync(streamPath, 'utf8').split('\n').filter(Boolean).flatMap(l => { try { return [JSON.parse(l)]; } catch { return []; } }) : [];
  const toolUses = events.filter(e => e.type === 'assistant').flatMap(e => e.message.content.filter(c => c.type === 'tool_use'));
  const prefix = `bun ${trial.directory}/tool.mjs `;
  const offAdapter = toolUses.filter(u => u.name !== 'Bash' || !String(u.input.command ?? '').startsWith(prefix) || /[;&|`]|\$\(/.test(String(u.input.command).slice(prefix.length).replace(/'[^']*'|"[^"]*"/g, '')))
    .map(u => ({ tool: u.name, command: u.input.command ?? null }));
  const result = events.findLast(e => e.type === 'result') ?? null;
  const kbOps = trace.filter(e => e.op === 'kb').map(e => e.args[0]);
  const guideBytes = trace.filter(e => e.op === 'guide').reduce((n, e) => n + e.visible_bytes, 0);
  const visible = trace.reduce((n, e) => n + e.visible_bytes, 0);
  out.push({ id: trial.id, task: trial.task, arm: trial.arm,
    calls: trace.length, kb_calls: kbOps.length, kb_ops: kbOps, ops: trace.map(e => e.op === 'kb' ? `kb:${e.args[0]}` : e.op),
    guide_calls: trace.filter(e => e.op === 'guide').length, visible_bytes: visible, guide_visible_bytes: guideBytes, visible_bytes_excl_guide: visible - guideBytes,
    raw_bytes: trace.reduce((n, e) => n + e.raw_stdout_bytes + e.raw_stderr_bytes, 0),
    truncated: trace.filter(e => e.truncated).length, nonzero_exit: trace.filter(e => e.exit_code !== 0).length,
    budget_refusals: trace.filter(e => /budget exhausted/.test(e.stderr)).length,
    tool_ms: +trace.reduce((n, e) => n + e.duration_ms, 0).toFixed(1),
    gold: gold.length, gold_exposed: exposed.length, gold_exposed_texts: exposed,
    corpus_unchanged: changed.length === 0 && missing.length === 0, changed_files: changed, missing_files: missing,
    extra_files: knowledge.filter(f => !(f in manifest.corpus_hashes)), kb_operational_files: files.filter(f => f.startsWith('.kb/')),
    transcript_tool_uses: toolUses.length, off_adapter_tool_uses: offAdapter, permission_denials: result?.permission_denials ?? null,
    solver_model: events.find(e => e.type === 'system' && e.subtype === 'init')?.model ?? null,
    solver_turns: result?.num_turns ?? null, solver_cost_usd: result?.total_cost_usd ?? null,
    solver_output_tokens: result?.usage?.output_tokens ?? null,
    solver_input_tokens_total: result?.usage ? (result.usage.input_tokens ?? 0) + (result.usage.cache_creation_input_tokens ?? 0) + (result.usage.cache_read_input_tokens ?? 0) : null,
    solver_completed: result?.subtype === 'success' && result?.is_error === false });
}
writeFileSync(join(here, 'measurements.json'), JSON.stringify(out, null, 2) + '\n');
console.log(`measured ${out.length} trials`);
