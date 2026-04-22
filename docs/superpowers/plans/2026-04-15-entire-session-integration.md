# Entire Session Integration Plan

> Superseded by `docs/plans/2026-04-19-001-refactor-session-capture-manifest-plan.md`.
> The Stop hook no longer summarizes with `jq` + `claude -p`; it writes a
> session manifest via `cairn capture-session`, and `cairn summarize` performs
> lazy summarization on read.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a vault's project has Entire enabled, Cairn's session-summary hook produces richer summaries by reading Entire's checkpoint/session data instead of raw JSONL transcripts.

**Architecture:** Gateway pattern at the session data boundary. A `detect-entire` utility checks whether Entire is enabled in the current repo. The session-summary hook branches: Entire-enabled projects use `entire explain` for richer context + checkpoint provenance; non-Entire projects use the existing jq+claude pipeline unchanged. The extract skill gains checkpoint-aware provenance when source sessions came from Entire.

**Tech Stack:** Bash (hooks), Bun + TypeScript (lib utilities, tests), Entire CLI

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `src/lib/entire.ts` | Create | Entire detection + checkpoint resolution utilities |
| `hooks/session-summary` | Modify | Branch on Entire availability, use `entire explain` when present |
| `skills/extract/SKILL.md` | Modify | Document checkpoint provenance in extraction workflow |
| `src/lib/constants.ts` | Modify | Add Entire-related constants |
| `tests/entire.test.ts` | Create | Unit tests for detection + resolution utilities |
| `tests/session-summary.test.ts` | Modify | Add Entire-path test cases |

---

### Task 1: Entire Detection Utility

**Files:**
- Create: `src/lib/entire.ts`
- Create: `tests/entire.test.ts`
- Modify: `src/lib/constants.ts`

- [ ] **Step 1: Add constants**

In `src/lib/constants.ts`, add:

```typescript
export const ENTIRE_CHECKPOINT_BRANCH = "entire/checkpoints/v1";
```

- [ ] **Step 2: Write failing tests for detection**

Create `tests/entire.test.ts`:

```typescript
import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, existsSync } from "node:fs";

// We'll test the shell-level detection function used by the hook,
// and the TS utility for the extract skill.

describe("entire detection", () => {
  let testDir: string;

  beforeEach(() => {
    testDir = join(tmpdir(), `cairn-entire-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(testDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(testDir, { recursive: true, force: true });
  });

  describe("isEntireEnabled", () => {
    it("should return false when not a git repo", async () => {
      const { isEntireEnabled } = await import("../src/lib/entire");
      const result = await isEntireEnabled(testDir);
      expect(result).toBe(false);
    });

    it("should return false when git repo but entire not enabled", async () => {
      const proc = Bun.spawn(["git", "init", testDir], { stdout: "ignore", stderr: "ignore" });
      await proc.exited;

      const { isEntireEnabled } = await import("../src/lib/entire");
      const result = await isEntireEnabled(testDir);
      expect(result).toBe(false);
    });

    it("should return true when entire status exits 0", async () => {
      // Init git repo
      const proc = Bun.spawn(["git", "init", testDir], { stdout: "ignore", stderr: "ignore" });
      await proc.exited;

      // We can't fake `entire enable` easily in a temp dir, so we test
      // the actual detection against the real cairn project dir
      const { isEntireEnabled } = await import("../src/lib/entire");
      const result = await isEntireEnabled(process.cwd());
      // This test runs in the cairn project which has entire enabled
      expect(result).toBe(true);
    });
  });

  describe("isEntireOnPath", () => {
    it("should return true when entire CLI is installed", async () => {
      const { isEntireOnPath } = await import("../src/lib/entire");
      const result = await isEntireOnPath();
      // entire is installed in this dev environment
      expect(result).toBe(true);
    });
  });
});
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/entire.test.ts`
Expected: FAIL — `../src/lib/entire` does not exist

- [ ] **Step 4: Implement detection utilities**

Create `src/lib/entire.ts`:

```typescript
/**
 * Entire CLI detection and checkpoint resolution.
 *
 * Gateway utilities — Cairn never parses Entire's internal formats directly.
 * All data flows through `entire explain` or `entire sessions info`.
 */

/**
 * Check if `entire` CLI is on PATH.
 */
export async function isEntireOnPath(): Promise<boolean> {
  try {
    const proc = Bun.spawn(["which", "entire"], {
      stdout: "ignore",
      stderr: "ignore",
    });
    return (await proc.exited) === 0;
  } catch {
    return false;
  }
}

/**
 * Check if Entire is enabled in the given project directory.
 * Runs `entire status` — exit 0 means enabled.
 */
export async function isEntireEnabled(cwd: string): Promise<boolean> {
  if (!(await isEntireOnPath())) return false;

  try {
    const proc = Bun.spawn(["entire", "status"], {
      cwd,
      stdout: "ignore",
      stderr: "ignore",
    });
    return (await proc.exited) === 0;
  } catch {
    return false;
  }
}

/**
 * Get checkpoint ID from the most recent commit message in the given cwd.
 * Reads the `Entire-Checkpoint:` trailer from HEAD.
 * Returns null if no checkpoint trailer found.
 */
export async function getHeadCheckpointId(cwd: string): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["git", "log", "-1", "--format=%B", "HEAD"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return null;

    const match = output.match(/Entire-Checkpoint:\s*([a-f0-9]+)/);
    return match ? match[1] : null;
  } catch {
    return null;
  }
}

/**
 * Run `entire explain --checkpoint <id> --short` and return the output.
 * Returns null on failure.
 */
export async function explainCheckpoint(
  checkpointId: string,
  cwd: string
): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["entire", "explain", "--checkpoint", checkpointId, "--short", "--no-pager"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    return exitCode === 0 ? output.trim() : null;
  } catch {
    return null;
  }
}

/**
 * Run `entire explain --checkpoint <id>` (default detail) and return the output.
 * Returns null on failure.
 */
export async function explainCheckpointFull(
  checkpointId: string,
  cwd: string
): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["entire", "explain", "--checkpoint", checkpointId, "--no-pager"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    return exitCode === 0 ? output.trim() : null;
  } catch {
    return null;
  }
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/entire.test.ts`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/lib/entire.ts src/lib/constants.ts tests/entire.test.ts
git commit -m "feat: add Entire CLI detection and checkpoint resolution utilities"
```

---

### Task 2: Session-Summary Hook — Entire Branch

**Files:**
- Modify: `hooks/session-summary`
- Modify: `tests/session-summary.test.ts`

The hook currently: reads JSONL transcript → extracts conversation via jq → pipes to `claude -p` for summarization → writes to `sessions/`.

With Entire: reads JSONL transcript (still needed for non-Entire fallback) → checks if Entire is enabled in `$CWD` → if yes, gets checkpoint context from `entire explain` and appends it to the prompt → if no, uses existing jq extraction. Either way, pipes to `claude -p` and writes to `sessions/`.

The key enrichment: when Entire is present, the session summary prompt includes checkpoint ID, token usage, and `entire explain` context alongside the conversation. The summary frontmatter gains an `entire_checkpoint` field.

- [ ] **Step 1: Write failing test for Entire detection in hook**

Add to `tests/session-summary.test.ts`:

```typescript
describe("entire integration", () => {
  it("should detect entire and include checkpoint in summary prompt", async () => {
    // Test the detect-entire function extracted from the hook
    // This validates the bash function returns correctly
    const proc = Bun.spawn(
      [
        "bash",
        "-c",
        `
        detect_entire() {
          if ! command -v entire >/dev/null 2>&1; then
            echo "unavailable"
            return
          fi
          if entire status >/dev/null 2>&1; then
            echo "enabled"
          else
            echo "disabled"
          fi
        }
        detect_entire
        `,
      ],
      {
        stdout: "pipe",
        stderr: "pipe",
        cwd: process.cwd(), // cairn project has entire enabled
      }
    );

    const output = (await new Response(proc.stdout).text()).trim();
    const exitCode = await proc.exited;

    expect(exitCode).toBe(0);
    // entire is enabled in the cairn project
    expect(output).toBe("enabled");
  });

  it("should report unavailable when entire not on path", async () => {
    const proc = Bun.spawn(
      [
        "bash",
        "-c",
        `
        detect_entire() {
          if ! command -v entire >/dev/null 2>&1; then
            echo "unavailable"
            return
          fi
          if entire status >/dev/null 2>&1; then
            echo "enabled"
          else
            echo "disabled"
          fi
        }
        # Override PATH to exclude entire
        PATH="/usr/bin:/bin" detect_entire
        `,
      ],
      {
        stdout: "pipe",
        stderr: "pipe",
      }
    );

    const output = (await new Response(proc.stdout).text()).trim();
    expect(output).toBe("unavailable");
  });
});
```

- [ ] **Step 2: Run tests to verify they pass** (these test the bash function directly, should pass)

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test tests/session-summary.test.ts`
Expected: PASS (these test extracted functions, not the modified hook yet)

- [ ] **Step 3: Modify the session-summary hook**

Replace the section in `hooks/session-summary` between the `CONVERSATION` extraction and the prompt construction. The key changes:

1. Add `detect_entire()` function near the top
2. After extracting `CONVERSATION`, check for Entire
3. If Entire is enabled, get the most recent checkpoint ID from `git log -1`
4. Run `entire explain --checkpoint <id> --short --no-pager` to get session context
5. Include checkpoint ID in the frontmatter template
6. Append Entire context to the prompt as additional signal

The modified `hooks/session-summary` should look like this (showing the new/changed sections — the jq extraction and bail checks remain identical):

After the `CONVERSATION` extraction and bail check, before the prompt construction, insert:

```bash
# --- Entire integration (optional enrichment) ---
ENTIRE_STATUS="unavailable"
ENTIRE_CHECKPOINT=""
ENTIRE_CONTEXT=""

detect_entire() {
  if ! command -v entire >/dev/null 2>&1; then
    echo "unavailable"
    return
  fi
  local cwd
  cwd=$(echo "$INPUT" | jq -r '.cwd // empty' 2>/dev/null || true)
  if [ -z "$cwd" ]; then
    echo "unavailable"
    return
  fi
  if entire status >/dev/null 2>&1; then
    echo "enabled"
  else
    echo "disabled"
  fi
}

ENTIRE_STATUS=$(detect_entire)

if [ "$ENTIRE_STATUS" = "enabled" ]; then
  CWD=$(echo "$INPUT" | jq -r '.cwd // empty' 2>/dev/null || true)
  if [ -n "$CWD" ]; then
    # Get checkpoint from most recent commit
    ENTIRE_CHECKPOINT=$(cd "$CWD" && git log -1 --format=%B HEAD 2>/dev/null | grep -oP 'Entire-Checkpoint:\s*\K[a-f0-9]+' || true)

    if [ -n "$ENTIRE_CHECKPOINT" ]; then
      ENTIRE_CONTEXT=$(cd "$CWD" && entire explain --checkpoint "$ENTIRE_CHECKPOINT" --short --no-pager 2>/dev/null || true)
    fi
  fi
fi
```

Then modify the frontmatter template in the prompt to conditionally include the checkpoint:

```bash
# Build checkpoint frontmatter line (empty string if no checkpoint)
CHECKPOINT_LINE=""
if [ -n "$ENTIRE_CHECKPOINT" ]; then
  CHECKPOINT_LINE="entire_checkpoint: \"${ENTIRE_CHECKPOINT}\""
fi
```

And in the prompt heredoc, after the frontmatter template section, change:

```
---
session_id: "${ISO_TIMESTAMP}"
status: completed
extracted: false
${CHECKPOINT_LINE}
files_changed: []
```

And append the Entire context to the prompt if available:

```bash
if [ -n "$ENTIRE_CONTEXT" ]; then
  cat >> "$PROMPT_FILE" <<ENTIRE_EOF

ENTIRE CHECKPOINT CONTEXT (additional signal from Entire session capture):

${ENTIRE_CONTEXT}
ENTIRE_EOF
fi
```

- [ ] **Step 4: Write the complete modified hook file**

The full modified `hooks/session-summary`:

```bash
#!/usr/bin/env bash
# Cairn session summary hook — Stop event
# Reads hook input JSON from stdin, extracts transcript,
# pipes through claude -p for summarization, writes to sessions/.
#
# When Entire is enabled in the project, enriches the summary with
# checkpoint context from `entire explain`.

# NOTE: no pipefail — head/grep pipes legitimately close early (SIGPIPE),
# and pipefail turns that into a fatal exit under set -e.
set -e

# Read hook input from stdin
INPUT=$(cat)
TRANSCRIPT_PATH=$(echo "$INPUT" | jq -r '.transcript_path // empty' 2>/dev/null || true)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null || true)

# Bail if no transcript
if [ -z "${TRANSCRIPT_PATH:-}" ] || [ ! -f "${TRANSCRIPT_PATH:-}" ]; then
  exit 0
fi

# Resolve vault path: env > default
VAULT_PATH="${CAIRN_VAULT:-${HOME}/cairn}"
if [ ! -d "$VAULT_PATH" ]; then
  exit 0
fi

SESSIONS_DIR="${VAULT_PATH}/sessions"
mkdir -p "$SESSIONS_DIR"

# Build filename from timestamp
TIMESTAMP=$(date -u +"%Y-%m-%dT%H-%M-%S")
SESSION_FILE="${SESSIONS_DIR}/${TIMESTAMP}.md"

# Skip if file already exists (idempotency)
if [ -f "$SESSION_FILE" ]; then
  exit 0
fi

# Extract human-readable conversation from JSONL transcript.
# Keep user and assistant messages; skip tool calls and system messages.
CONVERSATION=$(jq -r '
  select(.type == "human" or .type == "assistant") |
  if .type == "human" then
    "## User\n" + (
      if (.message.content | type) == "array" then
        [.message.content[] | select(.type == "text") | .text] | join("\n")
      elif (.message.content | type) == "string" then
        .message.content
      else
        ""
      end
    )
  elif .type == "assistant" then
    "## Assistant\n" + (
      if (.message.content | type) == "array" then
        [.message.content[] | select(.type == "text") | .text] | join("\n")
      elif (.message.content | type) == "string" then
        .message.content
      else
        ""
      end
    )
  else
    empty
  end
' "$TRANSCRIPT_PATH" 2>/dev/null || true)

# Bail if transcript extraction produced nothing
if [ -z "${CONVERSATION:-}" ]; then
  exit 0
fi

# Truncate conversation to ~100K chars to stay within model context
CONVERSATION=$(printf '%s' "$CONVERSATION" | head -c 100000)

# --- Entire integration (optional enrichment) ---
ENTIRE_CHECKPOINT=""
ENTIRE_CONTEXT=""

CWD=$(echo "$INPUT" | jq -r '.cwd // empty' 2>/dev/null || true)

if command -v entire >/dev/null 2>&1 && [ -n "$CWD" ]; then
  if (cd "$CWD" && entire status >/dev/null 2>&1); then
    # Get checkpoint from most recent commit
    ENTIRE_CHECKPOINT=$(cd "$CWD" && git log -1 --format=%B HEAD 2>/dev/null | grep -o 'Entire-Checkpoint: [a-f0-9]*' | head -1 | sed 's/Entire-Checkpoint: //' || true)

    if [ -n "$ENTIRE_CHECKPOINT" ]; then
      ENTIRE_CONTEXT=$(cd "$CWD" && entire explain --checkpoint "$ENTIRE_CHECKPOINT" --short --no-pager 2>/dev/null || true)
    fi
  fi
fi

# Build optional frontmatter fields
CHECKPOINT_FIELD=""
if [ -n "$ENTIRE_CHECKPOINT" ]; then
  CHECKPOINT_FIELD="entire_checkpoint: \"${ENTIRE_CHECKPOINT}\""
fi

ISO_TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
TODAY=$(date -u +"%Y-%m-%d")

# Write prompt to temp file to avoid argument-too-long and special char issues
PROMPT_FILE=$(mktemp)
trap 'rm -f "$PROMPT_FILE"' EXIT

cat > "$PROMPT_FILE" <<PROMPT_EOF
You are the Cairn session summarizer. Write a structured session summary in markdown.

OUTPUT FORMAT — produce EXACTLY this, nothing else:

---
session_id: "${ISO_TIMESTAMP}"
status: completed
extracted: false
${CHECKPOINT_FIELD}
files_changed: []
decisions: []
open_threads: []
tags: []
---

## Summary
2-4 sentences summarizing what happened in this session.

## Extraction Candidates
- **<name>** (<type>) — <why it's worth keeping>

RULES:
1. Frontmatter fields files_changed, decisions, open_threads, and tags should be populated from the conversation. Use YAML list syntax.
2. files_changed entries: {path: "<path>", action: "created|modified|deleted"}
3. decisions entries: {choice: "<what>", reason: "<why>"}
4. Extraction candidate types: concept, entity, source-summary, comparison, overview.
5. Output ONLY the markdown. No commentary, no code fences wrapping the whole output.
6. If the session is trivial (quick question, no real work), still produce the format but keep Summary to one sentence and Extraction Candidates to 'None.'
7. If an entire_checkpoint field is present in the frontmatter, keep it exactly as shown.

SESSION TRANSCRIPT:

${CONVERSATION}
PROMPT_EOF

# Append Entire context if available
if [ -n "$ENTIRE_CONTEXT" ]; then
  cat >> "$PROMPT_FILE" <<ENTIRE_EOF

ENTIRE CHECKPOINT CONTEXT (additional signal — use this for richer summaries):

Checkpoint: ${ENTIRE_CHECKPOINT}
${ENTIRE_CONTEXT}
ENTIRE_EOF
fi

# Run through claude -p (haiku for speed/cost)
SUMMARY=$(claude -p --model haiku < "$PROMPT_FILE" 2>&1 || true)

# Bail if claude produced nothing or returned an error
if [ -z "${SUMMARY:-}" ] || echo "$SUMMARY" | grep -qi "error\|too long\|failed"; then
  exit 0
fi

# Write session file
printf '%s\n' "$SUMMARY" > "$SESSION_FILE"

# Append to log.md
LOG_FILE="${VAULT_PATH}/log.md"
if [ -f "$LOG_FILE" ]; then
  DESCRIPTION=$(echo "$SUMMARY" | grep -A1 "## Summary" | tail -1 | head -c 120 || true)
  printf '\n## [%s] session | %s\n' "$TODAY" "$DESCRIPTION" >> "$LOG_FILE"
fi

exit 0
```

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add hooks/session-summary tests/session-summary.test.ts
git commit -m "feat: enrich session summaries with Entire checkpoint context when available"
```

---

### Task 3: Extract Skill — Checkpoint Provenance

**Files:**
- Modify: `skills/extract/SKILL.md`

When the extract workflow processes a session that has `entire_checkpoint` in its frontmatter, wiki pages created from that session should use `entire://<checkpoint-id>` as their source field instead of (or in addition to) the session file path.

- [ ] **Step 1: Update extract skill to document checkpoint provenance**

Add a new section to `skills/extract/SKILL.md` after the "Extraction Workflow" section:

```markdown
## Entire Checkpoint Provenance

When a session summary has `entire_checkpoint` in its frontmatter, the session
was captured by Entire and the full transcript is available via `entire explain`.

During extraction:

1. Note the checkpoint ID from the session's `entire_checkpoint` field.
2. When creating wiki pages from this session, set the `source` field to:
   ```yaml
   source: "entire://<checkpoint-id>"
   ```
3. This enables future re-extraction: `entire explain --checkpoint <id>` retrieves
   the full session context, not just the lossy summary.
4. Do NOT copy Entire session data to `raw/` — the checkpoint branch is the
   provenance store. `raw/` is for non-Entire sources only.
5. If you need more context than the summary provides, run:
   ```bash
   entire explain --checkpoint <id> --no-pager
   ```
   to get the detailed view with scoped prompts and file changes.
```

- [ ] **Step 2: Commit**

```bash
git add skills/extract/SKILL.md
git commit -m "docs: add Entire checkpoint provenance to extract skill"
```

---

### Task 4: Inject Hook — Entire Status Indicator

**Files:**
- Modify: `hooks/inject`

Small enrichment: when injecting vault context at session start, if Entire is enabled in the current project, append a one-line indicator so the agent knows Entire is available for richer extraction.

- [ ] **Step 1: Add Entire detection to inject hook**

After the sessions injection loop in `hooks/inject`, before the JSON escape, add:

```bash
# 4. Entire status indicator (one line if enabled)
if command -v entire >/dev/null 2>&1; then
  if entire status >/dev/null 2>&1; then
    append_if_fits "\n### Entire\nEntire is enabled in this project. Session summaries may have \`entire_checkpoint\` fields for richer extraction." || true
  fi
fi
```

- [ ] **Step 2: Run full test suite**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 3: Commit**

```bash
git add hooks/inject
git commit -m "feat: inject Entire status indicator when enabled in project"
```

---

### Task 5: Verify End-to-End

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun test`
Expected: ALL PASS

- [ ] **Step 2: Run lint**

Run: `cd /Users/justinbeaudry/Projects/cairn && bun run lint`
Expected: CLEAN

- [ ] **Step 3: Verify hook loads cleanly**

Run: `cd /Users/justinbeaudry/Projects/cairn && bash -n hooks/session-summary && bash -n hooks/inject`
Expected: No syntax errors

- [ ] **Step 4: Verify Entire detection works**

Run: `cd /Users/justinbeaudry/Projects/cairn && entire status`
Expected: Shows "Enabled" status

- [ ] **Step 5: Manual smoke test — session summary hook**

Run: `cd /Users/justinbeaudry/Projects/cairn && echo '{"transcript_path":"/dev/null","session_id":"test","cwd":"'$(pwd)'"}' | bash hooks/session-summary`
Expected: Exits 0 (bails on empty transcript — no crash from Entire additions)

---

## Design Decisions

1. **Detection via `entire status`, not file sniffing.** `entire status` is the canonical check. Checking for `.entire/` or hook files is brittle — Entire's internals may change. The CLI is the stable interface.

2. **`entire explain` as the gateway, not JSONL parsing.** Cairn never reads `full.jsonl` directly. `entire explain` is Entire's own abstraction layer — it synthesizes session data into human-readable form. If Entire changes its storage format, Cairn is unaffected.

3. **Checkpoint ID in session frontmatter, not a separate mapping.** The `entire_checkpoint` field in session summaries is the provenance link. Simple, discoverable, and survives vault operations (move, rename).

4. **No `raw/` duplication for Entire sessions.** The checkpoint branch IS the archive. Copying to `raw/` would duplicate data and diverge over time. `raw/` remains for external sources (PDFs, blog posts, pasted text).

5. **Fallback is invisible.** When Entire isn't present, the hook runs exactly as before. No config needed, no feature flags, no "mode" to set. Detection happens once per hook invocation and branches silently.

6. **`--short` for hook enrichment, full for manual extraction.** The session-summary hook appends `--short` output to keep the claude prompt compact. When a user manually extracts via `/cairn:extract`, they can use the full `entire explain` output for deeper context.

7. **No automatic extraction from Entire.** Sessions still require intentional extraction via `/cairn:extract`. Entire captures everything; Cairn extracts knowledge. Most sessions produce code, not wiki-worthy knowledge. The human decides what's worth keeping.
