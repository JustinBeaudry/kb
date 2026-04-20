# Karpathy LLM Wiki Alignment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align Cairn's vault conventions, injection script, and skill with Karpathy's LLM Wiki pattern — page type taxonomy, cascading ingest, categorized index, prioritized injection, and working set.

**Architecture:** Template-driven changes. CAIRN.md template defines vault conventions, inject bash script reads vault files in priority order, SKILL.md teaches Claude how to use it all. One new scaffold file (`context.md`) added to init.

**Tech Stack:** Bash (inject hook), TypeScript/Bun (scaffold), Markdown (templates/skill)

**Spec:** `docs/superpowers/specs/2026-04-15-karpathy-alignment-design.md`

---

### Task 1: Add `context.md` to scaffold

Add the `context.md` template and wire it into the vault scaffold so `cairn init` creates it.

**Files:**
- Modify: `src/lib/constants.ts:9`
- Modify: `src/lib/templates.ts:19-25`
- Modify: `src/lib/vault.ts:41-44`

- [ ] **Step 1: Write failing test — scaffold creates context.md**

Add to `tests/vault.test.ts` inside the `scaffoldVault` describe block:

```typescript
it("should create context.md", () => {
  const testDir = join(tmpdir(), `cairn-test-${Date.now()}`);
  scaffoldVault(testDir);

  expect(existsSync(join(testDir, "context.md"))).toBe(true);
  const content = readFileSync(join(testDir, "context.md"), "utf-8");
  expect(content).toContain("Working Set");

  rmSync(testDir, { recursive: true });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/vault.test.ts`
Expected: FAIL — `context.md` does not exist after scaffold

- [ ] **Step 3: Add CONTEXT_MD_STUB to templates.ts**

Add after `LOG_MD_STUB` in `src/lib/templates.ts`:

```typescript
export const CONTEXT_MD_STUB = `# Working Set

Current focus areas for context injection. Updated by the agent when focus shifts.

## Active
<!-- Pages and topics currently being worked on -->

## Background
<!-- Reference material relevant to active work -->
`;
```

- [ ] **Step 4: Add context.md to VAULT_FILES in constants.ts**

Change line 9 in `src/lib/constants.ts`:

```typescript
export const VAULT_FILES = ["CAIRN.md", "index.md", "log.md", "context.md"] as const;
```

- [ ] **Step 5: Add context.md content to FILE_CONTENT map in vault.ts**

Import `CONTEXT_MD_STUB` and add to the map. In `src/lib/vault.ts`:

```typescript
import { getCairnMdTemplate, INDEX_MD_STUB, LOG_MD_STUB, CONTEXT_MD_STUB } from "./templates";
```

And update `FILE_CONTENT`:

```typescript
const FILE_CONTENT: Record<string, string> = {
  "CAIRN.md": getCairnMdTemplate(),
  "index.md": INDEX_MD_STUB,
  "log.md": LOG_MD_STUB,
  "context.md": CONTEXT_MD_STUB,
};
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/vault.test.ts`
Expected: ALL PASS

- [ ] **Step 7: Update init integration test to check context.md**

Add to `tests/init.test.ts` inside "should scaffold vault at custom path", after the existing `expect` lines:

```typescript
expect(existsSync(join(vaultDir, "context.md"))).toBe(true);
```

- [ ] **Step 8: Run full tests**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 9: Commit**

```bash
cd /Users/justinbeaudry/Projects/cairn
git add src/lib/constants.ts src/lib/templates.ts src/lib/vault.ts tests/vault.test.ts tests/init.test.ts
git commit -m "feat: add context.md (working set) to vault scaffold"
```

---

### Task 2: Rewrite CAIRN.md template

Replace the entire `templates/CAIRN.md` with the new version containing page type taxonomy, expanded frontmatter, cascading ingest, discuss-before-filing, categorized index, heading-format log, contradiction lint, and context.md references.

**Files:**
- Rewrite: `templates/CAIRN.md`

- [ ] **Step 1: Rewrite templates/CAIRN.md**

Replace the entire file with:

```markdown
# Cairn — Knowledge Vault

This file defines how you interact with this vault. Follow these conventions exactly.

## Vault Structure

| Directory / File | Purpose | Who writes |
|------------------|---------|------------|
| `wiki/` | Knowledge pages — entities, concepts, summaries, comparisons, overviews | Agent |
| `raw/` | Archived source documents — originals preserved for provenance | Agent (copies here during ingest) |
| `sessions/` | Session summaries — auto-generated at session end | Agent (via hook) |
| `context.md` | Working set — current focus areas for context injection | Agent (with user direction) |
| `index.md` | Categorized pointer index — one-line entry per wiki page | Agent |
| `log.md` | Chronological record — append-only, heading-level entries | Agent |

## Page Types

Every wiki page has a `type` field in frontmatter. Use the type that best fits the content:

| Type | Purpose | Content shape |
|------|---------|---------------|
| `concept` | Ideas, techniques, patterns | Definition, examples, related concepts, when to use |
| `entity` | People, orgs, tools, projects | Description, role/purpose, relationships, links |
| `source-summary` | Digest of a `raw/` document | Key takeaways, quotes, what it changes about existing knowledge |
| `comparison` | X vs Y analysis | Criteria, trade-offs, recommendation, when to pick each |
| `overview` | Topic area index with narrative | Guided tour of a domain, links to all relevant pages |

## Frontmatter

Every wiki page starts with YAML frontmatter:

```yaml
---
title: Page Title
type: concept | entity | source-summary | comparison | overview
source: "[[raw/filename.md]]" or URL
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags:
  - topic/subtopic
aliases:
  - Alternate Name
---
```

Required fields: `title`, `type`, `created`, `updated`, `tags`.
`source` is required for `source-summary` pages, optional for others.
`aliases` is optional.

## Markdown Conventions

### Wikilinks
Use `[[wikilinks]]` for all internal references:
- `[[Page Name]]` — link to page
- `[[Page Name|Display Text]]` — aliased link
- `[[Page Name#Heading]]` — link to heading
- `[[Page Name#^block-id]]` — link to block

### Embeds and Callouts
- Embed a page: `![[Page Name]]`
- Embed a heading: `![[Page Name#Heading]]`
- Callouts: `> [!info]`, `> [!warning]`, `> [!tip]`

## Workflows

### Ingest

When the user asks you to ingest something (file path, URL, pasted text, or conversation context):

1. Read the source from wherever it lives — a local path, URL, or inline content.
2. If the source is a file, copy it to `raw/` for provenance (preserving original filename).
3. **Present key takeaways to the user**: entities found, concepts identified, relationships discovered, and any contradictions with existing wiki content. Confirm which are worth filing before proceeding.
4. For each confirmed entity/concept, create or update a wiki page in `wiki/`.
5. **Review all existing wiki pages that relate to this source.** Update them with new cross-references, corrections, or additional context from this source. A single ingest typically touches 5-15 existing pages.
6. Link related pages with `[[wikilinks]]` — every page links to at least 2 others.
7. Update `context.md` if the ingested material relates to current focus areas.
8. Add entries to `index.md` under the appropriate category.
9. Append to `log.md`:

```markdown
## [YYYY-MM-DD] ingest | <source title>

Created [[Page A]], [[Page B]]. Updated [[Page C]], [[Page D]], [[Page E]].
```

### Query

When the user asks a question the vault might answer:

1. Read `index.md` first to find relevant pages.
2. Follow `[[wikilinks]]` to read related pages.
3. Synthesize your answer, citing sources as `[[Page Name]]`.
4. If your answer contains novel knowledge worth keeping, write a new wiki page and add it to `index.md`.
5. Append to `log.md`:

```markdown
## [YYYY-MM-DD] query | <brief question summary>

Synthesized from [[Page A]], [[Page B]]. Created [[Page C]] (if applicable).
```

### Lint

When the user asks you to lint the vault:

1. **Orphan pages**: wiki pages with no inbound links from other wiki pages or index.md.
2. **Dead links**: `[[wikilinks]]` pointing to non-existent pages.
3. **Missing frontmatter**: wiki pages without required YAML frontmatter fields (`title`, `type`, `created`, `updated`, `tags`).
4. **Stale content**: pages with `updated` date older than 30 days.
5. **Missing types**: wiki pages without a valid `type` field.
6. **Contradictions**: claims in one wiki page that conflict with claims in another. Flag both pages and the conflicting statements. Contradictions are the most dangerous vault failure mode.
7. Report all findings. All fixes are opt-in — do not auto-fix without user approval.

## Index Format

`index.md` groups pages by topic category, newest first within each category:

```markdown
# Vault Index

## Architecture
- [[Layered Architecture]] — Handler → Service → Repo/Gateway boundaries
- [[Dependency Injection]] — Constructor-based DI pattern for Go services

## Tools
- [[React 19]] — New features: use hook, actions, compiler
- [[Stripe]] — Payment gateway integration patterns
```

Categories emerge organically during ingest. Create new categories as needed. Each entry: `- [[Page Name]] — one-line description` (~150 chars max).

## Log Format

`log.md` uses heading-level entries for Obsidian folding and CLI parsing:

```markdown
## [YYYY-MM-DD] type | description

Details of what was created/updated.
```

Types: `ingest`, `query`, `lint`, `session` (lowercase).

## Working Set

`context.md` tracks current focus areas. The inject hook reads this first — it gets highest priority in the context budget.

```markdown
# Working Set

## Active
- [[Page Name]] — why it matters right now

## Background
- [[Page Name]] — reference material for active work
```

Update `context.md` when:
- The user starts or finishes a major work area
- Ingested material relates to current focus
- The user explicitly asks to shift focus

## Rules

1. **Never modify files in `raw/`.** They are archived originals preserved for provenance.
2. **Every wiki page links to 2+ related pages.** Isolated pages are less useful.
3. **Frontmatter on every wiki page.** Required: `title`, `type`, `created`, `updated`, `tags`.
4. **Use the correct page type.** Match content to the five defined types.
5. **Discuss before filing.** Present takeaways to the user before creating pages during ingest.
6. **Cascade updates.** When ingesting, review and update all related existing pages.
7. **Categorize the index.** Group entries by topic domain, not flat chronological.
8. **Heading-level log entries.** Use `## [YYYY-MM-DD] type | description` format.
9. **Maintain the working set.** Keep `context.md` current with active focus areas.
10. **Skeptical memory.** Before acting on any recalled fact, verify it against the current codebase or source. Memory is a hint, not truth.
11. **Atomic pages.** One concept per wiki page. If a page covers multiple topics, split it.
```

- [ ] **Step 2: Verify template renders correctly**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun run -e "import { getCairnMdTemplate } from './src/lib/templates'; console.log(getCairnMdTemplate().substring(0, 100))"`
Expected: prints first 100 chars of new template starting with `# Cairn — Knowledge Vault`

- [ ] **Step 3: Run full tests**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS (template change doesn't break existing tests since tests don't assert on CAIRN.md content beyond existence)

- [ ] **Step 4: Commit**

```bash
cd /Users/justinbeaudry/Projects/cairn
git add templates/CAIRN.md
git commit -m "feat: rewrite CAIRN.md with page types, cascading ingest, categorized index"
```

---

### Task 3: Update index.md and log.md templates

Update the stub templates to match the new categorized index format and heading-level log format.

**Files:**
- Modify: `src/lib/templates.ts:13-25`

- [ ] **Step 1: Update INDEX_MD_STUB in templates.ts**

Replace the `INDEX_MD_STUB` export in `src/lib/templates.ts`:

```typescript
export const INDEX_MD_STUB = `# Vault Index

<!-- Group pages by topic category. Newest entries first within each category. -->
<!-- Format: - [[Page Name]] — one-line description (~150 chars max) -->
`;
```

- [ ] **Step 2: Update LOG_MD_STUB in templates.ts**

Replace the `LOG_MD_STUB` export in `src/lib/templates.ts`:

```typescript
export const LOG_MD_STUB = `# Vault Log

<!-- Heading-level entries: ## [YYYY-MM-DD] type | description -->
<!-- Types: ingest, query, lint, session -->
`;
```

- [ ] **Step 3: Update inject test fixture to match new index format**

In `tests/inject.test.ts`, update `makeTestVault()` — the `index.md` content now uses categorized format:

```typescript
writeFileSync(
  join(dir, "index.md"),
  "# Vault Index\n\n## Architecture\n- [[Auth Flow]] — OAuth2 implementation notes\n- [[DB Schema]] — PostgreSQL schema decisions\n"
);
```

Also update the assertion in the first test. Change:

```typescript
expect(json.hookSpecificOutput.additionalContext).toContain("Vault Index");
```

To:

```typescript
expect(json.hookSpecificOutput.additionalContext).toContain("Vault Index");
expect(json.hookSpecificOutput.additionalContext).toContain("Architecture");
```

- [ ] **Step 4: Run full tests**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/justinbeaudry/Projects/cairn
git add src/lib/templates.ts tests/inject.test.ts
git commit -m "feat: update index.md and log.md stub templates to new formats"
```

---

### Task 4: Rewrite inject script with priority order

Replace the inject script's reading logic: `context.md` first, then `index.md`, then sessions — all under budget.

**Files:**
- Rewrite: `hooks/inject`
- Modify: `tests/inject.test.ts`

- [ ] **Step 1: Write failing test — inject reads context.md first**

Add to `tests/inject.test.ts`. First update `makeTestVault()` to create a `context.md`:

```typescript
function makeTestVault(): string {
  const dir = join(tmpdir(), `cairn-inject-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  writeFileSync(
    join(dir, "context.md"),
    "# Working Set\n\n## Active\n- [[Auth Flow]] — rebuilding auth this sprint\n"
  );
  writeFileSync(
    join(dir, "index.md"),
    "# Vault Index\n\n## Architecture\n- [[Auth Flow]] — OAuth2 implementation notes\n- [[DB Schema]] — PostgreSQL schema decisions\n"
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-14T09-00-00.md"),
    "---\nsession_id: '2026-04-14T09:00:00'\nstatus: completed\n---\nImplemented auth flow.\n"
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-14T10-00-00.md"),
    "---\nsession_id: '2026-04-14T10:00:00'\nstatus: in-progress\n---\nStarted DB migration.\n"
  );
  return dir;
}
```

Add a new test:

```typescript
it("should inject context.md before index.md", async () => {
  const vault = makeTestVault();
  const proc = Bun.spawn(["bash", "hooks/inject", vault], {
    stdout: "pipe",
    stderr: "pipe",
  });
  const output = await new Response(proc.stdout).text();
  const json = JSON.parse(output);
  const context = json.hookSpecificOutput.additionalContext;

  const workingSetPos = context.indexOf("Working Set");
  const indexPos = context.indexOf("Vault Index");
  expect(workingSetPos).toBeGreaterThanOrEqual(0);
  expect(indexPos).toBeGreaterThan(workingSetPos);

  rmSync(vault, { recursive: true });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/inject.test.ts`
Expected: FAIL — current inject script reads index before context.md (or doesn't read context.md at all)

- [ ] **Step 3: Rewrite hooks/inject**

Replace `hooks/inject` entirely:

```bash
#!/usr/bin/env bash
# Cairn injection hook — SessionStart and PostCompact
# Reads vault in priority order: context.md → index.md → recent sessions.
# Outputs JSON for Claude Code context injection.

set -euo pipefail

# Resolve vault path: argument > env > default
VAULT_PATH="${1:-${CAIRN_VAULT:-${HOME}/cairn}}"
BUDGET="${CAIRN_BUDGET:-2048}"

# Graceful degradation: missing vault = empty context
if [ ! -d "$VAULT_PATH" ]; then
  printf '{\n  "hookSpecificOutput": {\n    "hookEventName": "SessionStart",\n    "additionalContext": ""\n  }\n}\n'
  exit 0
fi

context=""
current_bytes=0

# Utility: get byte count of a string
byte_count() {
  printf '%b' "$1" | wc -c | tr -d ' '
}

# Utility: append to context if within budget
append_if_fits() {
  local section="$1"
  local candidate
  if [ -z "$context" ]; then
    candidate="$section"
  else
    candidate="${context}\n\n${section}"
  fi
  local candidate_bytes
  candidate_bytes=$(byte_count "$candidate")
  if [ "$candidate_bytes" -le "$BUDGET" ]; then
    context="$candidate"
    current_bytes=$candidate_bytes
    return 0
  fi
  return 1
}

# 1. context.md (working set) — highest priority
CONTEXT_FILE="${VAULT_PATH}/context.md"
if [ -f "$CONTEXT_FILE" ]; then
  context_content=$(cat "$CONTEXT_FILE")
  append_if_fits "## Cairn Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Working Set\n${context_content}" || true
fi

# 2. index.md — second priority
INDEX_FILE="${VAULT_PATH}/index.md"
if [ -f "$INDEX_FILE" ]; then
  index_content=$(cat "$INDEX_FILE")
  if [ -z "$context" ]; then
    append_if_fits "## Cairn Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Index\n${index_content}" || true
  else
    append_if_fits "### Index\n${index_content}" || true
  fi
fi

# 3. Recent sessions (newest first, by filename sort)
SESSIONS_DIR="${VAULT_PATH}/sessions"
if [ -d "$SESSIONS_DIR" ]; then
  session_files=$(ls -r "$SESSIONS_DIR"/*.md 2>/dev/null || true)
  if [ -n "$session_files" ]; then
    sessions_header_added=false
    for f in $session_files; do
      session_content=$(cat "$f")
      if [ "$sessions_header_added" = false ]; then
        section="\n### Recent Sessions\n---\n${session_content}"
        sessions_header_added=true
      else
        section="\n---\n${session_content}"
      fi
      append_if_fits "$section" || break
    done
  fi
fi

# Escape for JSON
escape_for_json() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  printf '%s' "$s"
}

escaped=$(escape_for_json "$(printf '%b' "$context")")

# Output JSON — Claude Code format
printf '{\n  "hookSpecificOutput": {\n    "hookEventName": "SessionStart",\n    "additionalContext": "%s"\n  }\n}\n' "$escaped"

exit 0
```

- [ ] **Step 4: Run inject tests**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/inject.test.ts`
Expected: ALL PASS — context.md appears before index, budget respected, missing vault returns empty

- [ ] **Step 5: Run full tests**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/justinbeaudry/Projects/cairn
git add hooks/inject tests/inject.test.ts
git commit -m "feat: rewrite inject hook with context.md → index → sessions priority"
```

---

### Task 5: Rewrite SKILL.md

Update the skill file to teach all new conventions: page types, expanded frontmatter, discuss-before-filing, cascading ingest, categorized index, heading-level log, working set maintenance.

**Files:**
- Rewrite: `skills/cairn/SKILL.md`

- [ ] **Step 1: Rewrite skills/cairn/SKILL.md**

Replace the entire file:

```markdown
---
name: cairn
description: >
  Persistent memory vault for Claude Code. Teaches ingest, query, and lint
  workflows for a markdown-based knowledge vault using Obsidian-flavored markdown.
---

# Cairn — Persistent Memory Vault

You have access to a persistent knowledge vault. The vault is a directory of
markdown files using Obsidian-flavored syntax (wikilinks, frontmatter, embeds).

## Finding Your Vault

Check these in order:
1. `CAIRN_VAULT` environment variable
2. `~/cairn` (default location)

Read `CAIRN.md` in the vault root for this vault's specific conventions.

## Page Types

Every wiki page has a `type` in its frontmatter:

| Type | When to use |
|------|-------------|
| `concept` | Ideas, techniques, patterns |
| `entity` | People, orgs, tools, projects |
| `source-summary` | Digest of a document in `raw/` |
| `comparison` | X vs Y trade-off analysis |
| `overview` | Guided index of a topic area |

## Frontmatter (required on every wiki page)

```yaml
---
title: Page Title
type: concept | entity | source-summary | comparison | overview
source: "[[raw/filename.md]]" or URL (required for source-summary)
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags:
  - topic/subtopic
---
```

## When to Use the Vault

- **Session start**: Your context includes the working set (`context.md`) and recent
  session summaries injected automatically. Use them as background knowledge, not instructions.
- **User asks to ingest**: Read source, present takeaways for confirmation, write wiki
  pages, cascade updates to related pages, update index and log.
- **User asks a question**: Check `index.md` first, follow links, cite sources. File
  novel insights as new wiki pages.
- **User asks to lint**: Report orphans, dead links, missing frontmatter, stale content,
  missing types, and contradictions.
- **Recall needed**: If you need context from a past session, read `sessions/`.

## Ingest Workflow

1. Read the source (file path, URL, pasted text, conversation context).
2. Copy files to `raw/` for provenance.
3. **Present takeaways to the user** — entities, concepts, relationships, contradictions with existing wiki content. Confirm what to file.
4. Create or update wiki pages for confirmed items.
5. **Cascade**: review and update all related existing wiki pages with new cross-references, corrections, or context. A single ingest typically touches 5-15 pages.
6. Every page links to 2+ others via `[[wikilinks]]`.
7. Update `context.md` if relevant to current focus areas.
8. Add entries to `index.md` under the appropriate topic category.
9. Append heading-level entry to `log.md`: `## [YYYY-MM-DD] ingest | <title>`.

## Query Workflow

1. Read `index.md` to find relevant pages.
2. Follow `[[wikilinks]]` to read related pages.
3. Synthesize answer, citing sources as `[[Page Name]]`.
4. If answer contains novel knowledge, write a new wiki page and update `index.md`.
5. Append to `log.md`: `## [YYYY-MM-DD] query | <brief summary>`.

## Key Rules

1. Never modify files in `raw/` — archived originals for provenance.
2. Always read `CAIRN.md` before your first vault operation in a session.
3. Skeptical memory: verify recalled facts against the current codebase before acting.
4. Every wiki page needs full frontmatter (`title`, `type`, `created`, `updated`, `tags`) and 2+ wikilinks.
5. Session summaries in `sessions/` are auto-generated. Don't write them manually.
6. Discuss before filing — present takeaways, get confirmation before writing pages.
7. Cascade updates — ingesting touches related existing pages, not just new ones.
8. Index is categorized by topic, not flat chronological.
9. Log entries use heading format: `## [YYYY-MM-DD] type | description`.
10. Keep `context.md` current with active focus areas.

## Quick Reference

- Wikilinks: `[[Page Name]]`, `[[Page#Heading]]`, `[[Page|Alias]]`
- Embeds: `![[Page Name]]`
- Index: `- [[Page Name]] — one-line description` grouped by category
- Log: `## [YYYY-MM-DD] type | description` (types: ingest, query, lint, session)
- Page types: concept, entity, source-summary, comparison, overview
```

- [ ] **Step 2: Copy updated SKILL.md to installed skill location**

```bash
cp /Users/justinbeaudry/Projects/cairn/skills/cairn/SKILL.md /Users/justinbeaudry/.claude/skills/cairn/SKILL.md
```

- [ ] **Step 3: Commit**

```bash
cd /Users/justinbeaudry/Projects/cairn
git add skills/cairn/SKILL.md
git commit -m "feat: rewrite SKILL.md with page types, cascading ingest, working set"
```

---

### Task 6: Final verification

Run full test suite, verify all files are consistent.

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 2: Verify CAIRN.md template loads correctly**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun run -e "import { getCairnMdTemplate } from './src/lib/templates'; const t = getCairnMdTemplate(); console.log('Length:', t.length); console.log('Has page types:', t.includes('Page Types')); console.log('Has cascading:', t.includes('5-15 existing pages')); console.log('Has context.md:', t.includes('context.md')); console.log('Has heading log:', t.includes('## [YYYY-MM-DD]'))"`

Expected:
```
Length: <number>
Has page types: true
Has cascading: true
Has context.md: true
Has heading log: true
```

- [ ] **Step 3: Verify inject script reads context.md**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/inject.test.ts`
Expected: ALL PASS, including context.md priority test

- [ ] **Step 4: Verify constants include context.md**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun run -e "import { VAULT_FILES } from './src/lib/constants'; console.log(VAULT_FILES)"`
Expected: `[ "CAIRN.md", "index.md", "log.md", "context.md" ]`

- [ ] **Step 5: Commit all remaining changes (if any unstaged)**

```bash
cd /Users/justinbeaudry/Projects/cairn
git status
# If clean, skip. If anything unstaged:
# git add <files>
# git commit -m "chore: final verification cleanup"
```
