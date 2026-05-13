---
phase: 1.5c
status: ready
repo: gospel-engine-v2
depends_on: 1.5b
created: 2026-05-13
---

# Phase 1.5c — Cross-references (opt-in)

**Binding problem.** The `cross_references` table has 85,590 rows (footnote-derived bidirectional graph edges). No MCP tool exposes them. Studies that should cite related verses don't, because the agent can't see them without `read_file`-ing every chapter for footnote text.

**Design constraint.** Always-on cross-refs would bloat every `gospel_get` response. **Opt-in** keeps default responses lean. The study agent's research mode will be updated separately to pass `cross_refs: true`.

## Change

### Server (`internal/api/server.go`)

In `handleGet`, parse a new query param:

```go
crossRefs := r.URL.Query().Get("cross_refs") == "true"
```

When true and the result has verses, query for each verse's cross-references and dedupe:

```sql
SELECT target_volume, target_book, target_chapter, target_verse, reference_type
FROM cross_references
WHERE source_volume = $1 AND source_book = $2 AND source_chapter = $3 AND source_verse = $4
```

Add `cross_references: [...]` sibling array to the response. Each entry: `{ reference: "Hebrews 7:11-12", reference_type: "footnote", target_volume, target_book, target_chapter, target_verse }`.

Skip entirely for `chapter` responses (Phase 1.5b shape) — chapter-level cross-refs are a future enhancement.

### MCP wrapper (`cmd/gospel-mcp/main.go`)

Add `cross_refs` boolean parameter to `gospel_get` schema. Default `false`. Description: `"Include footnote-derived cross-references for returned verses (opt-in to keep default responses lean)"`.

## Out of scope

- Reverse lookup ("what scriptures reference *this* verse?") — separate phase if needed.
- Chapter-level cross-refs aggregation.
- Cross-refs on `gospel_search` results.

## Verify

Local docker:

1. `curl /api/get?reference=Mosiah+4:9` → no `cross_references` field
2. `curl /api/get?reference=Mosiah+4:9&cross_refs=true` → response includes `cross_references` array
3. `curl /api/get?reference=D%26C+93:24-30&cross_refs=true` → cross-refs deduplicated across the 7-verse range
4. MCP: `mcp_gospel-engine_gospel_get { reference: "Mosiah 4:9", cross_refs: true }` after rebuild → cross-refs visible

## Follow-up (NOT in this phase)

- Update `study` agent's research-mode prompt to pass `cross_refs: true`. Track in scripture-study repo.

## Commit checkpoints

1. `feat(api): cross_refs query param on /api/get (opt-in, deduped per range)`
2. `feat(mcp): cross_refs boolean param on gospel_get schema`

## Effort

Half session.
