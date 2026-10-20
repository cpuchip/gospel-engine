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

// normalizeBookName maps a book as typed or dictated to the canonical slug
// used in the scriptures.book and chapters.book columns ("1-ne", "matt",
// "dc"). It takes full names, the Church's own abbreviations (1 Ne., Hel.,
// Moro., JS—H, A of F), a set of common shorter forms (Eth, Mt, Jn, Phil),
// and spoken or suffixed ordinals (First Nephi, 1st Nephi, I Nephi). Anything
// it cannot name returns "" and the caller fails closed: a wrong verse is
// worse than none.
func normalizeBookName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	// Dashes of every kind and periods are spelling, not meaning: "JS—H",
	// "js-h", "1 Ne." and "1 ne" are one book.
	name = bookSpelling.Replace(name)
	name = strings.Join(strings.Fields(name), " ")
	name = leadingOrdinal(name)
	if abbr, ok := bookNameMap[name]; ok {
		return abbr
	}
	if abbr, ok := bookShortForms[name]; ok {
		return abbr
	}
	// The Church's abbreviations are the slugs themselves, spaced: "1 ne",
	// "w of m", "js h". So is a slug typed as such.
	slug := strings.ReplaceAll(name, " ", "-")
	for _, abbr := range bookNameMap {
		if slug == abbr {
			return abbr
		}
	}
	return ""
}

var bookSpelling = strings.NewReplacer("—", " ", "–", " ", "-", " ", ".", "", ",", "")

// leadingOrdinal turns a spoken, suffixed or Roman ordinal at the front of a
// book name into its digit: "first nephi", "1st nephi", "i nephi" -> "1 nephi";
// "1nephi" -> "1 nephi".
func leadingOrdinal(name string) string {
	word, rest, found := strings.Cut(name, " ")
	if !found {
		// "1ne", "3nephi": a digit glued to the name
		if len(word) > 1 && word[0] >= '1' && word[0] <= '4' && word[1] >= 'a' && word[1] <= 'z' {
			return word[:1] + " " + word[1:]
		}
		return name
	}
	if n, ok := ordinalWords[word]; ok {
		return n + " " + rest
	}
	return name
}

var ordinalWords = map[string]string{
	"first": "1", "second": "2", "third": "3", "fourth": "4",
	"1st": "1", "2nd": "2", "3rd": "3", "4th": "4",
	"i": "1", "ii": "2", "iii": "3", "iv": "4",
}

// bookShortForms are abbreviations in common use beyond the Church's own;
// each names exactly one book. Forms that could name two (Mor: Mormon or
// Moroni; Ez: Ezra or Ezekiel) are left out on purpose.
var bookShortForms = map[string]string{
	// Old Testament
	"gn": "gen", "ge": "gen", "exo": "ex", "exod": "ex", "lv": "lev", "nu": "num", "nm": "num",
	"dt": "deut", "jos": "josh", "jdg": "judg", "jgs": "judg", "ru": "ruth",
	"1 sa": "1-sam", "2 sa": "2-sam", "1 sm": "1-sam", "2 sm": "2-sam",
	"1 ki": "1-kgs", "2 ki": "2-kgs", "1 kings": "1-kgs", "2 kings": "2-kgs",
	"1 ch": "1-chr", "2 ch": "2-chr", "1 chron": "1-chr", "2 chron": "2-chr",
	"ezr": "ezra", "est": "esth", "psa": "ps", "pss": "ps", "pro": "prov", "pr": "prov",
	"ecc": "eccl", "eccles": "eccl", "qoheleth": "eccl",
	"song of songs": "song", "canticles": "song", "sos": "song",
	"is": "isa", "je": "jer", "la": "lam", "ezk": "ezek", "eze": "ezek", "dn": "dan",
	"hos": "hosea", "ob": "obad", "jon": "jonah", "mic": "micah", "nah": "nahum",
	"zep": "zeph", "zec": "zech",
	// New Testament
	"mt": "matt", "mk": "mark", "lk": "luke", "jn": "john", "ac": "acts", "ro": "rom", "rm": "rom",
	"1 co": "1-cor", "2 co": "2-cor", "ga": "gal", "ep": "eph", "phil": "philip", "php": "philip",
	"cl": "col", "1 th": "1-thes", "2 th": "2-thes", "1 thess": "1-thes", "2 thess": "2-thes",
	"1 ti": "1-tim", "2 ti": "2-tim", "tit": "titus", "phm": "philem", "hb": "heb", "jas": "james",
	"1 pe": "1-pet", "2 pe": "2-pet", "1 pt": "1-pet", "2 pt": "2-pet",
	"1 jo": "1-jn", "2 jo": "2-jn", "3 jo": "3-jn", "1 john": "1-jn", "2 john": "2-jn", "3 john": "3-jn",
	"re": "rev", "rv": "rev", "apoc": "rev", "apocalypse": "rev",
	// Book of Mormon
	"1 nep": "1-ne", "2 nep": "2-ne", "3 nep": "3-ne", "4 nep": "4-ne",
	"jac": "jacob", "wom": "w-of-m", "mos": "mosiah", "mosi": "mosiah", "al": "alma",
	"hela": "hel", "eth": "ether", "mrm": "morm", "mro": "moro",
	// Doctrine and Covenants, Pearl of Great Price
	"d and c": "dc", "d c": "dc", "d&c": "dc", "doctrine & covenants": "dc", "doctrine and covenants": "dc",
	"abra": "abr", "jsm": "js-m", "js matt": "js-m", "js matthew": "js-m",
	"jsh": "js-h", "js hist": "js-h", "js history": "js-h",
	"aof": "a-of-f", "a of f": "a-of-f", "articles": "a-of-f",
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
