---
date: 2026-05-08
topic: llm-tree-navigation-and-entire-session-offload
---

# LLM Tree Navigation and Entire Session Offload

## Problem Frame

`kb` currently mixes two responsibilities:

1. Curated markdown knowledge retrieval from the vault.
2. Claude-specific session capture, summarization, and extraction.

That made sense while the product was mainly a Claude Code memory plugin, but it now works against the more durable direction: `kb` should become an agent-neutral knowledge navigation substrate, with Claude, Codex, Cursor, and other hosts as adapters. Session/history search should move out of `kb` and defer to Entire.

The retrieval model also needs to move beyond keyword grep. Today `kb recall` walks `wiki/**` and returns substring matches. `qmd` is documented as an optional BM25 + vector layer in `templates/KB.md`, but the wiki research on PageIndex and technical-document RAG points to a different default: preserve document structure, let an LLM reason over a compact tree, and fetch exact sections by stable node IDs.

External grounding:
- PageIndex describes a JSON table-of-contents tree that sits in the LLM's active context, with node IDs used to fetch precise raw content: https://pageindex.ai/research/pageindex-intro
- PageIndex's technical-manual analysis shows why static vector similarity fails on repeated vocabulary, cross-references, procedures, and multi-hop safety context: https://pageindex.ai/blog/technical-manuals
- OpenKB compiles documents into a wiki and uses PageIndex-style trees for long documents, reinforcing the "compiled knowledge + navigable structure" direction: https://github.com/VectifyAI/OpenKB

## Navigation Flow

```text
Agent question + current chat context
  -> host adapter asks kb for a compact vault map
  -> kb returns cached tree/candidate nodes from curated wiki surfaces
  -> host LLM selects node IDs and identifies missing context
  -> adapter asks kb to fetch exact nodes/pages/neighbor sections
  -> kb returns reference envelopes with source, line ranges, and nav trace
  -> agent answers from evidence and cites the fetched material
```

This keeps `kb` provider-neutral while still making LLM tree navigation the default smart retrieval behavior. In agent-hosted use, the host model is already holding the user's question and chat state, so it is the best navigator. A standalone CLI LLM wrapper can be added later, but adapter-based navigation must not depend on it.

## Requirements

**Product Responsibility**
- R1. `kb` core must be described and designed as an agent-neutral curated knowledge CLI, not as a Claude-only session memory plugin.
- R2. Claude, Codex, Cursor, and other host integrations must be treated as adapters over the same core vault protocol.
- R3. New installs must not create, inject, summarize, or document `sessions/**` as a first-class `kb` surface.
- R4. Existing user `sessions/**` content must not be silently deleted during upgrade. It may be ignored, archived by an explicit migration command, or reported by `doctor`.
- R5. Entire owns session/history search. `kb` may preserve `entire://...` provenance when a user explicitly promotes session-derived knowledge into `wiki/**`, but `kb` must not parse or store Entire session history as its own source of truth.

**LLM Tree Navigation**
- R6. `kb` must expose a compact, cached structural map of curated knowledge. The map should be derived from `index.md`, `wiki/**` frontmatter, page titles, aliases, tags, page types, headings, line ranges, wikilinks, backlinks, and file hashes or mtimes.
- R7. LLM tree navigation is the default smart retrieval flow. The model sees the compact map or a filtered subtree, selects relevant node IDs, requests exact evidence, and iterates only when the fetched evidence is insufficient.
- R8. In adapter mode, the host LLM should perform tree navigation using deterministic `kb` map/fetch commands. The core CLI must not require a separate LLM API key for adapter-hosted navigation.
- R9. Before the LLM sees the tree, `kb` should reduce the candidate space with cheap local signals: title/alias matches, tag matches, heading matches, wikilink neighborhoods, backlinks, and lexical search over curated pages.
- R10. Retrieval must fetch natural document units: pages, headings, subsections, or neighboring sections. It must not depend on arbitrary fixed-size chunks as the primary unit.
- R11. Navigation must support cross-reference following. If fetched evidence points to another wiki page, heading, backlink, or explicit reference, the adapter should be able to fetch that related node without restarting the search.

**Evidence, Trust, and Safety**
- R12. All fetched material must continue to use the length-prefixed reference envelope from `src/lib/envelope.ts`, extended as needed with node IDs, heading metadata, and navigation trace.
- R13. Retrieval results must be evidence-first. Core `kb` retrieval should return sources and bounded content; final answer synthesis belongs to the host agent or an explicit higher-level command.
- R14. `raw/**` remains outside default retrieval and stays ask-gated. `sessions/**` is removed from the default retrieval/trust model entirely.
- R15. Access logging must remain minimized. Logs may record command, result counts, byte counts, query hash, and query length, but should avoid plaintext user queries by default.

**Performance**
- R16. SessionStart and PostCompact must not call an LLM. They should remain pointer-only in lazy mode and stay under the existing small payload target.
- R17. Map generation must be cached and incrementally invalidated from file hashes or mtimes so repeat navigation does not reread and reparse the whole vault when nothing changed.
- R18. LLM navigation prompt size must be bounded. If the full map exceeds the budget, `kb` must provide a locally filtered subtree or candidate list before model selection.
- R19. A vector database must not be mandatory. Optional tools such as `qmd` may contribute candidate generation, but the authoritative retrieval path is the structural map plus exact evidence fetch.
- R20. Exact-title, exact-alias, or explicit-page requests should have a zero-LLM fast path.

**Docs, Migration, and Packaging**
- R21. `README.md`, `templates/KB.md`, `skills/**`, `commands/**`, and hooks must be updated so session capture/extraction is no longer presented as a `kb` workflow.
- R22. `doctor` should detect legacy session setup, stale Stop hooks, or old vault directories and explain that Entire is the supported session/history path.
- R23. The codebase should become package-boundary-ready for a future monorepo split: core CLI/retrieval protocol in one boundary, host adapters in separate boundaries. The physical monorepo split is allowed but not required for the first tree-navigation release.
- R24. Adapter documentation must teach the same retrieval protocol across hosts: get the map, let the host model choose node IDs, fetch exact evidence, answer with citations.

## Success Criteria

- Fresh `kb init` does not scaffold `sessions/`, `sessions/summaries/`, or `sessions/.trash/`.
- Claude adapter setup does not install a Stop hook for `kb capture-session`.
- `kb` docs no longer tell users to extract from sessions; they point to Entire for session/history search.
- A tree-navigation query like "RAG over technical docs with PageIndex" surfaces the relevant wiki cluster: PageIndex, reasoning-based retrieval, vector RAG limitations, context blindness, and technical-manual examples.
- Returned evidence includes page source, line range, and enough node/heading metadata for an agent to cite and fetch related material.
- Lazy inject remains pointer-only and does not perform model calls.
- `qmd` absence does not disable core navigation.
- Existing vaults with legacy `sessions/**` are not destroyed by upgrade.

## Scope Boundaries

- Not cloning OpenKB's full ingestion, watch, chat, lint, or document-compilation lifecycle in this release.
- Not making vector search the default retrieval architecture.
- Not building session search, session summarization, or session extraction inside `kb`.
- Not deleting existing user session files automatically.
- Not requiring the physical monorepo split before the retrieval protocol is proven.
- Not adding PDF OCR or PageIndex OCR ingestion in the first slice. The first slice works over the existing markdown vault.
- Not adding multi-user ACLs, encryption, or a hosted service.

## Key Decisions

- **LLM tree navigation from day one.** Deterministic map/search alone would improve speed, but it would not address the PageIndex lesson: technical docs often require structure-aware, multi-hop relevance reasoning.
- **Host model navigates in adapter mode.** The agent already has the user's task and chat context. Using it as the navigator avoids a second provider configuration, avoids context loss, and keeps core `kb` provider-neutral.
- **Deterministic cache under the LLM.** The LLM should reason over a compact map, not over raw files. Local filtering and exact fetch keep the model prompt bounded.
- **Sessions move to Entire.** `kb` should curate durable knowledge, not maintain an agent transcript archive.
- **Evidence-first core.** Core retrieval returns inspectable evidence. Answer synthesis is adapter behavior or an explicit higher-level command.
- **Monorepo readiness before monorepo ceremony.** Package boundaries matter now; physical repo splitting should happen when a second real adapter makes the boundary pay for itself.

## Dependencies / Assumptions

- Existing retrieval commands and envelope code provide the trust-boundary base: `src/commands/recall.ts`, `src/commands/get.ts`, `src/commands/list-topics.ts`, and `src/lib/envelope.ts`.
- Existing lazy pointer behavior in `src/lib/inject/pointer.ts` is aligned with this direction.
- Current session ownership is verified in `src/lib/constants.ts`, `src/cli.ts`, `src/lib/inject/eager.ts`, `hooks/session-summary`, `commands/extract.md`, `skills/extract/SKILL.md`, `templates/KB.md`, and `README.md`.
- `src/lib/entire.ts` already exists as a narrow Entire integration point. Future work should keep that boundary narrow rather than parsing Entire internals.
- The exact command names for map, node fetch, and navigation are deferred to planning.

## Outstanding Questions

### Resolve Before Planning

(None. The product direction is settled: LLM tree navigation is in scope, and sessions move to Entire.)

### Deferred to Planning

- [Affects R6, R12][Technical] Exact node ID scheme, cache file location, and cache invalidation format.
- [Affects R6, R9, R18][Technical] Whether the map is one tree, several category trees, or a graph with tree projections for LLM prompts.
- [Affects R8, R13][Technical] Exact command split between "return map", "fetch node", "navigate", and "answer". Adapter-hosted navigation is required; standalone CLI synthesis is optional.
- [Affects R9, R19][Technical] Candidate generation algorithm and whether optional `qmd` results are merged, ranked, or only used as fallback hints.
- [Affects R10, R11][Technical] Section-boundary parsing rules for Markdown headings, backlinks, Obsidian wikilinks, and neighboring sections.
- [Affects R16, R18][Needs measurement] Prompt budget and latency targets for vaults at roughly 200, 1,000, and 10,000 pages.
- [Affects R21, R22][Technical] Legacy session migration UX: ignore only, archive command, or doctor-guided cleanup.
- [Affects R23][Technical] Whether the first implementation should physically split packages or keep a single repo with enforced internal boundaries.

## Next Steps

-> `/ce-plan` for structured implementation planning.
