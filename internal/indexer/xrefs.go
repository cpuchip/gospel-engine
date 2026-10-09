package indexer

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cross-references from the library's own chapter footnotes.
//
// Every scripture chapter file ends in a "## Footnotes" section whose lines
// look like
//
//	<a id="fn-21a"></a>**21a** [Heb. 11:1](../../nt/heb/11.md) ; [TG Faith](../../tg/faith.md).
//
// Each link becomes one row of cross_references keyed by the engine's path
// slugs (eng/scriptures/{volume}/{book}/{chapter}.md), the same keys the
// scriptures table uses:
//
//   - a scripture target takes volume/book/chapter from the href and the
//     verse from the link TEXT, read after the href's own "<chapter>:" (the
//     href names only the chapter file). A range a–b expands when b-a < 10,
//     else gives its first verse; a range that runs into another chapter
//     stops at the chapter break; a parenthesis is context and is ignored;
//     no verse at all gives a chapter-level row (target_verse NULL).
//     reference_type 'footnote'.
//   - a "JST …" link to a KJV chapter is JST-numbered; it takes the KJV verse
//     from its parenthesis when that names the href's chapter, else it is
//     chapter-level.
//   - a study-aid target (tg, bd, gs) gives target_volume = the aid, target_book
//     = the file stem, target_chapter 0; a JST target gives target_book = its
//     'jst-<book>' folder and target_chapter = its chapter. target_verse NULL,
//     reference_type = the aid.
//   - targets with no indexed row (title pages, facsimiles, a chapter with no
//     verses such as OD 1) are skipped and counted.
//
// This is a port of the one-time loader that first filled the table on
// 2026-10-09 (engine-v3-probe xref/build_xrefs.py, 69,572 rows); the corpus
// test reproduces that file row for row.

// XrefRow is one cross_references row.
type XrefRow struct {
	SourceVolume  string
	SourceBook    string
	SourceChapter int
	SourceVerse   int
	TargetVolume  string
	TargetBook    string
	TargetChapter int
	TargetVerse   *int
	ReferenceType string
}

// XrefSummary reports what a build found.
type XrefSummary struct {
	Chapters         int
	Rows             int
	ByType           map[string]int
	UnmatchedSources int // rows whose source verse is not an indexed verse (expected 0)
	UnmatchedTargets int // verse-level scripture targets that are not indexed verses (expected 0)
	Skipped          map[string]int
	Duration         time.Duration
}

var (
	xrefVolumes = []string{"ot", "nt", "bofm", "dc-testament", "pgp"}
	xrefAids    = map[string]bool{"tg": true, "bd": true, "gs": true, "jst": true}

	xrefLinkRe  = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+\.md)(?:#[^)]*)?\)`)
	xrefFnIDRe  = regexp.MustCompile(`<a id="fn-(\d+)[a-z]+"></a>`)
	xrefParenRe = regexp.MustCompile(`\([^)]*\)`)
	xrefParGrp  = regexp.MustCompile(`\(([^)]*)\)`)
	xrefNotesRe = regexp.MustCompile(`\bnotes?\b.*$`)
	xrefXChapRe = regexp.MustCompile(`[–-]\s*\d+:`)
	xrefListRe  = regexp.MustCompile(`[,;]`)
	xrefRangeRe = regexp.MustCompile(`^(\d+)(?:–(\d+))?`)
)

const enDash = "–"

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// chapterColons returns the end offsets of every "<ch>:" in t that is not
// preceded by a digit (so chapter 1 does not match inside "21:").
func chapterColons(t string, ch int) []int {
	needle := strconv.Itoa(ch) + ":"
	var ends []int
	for i := 0; ; {
		j := strings.Index(t[i:], needle)
		if j < 0 {
			return ends
		}
		at := i + j
		if at == 0 || t[at-1] < '0' || t[at-1] > '9' {
			ends = append(ends, at+len(needle))
		}
		i = at + 1
	}
}

// versesFromText returns the verses a footnote link's text names in chapter
// ch (the chapter its href points at). Empty means chapter-level.
func versesFromText(text string, ch int) []int {
	if strings.HasPrefix(strings.TrimSpace(text), "JST") {
		// "JST Gen. 18:23 (Gen. 18:22 note a)": the parenthesis carries the
		// KJV verse that the href's chapter has.
		par := xrefParGrp.FindAllStringSubmatch(text, -1)
		if len(par) > 0 {
			last := par[len(par)-1][1]
			if len(chapterColons(last, ch)) > 0 {
				return versesFromText(xrefNotesRe.ReplaceAllString(last, ""), ch)
			}
		}
		return nil
	}
	t := strings.ReplaceAll(xrefParenRe.ReplaceAllString(text, ""), "—", " ")
	if !strings.Contains(t, ":") {
		return nil
	}
	var tail string
	if ends := chapterColons(t, ch); len(ends) > 0 {
		tail = t[ends[len(ends)-1]:]
	} else {
		tail = t[strings.LastIndex(t, ":")+1:]
	}
	if loc := xrefXChapRe.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]] // a range that runs into another chapter
	}
	var out []int
	for _, piece := range xrefListRe.Split(tail, -1) {
		piece = strings.ReplaceAll(strings.TrimSpace(piece), "-", enDash)
		m := xrefRangeRe.FindStringSubmatch(piece)
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		if d := b - a; d >= 0 && d < 10 {
			for v := a; v <= b; v++ {
				out = append(out, v)
			}
		} else {
			out = append(out, a)
		}
	}
	return out
}

type xrefChapter struct {
	volume, book string
	chapter      int
	path         string
}

type verseKey struct {
	volume, book   string
	chapter, verse int
}

type chapKey struct {
	volume, book string
	chapter      int
}

// BuildCrossReferences parses every scripture chapter under
// gospelRoot/eng/scriptures and returns the deduplicated rows, sorted. It
// reads files only; it never touches the database.
func BuildCrossReferences(gospelRoot string) ([]XrefRow, *XrefSummary, error) {
	start := time.Now()
	scr := filepath.Join(gospelRoot, "eng", "scriptures")
	sum := &XrefSummary{ByType: map[string]int{}, Skipped: map[string]int{}}

	// 1. the scriptures key set, by the indexer's own rule (parseVerses).
	keys := map[verseKey]bool{}
	var chapters []xrefChapter
	for _, v := range xrefVolumes {
		books, err := os.ReadDir(filepath.Join(scr, v))
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", v, err)
		}
		for _, b := range books {
			if !b.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(scr, v, b.Name()))
			if err != nil {
				return nil, nil, err
			}
			for _, f := range files {
				stem := strings.TrimSuffix(f.Name(), ".md")
				if !strings.HasSuffix(f.Name(), ".md") || !isDigits(stem) {
					continue
				}
				ch, _ := strconv.Atoi(stem)
				p := filepath.Join(scr, v, b.Name(), f.Name())
				chapters = append(chapters, xrefChapter{v, b.Name(), ch, p})
				body, err := os.ReadFile(p)
				if err != nil {
					return nil, nil, err
				}
				for _, pv := range parseVerses(string(body)) {
					keys[verseKey{v, b.Name(), ch, pv.Number}] = true
				}
			}
		}
	}
	sum.Chapters = len(chapters)
	chapKeys := map[chapKey]bool{}
	for k := range keys {
		chapKeys[chapKey{k.volume, k.book, k.chapter}] = true
	}

	// 2. rows from each chapter's ## Footnotes.
	type rowKey struct {
		sv, sb, tv, tb, rt string
		sc, svs, tc, tvs   int // tvs -1 = NULL
	}
	seen := map[rowKey]bool{}
	var rows []XrefRow
	add := func(r XrefRow) {
		tv := -1
		if r.TargetVerse != nil {
			tv = *r.TargetVerse
		}
		k := rowKey{r.SourceVolume, r.SourceBook, r.TargetVolume, r.TargetBook, r.ReferenceType,
			r.SourceChapter, r.SourceVerse, r.TargetChapter, tv}
		if !seen[k] {
			seen[k] = true
			rows = append(rows, r)
		}
	}
	for _, c := range chapters {
		body, err := os.ReadFile(c.path)
		if err != nil {
			return nil, nil, err
		}
		txt := strings.ReplaceAll(string(body), "\r\n", "\n")
		i := strings.Index(txt, "## Footnotes")
		if i < 0 {
			continue
		}
		dir := path.Dir(slashPath(c.path))
		for _, line := range strings.Split(txt[i+len("## Footnotes"):], "\n") {
			m := xrefFnIDRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			srcVerse, _ := strconv.Atoi(m[1])
			parts3 := strings.SplitN(line, "**", 3)
			for _, lm := range xrefLinkRe.FindAllStringSubmatch(parts3[len(parts3)-1], -1) {
				text, href := lm[1], lm[2]
				rel, ok := scripturesRel(path.Join(dir, href))
				if !ok {
					sum.Skipped["target_outside_scriptures"]++
					continue
				}
				parts := strings.Split(strings.TrimSuffix(rel, ".md"), "/")
				r := XrefRow{SourceVolume: c.volume, SourceBook: c.book, SourceChapter: c.chapter, SourceVerse: srcVerse}
				switch {
				case xrefAids[parts[0]]:
					switch {
					case parts[0] == "jst" && len(parts) == 3 && isDigits(parts[2]):
						r.TargetVolume, r.TargetBook, r.ReferenceType = "jst", parts[1], "jst"
						r.TargetChapter, _ = strconv.Atoi(parts[2])
						add(r)
					case parts[0] != "jst" && len(parts) == 2:
						r.TargetVolume, r.TargetBook, r.ReferenceType = parts[0], parts[1], parts[0]
						add(r)
					default:
						sum.Skipped["aid_unrecognised"]++
					}
				case isVolume(parts[0]) && len(parts) == 3 && isDigits(parts[2]):
					tc, _ := strconv.Atoi(parts[2])
					r.TargetVolume, r.TargetBook, r.TargetChapter, r.ReferenceType = parts[0], parts[1], tc, "footnote"
					vs := versesFromText(text, tc)
					if len(vs) == 0 {
						// a chapter-level target must name a chapter with indexed verses
						if !chapKeys[chapKey{parts[0], parts[1], tc}] {
							sum.Skipped["unindexed_chapter_target"]++
							continue
						}
						add(r)
					}
					for _, v := range vs {
						rv := r
						vv := v
						rv.TargetVerse = &vv
						add(rv)
					}
				default:
					sum.Skipped["unindexed_target"]++
				}
			}
		}
	}

	// 3. checks against the key set, as the one-time loader made them.
	for _, r := range rows {
		if !keys[verseKey{r.SourceVolume, r.SourceBook, r.SourceChapter, r.SourceVerse}] {
			sum.UnmatchedSources++
		}
		if r.ReferenceType == "footnote" && r.TargetVerse != nil &&
			!keys[verseKey{r.TargetVolume, r.TargetBook, r.TargetChapter, *r.TargetVerse}] {
			sum.UnmatchedTargets++
		}
		sum.ByType[r.ReferenceType]++
	}
	sortXrefs(rows)
	sum.Rows = len(rows)
	sum.Duration = time.Since(start)
	return rows, sum, nil
}

// slashPath turns a stored or local path into forward slashes, whatever OS
// wrote it (a database indexed on Windows holds backslashes, which
// filepath.ToSlash leaves alone on Linux).
func slashPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// scripturesRel cleans p and returns its part under eng/scriptures/, matched
// at the start of the path or after a slash (a relative library root such as
// "." gives "eng/scriptures/..." with no leading slash).
func scripturesRel(p string) (string, bool) {
	tp := path.Clean(slashPath(p))
	const marker = "eng/scriptures/"
	if strings.HasPrefix(tp, marker) {
		return tp[len(marker):], true
	}
	if j := strings.Index(tp, "/"+marker); j >= 0 {
		return tp[j+1+len(marker):], true
	}
	return "", false
}

// xrefRebuildLock serialises cross_references rebuilds across concurrent
// index passes and admin calls (pg_advisory_xact_lock key).
const xrefRebuildLock = 0x67650001

func isVolume(s string) bool {
	for _, v := range xrefVolumes {
		if s == v {
			return true
		}
	}
	return false
}

// sortXrefs orders rows the way the loader's TSV was ordered (each field
// compared as text, NULL as the empty string), so a dump diffs line for line.
func sortXrefs(rows []XrefRow) {
	f := func(r XrefRow) []string {
		tv := ""
		if r.TargetVerse != nil {
			tv = strconv.Itoa(*r.TargetVerse)
		}
		return []string{r.SourceVolume, r.SourceBook, strconv.Itoa(r.SourceChapter), strconv.Itoa(r.SourceVerse),
			r.TargetVolume, r.TargetBook, strconv.Itoa(r.TargetChapter), tv, r.ReferenceType}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := f(rows[i]), f(rows[j])
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
}

// TSV renders a row in Postgres COPY text format (\N for NULL), the format
// of the loader's file.
func (r XrefRow) TSV() string {
	tv := `\N`
	if r.TargetVerse != nil {
		tv = strconv.Itoa(*r.TargetVerse)
	}
	return strings.Join([]string{r.SourceVolume, r.SourceBook, strconv.Itoa(r.SourceChapter), strconv.Itoa(r.SourceVerse),
		r.TargetVolume, r.TargetBook, strconv.Itoa(r.TargetChapter), tv, r.ReferenceType}, "\t")
}

// RebuildCrossReferences replaces the cross_references table with a fresh
// build from the library, in one transaction. It refuses to write when the
// build's own checks fail (an unmatched source or target means the parser and
// the indexer disagree on keys), leaving the table as it was.
func (idx *Indexer) RebuildCrossReferences(ctx context.Context) (*XrefSummary, error) {
	if idx.GospelLibraryRoot == "" {
		return nil, fmt.Errorf("no gospel library root configured")
	}
	rows, sum, err := BuildCrossReferences(idx.GospelLibraryRoot)
	if err != nil {
		return nil, err
	}
	if sum.UnmatchedSources != 0 || sum.UnmatchedTargets != 0 {
		return sum, fmt.Errorf("cross-reference build failed its checks: %d unmatched sources, %d unmatched targets; table left unchanged",
			sum.UnmatchedSources, sum.UnmatchedTargets)
	}
	if len(rows) == 0 {
		return sum, fmt.Errorf("cross-reference build produced no rows; table left unchanged")
	}
	tx, err := idx.DB.Pool.Begin(ctx)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback(ctx)
	// Two rebuilds at once would each DELETE only the rows they can see and
	// both COPY, doubling the table; the lock makes the second wait its turn.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(xrefRebuildLock)); err != nil {
		return sum, fmt.Errorf("lock cross_references rebuild: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cross_references`); err != nil {
		return sum, fmt.Errorf("clear cross_references: %w", err)
	}
	n, err := tx.CopyFrom(ctx, pgx.Identifier{"cross_references"},
		[]string{"source_volume", "source_book", "source_chapter", "source_verse",
			"target_volume", "target_book", "target_chapter", "target_verse", "reference_type"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			var tv any
			if r.TargetVerse != nil {
				tv = int32(*r.TargetVerse)
			}
			return []any{r.SourceVolume, r.SourceBook, int32(r.SourceChapter), int32(r.SourceVerse),
				r.TargetVolume, r.TargetBook, int32(r.TargetChapter), tv, r.ReferenceType}, nil
		}))
	if err != nil {
		return sum, fmt.Errorf("copy cross_references: %w", err)
	}
	if int(n) != len(rows) {
		return sum, fmt.Errorf("copied %d of %d cross_references rows", n, len(rows))
	}
	if err := tx.Commit(ctx); err != nil {
		return sum, err
	}
	return sum, nil
}

// maybeRebuildCrossReferences rebuilds the table after an index pass that
// touched any scripture chapter, or when the table is empty. A failure is
// logged and counted, never fatal to the index pass: the old rows stay.
func (idx *Indexer) maybeRebuildCrossReferences(ctx context.Context, r *Result) {
	if r.ChaptersIndexed == 0 {
		var n int64
		if err := idx.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_references`).Scan(&n); err != nil || n > 0 {
			return
		}
	}
	sum, err := idx.RebuildCrossReferences(ctx)
	if err != nil {
		log.Printf("cross-references: rebuild failed: %v", err)
		r.Errors++
		return
	}
	r.XrefRows = sum.Rows
	log.Printf("cross-references: rebuilt %d rows from %d chapters %v (skipped %v) in %s",
		sum.Rows, sum.Chapters, sum.ByType, sum.Skipped, sum.Duration)
}
