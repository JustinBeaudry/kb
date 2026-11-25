---
title: KB product
type: entity
created: 2026-11-13
updated: 2026-11-13
tags:
  - product
source: README.md
---

# KB product

KB is a local Markdown knowledge vault for people and agents. The same pages are edited in Obsidian or a text editor and retrieved through a CLI that returns source paths, line ranges, and bounded evidence. Shell and file access are enough for the local workflow.

An explicit remote memory client talks to a separate graph service. It never silently falls back to the local vault. Native hooks, qmd, and MCP are optional.

The pattern follows Andrej Karpathy's LLM Wiki. The repository is `JustinBeaudry/kb`. Release 0.7.0 is the version recorded in this vault's `.kb/state.json`.

## Role

- Local vault: curated `wiki/`, archived `raw/`, index, working set, and change log.
- Retrieval: `kb map`, `kb get-node`, `kb get`, `kb recall`, and `kb list-topics`.
- Sessions: capture, summarize, and mark extracted, with an optional summarizer command.
- Remote memory: `kb remote` against an enrolled API, plus `kb-mcp` for stdio MCP.

## Relationships

- [[local-vault|Local vault]] is the default read and write surface.
- [[remote-memory|Remote memory]] is the private graph service.
- [[delivery-status|Delivery status]] records what has shipped and what remains.

## Backlinks

- [[local-vault|Local vault]] describes the on-disk product.
- [[remote-memory|Remote memory]] describes the separate service.
- [[delivery-status|Delivery status]] uses this page as the product name.
