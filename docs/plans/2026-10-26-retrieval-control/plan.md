# Retrieval Control Pilot Implementation Plan

> Execution shape: task-graph. This is a bounded measurement probe requested by the user, not a production feature or release.

**Goal:** Produce reproducible initial evidence and an honest measurement plan for KB versus file search.

**Architecture:** Generate a synthetic Markdown corpus and hidden gold, provide a recording shell/file wrapper to fresh solvers, then score paired saved answers independently. Keep all artifacts under this plan or temporary fixture directories.

**Tech Stack:** Existing Bun CLI, Node-compatible local probe scripts, ripgrep, fresh collaboration agents.

**Spec:** [protocol.md](protocol.md).

## Global constraints

- Production code, current KB skills, real vault and historical evaluation results stay unchanged.
- Eight tasks, two arms, one fresh solver per task/arm; 16 calls and 128 KiB visible output per trial.
- No extra provider credentials, installation, external paid service, publication or merge.
- No exact token, cost, model-version or confidence claim without measured evidence.

## Review focus

- Hidden expected answers must not enter solver manifests or prompts.
- Both arms must have equal source bytes and powerful regex/focused file access.
- Map metadata is navigation, not source-fact evidence.
- Count all exposed guidance/output, preserve failures, and distinguish cold indexing overhead.
- Corpus hashes must detect unauthorized knowledge edits and truncated output must remain explicit.

## Tasks

1. Audit existing evaluations and benchmark methodology; review controls before freezing.
2. Create `prepare.mjs` and `tool.mjs` as local experiment probes, with gold outside the trial directories. Self-check matched corpus hashes, wrapper bounds, regex search, numbered reads and KB source evidence before real trials. Archive probe checks separately from actual trials.
3. Freeze protocol, corpus, gold, prompts and source hashes. Dispatch each P1 solver with only its trial path, allowed tool instructions and question. Alternate/randomize arm order and record it.
4. Aggregate deterministic traces and knowledge hashes; independent assessor grades opaque answers against gold. Retain all failures.
5. Publish `results.md`, `results.json`, updated graph/checks/progress and reusable protocol. Run the repository gate if persistent code changes are introduced; do not modify KB to improve this pilot's scores.
