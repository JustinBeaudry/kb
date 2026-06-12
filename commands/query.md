---
name: query
description: Query the KB vault and synthesize an answer from wiki pages
argument-hint: "<question>"
---

# Query the KB Vault

The user wants an answer grounded in vault content. Invoke the `kb` skill
and follow its Query workflow against the argument. If no argument was
provided, ask the user for their question.

Expected behavior:

1. Read `KB.md` if you haven't this session.
2. Run `kb map <query>` for a budget-bounded structural map, select the most
   promising node IDs from the summaries, then fetch exact evidence with
   `kb get-node <id>` (add `--neighbors` or `--follow-wikilinks <n>` when
   useful). Fall back to `kb recall <query>` for plain-text search. If qmd
   MCP tools (`mcp__qmd__qmd_deep_search`, `mcp__qmd__qmd_search`,
   `mcp__qmd__qmd_get`) are present, `qmd_deep_search` can serve as an
   additional first pass.
3. Synthesize the answer, citing sources as `[[kebab-filename|Display Title]]`.
4. If the answer contains novel knowledge, write a new wiki page and update
   `index.md`.
5. Append to `log.md`: `## [YYYY-MM-DD] query | <summary>`.
