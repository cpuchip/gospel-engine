// Package indexer walks markdown content under /data/gospel-library and
// /data/books and inserts/upserts rows into PostgreSQL.
//
// This is intentionally simpler than the existing gospel-engine indexer:
// Phase 1 just gets content into PG. Enrichment (TITSW, chapter lenses,
// cross-references) is migrated separately from the existing SQLite.
package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
)

// Indexer parses markdown files from disk and upserts rows into Postgres.
type Indexer struct {
	DB                *db.DB
	GospelLibraryRoot string // /data/gospel-library
	BooksRoot         string // /data/books
}

// New builds an Indexer.
func New(database *db.DB, gospelRoot, booksRoot string) *Indexer {
	return &Indexer{DB: database, GospelLibraryRoot: gospelRoot, BooksRoot: booksRoot}
}

// Result is a small summary returned to the caller.
type Result struct {
	ScripturesIndexed int
	ChaptersIndexed   int
	TalksIndexed      int
	ManualsIndexed    int
	BooksIndexed      int
	Skipped           int
	Errors            int
	Duration          time.Duration
}

// IndexAll walks both content roots and upserts everything that has changed
// since the last index (based on file mtime + size).
func (idx *Indexer) IndexAll(ctx context.Context) (*Result, error) {
	start := time.Now()
	r := &Result{}

	if idx.GospelLibraryRoot != "" {
		if _, err := os.Stat(idx.GospelLibraryRoot); err == nil {
			if err := idx.indexGospelLibrary(ctx, r); err != nil {
				return r, fmt.Errorf("gospel-library: %w", err)
			}
		}
	}
	if idx.BooksRoot != "" {
		if _, err := os.Stat(idx.BooksRoot); err == nil {
			if err := idx.indexBooks(ctx, r); err != nil {
				return r, fmt.Errorf("books: %w", err)
			}
		}
	}

	r.Duration = time.Since(start)
	return r, nil
}

// ============================================================================
// gospel-library walker — dispatches by path shape
// ============================================================================

func (idx *Indexer) indexGospelLibrary(ctx context.Context, r *Result) error {
	return filepath.WalkDir(idx.GospelLibraryRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate transient errors mid-walk
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(idx.GospelLibraryRoot, path)
		rel = filepath.ToSlash(rel)
		// Expected layouts:
		//   eng/scriptures/{volume}/{book}/{chapter}.md
		//   eng/general-conference/{year}/{month}/{slug}.md
		//   eng/manual/{collection}/.../...md
		parts := strings.Split(rel, "/")
		if len(parts) < 3 {
			return nil
		}
		section := parts[1]

		info, statErr := os.Stat(path)
		if statErr != nil {
			r.Errors++
			return nil
		}
		if !idx.shouldIndex(ctx, path, info, sectionType(section)) {
			r.Skipped++
			return nil
		}

		switch section {
		case "scriptures":
			if err := idx.indexScriptureFile(ctx, path, parts, r); err != nil {
				r.Errors++
			}
		case "general-conference":
			if err := idx.indexTalkFile(ctx, path, parts, r); err != nil {
				r.Errors++
			}
		case "manual":
			if err := idx.indexManualFile(ctx, path, parts, r); err != nil {
				r.Errors++
			}
		default:
			r.Skipped++
			return nil
		}

		_ = idx.recordIndexed(ctx, path, info, sectionType(section), 1)
		return nil
	})
}

// sectionType maps gospel-library section names to index_metadata content_type.
func sectionType(section string) string {
	switch section {
	case "scriptures":
		return "scriptures"
	case "general-conference":
		return "talks"
	case "manual":
		return "manuals"
	default:
		return section
	}
}

// shouldIndex returns true if the file is new or modified since last index.
func (idx *Indexer) shouldIndex(ctx context.Context, path string, info os.FileInfo, contentType string) bool {
	var (
		mtime time.Time
		size  int64
	)
	err := idx.DB.Pool.QueryRow(ctx,
		`SELECT file_mtime, file_size FROM index_metadata WHERE file_path = $1`, path,
	).Scan(&mtime, &size)
	if err != nil {
		return true // not indexed yet
	}
	return !info.ModTime().UTC().Equal(mtime.UTC()) || size != info.Size()
}

func (idx *Indexer) recordIndexed(ctx context.Context, path string, info os.FileInfo, contentType string, n int) error {
	_, err := idx.DB.Pool.Exec(ctx, `
		INSERT INTO index_metadata (file_path, content_type, file_mtime, file_size, record_count, indexed_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (file_path) DO UPDATE
		SET content_type = EXCLUDED.content_type,
		    file_mtime   = EXCLUDED.file_mtime,
		    file_size    = EXCLUDED.file_size,
		    record_count = EXCLUDED.record_count,
		    indexed_at   = NOW()
	`, path, contentType, info.ModTime().UTC(), info.Size(), n)
	return err
}

// ============================================================================
// scriptures
// ============================================================================

var (
	verseRe       = regexp.MustCompile(`^\*\*(\d+)\.\*\*\s*(.+)`)
	footnoteRe    = regexp.MustCompile(`<sup>\[[^\]]+\]\(#fn-[^)]+\)</sup>`)
	superscriptRe = regexp.MustCompile(`<sup>[^<]+</sup>`)
	linkRe        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
)

var scriptureVolumes = map[string]bool{
	"ot": true, "nt": true, "bofm": true, "dc-testament": true, "pgp": true,
}

func (idx *Indexer) indexScriptureFile(ctx context.Context, path string, parts []string, r *Result) error {
	// eng/scriptures/{volume}/{book}/{chapter}.md
	if len(parts) < 5 {
		return nil
	}
	volume := parts[2]
	if !scriptureVolumes[volume] {
		return nil // skip study aids: tg, bd, gs, jst — Phase 2
	}
	book := parts[3]
	chapterStr := strings.TrimSuffix(parts[4], ".md")
	chapter, err := strconv.Atoi(chapterStr)
	if err != nil {
		return nil
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	full := string(body)

	title := firstHeading(full)

	// Upsert chapter.
	if _, err := idx.DB.Pool.Exec(ctx, `
		INSERT INTO chapters (volume, book, chapter, title, full_content, file_path)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (volume, book, chapter) DO UPDATE
		SET title = EXCLUDED.title,
		    full_content = EXCLUDED.full_content,
		    file_path = EXCLUDED.file_path
	`, volume, book, chapter, title, full, path); err != nil {
		return fmt.Errorf("upsert chapter: %w", err)
	}
	r.ChaptersIndexed++

	verses := parseVerses(full)
	for _, v := range verses {
		ref := formatReference(book, chapter, v.Number)
		if _, err := idx.DB.Pool.Exec(ctx, `
			INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (volume, book, chapter, verse) DO UPDATE
			SET reference = EXCLUDED.reference,
			    text = EXCLUDED.text,
			    file_path = EXCLUDED.file_path
		`, volume, book, chapter, v.Number, ref, v.Text, path); err != nil {
			return fmt.Errorf("upsert verse %d: %w", v.Number, err)
		}
		r.ScripturesIndexed++
	}
	return nil
}

type parsedVerse struct {
	Number int
	Text   string
}

func parseVerses(content string) []parsedVerse {
	var verses []parsedVerse
	for _, line := range strings.Split(content, "\n") {
		m := verseRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		text := cleanInlineMarkdown(m[2])
		if text != "" {
			verses = append(verses, parsedVerse{Number: n, Text: text})
		}
	}
	return verses
}

func cleanInlineMarkdown(s string) string {
	s = footnoteRe.ReplaceAllString(s, "")
	s = superscriptRe.ReplaceAllString(s, "")
	s = linkRe.ReplaceAllString(s, "$1")
	return strings.Join(strings.Fields(s), " ")
}

func firstHeading(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// formatReference produces a display string like "1 Nephi 3:7" from
// gospel-library's slugged book names ("1-ne", "matt", "dc"). The mapping
// for the long tail of book slugs lives in the existing gospel-engine
// urlgen package; for Phase 1 we use a minimal map and fall back to slug.
func formatReference(bookSlug string, chapter, verse int) string {
	name := bookDisplayName(bookSlug)
	return fmt.Sprintf("%s %d:%d", name, chapter, verse)
}

// bookDisplayName converts gospel-library book slugs to display names.
// This list is intentionally minimal; missing entries fall back to the slug.
var bookNames = map[string]string{
	// Book of Mormon
	"1-ne": "1 Nephi", "2-ne": "2 Nephi", "jacob": "Jacob", "enos": "Enos",
	"jarom": "Jarom", "omni": "Omni", "w-of-m": "Words of Mormon",
	"mosiah": "Mosiah", "alma": "Alma", "hel": "Helaman",
	"3-ne": "3 Nephi", "4-ne": "4 Nephi", "morm": "Mormon", "ether": "Ether",
	"moro": "Moroni",
	// Doctrine & Covenants
	"dc": "D&C", "od": "Official Declaration",
	// Pearl of Great Price
	"moses": "Moses", "abr": "Abraham", "js-m": "JS—Matthew",
	"js-h": "Joseph Smith—History", "a-of-f": "Articles of Faith",
	// Old Testament (samples — full map TODO)
	"gen": "Genesis", "ex": "Exodus", "lev": "Leviticus", "num": "Numbers",
	"deut": "Deuteronomy", "ps": "Psalms", "isa": "Isaiah", "mal": "Malachi",
	// New Testament (samples)
	"matt": "Matthew", "mark": "Mark", "luke": "Luke", "john": "John",
	"acts": "Acts", "rom": "Romans", "rev": "Revelation",
}

func bookDisplayName(slug string) string {
	if v, ok := bookNames[slug]; ok {
		return v
	}
	return slug
}

// ============================================================================
// general-conference talks
// ============================================================================

func (idx *Indexer) indexTalkFile(ctx context.Context, path string, parts []string, r *Result) error {
	// eng/general-conference/{year}/{month}/{slug}.md
	if len(parts) < 5 {
		return nil
	}
	year, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil
	}
	month := parts[3]

	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	full := string(body)

	title, speaker, content := parseTalkHeader(full)

	if _, err := idx.DB.Pool.Exec(ctx, `
		INSERT INTO talks (year, month, speaker, title, content, file_path)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (file_path) DO UPDATE
		SET year = EXCLUDED.year,
		    month = EXCLUDED.month,
		    speaker = EXCLUDED.speaker,
		    title = EXCLUDED.title,
		    content = EXCLUDED.content
	`, year, month, speaker, title, content, path); err != nil {
		return fmt.Errorf("upsert talk: %w", err)
	}
	r.TalksIndexed++
	return nil
}

// parseTalkHeader extracts title (first H1), speaker (first speaker-like line
// after the title), and content (everything after the speaker line).
//
// The modern church.org export inserts noise between the title and the
// speaker that the previous version of this parser captured as "speaker":
//
//	# Title
//
//	🎧 [Listen to Audio](https://...mp3)
//
//	# Title                       <- duplicated H1
//
//	By President Dallin H. Oaks   <- actual speaker, with "By " prefix
//
// Older talks (pre-redesign) had the format:
//
//	# Title
//
//	Elder Bruce R. McConkie
//
// This parser handles both. After the title is found, it skips lines that
// are empty, audio-link lines (start with "🎧" or "[Listen to Audio"), or
// further H1 headings (the duplicated title case). The first surviving
// non-empty line is the speaker candidate. If it begins with "By " (case
// insensitive) the prefix is stripped. The result is run through
// cleanInlineMarkdown to strip italics / link wrappers.
//
// If no speaker can be found the function returns speaker="" and the caller
// is responsible for logging / not overwriting an existing DB value.
func parseTalkHeader(s string) (title, speaker, content string) {
	lines := strings.Split(s, "\n")
	contentStart := 0

	// 1) Find the first H1 = title.
	titleIdx := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(t, "# "))
			titleIdx = i
			contentStart = i + 1
			break
		}
	}
	if titleIdx < 0 {
		return
	}

	// 2) Walk forward looking for the speaker, skipping known noise.
	for i := titleIdx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if isAudioLinkLine(t) {
			continue
		}
		if strings.HasPrefix(t, "# ") {
			// duplicated title; skip
			continue
		}
		// Candidate speaker line.
		cleaned := cleanInlineMarkdown(t)
		// Strip a leading "By " (case-insensitive). Only strip if there's
		// something after — "By " alone is meaningless and should fall
		// through to the empty-result branch.
		if len(cleaned) > 3 {
			lower := strings.ToLower(cleaned)
			if strings.HasPrefix(lower, "by ") {
				cleaned = strings.TrimSpace(cleaned[3:])
			}
		}
		if cleaned != "" {
			speaker = cleaned
			contentStart = i + 1
		}
		break
	}

	content = strings.Join(lines[contentStart:], "\n")
	return
}

// isAudioLinkLine matches the church.org "Listen to Audio" lines that
// appear between the title and the speaker in modern talk markdown.
// Examples:
//
//	🎧 [Listen to Audio](https://...mp3)
//	[Listen to Audio](https://...mp3)
func isAudioLinkLine(t string) bool {
	if strings.HasPrefix(t, "🎧") {
		return true
	}
	if strings.HasPrefix(t, "[Listen to Audio") {
		return true
	}
	return false
}

// ============================================================================
// manuals (Come Follow Me, etc.)
// ============================================================================

func (idx *Indexer) indexManualFile(ctx context.Context, path string, parts []string, r *Result) error {
	// eng/manual/{collection}/.../.md
	if len(parts) < 4 {
		return nil
	}
	collection := parts[2]
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	full := string(body)
	title := firstHeading(full)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	if _, err := idx.DB.Pool.Exec(ctx, `
		INSERT INTO manuals (content_type, collection_id, title, content, file_path)
		VALUES ('manual', $1, $2, $3, $4)
		ON CONFLICT (file_path) DO UPDATE
		SET title = EXCLUDED.title,
		    content = EXCLUDED.content,
		    collection_id = EXCLUDED.collection_id
	`, collection, title, full, path); err != nil {
		return fmt.Errorf("upsert manual: %w", err)
	}
	r.ManualsIndexed++
	return nil
}

// ============================================================================
// books — /data/books/{collection}/{...}.md
// ============================================================================

func (idx *Indexer) indexBooks(ctx context.Context, r *Result) error {
	return filepath.WalkDir(idx.BooksRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(idx.BooksRoot, path)
		rel = filepath.ToSlash(rel)
		parts := strings.Split(rel, "/")
		collection := "misc"
		section := strings.TrimSuffix(filepath.Base(path), ".md")
		if len(parts) >= 1 {
			collection = parts[0]
		}

		info, statErr := os.Stat(path)
		if statErr != nil {
			r.Errors++
			return nil
		}
		if !idx.shouldIndex(ctx, path, info, "books") {
			r.Skipped++
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			r.Errors++
			return nil
		}
		full := string(body)
		title := firstHeading(full)
		if title == "" {
			title = section
		}

		if _, err := idx.DB.Pool.Exec(ctx, `
			INSERT INTO books (collection, section, title, content, file_path)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (file_path) DO UPDATE
			SET title = EXCLUDED.title,
			    content = EXCLUDED.content,
			    collection = EXCLUDED.collection,
			    section = EXCLUDED.section
		`, collection, section, title, full, path); err != nil {
			r.Errors++
			return nil
		}
		r.BooksIndexed++
		_ = idx.recordIndexed(ctx, path, info, "books", 1)
		return nil
	})
}
