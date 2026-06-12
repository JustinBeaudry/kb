// src/lib/templates.ts
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const TEMPLATES_DIR = join(__dirname, "..", "..", "templates");

export function getKbMdTemplate(): string {
  return readFileSync(join(TEMPLATES_DIR, "KB.md"), "utf-8");
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
