-- Load the Strong's tables from build_strongs.py's output, replacing what is there, in one transaction.
--   psql -v dir=/path/to/out -f load.sql
-- COPY reads :dir on the DATABASE server (copy out/ into the database container first; see README.md). Checks before COMMIT: 19,570 lexicon entries, 31,102 verses,
-- every tagged number present in the lexicon, every KJV verse in scriptures. Any failure rolls the whole load back.
\set ON_ERROR_STOP on
BEGIN;
TRUNCATE kjv_words, strongs_lexicon;
\set lexfile :dir '/strongs_lexicon.tsv'
\set wordfile :dir '/kjv_words.tsv'
COPY strongs_lexicon (number, lang, lemma, translit, pron, strongs_def, kjv_def, derivation, step_gloss, step_def) FROM :'lexfile';
COPY kjv_words (volume, book, chapter, verse, position, word, numbers) FROM :'wordfile';
DO $$
DECLARE n_lex int; n_verses int; n_missing int; n_unjoined int;
BEGIN
  SELECT count(*) INTO n_lex FROM strongs_lexicon;
  SELECT count(*) INTO n_verses FROM (SELECT DISTINCT book, chapter, verse FROM kjv_words) v;
  SELECT count(*) INTO n_missing FROM (SELECT DISTINCT unnest(numbers) AS n FROM kjv_words) x
    WHERE NOT EXISTS (SELECT 1 FROM strongs_lexicon l WHERE l.number = x.n);
  SELECT count(*) INTO n_unjoined FROM (SELECT DISTINCT volume, book, chapter, verse FROM kjv_words) v
    WHERE NOT EXISTS (SELECT 1 FROM scriptures s WHERE s.volume = v.volume AND s.book = v.book
                                                   AND s.chapter = v.chapter AND s.verse = v.verse);
  RAISE NOTICE 'lexicon %, verses %, numbers missing from lexicon %, verses not in scriptures %', n_lex, n_verses, n_missing, n_unjoined;
  IF n_lex <> 19570 OR n_verses <> 31102 OR n_missing <> 0 OR n_unjoined <> 0 THEN
    RAISE EXCEPTION 'Strong''s load failed its checks; rolled back';
  END IF;
END $$;
SELECT word, numbers FROM kjv_words WHERE book = 'ex' AND chapter = 20 AND verse = 13 ORDER BY position;
COMMIT;
