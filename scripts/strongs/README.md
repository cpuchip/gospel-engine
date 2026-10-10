# Loading Strong's into the engine

The engine's Strong's tables (migration `005_strongs.sql`: `strongs_lexicon`, `kjv_words`) are filled once on the
server from their sources. None of the source data is committed here: CrossWire's module is GPL, and building it on
the server keeps this repository's code under its own licence.

## Sources

| data | where | licence |
|---|---|---|
| KJV with Strong's numbers | CrossWire's SWORD KJV module, `KJV.zip` (rawzip package, https://www.crosswire.org/sword/modules/ModInfo.jsp?modName=KJV) | GPL; CrossWire grants use "for any purpose"; OT Strong's from The Bible Foundation, NT from the KJV2003 Project |
| the lexicon (19,570 entries) | `data/strongs-lexicon.json.gz` in cpuchip/strongs-concordance-mcp | Strong's 1890 public domain via OpenScriptures (CC BY 4.0 / CC BY-SA); STEPBible TBESH/TBESG glosses (CC BY 4.0) |

## Steps (after the deploy that applies migration 005)

```sh
python3 -m venv /tmp/strongs-venv && /tmp/strongs-venv/bin/pip install pysword==0.2.8
/tmp/strongs-venv/bin/python scripts/strongs/build_strongs.py \
  --kjv-zip KJV.zip --lexicon strongs-lexicon.json.gz \
  --library /path/to/gospel-library --out scripts/strongs/out
# COPY reads files on the DATABASE server: put out/ where the server process can read it, e.g. inside its container
docker cp scripts/strongs/out <db-container>:/tmp/strongs-out
docker cp scripts/strongs/load.sql <db-container>:/tmp/load.sql
docker exec <db-container> psql -U <user> -d <db> -v dir=/tmp/strongs-out -f /tmp/load.sql
```

`build_strongs.py` refuses to write unless every KJV verse lands on a verse the engine indexes, every indexed KJV
verse has tagging, no verse is untagged, and the lexicon has 19,570 entries. `load.sql` replaces both tables in one
transaction and rolls back unless the database agrees: 19,570 entries, 31,102 verses, every number in the lexicon,
every verse in `scriptures`; a failed load leaves the previous tables as they were. Tested this way on 2026-10-09
(pgvector:pg18 throwaway, the library's 41,995 verses seeded): COMMIT on the real build; ROLLBACK, previous load
intact, with one KJV verse removed from `scriptures`.

## What it gave on 2026-10-09 (the library of that date)

31,102 verses, 370,654 tagged words and phrases (349,076 carrying numbers), 14,074 distinct numbers, all in the
lexicon; 0 verses without a tag (Exodus 20:13, untagged in the earlier source, is H7523). Numbers are CrossWire's
lemma numbers, unpadded (H07225 -> H7225), as ruled on 2026-10-09.

Until the load runs, the Strong's endpoints answer 503.
