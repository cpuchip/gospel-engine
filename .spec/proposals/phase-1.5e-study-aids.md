---
phase: 1.5e
status: ready
repo: gospel-engine-v2
created: 2026-05-13
---

# Phase 1.5e — Study aids indexed (TG, BD, GS, JST)

**Binding problem.** `internal/indexer/indexer.go::indexScriptureFile` early-returns for any volume not in `scriptureVolumes` (`ot`, `nt`, `bofm`, `dc-testament`, `pgp`). That excludes:

| Slug | Name | Why it matters |
|------|------|----------------|
| `tg` | Topical Guide | Curated topical reference lists with verse pointers |
| `bd` | Bible Dictionary | Short doctrinal articles on biblical concepts |
| `gs` | Guide to the Scriptures | Short doctrinal articles, the Restoration-era equivalent of BD |
| `jst` | Joseph Smith Translation | Restored verses, often cited in scripture studies |

The agent loses access to all four during studies. They live on disk in `gospel-library/eng/scriptures/{tg,bd,gs,jst}/` but are not searchable.

## Schema (new table)

Ratified 2026-05-13: dedicated `study_aids` table (not stuffed into `manuals` or `scriptures`).

```sql
CREATE TABLE study_aids (
  id            SERIAL PRIMARY KEY,
  aid_type      TEXT NOT NULL CHECK (aid_type IN ('tg','bd','gs','jst')),
  slug          TEXT NOT NULL,           -- entry slug, e.g. 'faith' for tg/faith.md
  title         TEXT NOT NULL,           -- display title from H1
  content       TEXT NOT NULL,           -- full markdown body
  file_path     TEXT NOT NULL UNIQUE,
  content_tsvector TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || content)) STORED,
  embedding     vector(768),             -- nullable; populated by separate embedding pass
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX study_aids_aid_type_idx        ON study_aids(aid_type);
CREATE INDEX study_aids_aid_type_slug_idx   ON study_aids(aid_type, slug);
CREATE INDEX study_aids_tsvector_idx        ON study_aids USING GIN(content_tsvector);
CREATE INDEX study_aids_embedding_hnsw_idx  ON study_aids USING hnsw(embedding vector_cosine_ops);
```

Migration file added under whatever convention the existing schema uses (check `internal/db/`).

## Indexer change

### `internal/indexer/indexer.go::indexScriptureFile`

Before the early-return on `!scriptureVolumes[volume]`, dispatch:

```go
if studyAidTypes[volume] {
    return idx.indexStudyAidFile(ctx, path, parts, r)
}
if !scriptureVolumes[volume] {
    return nil
}
```

Add `studyAidTypes` map: `{"tg": true, "bd": true, "gs": true, "jst": true}`.

New `indexStudyAidFile`:
- Path layout: `eng/scriptures/{tg|bd|gs|jst}/{slug}.md` (or possibly `{aid_type}/{section}/{slug}.md` for nested entries — verify against actual disk layout).
- Parse first H1 as title.
- Body is everything after the H1 (keep markdown links — Topical Guide entries are mostly link lists; stripping them would destroy the value).
- UPSERT on `(file_path)`.

### Result counter

Add `StudyAidsIndexed int` to `Result`.

## Search integration

### `internal/search/`

Add `study_aids` as a valid `Source`. When `sources` filter includes it (or is empty for all-sources searches), include study-aids hits in results. Result row needs `aid_type` + `slug` + `title` to be useful.

`gospel_search { sources: ["study_aids"] }` → only TG/BD/GS/JST hits.
`gospel_search` with no sources → includes them by default (matches current "all sources" behavior).

### MCP wrapper

Update `gospel_search` source enum to include `study_aids`.

## Embedding pass

Embeddings are populated server-side by the existing nightly/manual job (per `v2-hosted.md` architecture). New rows start with `embedding IS NULL` and pick up on next pass. Keyword search works immediately; semantic search for study aids waits for the embedding job.

## Verify

Local docker:

1. Reindex picks up `tg/`, `bd/`, `gs/`, `jst/` files. Counter shows non-zero `StudyAidsIndexed`.
2. `SELECT aid_type, COUNT(*) FROM study_aids GROUP BY aid_type;` shows all four types populated.
3. `curl /api/search?q=faith&sources=study_aids` returns TG/BD entries.
4. `curl /api/search?q=charity` (no sources filter) — study aids appear alongside scriptures/talks.
5. MCP after rebuild: `mcp_gospel-engine_gospel_search { query: "atonement", sources: ["study_aids"] }` returns relevant TG entries.

## Commit checkpoints

1. `feat(db): study_aids table + indices (FTS + pgvector hnsw)`
2. `feat(indexer): indexStudyAidFile dispatcher for tg/bd/gs/jst`
3. `feat(search): study_aids as a Source; surface aid_type+slug in result rows`
4. `feat(mcp): gospel_search sources enum includes study_aids`

## Effort

Half-to-full session, including reindex pass and verification.
