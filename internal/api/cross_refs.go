package api

import (
	"context"
	"fmt"
)

// xrefRow is one cross-reference attached to a verse result.
//
// `reference` is the human-readable target ("Hebrews 7:11" or "heb 7"). It is
// resolved from scriptures.reference when the target verse exists, with a
// fallback constructed from the abbreviation when it doesn't (chapter-only
// xrefs, or rare cases where the target verse hasn't been indexed yet).
type xrefRow struct {
	Reference     string `json:"reference"`
	ReferenceType string `json:"reference_type,omitempty"`
	TargetVolume  string `json:"target_volume"`
	TargetBook    string `json:"target_book"`
	TargetChapter int    `json:"target_chapter"`
	TargetVerse   *int   `json:"target_verse,omitempty"`
}

// fetchCrossRefs loads deduplicated cross-references for the given source
// verses in a single batched query. Returns nil (not error) when there are
// no source verses or no matching xrefs — callers can JSON-encode the result
// directly without nil-checking.
func (s *Server) fetchCrossRefs(ctx context.Context, sources []verseRow) ([]xrefRow, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	volumes := make([]string, len(sources))
	books := make([]string, len(sources))
	chapters := make([]int32, len(sources))
	verses := make([]int32, len(sources))
	for i, v := range sources {
		volumes[i] = v.Volume
		books[i] = v.Book
		chapters[i] = int32(v.Chapter)
		verses[i] = int32(v.Verse)
	}

	const q = `
WITH src(volume, book, chapter, verse) AS (
    SELECT * FROM unnest($1::text[], $2::text[], $3::int[], $4::int[])
)
SELECT DISTINCT ON (cr.target_volume, cr.target_book, cr.target_chapter, cr.target_verse, cr.reference_type)
    cr.target_volume,
    cr.target_book,
    cr.target_chapter,
    cr.target_verse,
    cr.reference_type,
    COALESCE(
        s.reference,
        cr.target_book || ' ' || cr.target_chapter::text ||
            CASE WHEN cr.target_verse IS NOT NULL
                 THEN ':' || cr.target_verse::text
                 ELSE ''
            END
    ) AS target_reference
FROM cross_references cr
JOIN src
  ON cr.source_volume  = src.volume
 AND cr.source_book    = src.book
 AND cr.source_chapter = src.chapter
 AND cr.source_verse   = src.verse
LEFT JOIN scriptures s
  ON s.book    = cr.target_book
 AND s.chapter = cr.target_chapter
 AND s.verse   = cr.target_verse
ORDER BY cr.target_volume, cr.target_book, cr.target_chapter,
         cr.target_verse NULLS FIRST, cr.reference_type
`

	rows, err := s.DB.Pool.Query(ctx, q, volumes, books, chapters, verses)
	if err != nil {
		return nil, fmt.Errorf("cross_references query: %w", err)
	}
	defer rows.Close()

	var out []xrefRow
	for rows.Next() {
		var (
			x      xrefRow
			tVerse *int32
			refTyp *string
		)
		if err := rows.Scan(
			&x.TargetVolume, &x.TargetBook, &x.TargetChapter,
			&tVerse, &refTyp, &x.Reference,
		); err != nil {
			return nil, fmt.Errorf("cross_references scan: %w", err)
		}
		if tVerse != nil {
			v := int(*tVerse)
			x.TargetVerse = &v
		}
		if refTyp != nil {
			x.ReferenceType = *refTyp
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cross_references iterate: %w", err)
	}
	return out, nil
}
