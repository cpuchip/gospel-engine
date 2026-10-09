package indexer

import (
	"context"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The explicit-link graph (v3): every link the library makes, as edges between
// text keys (see migration 004_graph_edges.sql for the key forms). Built from
// rows the index already holds, so it needs no second walk of the files:
//
//   - footnote:     cross_references, as rebuilt from the chapter footnotes
//   - tg/bd/gs_ref: the links in a study-aid entry's stored markdown
//   - talk_cites:   the links in each talk paragraph (talks.content)
//   - manual_cites: the links in each manual paragraph (manuals.content)
//
// A paragraph's index is the engine's own: the position among the paragraphs
// splitParagraphs keeps, which is also the N of the embeddings layer
// "paragraph_N", so a talk node names the same passage search returns.

// GraphEdge is one directed link as the library wrote it.
type GraphEdge struct {
	Src, Dst, Type string
}

// GraphSummary reports what a graph build found.
type GraphSummary struct {
	Edges    int            `json:"edges"` // distinct directed links (the table holds twice this, with mirrors)
	ByType   map[string]int `json:"by_type"`
	Duration time.Duration  `json:"-"`
}

// VerseKey, ChapterKey, AidKey, TalkKey and ManualKey build node keys.
func VerseKey(volume, book string, chapter, verse int) string {
	return fmt.Sprintf("v:%s/%s/%d:%d", volume, book, chapter, verse)
}
func ChapterKey(volume, book string, chapter int) string {
	return fmt.Sprintf("c:%s/%s/%d", volume, book, chapter)
}
func AidKey(aidType, slug string) string       { return aidType + ":" + slug }
func TalkKey(id int64, paragraph int) string   { return fmt.Sprintf("t:%d#%d", id, paragraph) }
func ManualKey(id int64, paragraph int) string { return fmt.Sprintf("m:%d#%d", id, paragraph) }

// xrefTargetKey is the node a cross_references row points at.
func xrefTargetKey(r XrefRow) string {
	switch r.ReferenceType {
	case "footnote":
		if r.TargetVerse != nil {
			return VerseKey(r.TargetVolume, r.TargetBook, r.TargetChapter, *r.TargetVerse)
		}
		return ChapterKey(r.TargetVolume, r.TargetBook, r.TargetChapter)
	case "jst":
		return AidKey("jst", fmt.Sprintf("%s/%d", r.TargetBook, r.TargetChapter))
	}
	return AidKey(r.ReferenceType, r.TargetBook)
}

// LinkTargets resolves one markdown link found in the file at srcPath to the
// node keys it names: verses (by the footnote rules of versesFromText), a
// chapter when the text names no verse, or a study-aid entry. Links outside
// the scriptures, and scripture files that are not numbered chapters, name
// nothing.
func LinkTargets(srcPath, text, href string) []string {
	rel, ok := scripturesRel(path.Join(path.Dir(slashPath(srcPath)), href))
	if !ok {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(rel, ".md"), "/")
	switch {
	case parts[0] == "jst":
		if len(parts) == 3 && isDigits(parts[2]) {
			return []string{AidKey("jst", parts[1]+"/"+parts[2])}
		}
	case xrefAids[parts[0]]:
		if len(parts) == 2 {
			return []string{AidKey(parts[0], parts[1])}
		}
	case isVolume(parts[0]) && len(parts) == 3 && isDigits(parts[2]):
		ch, _ := strconv.Atoi(parts[2])
		vs := versesFromText(text, ch)
		if len(vs) == 0 {
			return []string{ChapterKey(parts[0], parts[1], ch)}
		}
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = VerseKey(parts[0], parts[1], ch, v)
		}
		return out
	}
	return nil
}

// Paragraphs returns the engine's paragraphs of a talk or manual body: the
// texts the embeddings table holds as "paragraph", "paragraph_1", ... .
func Paragraphs(content string) []string { return splitParagraphs(content) }

// paragraphLinks calls fn for every link in every paragraph splitParagraphs
// keeps, with that paragraph's index. It walks the RAW paragraph (links
// intact) but counts exactly as splitParagraphs does.
func paragraphLinks(content string, fn func(p int, text, href string)) {
	p := 0
	for _, raw := range strings.Split(content, "\n\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" || cleanInlineMarkdown(raw) == "" {
			continue
		}
		for _, m := range xrefLinkRe.FindAllStringSubmatch(raw, -1) {
			fn(p, m[1], m[2])
		}
		p++
	}
}

// BuildGraphEdges reads cross_references, study_aids, talks and manuals and
// returns the distinct directed edges.
func (idx *Indexer) BuildGraphEdges(ctx context.Context) ([]GraphEdge, *GraphSummary, error) {
	start := time.Now()
	sum := &GraphSummary{ByType: map[string]int{}}
	seen := map[GraphEdge]bool{}
	var edges []GraphEdge
	add := func(src, dst, typ string) {
		e := GraphEdge{src, dst, typ}
		if src == dst || seen[e] {
			return
		}
		seen[e] = true
		edges = append(edges, e)
		sum.ByType[typ]++
	}

	// 1. footnotes, from cross_references
	rows, err := idx.DB.Pool.Query(ctx, `
		SELECT source_volume, source_book, source_chapter, source_verse,
		       target_volume, target_book, target_chapter, target_verse, coalesce(reference_type, '')
		FROM cross_references`)
	if err != nil {
		return nil, nil, fmt.Errorf("read cross_references: %w", err)
	}
	for rows.Next() {
		var r XrefRow
		var tv *int32
		if err := rows.Scan(&r.SourceVolume, &r.SourceBook, &r.SourceChapter, &r.SourceVerse,
			&r.TargetVolume, &r.TargetBook, &r.TargetChapter, &tv, &r.ReferenceType); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if tv != nil {
			v := int(*tv)
			r.TargetVerse = &v
		}
		add(VerseKey(r.SourceVolume, r.SourceBook, r.SourceChapter, r.SourceVerse), xrefTargetKey(r), "footnote")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// 2. study-aid entries -> the verses they list (JST chapters carry their
	// own text, not a verse list, so they stay targets only)
	rows, err = idx.DB.Pool.Query(ctx, `SELECT aid_type, slug, content, file_path FROM study_aids WHERE aid_type IN ('tg','bd','gs')`)
	if err != nil {
		return nil, nil, fmt.Errorf("read study_aids: %w", err)
	}
	for rows.Next() {
		var typ, slug, content, fp string
		if err := rows.Scan(&typ, &slug, &content, &fp); err != nil {
			rows.Close()
			return nil, nil, err
		}
		src := AidKey(typ, slug)
		for _, m := range xrefLinkRe.FindAllStringSubmatch(content, -1) {
			for _, dst := range LinkTargets(fp, m[1], m[2]) {
				if dst[0] == 'v' || dst[0] == 'c' {
					add(src, dst, typ+"_ref")
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// 3. talk and manual paragraphs -> what they cite
	for _, t := range []struct {
		table, typ string
		key        func(int64, int) string
	}{{"talks", "talk_cites", TalkKey}, {"manuals", "manual_cites", ManualKey}} {
		rows, err = idx.DB.Pool.Query(ctx, `SELECT id, content, file_path FROM `+t.table)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", t.table, err)
		}
		for rows.Next() {
			var id int64
			var content, fp string
			if err := rows.Scan(&id, &content, &fp); err != nil {
				rows.Close()
				return nil, nil, err
			}
			paragraphLinks(content, func(p int, text, href string) {
				for _, dst := range LinkTargets(fp, text, href) {
					add(t.key(id, p), dst, t.typ)
				}
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}

	sum.Edges = len(edges)
	sum.Duration = time.Since(start)
	return edges, sum, nil
}

// RebuildGraph replaces graph_edges with a fresh build, each link stored with
// its mirror, in one transaction.
func (idx *Indexer) RebuildGraph(ctx context.Context) (*GraphSummary, error) {
	edges, sum, err := idx.BuildGraphEdges(ctx)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return sum, fmt.Errorf("graph build produced no edges; table left unchanged")
	}
	tx, err := idx.DB.Pool.Begin(ctx)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(graphRebuildLock)); err != nil {
		return sum, fmt.Errorf("lock graph rebuild: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM graph_edges`); err != nil {
		return sum, fmt.Errorf("clear graph_edges: %w", err)
	}
	n, err := tx.CopyFrom(ctx, pgx.Identifier{"graph_edges"}, []string{"src", "dst", "edge_type", "reverse"},
		pgx.CopyFromSlice(2*len(edges), func(i int) ([]any, error) {
			e := edges[i/2]
			if i%2 == 1 {
				return []any{e.Dst, e.Src, e.Type, true}, nil
			}
			return []any{e.Src, e.Dst, e.Type, false}, nil
		}))
	if err != nil {
		return sum, fmt.Errorf("copy graph_edges: %w", err)
	}
	if int(n) != 2*len(edges) {
		return sum, fmt.Errorf("copied %d of %d graph_edges rows", n, 2*len(edges))
	}
	if _, err := tx.Exec(ctx, `ANALYZE graph_edges`); err != nil {
		return sum, err
	}
	return sum, tx.Commit(ctx)
}

// graphRebuildLock serialises graph_edges rebuilds (pg_advisory_xact_lock key).
const graphRebuildLock = 0x67650002

// maybeRebuildGraph rebuilds after a pass that indexed anything the graph
// reads or rebuilt cross_references, or when the table is empty. Failures are
// logged, never fatal.
func (idx *Indexer) maybeRebuildGraph(ctx context.Context, r *Result) {
	if r.ChaptersIndexed+r.TalksIndexed+r.ManualsIndexed+r.StudyAidsIndexed+r.XrefRows == 0 {
		var n int64
		if err := idx.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM graph_edges`).Scan(&n); err != nil || n > 0 {
			return
		}
	}
	sum, err := idx.RebuildGraph(ctx)
	if err != nil {
		log.Printf("graph: rebuild failed: %v", err)
		r.Errors++
		return
	}
	r.GraphEdges = sum.Edges
	log.Printf("graph: rebuilt %d links %v in %s", sum.Edges, sum.ByType, sum.Duration)
}
