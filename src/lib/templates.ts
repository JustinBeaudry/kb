// src/lib/templates.ts
// KB.md is embedded at build time (Bun text import) rather than read from a
// path relative to this file, so compiled single-file binaries ship it too.
import kbMdTemplate from "../../templates/KB.md" with { type: "text" };

export function getKbMdTemplate(): string {
  return kbMdTemplate;
}

export const INDEX_MD_STUB = `# Vault Index

<!-- Group pages by topic category. Newest entries first within each category. -->
<!-- Format: - [[kebab-filename|Display Title]] — one-line description (~150 chars max) -->
`;

export const LOG_MD_STUB = `# Vault Log

<!-- Heading-level entries: ## [YYYY-MM-DD] type | description -->
<!-- Types: ingest, query, lint, refine, session -->
`;

export const CONTEXT_MD_STUB = `# Working Set

Current focus areas for context injection. Updated by the agent when focus shifts.

## Active
<!-- Pages and topics currently being worked on -->

## Background
<!-- Reference material relevant to active work -->
`;
