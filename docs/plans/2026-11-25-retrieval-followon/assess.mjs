// Blinded assessment: one `claude -p --model sonnet` call per answer with opaque IDs, gold, answer and cited span text.
// Usage: CLAUDE_BIN=/path/to/claude bun assess.mjs <evaluator-manifest.json>
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const bin = process.env.CLAUDE_BIN ?? 'claude';
const sha = value => createHash('sha256').update(value).digest('hex');
const tasks = Object.fromEntries(manifest.tasks.map(t => [t.id, t]));
const corpus = join(manifest.root, 'frozen-corpus');
const spanText = c => {
  const path = join(corpus, String(c.source ?? ''));
  if (!/^(wiki\/[a-z0-9-]+\.md|index\.md|context\.md|KB\.md)$/.test(String(c.source ?? '')) || !existsSync(path)) return '[cited source does not exist]';
  const lines = readFileSync(path, 'utf8').split('\n');
  const start = Math.max(1, Number(c.start) || 1), end = Math.min(lines.length, Number(c.end) || start);
  const numbered = lines.slice(start - 1, end).map((l, i) => `${start + i}:${l}`);
  return numbered.length > 80 ? [...numbered.slice(0, 40), `[... ${numbered.length - 80} cited lines omitted ...]`, ...numbered.slice(-40)].join('\n') : numbered.join('\n');
};
const packet = [], key = {};
for (const trial of manifest.trials) {
  const answer = JSON.parse(readFileSync(join(here, 'answers', `${trial.id}.json`), 'utf8'));
  const opaque = sha(`assess:${trial.id}:followon`).slice(0, 8);
  key[opaque] = trial.id;
  const task = tasks[trial.task];
  packet.push({ opaque_id: opaque, question: task.question, category: task.category, gold_facts: task.gold.map(g => g.text), abstention_rule: task.abstention ?? null,
    answer: answer.parsed_ok ? answer.answer ?? null : null, raw_final_message_if_unparsed: answer.parsed_ok ? null : answer.final_text,
    limitations: answer.limitations ?? null,
    cited_spans: (answer.citations ?? []).map(c => ({ source: c.source, start: c.start, end: c.end, text: spanText(c) })) });
}
packet.sort((a, b) => a.opaque_id.localeCompare(b.opaque_id));
writeFileSync(join(here, 'assessor-packet.json'), JSON.stringify(packet, null, 2) + '\n');
writeFileSync(join(here, 'blind-key.json'), JSON.stringify(key, null, 2) + '\n');

const RUBRIC = `You are a blinded assessor grading one answer to a question about a synthetic Markdown knowledge base. You see the question, its category, the frozen gold facts (or an abstention rule when gold is empty), the answer, its stated limitations, and the exact corpus text of every span the answer cited. Judge only from this material. Do not use outside knowledge.

Definitions:
- facts_required: number of gold facts (0 when gold is empty). facts_correct: gold facts the answer states correctly (paraphrase is fine; wrong values, wrong entities or missing facts do not count).
- Material factual claims: each distinct factual assertion about the knowledge base in the answer (not hedges or statements about the answerer's own process). claims_total counts them; claims_supported counts those literally supported by the cited span text shown. List each unsupported claim verbatim or closely paraphrased in unsupported_claims.
- wrong_or_superseded: true if the answer presents a wrong value, a wrong entity's fact (for example a different project's value, a sandbox/legacy/lab variant), or a superseded fact as the answer.
- citation_support: claims_supported / claims_total (1.0 if claims_total is 0).
- behavior_ok: the category's required behavior. temporal-update: gives the current value and marks the old one superseded. conflict: reports both values and does not pick one. absent (empty gold): abstains on every requested item without substituting another person/project, inventing a value, or claiming the event did not happen. exhaustive-list: lists all required items and no excluded or other-project items. Other categories: answers the question asked for the named entity.
- strict_success: true only if facts_correct == facts_required, every material claim is supported (no unsupported_claims), wrong_or_superseded is false and behavior_ok is true. An unparsed or missing answer fails.

Return ONLY one JSON object, no prose, with exactly these keys:
{"opaque_id": string, "facts_correct": int, "facts_required": int, "claims_total": int, "claims_supported": int, "unsupported_claims": [string], "wrong_or_superseded": bool, "citation_support": number, "behavior_ok": bool, "strict_success": bool, "note": string}

Item:
`;
mkdirSync(join(here, 'assessments-raw'), { recursive: true });
const results = [];
const queue = [...packet];
await Promise.all(Array.from({ length: 4 }, async () => {
  while (queue.length) {
    const item = queue.shift();
    const proc = Bun.spawn([bin, '-p', '--model', 'sonnet', '--safe-mode', '--tools', '', '--no-session-persistence', '--strict-mcp-config', '--output-format', 'json', RUBRIC + JSON.stringify(item, null, 2)],
      { cwd: manifest.root, stdout: 'pipe', stderr: 'pipe', env: process.env });
    const [stdout, stderr] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text()]);
    await proc.exited;
    writeFileSync(join(here, 'assessments-raw', `${item.opaque_id}.json`), stdout + (stderr ? `\n/* STDERR: ${stderr} */\n` : ''));
    let parsed = null, envelope = null;
    try { envelope = JSON.parse(stdout); const t = envelope.result; parsed = JSON.parse(t.slice(t.indexOf('{'), t.lastIndexOf('}') + 1)); } catch { parsed = null; }
    results.push({ ...(parsed ?? { opaque_id: item.opaque_id, parse_error: true }), assessor_model: envelope ? Object.keys(envelope.modelUsage ?? {}).join(',') || null : null, assessor_cost_usd: envelope?.total_cost_usd ?? null });
    console.log(`${item.opaque_id} parsed=${parsed !== null}`);
  }
}));
results.sort((a, b) => a.opaque_id.localeCompare(b.opaque_id));
writeFileSync(join(here, 'assessments.json'), JSON.stringify(results, null, 2) + '\n');
