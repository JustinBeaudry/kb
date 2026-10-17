# Template: bounded PR corrections

1. Pin reviewed base/head and enumerate findings with observable failures.
2. Group independent fixes by file ownership; give each worker only its finding, relevant code and expected behavior.
3. Write regressions first. Root runs each RED gate before releasing that implementation node.
4. Apply the smallest complete correction; keep APIs, stored data and unrelated work intact.
5. Root runs GREEN, required stack checks and distribution/evaluation checks affected by the change.
6. For instruction-routing changes, pair deterministic resource checks with a limited consuming-agent smoke. State what that smoke does not establish.
7. Review spec compliance before code quality. Send failed gates back only to their owner, capped at three correction attempts.
8. Commit and push when authorized; verify actual remote PR head and open state. Keep publication distinct from merge/deployment.
9. Reconcile `graph.md`, `checks.md`, `progress.md` and the concise verification report with observed results.

Record TASK, OUTPUT, CHECKS, SOURCES, SCOPE and LIMIT before starting. Use synthetic temporary fixtures for subprocess and recovery tests. Watch side effects after timeout; follow recovery instructions literally; preserve original records and cached user content.

Keep failed verification runs in the record even when later checks pass. Investigate whether the failing code changed, run focused repeats, and disclose an unconfirmed cause without weakening the test. Record the verified implementation commit before adding an evidence-only commit, then verify the final remote head when updating the PR description.
