-- gospel-engine v2 schema
-- PostgreSQL 18 + pgvector + pg_trgm
-- Single migration file; subsequent changes will be additive (002_*.sql, 003_*.sql).

-- ============================================================================
-- EXTENSIONS
-- ============================================================================
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ============================================================================
-- SCHEMA VERSION
-- ============================================================================
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================================
-- SCRIPTURES (verse-level)
-- ============================================================================
CREATE TABLE IF NOT EXISTS scriptures (
    id          BIGSERIAL PRIMARY KEY,
    volume      TEXT NOT NULL,                  -- ot, nt, bofm, dc-testament, pgp
    book        TEXT NOT NULL,                  -- gen, matt, 1-ne, dc, moses
    chapter     INT NOT NULL,
    verse       INT NOT NULL,
    reference   TEXT NOT NULL,                  -- "1 Nephi 3:7"
    text        TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    source_url  TEXT,
    tsv         tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    UNIQUE (volume, book, chapter, verse)
);

CREATE INDEX IF NOT EXISTS idx_scriptures_volume      ON scriptures(volume);
CREATE INDEX IF NOT EXISTS idx_scriptures_book        ON scriptures(volume, book);
CREATE INDEX IF NOT EXISTS idx_scriptures_chapter     ON scriptures(volume, book, chapter);
CREATE INDEX IF NOT EXISTS idx_scriptures_reference   ON scriptures(reference);
CREATE INDEX IF NOT EXISTS idx_scriptures_file        ON scriptures(file_path);
CREATE INDEX IF NOT EXISTS idx_scriptures_tsv         ON scriptures USING GIN(tsv);
CREATE INDEX IF NOT EXISTS idx_scriptures_text_trgm   ON scriptures USING GIN(text gin_trgm_ops);

-- ============================================================================
-- CHAPTERS (with enrichment "lenses")
-- ============================================================================
CREATE TABLE IF NOT EXISTS chapters (
    id                       BIGSERIAL PRIMARY KEY,
    volume                   TEXT NOT NULL,
    book                     TEXT NOT NULL,
    chapter                  INT NOT NULL,
    title                    TEXT,
    full_content             TEXT NOT NULL,
    file_path                TEXT NOT NULL,
    source_url               TEXT,
    -- enrichment ("chapter lenses" from existing gospel-engine)
    enrichment_summary       TEXT,
    enrichment_keywords      TEXT,
    enrichment_key_verse     TEXT,
    enrichment_christ_types  TEXT,
    enrichment_connections   JSONB,
    enrichment_model         TEXT,
    enrichment_raw_output    TEXT,
    UNIQUE (volume, book, chapter)
);

CREATE INDEX IF NOT EXISTS idx_chapters_volume   ON chapters(volume);
CREATE INDEX IF NOT EXISTS idx_chapters_book     ON chapters(volume, book);
CREATE INDEX IF NOT EXISTS idx_chapters_file     ON chapters(file_path);

-- ============================================================================
-- CONFERENCE TALKS (with TITSW)
-- ============================================================================
CREATE TABLE IF NOT EXISTS talks (
    id                  BIGSERIAL PRIMARY KEY,
    year                INT NOT NULL,
    month               TEXT NOT NULL,           -- "04" or "10"
    session             TEXT,
    speaker             TEXT NOT NULL,
    title               TEXT NOT NULL,
    content             TEXT NOT NULL,
    file_path           TEXT NOT NULL,
    source_url          TEXT,
    -- TITSW enrichment columns (carry forward from existing gospel-engine)
    titsw_dominant      TEXT,
    titsw_mode          TEXT,
    titsw_pattern       TEXT,
    titsw_teach         INT,
    titsw_help          INT,
    titsw_love          INT,
    titsw_spirit        INT,
    titsw_doctrine      INT,
    titsw_invite        INT,
    titsw_summary       TEXT,
    titsw_key_quote     TEXT,
    titsw_keywords      TEXT,
    titsw_reasoning     TEXT,
    titsw_raw_output    TEXT,
    titsw_model         TEXT,
    tsv                 tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(title,'') || ' ' || coalesce(content,''))) STORED,
    UNIQUE (file_path)
);

CREATE INDEX IF NOT EXISTS idx_talks_year       ON talks(year);
CREATE INDEX IF NOT EXISTS idx_talks_year_month ON talks(year, month);
CREATE INDEX IF NOT EXISTS idx_talks_speaker    ON talks(speaker);
CREATE INDEX IF NOT EXISTS idx_talks_titsw_mode ON talks(titsw_mode);
CREATE INDEX IF NOT EXISTS idx_talks_titsw_dom  ON talks(titsw_dominant);
CREATE INDEX IF NOT EXISTS idx_talks_tsv        ON talks USING GIN(tsv);
CREATE INDEX IF NOT EXISTS idx_talks_content_trgm ON talks USING GIN(content gin_trgm_ops);

-- ============================================================================
-- MANUALS (Come Follow Me, handbooks, etc.)
-- ============================================================================
CREATE TABLE IF NOT EXISTS manuals (
    id              BIGSERIAL PRIMARY KEY,
    content_type    TEXT NOT NULL,
    collection_id   TEXT NOT NULL,
    section         TEXT,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    source_url      TEXT,
    tsv             tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(title,'') || ' ' || coalesce(content,''))) STORED,
    UNIQUE (file_path)
);

CREATE INDEX IF NOT EXISTS idx_manuals_type       ON manuals(content_type);
CREATE INDEX IF NOT EXISTS idx_manuals_collection ON manuals(collection_id);
CREATE INDEX IF NOT EXISTS idx_manuals_tsv        ON manuals USING GIN(tsv);

-- ============================================================================
-- BOOKS (Lectures on Faith, etc.)
-- ============================================================================
CREATE TABLE IF NOT EXISTS books (
    id          BIGSERIAL PRIMARY KEY,
    collection  TEXT NOT NULL,
    section     TEXT NOT NULL,
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    source_url  TEXT,
    tsv         tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(title,'') || ' ' || coalesce(content,''))) STORED,
    UNIQUE (file_path)
);

CREATE INDEX IF NOT EXISTS idx_books_collection ON books(collection);
CREATE INDEX IF NOT EXISTS idx_books_tsv        ON books USING GIN(tsv);

-- ============================================================================
-- CROSS REFERENCES
-- ============================================================================
CREATE TABLE IF NOT EXISTS cross_references (
    id              BIGSERIAL PRIMARY KEY,
    source_volume   TEXT NOT NULL,
    source_book     TEXT NOT NULL,
    source_chapter  INT NOT NULL,
    source_verse    INT NOT NULL,
    target_volume   TEXT NOT NULL,
    target_book     TEXT NOT NULL,
    target_chapter  INT NOT NULL,
    target_verse    INT,
    reference_type  TEXT
);

CREATE INDEX IF NOT EXISTS idx_xref_source ON cross_references(source_volume, source_book, source_chapter, source_verse);
CREATE INDEX IF NOT EXISTS idx_xref_target ON cross_references(target_volume, target_book, target_chapter, target_verse);

-- ============================================================================
-- EMBEDDINGS (unified, 768-dim — nomic-embed-text v1.5)
-- ============================================================================
CREATE TABLE IF NOT EXISTS embeddings (
    id           BIGSERIAL PRIMARY KEY,
    source_type  TEXT NOT NULL,        -- scriptures, talks, manuals, books, chapters
    source_id    BIGINT NOT NULL,
    layer        TEXT NOT NULL,        -- verse, paragraph, summary, theme
    content      TEXT NOT NULL,        -- the indexed text (kept for reranking + display)
    embedding    vector(768) NOT NULL,
    model        TEXT NOT NULL DEFAULT 'nomic-embed-text-v1.5',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_type, source_id, layer)
);

CREATE INDEX IF NOT EXISTS idx_embeddings_source ON embeddings(source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_embeddings_hnsw   ON embeddings USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 128);

-- ============================================================================
-- INDEX METADATA (for incremental re-indexing)
-- ============================================================================
CREATE TABLE IF NOT EXISTS index_metadata (
    file_path     TEXT PRIMARY KEY,
    content_type  TEXT NOT NULL,
    indexed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    file_mtime    TIMESTAMPTZ NOT NULL,
    file_size     BIGINT NOT NULL,
    record_count  INT NOT NULL,
    checksum      TEXT
);

CREATE INDEX IF NOT EXISTS idx_metadata_type ON index_metadata(content_type);

-- ============================================================================
-- API TOKENS
-- ============================================================================
CREATE TABLE IF NOT EXISTS api_tokens (
    id              BIGSERIAL PRIMARY KEY,
    external_user   TEXT,                    -- ibeco.me user identifier (optional)
    name            TEXT NOT NULL,
    prefix          TEXT NOT NULL,           -- "stdy_" + 8 chars (for fast lookup)
    token_hash      TEXT NOT NULL,           -- bcrypt hash of full token
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used       TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    rate_limit      INT NOT NULL DEFAULT 60, -- requests per minute
    revoked         BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_tokens_prefix   ON api_tokens(prefix);
CREATE INDEX IF NOT EXISTS idx_tokens_external ON api_tokens(external_user);

-- Mark this migration applied.
INSERT INTO schema_migrations (version) VALUES (1) ON CONFLICT DO NOTHING;
