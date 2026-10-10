-- v3: Strong's concordance in the engine.
--
-- strongs_lexicon: the dual lexicon (Strong's 1890 via OpenScriptures, CC BY 4.0 / CC BY-SA; STEPBible TBESH/TBESG
-- glosses, CC BY 4.0), 19,570 entries, keyed by the unpadded number (H7225, G26).
-- kjv_words: the KJV's word-by-word Strong's tagging from CrossWire's SWORD KJV module (GPL; OT Strong's from The
-- Bible Foundation, NT from the KJV2003 Project), keyed by the engine's path slugs so a verse joins straight to
-- scriptures. One row per tagged word or phrase as the module marks it (a translator's added word has no numbers).
-- Filled once by scripts/strongs (build_strongs.py + load.sql), never from data committed to this repository.
CREATE TABLE IF NOT EXISTS strongs_lexicon (
    number       TEXT PRIMARY KEY,
    lang         TEXT NOT NULL,
    lemma        TEXT NOT NULL DEFAULT '',
    translit     TEXT NOT NULL DEFAULT '',
    pron         TEXT NOT NULL DEFAULT '',
    strongs_def  TEXT NOT NULL DEFAULT '',
    kjv_def      TEXT NOT NULL DEFAULT '',
    derivation   TEXT NOT NULL DEFAULT '',
    step_gloss   TEXT NOT NULL DEFAULT '',
    step_def     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS kjv_words (
    volume   TEXT   NOT NULL,
    book     TEXT   NOT NULL,
    chapter  INT    NOT NULL,
    verse    INT    NOT NULL,
    position INT    NOT NULL,
    word     TEXT   NOT NULL,
    numbers  TEXT[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (book, chapter, verse, position)
);

CREATE INDEX IF NOT EXISTS idx_kjv_words_numbers ON kjv_words USING GIN (numbers);

INSERT INTO schema_migrations (version) VALUES (5) ON CONFLICT DO NOTHING;
