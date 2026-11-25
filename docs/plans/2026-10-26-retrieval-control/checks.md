# Checks

| node | requirement | verdict | evidence |
|---|---|---|---|
| A1 | Existing evidence audited | pass | audit.md |
| A2 | Fair control and metrics specified | pass | design-review.md, protocol.md |
| A3 | Primary benchmark methods checked | pass | research.md |
| C1 | Paired fixtures and gold frozen | pass | freeze.json; regenerated fixtures match all hashes (execution-manifest.json notes) |
| P1 | Controlled solver trials completed | pass with deviations | 16/16 answers in answers/; Codex partial run archived; deviations in results.md |
| C2 | Measured | pass | measurements.json: calls, bytes, truncation, exits, gold exposure, corpus hashes, off-adapter audit |
| R1 | Evidence-graded, blinded | pass (single agent assessor, uncalibrated) | assessor-packet.json, assessments.json, blind-key.json |
| C3 | Limits and next experiment reported | pass | results.md, results.json |
