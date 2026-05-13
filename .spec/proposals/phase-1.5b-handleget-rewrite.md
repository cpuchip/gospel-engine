---
phase: 1.5b
status: ready
repo: gospel-engine-v2
depends_on: 1.5a
created: 2026-05-13
---

# Phase 1.5b — `gospel_get` handler rewrite

**Binding problem.** Three foot-guns in one handler:

1. Server error message says `provide either ref= or (type= and id=)`, but the MCP schema accepts `reference`. Agent retries with `ref:`, MCP layer drops it silently, second call fails. Two passes lost per mistake.
2. `gospel_get { reference: "D&C 93:24-30" }` returns 404. Verse ranges don't work — the SQL is `WHERE reference = $1` exact-match. (Regression vs. gospel-mcp's Feb 15 fix.)
3. `gospel_get { reference: "Mosiah 4" }` (chapter-only) returns 400. The `chapters` table has `full_content` ready, the handler just doesn't expose it.

## Change

### Server (`internal/api/server.go`)

In `handleGet`, accept BOTH `ref` and `reference`:

```go
ref := strings.TrimSpace(r.URL.Query().Get("ref"))
if ref == "" {
    ref = strings.TrimSpace(r.URL.Query().Get("reference"))
}
```

Update the error message:

```go
http.Error(w, "provide either reference= or (type= and id=)", http.StatusBadRequest)
```

Add a `parseReference` helper (port from `scripts/gospel-mcp/internal/tools/get.go` — already tested; handles `"1 Nephi 3:7"`, `"D&C 93:24-30"`, `"Mosiah 4"`, multi-word book names, slug normalization).

Rewrite `getByReference` to dispatch on parse result:

- **Single verse** (`book chapter:verse`): SELECT one row from `scriptures`, return as 1-element `verses` array.
- **Verse range** (`book chapter:verse-verseEnd`): SELECT `WHERE volume=$1 AND book=$2 AND chapter=$3 AND verse BETWEEN $4 AND $5 ORDER BY verse`. Cap at 50 verses (HTTP 400 if range exceeds). Return as `verses` array.
- **Chapter only** (`book chapter`, no `:`): SELECT one row from `chapters` table, return as `chapter` object.

### Response shapes

Single verse and verse range — uniform:

```json
{
  "source_type": "scriptures",
  "reference_query": "D&C 93:24-30",
  "verses": [
    { "volume": "dc-testament", "book": "dc", "chapter": 93, "verse": 24,
      "reference": "D&C 93:24", "text": "...", "file_path": "..." },
    ...
  ]
}
```

Chapter:

```json
{
  "source_type": "chapters",
  "reference_query": "Mosiah 4",
  "chapter": {
    "volume": "bofm", "book": "mosiah", "chapter": 4,
    "title": "...", "full_content": "...", "file_path": "..."
  }
}
```

### MCP wrapper (`cmd/gospel-mcp/main.go`)

- Description for `gospel_get` updated to mention all three forms (single verse, range, chapter).
- Schema `reference` description: `"Scripture reference: '1 Nephi 3:7', 'D&C 93:24-30', or 'Mosiah 4' (chapter)"`.
- Schema unchanged otherwise (still takes `reference`; server now also accepts `ref`).

## Verify

Local docker:

1. `curl /api/get?reference=1+Nephi+3:7` → 1-element `verses` array
2. `curl /api/get?reference=D%26C+93:24-30` → 7-element `verses` array
3. `curl /api/get?reference=Mosiah+4` → `chapter` object with `full_content`
4. `curl /api/get?ref=1+Nephi+3:7` → same as #1 (back-compat)
5. `curl /api/get` (no params) → 400 with new error message containing `reference=`
6. `curl /api/get?reference=Genesis+1:1-100` → 400 (range cap exceeded)
7. MCP tool: `mcp_gospel-engine_gospel_get { reference: "D&C 93:24-30" }` after rebuild → 7 verses

## Commit checkpoints

1. `feat(api): accept both ref= and reference= query params; clearer error message`
2. `feat(api): port parseReference helper from gospel-mcp v1`
3. `feat(api): verse-range support in getByReference (uniform {verses:[...]} shape)`
4. `feat(api): chapter-level fetch when reference omits ':'`
5. `feat(mcp): update gospel_get schema docs (range + chapter forms)`

## Effort

Half session.
