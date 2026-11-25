# Results: remote memory vs local vault vs no memory (APP-15, O3)

Run 2026-11-25 on the local compose fixture only. All data was synthetic. 24 scored trials: 8 tasks × 3 arms × 1 run. Each trial was one fresh `claude -p --model haiku` process (resolved to `claude-haiku-5-5`, Claude Code 2.1.332). The protocol, corpus, gold and harness were frozen before solving; the hashes are in `freeze.json`.

Raw data:
- `measurements.json`: trace-derived counts.
- `assessments.json`: blinded grades.
- `blind-key.json`: the blinding key.
- `results.json`: the joined table.
- `answers/` and `traces/`: per-trial output.
- `seed-receipt.json`: the published IDs, with no secrets.

## Headline

| Arm | Strict success | Fail conditions | Wrapper calls | Visible bytes (incl. guide) | Retrieval-only bytes | Failed calls | Cost (USD) |
|---|---|---|---|---|---|---|---|
| none | **2/8** | 0 | 0 | 0 | 0 | 0 | 0.0065 |
| local vault | **8/8** | 0 | 62 | 56,926 | 42,878 | 9 | 0.0231 |
| remote | **8/8** | 0 | 49 | 47,754 | 34,314 | 0 | 0.0214 |

- **Memory matters a lot. Which memory matters little here.** Without memory the agent solved only the two abstention tasks (T3, T8). It answered "unknown" to everything, which is correct there only because nothing was recorded. It invented nothing. Both memory arms solved all 8.
- **Remote and local tie on outcomes.** Remote read about 20% fewer retrieval bytes: 34,314 against 42,878, which is 19.97% fewer and just under the plan's 20% bar. It made 41 retrieval calls against 54 and had no failed calls.
- **All 9 failed local calls had one cause.** `kb map` returns `source: wiki/<page>.md`, but `kb get wiki/<page>.md` fails with `page not found`. Only `kb get <page>` works. Agents recovered every time, but each failure cost a call.
- **The plan's O3 usefulness threshold is not met.** It requires a tie or better plus either ≥2 additional solved cases or ≥20% fewer retrieval bytes with no loss. Remote is a tie with 19.97% fewer bytes, on 8 cases rather than the 24 the threshold assumes. No usefulness claim follows.

## Per task

✓ means strict success. Calls/bytes are wrapper totals, guide included.

| Task | Family | none | local | remote | local calls/bytes | remote calls/bytes |
|---|---|---|---|---|---|---|
| T1 | superseded decision | ✗ (unknown) | ✓ | ✓ | 5 / 4,277 | 6 / 5,093 |
| T2 | unverified-candidate trap | ✗ (unknown) | ✓ | ✓ | 6 / 4,409 | 4 / 3,648 |
| T3 | cross-project leakage trap | ✓ (unknown) | ✓ | ✓ | 8 / 18,115 | 9 / 9,513 |
| T4 | conflict | ✗ (unknown) | ✓ | ✓ | 7 / 4,366 | 5 / 3,855 |
| T5 | owner-verified multi-fact | ✗ (unknown) | ✓ | ✓ | 10 / 5,834 | 7 / 9,258 |
| T6 | superseded decision | ✗ (unknown) | ✓ | ✓ | 6 / 3,752 | 6 / 5,050 |
| T7 | unverified-candidate trap | ✗ (unknown) | ✓ | ✓ | 8 / 4,939 | 6 / 4,031 |
| T8 | abstention | ✓ (unknown) | ✓ | ✓ | 12 / 11,234 | 6 / 7,306 |

On every answerable task both memory arms exposed the gold record text in their traces.

## Trust labels

- **No arm presented an unverified candidate as verified.**
- **Local** exposed both candidates: 900/min in T2 and 04:00 UTC in T7. In both answers the agent named the candidate and labelled it an unverified proposal, citing the page's `status: candidate` and its Trust line. The trap was tested and passed, but it depended on the agent's judgment.
- **Remote** never exposed a candidate. Default-mode search and orient hide task candidates. The trap was avoided by design, so these trials say nothing about how the agent judges candidates.
- **Remote mislabels superseded revisions.** In T1 and T6 both revisions came back as `claim_status: verified` with a `conflicting_versions` warning. Neither was marked `superseded`, although the schema has that status. The agents got the current decision right only because the newer text says "This supersedes…". Without that sentence, remote would show two verified, conflicting records with no ordering signal; the revision IDs are random. Local showed a "Superseded version" section and had no ambiguity.
- **Remote hides owner review.** Owner-verified remote chunks still show `origin_trust: submitted`, as the 2026-11-24 usability review also found. One remote answer (T1) concluded that the records were "user-submitted notes rather than independently confirmed facts".

## Leakage

- **No answer attributed another project's fact to the asked project.**
- **Remote**: no trace exposed another project's records. The solver credential is scoped to one project, and no agent tried another one.
- **Local, T3**: the trace exposed the Birch escalation owner record through `kb recall escalation`. The answer correctly abstained for Atlas and mentioned Teo Lindqvist only as Birch's owner. This is cross-project exposure, not leakage. The local vault has no project scoping, so it relies on the agent's discipline.

## Integrity and execution

- 24 of 24 trials completed with a parsed JSON answer. None failed, none was rerun, and there were 0 permission denials, so no tool outside the wrapper was used. No trial hit the budget, and no output was truncated.
- One harness qualification run (T2 remote, on a discarded probe root, before the freeze) is excluded. See `protocol.md`.
- The first seeding attempt failed with `forbidden` because the seeding agent lacked `evidence:read`. Its credentials and one completed artifact with no claim stayed in the fixture. The artifact has no claim, so it is never retrievable. Seeding was rerun with suffixed client IDs (`-s2`).
- The fixture was torn down with `deploy/compose-smoke.sh down`: no containers or volumes remain. The fixture directory was deleted. The two macOS Keychain entries (service `com.beaudry.kb.remote`, profiles `o3-atlas` and `o3-birch`) were deleted and confirmed absent.

## Limits

- One model, one run per cell, and 8 tasks the designer wrote. Grading was by one agent assessor with no human calibration. Blinding was partial: evidence lists were withheld, but answer prose can hint at the arm.
- The corpus is tiny (15 records), so retrieval is easy. This trial tests trust labels, scoping and abstention, not recall at scale.
- The arms differ in how they represent knowledge, by design: candidates are hidden in remote and labelled in local, and project scoping is enforced in remote and absent in local. So the trap results reflect system design as much as agent behavior.
- The none arm's abstention successes come from having no access. They do not confirm a gap.
- Cost and tokens come from the harness. Wall time ran 4 trials in parallel on a shared host and is descriptive only.

## Follow-ups suggested by the evidence

1. Mark non-head target revisions `superseded`, or return only the head, in remote retrieval.
2. Expose owner adjudication on verified chunks instead of `origin_trust: submitted`.
3. Make `kb get` accept the `wiki/<page>.md` path that `kb map` returns.
4. Run a larger preregistered study with a candidate-trap variant in remote working mode, more cases, several runs per cell, and a corpus large enough to stress retrieval.
