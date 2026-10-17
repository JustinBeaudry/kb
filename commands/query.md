---
name: query
description: Answer a question from KB evidence without changing knowledge
argument-hint: "<question>"
---

# Query the vault

Load the registered `kb` skill, then read `references/query.md` from that skill's
directory for the user's question. If no question was supplied, ask for it.

Follow `kb map` → select nodes → `kb get-node`, or direct `kb get` for a known
page. Cite evidence, disclose uncertainty and leave knowledge unchanged.
This slash command is optional; natural-language questions use the same guide.
