// Join measurements with unblinded assessments into results.json. Written after grading; no scoring rule is changed here.
// Usage: bun aggregate.mjs
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const load = name => JSON.parse(readFileSync(join(here, name), 'utf8'));
const key = load('blind-key.json');
const A = Object.fromEntries(load('assessments.json').map(a => [key[a.opaque_id], a]));
const M = load('measurements.json');
const arms = ['files', 'kb-full', 'kb-minimal'];
const sum = (xs, f) => xs.reduce((n, x) => n + f(x), 0);
const summary = {};
for (const arm of arms) {
  const ms = M.filter(m => m.arm === arm);
  const as = ms.map(m => A[m.id]);
  const answerable = ms.filter(m => m.gold > 0);
  summary[arm] = { n: ms.length, strict_success: sum(as, a => +a.strict_success),
    fact_recall_macro: +(sum(answerable, m => A[m.id].facts_correct / A[m.id].facts_required) / answerable.length).toFixed(3),
    evidence_exposed: `${sum(ms, m => m.gold_exposed)}/${sum(ms, m => m.gold)}`,
    claims_supported: `${sum(as, a => a.claims_supported)}/${sum(as, a => a.claims_total)}`,
    wrong_or_superseded: sum(as, a => +a.wrong_or_superseded), behavior_ok: sum(as, a => +a.behavior_ok),
    failed_only_on_unsupported_extra_claim: sum(as, a => +(!a.strict_success && !a.wrong_or_superseded && a.behavior_ok && a.facts_correct === a.facts_required)),
    calls: sum(ms, m => m.calls), kb_calls: sum(ms, m => m.kb_calls), trials_using_kb: sum(ms, m => +(m.kb_calls > 0)),
    visible_bytes: sum(ms, m => m.visible_bytes), guide_bytes: sum(ms, m => m.guide_visible_bytes), visible_bytes_excl_guide: sum(ms, m => m.visible_bytes_excl_guide),
    raw_bytes: sum(ms, m => m.raw_bytes), truncated: sum(ms, m => m.truncated), nonzero_exit: sum(ms, m => m.nonzero_exit),
    tool_ms: +sum(ms, m => m.tool_ms).toFixed(1), solver_cost_usd: +sum(ms, m => m.solver_cost_usd ?? 0).toFixed(4),
    solver_output_tokens: sum(ms, m => m.solver_output_tokens ?? 0), solver_input_tokens_total: sum(ms, m => m.solver_input_tokens_total ?? 0),
    corpus_unchanged: ms.every(m => m.corpus_unchanged), off_adapter_tool_uses: sum(ms, m => m.off_adapter_tool_uses.length) };
}
const paired = [...new Set(M.map(m => m.task))].sort().map(task => ({ task, ...Object.fromEntries(arms.map(arm => {
  const m = M.find(x => x.task === task && x.arm === arm), a = A[m.id];
  return [arm, { id: m.id, strict_success: a.strict_success, facts: `${a.facts_correct}/${a.facts_required}`, wrong_or_superseded: a.wrong_or_superseded, behavior_ok: a.behavior_ok,
    unsupported_claims: a.unsupported_claims, calls: m.calls, kb_ops: m.kb_ops, visible_bytes: m.visible_bytes, visible_bytes_excl_guide: m.visible_bytes_excl_guide,
    truncated: m.truncated, evidence_exposed: `${m.gold_exposed}/${m.gold}` }];
})) }));
// Pilot reference (docs/plans/2026-10-26-retrieval-control/results.json summary): Opus solver, 120 pages.
const pilot = { files: { strict_success: 6, n: 8, fact_recall_macro: 1.0, evidence_exposed: '20/20', calls: 34, kb_calls: 0, visible_bytes: 104430, wrong_or_superseded: 0 },
  kb: { strict_success: 6, n: 8, fact_recall_macro: 1.0, evidence_exposed: '20/20', calls: 41, kb_calls: 5, visible_bytes: 192426, wrong_or_superseded: 0 } };
writeFileSync(join(here, 'results.json'), JSON.stringify({ summary, paired, pilot_reference: pilot }, null, 2) + '\n');
console.log(JSON.stringify(summary, null, 2));
for (const r of paired) console.log(r.task, arms.map(a => `${r[a].strict_success ? 'Y' : 'N'} ${r[a].facts} ${r[a].calls}c ${r[a].visible_bytes}B ${r[a].visible_bytes_excl_guide}xg kb=${r[a].kb_ops.join('+')}`).join(' | '));
