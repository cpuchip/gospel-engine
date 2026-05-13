// Package api — reference parsing for /api/get.
//
// Ported from scripts/gospel-mcp/internal/tools/get.go (already battle-tested
// against the markdown corpus). Handles "1 Nephi 3:7", "D&C 93:24-30",
// "Mosiah 4" (chapter-only), and the various book-name spellings that show up
// in real agent traffic. Volume is intentionally omitted from the parsed
// result — book abbreviations are globally unique across the standard works,
// so (book, chapter[, verse]) is enough to hit the indexed columns.
package api

import (
	"fmt"
	"strings"
)

// parsedRef is the result of parsing a scripture reference. EndVerse == 0
// means single-verse; Verse == 0 means chapter-only.
type parsedRef struct {
	Book     string // canonical lowercase abbreviation, e.g. "1-ne", "mosiah", "matt", "dc"
	Chapter  int
	Verse    int
	EndVerse int
}

// parseReference parses scripture references into their structural pieces.
// Returns ok=false if the reference can't be normalized to a known book.
func parseReference(ref string) (parsedRef, bool) {
	original := strings.TrimSpace(ref)
	if original == "" {
		return parsedRef{}, false
	}
	normalized := strings.ToLower(original)
	normalized = strings.ReplaceAll(normalized, "doctrine and covenants", "dc")
	normalized = strings.ReplaceAll(normalized, "d&c", "dc")

	parts := strings.Fields(normalized)
	if len(parts) < 2 {
		return parsedRef{}, false
	}

	last := parts[len(parts)-1]
	bookParts := parts[:len(parts)-1]

	if colonIdx := strings.Index(last, ":"); colonIdx > 0 {
		// "chapter:verse" or "chapter:verse-endverse"
		chapterStr := last[:colonIdx]
		verseStr := last[colonIdx+1:]

		var chapter int
		if _, err := fmt.Sscanf(chapterStr, "%d", &chapter); err != nil || chapter <= 0 {
			return parsedRef{}, false
		}

		var verse, endVerse int
		if dashIdx := strings.Index(verseStr, "-"); dashIdx > 0 {
			fmt.Sscanf(verseStr[:dashIdx], "%d", &verse)
			fmt.Sscanf(verseStr[dashIdx+1:], "%d", &endVerse)
		} else {
			fmt.Sscanf(verseStr, "%d", &verse)
		}
		if verse <= 0 {
			return parsedRef{}, false
		}

		book := normalizeBookName(strings.Join(bookParts, " "))
		if book == "" {
			return parsedRef{}, false
		}
		return parsedRef{Book: book, Chapter: chapter, Verse: verse, EndVerse: endVerse}, true
	}

	// Chapter-only: "Mosiah 4"
	var chapter int
	if _, err := fmt.Sscanf(last, "%d", &chapter); err != nil || chapter <= 0 {
		return parsedRef{}, false
	}
	book := normalizeBookName(strings.Join(bookParts, " "))
	if book == "" {
		return parsedRef{}, false
	}
	return parsedRef{Book: book, Chapter: chapter}, true
}

// normalizeBookName maps full names + variations to the canonical abbreviation
// used in the `scriptures.book` and `chapters.book` columns.
func normalizeBookName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if abbr, ok := bookNameMap[name]; ok {
		return abbr
	}
	// Already an abbreviation?
	for _, abbr := range bookNameMap {
		if name == abbr {
			return abbr
		}
	}
	return ""
}

var bookNameMap = map[string]string{
	// Old Testament
	"genesis": "gen", "exodus": "ex", "leviticus": "lev", "numbers": "num",
	"deuteronomy": "deut", "joshua": "josh", "judges": "judg", "ruth": "ruth",
	"1 samuel": "1-sam", "2 samuel": "2-sam", "1 kings": "1-kgs", "2 kings": "2-kgs",
	"1 chronicles": "1-chr", "2 chronicles": "2-chr", "ezra": "ezra", "nehemiah": "neh",
	"esther": "esth", "job": "job", "psalms": "ps", "psalm": "ps", "proverbs": "prov",
	"ecclesiastes": "eccl", "song of solomon": "song", "isaiah": "isa", "jeremiah": "jer",
	"lamentations": "lam", "ezekiel": "ezek", "daniel": "dan", "hosea": "hosea",
	"joel": "joel", "amos": "amos", "obadiah": "obad", "jonah": "jonah", "micah": "micah",
	"nahum": "nahum", "habakkuk": "hab", "zephaniah": "zeph", "haggai": "hag",
	"zechariah": "zech", "malachi": "mal",
	// New Testament
	"matthew": "matt", "mark": "mark", "luke": "luke", "john": "john", "acts": "acts",
	"romans": "rom", "1 corinthians": "1-cor", "2 corinthians": "2-cor",
	"galatians": "gal", "ephesians": "eph", "philippians": "philip",
	"colossians": "col", "1 thessalonians": "1-thes", "2 thessalonians": "2-thes",
	"1 timothy": "1-tim", "2 timothy": "2-tim", "titus": "titus", "philemon": "philem",
	"hebrews": "heb", "james": "james", "1 peter": "1-pet", "2 peter": "2-pet",
	"1 john": "1-jn", "2 john": "2-jn", "3 john": "3-jn", "jude": "jude",
	"revelation": "rev", "revelations": "rev",
	// Book of Mormon
	"1 nephi": "1-ne", "2 nephi": "2-ne", "jacob": "jacob", "enos": "enos",
	"jarom": "jarom", "omni": "omni", "words of mormon": "w-of-m",
	"mosiah": "mosiah", "alma": "alma", "helaman": "hel",
	"3 nephi": "3-ne", "4 nephi": "4-ne", "mormon": "morm",
	"ether": "ether", "moroni": "moro",
	// D&C
	"dc": "dc",
	// Pearl of Great Price
	"moses": "moses", "abraham": "abr", "js matthew": "js-m", "js history": "js-h",
	"joseph smith matthew": "js-m", "joseph smith history": "js-h",
	"articles of faith": "a-of-f",
}
