-- Phase 1.5e — study aids (Topical Guide, Bible Dictionary, Guide to the
-- Scriptures, Joseph Smith Translation).
--
-- Lives in its own table rather than being stuffed into `manuals` or
-- `scriptures`: a TG entry is neither a verse nor a manual chapter, and
-- callers want to filter by aid_type without parsing slugs.
--
-- Embeddings continue to live in the unified `embeddings` table from 001
-- (source_type='study_aids'), so no inline vector column here.

-- aid_type domain is verified against the on-disk layout
-- gospel-library/eng/scriptures/{tg,bd,gs,jst}/* (2026-05-13). Adding a
-- new aid type later requires both an indexer dispatch entry and a
-- migration that ALTERs this CHECK constraint.
CREATE TABLE IF NOT EXISTS study_aids (
    id          BIGSERIAL PRIMARY KEY,
    aid_type    TEXT NOT NULL CHECK (aid_type IN ('tg','bd','gs','jst')),
    slug        TEXT NOT NULL,                   -- 'faith' for tg/faith.md; 'jst-1-chr/21' for nested JST
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    file_path   TEXT NOT NULL UNIQUE,
    source_url  TEXT,
    tsv         tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(title,'') || ' ' || coalesce(content,''))) STORED
);

CREATE INDEX IF NOT EXISTS idx_study_aids_type      ON study_aids(aid_type);
CREATE INDEX IF NOT EXISTS idx_study_aids_type_slug ON study_aids(aid_type, slug);
CREATE INDEX IF NOT EXISTS idx_study_aids_tsv       ON study_aids USING GIN(tsv);
CREATE INDEX IF NOT EXISTS idx_study_aids_content_trgm ON study_aids USING GIN(content gin_trgm_ops);

INSERT INTO schema_migrations (version) VALUES (2) ON CONFLICT DO NOTHING;
