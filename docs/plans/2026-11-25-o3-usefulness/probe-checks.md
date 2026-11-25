# Probe checks (before freeze)

These checks ran against the seeded fixture before any scored solver ran. They test the wrappers and how the system behaves. They are not outcomes.

## Remote (`kb remote`, solver credential scoped to one project)

- `status` reports `authorized_project_ids: [atlas-api]` and capabilities `status:read, retrieval:read, evidence:read`.
- `search --project birch-pipeline` with the Atlas credential returns `forbidden`, exit 1. Cross-project reads are blocked by the credential.
- `search --project atlas-api` with no query lists all 8 eligible Atlas chunks (about 6 KB). **Both revisions of `atlas-webhook-queue` are returned**, each with `claim_status: verified` and warning `conflicting_versions`. The older revision is not labelled `superseded`, although the enum has that value. Only the text of the newer revision says it supersedes the older one.
- Unverified candidates (`atlas-rate-limit-proposal`, `birch-batch-proposal`) never appear in default-mode search or orient.
- Search is lexical. `--query dedupe` returns `no_results` for a record whose text says "deduplicates". `--query 'escalation owner on-call'` returns `no_results` in Atlas, which is correct.
- Verified chunks carry `origin_trust: submitted` and no `verification_outcome`. This matches the 2026-11-24 usability review finding.
- A chunk is about 750 bytes for 50 to 200 bytes of claim text.

## Local (`kb` CLI on the synthetic vault)

- `kb map 'rate limit'` returns both the verified page and the candidate page. Each is labelled by title only; the trust label is in the page body.
- `kb recall escalation` from an Atlas question returns the Birch escalation page, with its project shown in tags and body. The local vault does not scope by project.
- `kb list-topics` returns no chunks for this vault, because the tags are `project/...`.

## Harness

- `claude -p --model haiku --setting-sources "" --strict-mcp-config --tools ""` in `/tmp` reports no tools and no knowledge of kb, Atlas, Birch or prior sessions. The resolved model is `claude-haiku-5-5`.
- `--allowedTools` is variadic, so the prompt must follow `--`.
