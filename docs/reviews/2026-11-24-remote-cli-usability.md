# Remote CLI usability review

Date: 2026-11-24. Linear: APP-16. Reviewer: Claude Code session.

The 2026-10-29 preflight scored the remote plan 6.8 of 10 on paper and assigned a live review. This is that review. It ran against the disposable local compose fixture (`deploy/compose-smoke.sh up`), not production. All data was synthetic.

## What was timed

| Step | Time |
|---|---|
| Build and start the fixture | 2 min 2 s (46 s startup after build) |
| Wait for first backup to open maintenance | about 45 s more |
| `credential-set` | 0.15 s |
| `status` | 0.32 s |
| First `orient` (cold) | 2.0 s, no results |
| Owner path: enroll collector, upload, candidate, owner preview and verify | 9.6 s |
| First cited `search` | 2.9 s cold, 0.13 s warm |
| `fetch` of the cited excerpt | under 0.2 s |
| Continuation page | under 0.2 s |

From a fresh fixture, an agent reaches its first cited result in about 3 minutes. Most of that is build, startup, and the first backup. Once the service is open, a cited answer takes under 3 seconds.

## What works

- Credential failures are specific: `credential_unavailable` tells the user to run `credential-set` with the bearer on stdin.
- Remote failures say that no local fallback was used.
- Continuation works across pages and the response carries an `action` that says to repeat identical parameters.
- Fetch labels partial passages (`partial_source_passage`) and source evidence (`source_evidence_is_not_a_verified_claim`).
- Bearers never appear in argv or output.

## Findings

| Severity | Finding | State |
|---|---|---|
| High | Every CLI argument error returned the same `invalid_input` text and never named the missing or bad option (missing `--project`, non-HTTPS origin, positional query, bad bearer, budget over limit). | Fixed in this branch: `error.detail` names the failed check with fixed text and never echoes argument values. |
| High | The HTTP API returns `invalid_input` / "Invalid request." with no field. Omitting the required `applicability` object on a candidate or evidence ref produced a bare 400, and the server log has no reason either. | Open. Needs a schema-violation detail (field path only, no values). |
| Medium | Success output is a byte-length line followed by JSON. This is intentional framing, but help did not say so, so `kb remote search ... \| jq` fails. | Help now describes the framing. Consider a `--json` mode that prints only the body. |
| Medium | Payload efficiency is low. A chunk with 55 bytes of claim text is about 770 bytes, mostly 64-hex IDs and the owner client ID. At `--budget 1600` a page holds 1 result. | Open. Consider short evidence references and dropping repeated origin data. |
| Medium | A budget that can't fit one chunk returns `budget` instead of a truncated page with a continuation. | Open. Decide whether one oversized chunk should split. |
| Medium | Changing `--budget` while passing a continuation returns `stale`, which reads like a data change. It is a parameter mismatch. | Open. A distinct code or detail would help. |
| Low | An owner-verified record shows `origin_trust: submitted` and no `verification_outcome`. A reader can't tell from the chunk that an owner adjudicated it. | Open. Expose the `owner_adjudicated` label. |
| Low | `kb remote orient --help` prints the same help as `kb remote --help`. There are no examples and no pointer to enrollment (`kb-admin enroll` on the host). | Open. |
| Low | Readiness lags up to 30 s after maintenance opens, so the first calls after startup return `unavailable`. | Documented behavior; the CLI could add "retry after readiness" to the action. |

## Score

Against the preflight's rubric, the live experience is about 7 of 10 after the CLI detail fix. Reaching the 8 target needs the server-side error detail and a body-only output mode.
