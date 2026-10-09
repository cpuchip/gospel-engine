package api

import (
	"context"
	"fmt"

	"github.com/cpuchip/gospel-engine/internal/indexer"
)

// xrefRow is one cross-reference attached to a verse result.
//
// `reference` is the human-readable target, from xrefLabel: the verse's own
// scriptures.reference ("Hebrews 7:11"), the study aid's title ("TG Grace",
// "BD Aaron", "JST, Revelation 2"), or the book's display name and chapter for
// a chapter-level target ("Psalms 119").
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
    s.reference,
    sa.title
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
LEFT JOIN study_aids sa
  ON cr.reference_type IN ('tg', 'bd', 'gs', 'jst')
 AND sa.aid_type = cr.target_volume
 AND sa.slug = CASE WHEN cr.target_volume = 'jst'
                    THEN cr.target_book || '/' || cr.target_chapter::text
                    ELSE cr.target_book END
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
			x        xrefRow
			tVerse   *int32
			refTyp   *string
			verseRef *string
			aidTitle *string
		)
		if err := rows.Scan(
			&x.TargetVolume, &x.TargetBook, &x.TargetChapter,
			&tVerse, &refTyp, &verseRef, &aidTitle,
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
		x.Reference = xrefLabel(x, deref(verseRef), deref(aidTitle))
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cross_references iterate: %w", err)
	}
	return out, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// aidPrefix names a study aid in a label; JST titles already carry "JST,".
var aidPrefix = map[string]string{"tg": "TG ", "bd": "BD ", "gs": "GS "}

// xrefLabel is the display text for one cross-reference target.
func xrefLabel(x xrefRow, verseRef, aidTitle string) string {
	switch x.ReferenceType {
	case "tg", "bd", "gs", "jst":
		if aidTitle != "" {
			return aidPrefix[x.ReferenceType] + aidTitle
		}
		if x.ReferenceType == "jst" {
			return fmt.Sprintf("JST %s %d", x.TargetBook, x.TargetChapter)
		}
		return aidPrefix[x.ReferenceType] + x.TargetBook
	}
	if verseRef != "" {
		return verseRef
	}
	ref := fmt.Sprintf("%s %d", indexer.BookDisplayName(x.TargetBook), x.TargetChapter)
	if x.TargetVerse != nil {
		ref += fmt.Sprintf(":%d", *x.TargetVerse)
	}
	return ref
}
