# Checks

| node | requirement | verdict | evidence |
|---|---|---|---|
| D1 | Design answers scale/weaker-solver and guide-vs-commands questions | pass | protocol.md |
| C1 | Pilot pages, tasks, gold, guides identical; ~1,000 pages; gold unique; arm C guide within 5% of A | pass | prepare.mjs assertions (pilot subset hash, guide hashes e107651b…/f856939e…, 975 vs 962 bytes, 1,002 pages); freeze.json |
| C2 | Harness restricts solver to the adapter; frozen before trials | pass with caveat | probe: empty cwd denies `cat`/`grep`/`bun -e`; smoke run on non-task question; freeze commit 13be34d. Caveat: compound commands of allowed parts pass (A T06) |
| P1 | 24 fresh Haiku trials, ≤4 concurrent | pass | answers/ (24/24 exit 0, parsed); model claude-haiku-5-5 in every init event |
| M1 | Mechanical measures, integrity, off-adapter audit | pass | measurements.json: all corpora unchanged, 0 permission denials, 2 off-adapter commands disclosed |
| R1 | Blinded assessment, verbatim output | pass (single agent assessor, uncalibrated; one inconsistency noted) | assessor-packet.json, assessments-raw/, assessments.json, blind-key.json |
| C3 | Paired table, B-vs-C, scale effect, limits | pass | results.md, results.json |
