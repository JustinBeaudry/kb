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
2. If qmd MCP tools (`mcp__qmd__qmd_deep_search`, `mcp__qmd__qmd_search`,
   `mcp__qmd__qmd_get`) are present in your tool list, use `qmd_deep_search`
   first. Otherwise read `index.md` and walk wikilinks.
3. Synthesize the answer, citing sources as `[[Page Name]]`.
4. If the answer contains novel knowledge, write a new wiki page and update
   `index.md`.
5. Append to `log.md`: `## [YYYY-MM-DD] query | <summary>`.
