---
title: Match review ceremony to codebase scale; prefer walkthrough over multi-agent fan-out for small focused questions
date: 2026-04-25
category: docs/solutions/workflow-issues/
module: compound-engineering-workflows
problem_type: workflow_issue
component: development_workflow
severity: medium
resolution_type: workflow_improvement
applies_when:
  - User asks a concrete diagnostic question ("how does X work, is it right?") about a small codebase (<5K LOC)
  - Considering /ce:brainstorm + /ce:plan + multi-agent parallel review for a solo-dev or single-module project
  - A document-review pass on a planning artifact returns multiple P0 findings against the plan's own premise
  - User signals scope concern ("this feels like overkill", "losing faith in this design")
  - Workflow ceremony (agents, synthesis, scripted runs) exceeds the LOC of the artifact under review
related_components:
  - tooling
  - documentation
tags:
  - ce-brainstorm
  - ce-plan
  - scope-matching
  - multi-agent-overkill
  - walkthrough-mode
  - yagni
---

# Match review ceremony to codebase scale

## Context

The user asked a small, concrete question: "systematically review the cairn implementation — how it works, frontmatter, context injection, how much context." Cairn is ~2,600 LOC, solo-developed, version 0.6.0. The right-sized response was a 30-minute walkthrough.

Instead, brainstorming chained reasonable-sounding clarifications ("all of the above?", "multi-persona parallel review?") into a seven-agent orchestration: 2 deep-lens agents (injection-correctness, frontmatter/Obsidian-schema), 4 breadth personas (correctness, reliability, architecture, CLI agent readiness), 1 scripted Claude-runs behavioral exercise, plus a synthesis pass writing `docs/reviews/*.md`. Each individual `?` from the brainstorm flow was a yes/no the user could plausibly say yes to. The aggregate was ceremony.

The signal that the design was wrong arrived loudly: document-review on the plan returned **5 P0 findings and 10 P1 findings**, several of them existential — the entire plan rested on in-session parallel `Agent` dispatch that had never been demonstrated, the synthesis agent couldn't actually read sibling outputs, and "read-only review" claims contradicted the unit instructions ("extend tests/inject.test.ts" vs "temp copy under $TMPDIR"). The agent's instinct was to patch the plan. The user's instinct — "this feels like insane overkill, I'm losing faith in this design" — was correct.

After scrapping the plan and doing a direct walkthrough of 5 files (~1,000 LOC) over 30 minutes, four concrete bugs surfaced and were fixed in 2 commits: a 20KB `index.md` silently breaking the 2KB injection budget, 224 unmigrated legacy session files in three schemas, a gating-condition bug on the vault-context disclaimer, an incomplete JSON-escape, and a migration tool that crashed on a single malformed-YAML file.

This is not the first time the same recognition has surfaced. (session history) On 2026-04-21 a Cursor agent running `/ce:brainstorm` on an 80-line context-hygiene doc explicitly noted that dispatching document-review's sub-agents "feels heavyweight for an 80-line doc" and self-reviewed inline against the checklist instead. The user did not object — it was accepted silently. The current session is the second documented instance of an agent (or user) recognizing that ceremony scale was miscalibrated to artifact size. The lesson should be made explicit so the third instance is the heuristic, not another rediscovery.

## Guidance

Before scaling a review/audit task into a multi-agent orchestration, run this triage:

```
LOC under review:        < 5K   → walkthrough
                         5K-25K → one focused agent OR walkthrough
                         > 25K  → consider orchestration
Question shape:          "how does X work?"        → walkthrough
                         "find bugs in X"          → walkthrough or 1 agent
                         "audit X across N axes"   → maybe orchestration
                         "compare across services" → orchestration
Prior agent count:       proposed > 3 on a < 5K-LOC codebase → STOP, justify
Dedup expectation:       > 30% expected overlap between personas → fewer personas
```

Concrete rules:

1. **The "all of the above?" trap.** When a brainstorm asks the user to choose among scopes and they answer "all," the agent must re-cost the union, not just sum the parts. Three reasonable axes do not compose into a reasonable plan.
2. **Treat document-review P0s as plan-level signal, not a bug list.** If a planning doc has more than ~2 P0s, the plan is wrong — rethink, don't patch. Patching produces a fragile plan defended by ad-hoc fixes. (session history) The April 18 schema-migration plan's adversarial review surfaced "premise rests on N=1 evidence" as a P1; the user's response was to drop scope to a simpler Option C, not patch the plan. The same instinct served them here.
3. **Reject orchestration when expected overlap is high.** If three breadth personas would all read the same five files, you have one persona, not three.
4. **A "read-only review" that needs a temp copy is not a read-only review.** Internal contradictions in the plan are tells.
5. **When the user says "feels like overkill," do not defend the investment.** The brainstorm + plan are sunk cost. Offer downsizings, not patches.
6. **Default to walkthrough for codebases under ~5K LOC.** Orchestration overhead (dispatch, synthesis, dedup, doc-writing) exceeds total review time on small targets.
7. **Document-review overhead scales with document size.** (session history) Below ~2 pages or ~100 lines, an inline checklist self-review beats parallel persona dispatch — the Cursor agent's April 21 instinct should be the default, not the exception.

## Why This Matters

Token, wall-clock, and signal-quality costs all point the same way:

- **Token cost.** Seven agents × full-file reads × synthesis-prompt-with-7-outputs is roughly 20–50× a direct walkthrough. The synthesis prompt alone risks context exhaustion (one of the P0s).
- **Wall-clock cost.** Walkthrough: 30 minutes, 4 bugs found, 2 commits. Orchestration estimate: hours of dispatch + synthesis + review-of-review, with output as a `docs/reviews/*.md` artifact rather than fixed bugs. (session history) The April 20 session-capture brainstorm's double-document-review pattern (5 agents on the brainstorm doc + 5 agents on the resulting plan) consumed roughly 35 minutes of elapsed ceremony before any code ran, and the session was interrupted by the user mid-stream — a friction signal that went unrecorded at the time but matches the explicit pushback in the current session.
- **Signal quality.** The walkthrough bugs were specific and falsifiable: `index.md is 20KB`, `224 legacy files unmigrated`, `disclaimer gated on wrong condition`. In a seven-persona dedup pass, the 20KB `index.md` finding likely lands in P2-noise under "context budget concerns" alongside three persona-specific theoretical risks. Concrete bugs get smoothed into themes.
- **Identity drift.** Cairn is a solo-dev plugin. Applying enterprise-shaped review processes (multi-persona, synthesis docs, severity matrices) to a 2,600-LOC plugin teaches the wrong lesson about when ceremony is justified, and erodes the user's trust in the agent's judgment ("I'm losing faith in this design").

## When to Apply

Apply this guidance when:

- The target is a single project under ~5K LOC, especially solo-dev or pre-1.0.
- The question is investigative ("how does this work", "find bugs", "is this correct") rather than comparative across many systems.
- A proposed plan has 3+ agents on a codebase that fits in one agent's context.
- Document-review returns 3+ P0s on a planning doc.
- The user expresses doubt about scope ("feels like overkill," "this is a lot").
- Expected persona overlap exceeds ~30%.

Do **not** apply when the codebase is genuinely large (25K+ LOC), the axes are genuinely independent (security audit + perf audit + a11y audit), or the work product must be a durable artifact for stakeholders rather than fixed bugs.

## Examples

**Before (seven-agent design):**
- Unit 1: Deep-lens injection-correctness agent
- Unit 2: Deep-lens frontmatter/Obsidian-schema agent
- Unit 3: Breadth correctness persona
- Unit 4: Breadth reliability persona
- Unit 5: Breadth architecture persona
- Unit 6: Breadth CLI-agent-readiness persona
- Unit 7: Scripted Claude-runs behavioral exercise
- Synthesis: collate seven outputs into `docs/reviews/2026-04-24-cairn-systematic-review.md`
- Result before execution: 5 P0s in document-review, plan abandoned.

**After (walkthrough):**
- Read `hooks/inject` (109 lines)
- Read `src/lib/frontmatter.ts` (47 lines)
- Read `src/lib/manifest.ts` (~160 lines)
- Read `templates/CAIRN.md` (~390 lines)
- Read `skills/cairn/SKILL.md` (~130 lines)
- Inspect `~/cairn` vault on disk
- Result: 4 bugs found and fixed in 2 commits, 30 minutes (commits `7d5269d`, `5cdea88`).

**Bugs the walkthrough found that orchestrated review would have buried:**

- *20KB `index.md` vs 2KB injection budget.* Concrete, on-disk, falsifiable. In a seven-persona pass, this gets generalized into "context-budget enforcement is best-effort" and lands as P2 design feedback.
- *224 legacy session files in three schemas.* Found by `ls ~/cairn/sessions | wc -l` plus reading three sample files. No persona was assigned to "actually look at the user's vault."
- *Migration tool crashes on one malformed-YAML file.* Found by running the tool. Behavioral persona was scoped to scripted Claude runs, not the migration CLI.
- *JSON escape missing control chars beyond `\n\r\t`.* Found by reading `hooks/inject` line by line. A correctness persona summarizing 109 lines of bash would likely note "escaping looks reasonable" and move on.

The pattern: walkthroughs find bugs by **co-locating reading and execution on the actual artifact**. Orchestrated reviews find themes by summarizing reads, and themes lose specificity at the synthesis step.

**Contrast case — when orchestration was right-sized.** (session history) On 2026-04-18 the user ran `/ce:review` on cairn's skills against an upstream Karpathy gist. That review dispatched parallel sub-agents and returned 6 findings without any ceremony complaint. The difference: focused external comparison target, bounded artifact set (3 skill files + templates), specific axis (does-this-match-source). When the question genuinely has multiple independent axes and an external reference frame, parallel personas earn their cost.

## Related

- Triggering session: the cairn 0.6.0 review (commits `7d5269d`, `5cdea88` on `main`, 2026-04-25).
- Sibling instinct from a different agent: 2026-04-21 Cursor `/ce:brainstorm` on context-hygiene, where the Cursor agent self-reviewed an 80-line doc inline rather than dispatching parallel personas — the same calibration judgment, accepted silently. (session history)
- This is the first entry under `docs/solutions/workflow-issues/` and bootstraps the directory.
