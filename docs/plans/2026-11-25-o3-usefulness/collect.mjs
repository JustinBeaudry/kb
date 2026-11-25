// Collects answers, mechanical trace checks and the blinded assessor packet.
// Usage: bun collect.mjs TRIAL_ROOT   (writes into this directory)
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { records, tasks } from './corpus.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const root = process.argv[2];
const manifest = JSON.parse(readFileSync(join(root, 'manifest.json'), 'utf8'));
const text = (id) => records.find((r) => r.id === id).revisions.at(-1);
const goldRecords = { T1: ['atlas-webhook-queue'], T2: ['atlas-rate-limit'], T3: [], T4: ['atlas-latency-slo-platform', 'atlas-latency-slo-product'], T5: ['atlas-deploy-approval', 'atlas-migration-lock'], T6: ['birch-dedupe-key'], T7: ['birch-nightly-batch'], T8: [] };
const candidateMarkers = { T2: '900 requests per minute', T7: '04:00 UTC' };
const otherProject = { 'atlas-api': records.filter((r) => r.project === 'birch-pipeline'), 'birch-pipeline': records.filter((r) => r.project === 'atlas-api') };
const supersededMarkers = { T1: 'delivered through a RabbitMQ queue', T6: 'Birch deduplicates records by event_id.' };

mkdirSync(join(here, 'answers'), { recursive: true });
const measurements = [], packet = [], key = {};
for (const t of manifest.trials) {
  const result = JSON.parse(readFileSync(join(t.dir, 'result.json'), 'utf8'));
  let harness = null;
  try { harness = JSON.parse(result.stdout); } catch {}
  const finalText = harness?.result ?? '';
  let answer = null;
  const m = finalText.match(/\{[\s\S]*\}/);
  if (m) { try { answer = JSON.parse(m[0]); } catch {} }
  const traceFile = join(t.dir, 'trace.jsonl');
  const trace = existsSync(traceFile) ? readFileSync(traceFile, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)) : [];
  const exposed = trace.map((e) => e.stdout).join('\n');
  const usage = harness?.usage ?? null;
  const row = {
    trial: t.id, task: t.task, arm: t.arm, project: t.project,
    harness_ok: result.exit_code === 0 && !!harness && !harness.is_error, answer_parsed: !!answer,
    num_turns: harness?.num_turns ?? null, cost_usd: harness?.total_cost_usd ?? null,
    input_tokens: usage ? usage.input_tokens + usage.cache_creation_input_tokens + usage.cache_read_input_tokens : null, output_tokens: usage?.output_tokens ?? null,
    permission_denials: harness?.permission_denials?.length ?? null, wall_ms: result.wall_ms,
    wrapper_calls: trace.length, visible_bytes: trace.reduce((n, e) => n + e.visible_bytes, 0),
    retrieval_calls: trace.filter((e) => e.op !== 'guide').length, failed_calls: trace.filter((e) => e.exit_code !== 0).length,
    truncated_calls: trace.filter((e) => e.truncated).length,
    ops: trace.map((e) => `${e.op}${e.args[0] ? ' ' + e.args[0] : ''}`),
    gold_exposed: goldRecords[t.task].length ? goldRecords[t.task].every((id) => exposed.includes(text(id))) : null,
    candidate_exposed: candidateMarkers[t.task] ? exposed.includes(candidateMarkers[t.task]) : null,
    superseded_exposed: supersededMarkers[t.task] ? exposed.includes(supersededMarkers[t.task]) : null,
    other_project_records_exposed: otherProject[t.project].filter((r) => r.revisions.some((v) => exposed.includes(v))).map((r) => r.id),
  };
  measurements.push(row);
  writeFileSync(join(here, 'answers', `${t.task}-${t.arm}.json`), JSON.stringify({ trial: t.id, task: t.task, arm: t.arm, final: finalText, parsed: answer }, null, 2) + '\n');
  const opaque = createHash('sha256').update(`blind:${t.id}:o3`).digest('hex').slice(0, 8);
  key[opaque] = t.id;
  const task = tasks.find((x) => x.id === t.task);
  packet.push({ opaque_id: opaque, project: t.project, question: task.question, family: task.family,
    gold_facts: task.gold, abstention_rule: task.abstention ?? null, trap: task.trap ?? null, fail_if: task.fail_if,
    answer: answer?.answer ?? finalText, trust_notes: answer?.trust_notes ?? null, limitations: answer?.limitations ?? null });
}
packet.sort((a, b) => a.opaque_id.localeCompare(b.opaque_id));
writeFileSync(join(here, 'measurements.json'), JSON.stringify(measurements, null, 2) + '\n');
writeFileSync(join(here, 'assessor-packet.json'), JSON.stringify(packet, null, 2) + '\n');
writeFileSync(join(here, 'blind-key.json'), JSON.stringify(key, null, 2) + '\n');
console.log(JSON.stringify(measurements.map((r) => [r.task, r.arm, r.harness_ok, r.answer_parsed, r.wrapper_calls, r.visible_bytes, r.permission_denials, r.gold_exposed, r.candidate_exposed, r.other_project_records_exposed.length])));
