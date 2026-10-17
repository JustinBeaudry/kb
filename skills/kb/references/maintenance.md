# Lint and refine

Read `KB.md` for local schema and conventions. Use the shared
[query workflow](query.md) to inspect relevant curated pages. `kb map` can
orient a whole-vault pass; follow its budget guidance before claiming complete
coverage. Direct file inspection of curated pages is appropriate for structural
audits, but raw/session content still uses the sanctioned CLI.

## Lint: report findings

Check required frontmatter, valid page types, dead links, missing backlinks,
duplicate coverage, conflicting claims and stale evidence. A page's age or few
links is a review signal, not proof that it is wrong or needs more links.
Research gaps are questions to investigate; do not create facts to fill them.

Cite the affected pages and explain each finding. Report counts only for the
portion actually inspected. Lint is read-only unless the user separately
authorizes fixes; no automatic wiki edits or `log.md` entry.

## Refine: apply the agreed changes

1. Establish the findings and proposed scope: corrections, useful connections,
   duplicate pages, merge/split candidates, or backlink repairs.
2. Apply changes the user authorized. Present ambiguous content decisions or
   wider structural changes before acting. Do not require redundant approval
   for an already specified repair.
3. Preserve provenance and resolve inbound links when moving or consolidating
   pages. Do not silently delete source material. Use an agreed archive location
   if archival is requested.
4. Update affected index entries and backlinks. Do not add unrelated links or
   pages to satisfy a count. Update the working set only when relevant.
5. Inspect the result for broken references and content loss. Compare measured
   findings before/after, record applied changes in `log.md`, and report any
   remaining uncertainty.

If a cited session is necessary to resolve a finding, use the
[session guide](sessions.md) with source-access authorization. Failed or degraded
summaries are limitations to report, not reasons to fabricate a correction.
