-- v3: the explicit-link graph behind gospel_related.
--
-- Every link the library itself makes, as text-keyed edges, stored in both
-- directions so a walk is one indexed lookup per hop:
--   v:<volume>/<book>/<chapter>:<verse>   a verse        (scriptures row)
--   c:<volume>/<book>/<chapter>           a chapter      (a link naming no verse)
--   tg:<slug> bd:<slug> gs:<slug>         a study-aid entry (study_aids row)
--   jst:<jst-book>/<chapter>              a JST chapter  (study_aids row)
--   t:<talk id>#<paragraph>               a talk paragraph   (talks row, splitParagraphs index)
--   m:<manual id>#<paragraph>             a manual paragraph (manuals row, same index)
-- edge_type: footnote (chapter footnotes, = cross_references), talk_cites,
-- manual_cites, tg_ref / bd_ref / gs_ref (an entry's verse list). reverse is
-- FALSE on the row as the library wrote it, TRUE on its mirror.
--
-- Rebuilt whole, in one transaction, after an index pass (indexer.RebuildGraph);
-- no Apache AGE: a recursive CTE over this table answers two hops in tens of ms.
CREATE TABLE IF NOT EXISTS graph_edges (
    src       TEXT    NOT NULL,
    dst       TEXT    NOT NULL,
    edge_type TEXT    NOT NULL,
    reverse   BOOLEAN NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_graph_edges_src ON graph_edges(src);

INSERT INTO schema_migrations (version) VALUES (4) ON CONFLICT DO NOTHING;
