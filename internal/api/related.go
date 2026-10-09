package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/cpuchip/gospel-engine/internal/indexer"
)

// /api/related — the passages the library itself links to a passage, one or
// two hops out over graph_edges (footnotes, Topical Guide / Bible Dictionary /
// Guide entries, talk and manual citations). Seeds:
//
//	?reference=Ether 12:27     a verse, a verse range, or a chapter
//	?type=talks&id=123         every paragraph of a talk (also manuals, study_aids)
//
// hops=1|2 (default 1), kinds=verses,talks,manuals,aids (default all),
// limit (default 30, cap 200). Ranked by hop count, then by how many distinct
// links reach the passage. Each result says how it was reached.

const (
	relatedDefaultLimit = 30
	relatedMaxLimit     = 200
)

// relatedKinds maps a kinds= value to the key prefixes it admits.
var relatedKinds = map[string][]string{
	"verses":  {"v", "c"},
	"talks":   {"t"},
	"manuals": {"m"},
	"aids":    {"tg", "bd", "gs", "jst"},
}

// relatedVia says how a result was reached. Edge is the link type of the
// first hop and Direction is "out" when the seed side wrote the link (the
// seed's footnote, a seed talk's citation) and "in" when the other side did
// (a talk citing the seed, an entry listing it). Through names the passage in
// between on a two-hop result.
type relatedVia struct {
	Edge      string         `json:"edge"`
	Direction string         `json:"direction"`
	Through   *relatedResult `json:"through,omitempty"`
}

type relatedResult struct {
	Kind      string      `json:"kind"` // verse | chapter | talk | manual | study_aid
	Key       string      `json:"key"`
	Hops      int         `json:"hops,omitempty"`
	Links     int         `json:"links,omitempty"` // distinct links that reach it
	Via       *relatedVia `json:"via,omitempty"`
	ID        int64       `json:"id,omitempty"` // scriptures / talks / manuals / study_aids id
	Reference string      `json:"reference,omitempty"`
	Title     string      `json:"title,omitempty"`
	Speaker   string      `json:"speaker,omitempty"`
	Year      int         `json:"year,omitempty"`
	Month     string      `json:"month,omitempty"`
	Paragraph *int        `json:"paragraph,omitempty"`
	Text      string      `json:"text,omitempty"`
}

func (s *Server) handleRelated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hops := 1
	if h := q.Get("hops"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n < 1 || n > 2 {
			http.Error(w, "hops must be 1 or 2", http.StatusBadRequest)
			return
		}
		hops = n
	}
	limit := relatedDefaultLimit
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, relatedMaxLimit)
	}
	var prefixes []string
	if k := strings.TrimSpace(q.Get("kinds")); k != "" {
		for _, name := range strings.Split(k, ",") {
			p, ok := relatedKinds[strings.TrimSpace(name)]
			if !ok {
				http.Error(w, fmt.Sprintf("unknown kind %q (verses, talks, manuals, aids)", name), http.StatusBadRequest)
				return
			}
			prefixes = append(prefixes, p...)
		}
	} else {
		for _, p := range relatedKinds {
			prefixes = append(prefixes, p...)
		}
	}

	seeds, seedErr, status := s.relatedSeeds(r.Context(), q.Get("reference"), q.Get("type"), q.Get("id"))
	if seedErr != "" {
		http.Error(w, seedErr, status)
		return
	}
	if len(seeds) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"seeds": []string{}, "hops": hops, "count": 0, "results": []relatedResult{}})
		return
	}

	results, err := s.walkRelated(r.Context(), seeds, hops, prefixes, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("related failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"seeds":   seeds,
		"hops":    hops,
		"count":   len(results),
		"results": results,
		"source":  "links the library itself makes: chapter footnotes, Topical Guide, Bible Dictionary and Guide to the Scriptures entries, and the scripture citations in conference talks and manuals",
	})
}

// relatedSeeds turns the request into node keys. A non-empty error string is
// the message for status.
func (s *Server) relatedSeeds(ctx context.Context, ref, typ, idStr string) ([]string, string, int) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		p, ok := parseReference(ref)
		if !ok {
			return nil, fmt.Sprintf("could not parse reference %q (try '1 Nephi 3:7', 'D&C 93:24-30', or 'Mosiah 4')", ref), http.StatusBadRequest
		}
		lo, hi := p.Verse, p.EndVerse
		if hi == 0 {
			hi = lo
		}
		if lo == 0 { // a chapter: every verse
			lo, hi = 1, 1<<30
		}
		rows, err := s.DB.Pool.Query(ctx,
			`SELECT volume, book, chapter, verse FROM scriptures
			 WHERE book = $1 AND chapter = $2 AND verse BETWEEN $3 AND $4 ORDER BY verse`,
			p.Book, p.Chapter, lo, hi)
		if err != nil {
			return nil, err.Error(), http.StatusInternalServerError
		}
		defer rows.Close()
		var seeds []string
		var vol string
		for rows.Next() {
			var book string
			var ch, v int
			if err := rows.Scan(&vol, &book, &ch, &v); err != nil {
				return nil, err.Error(), http.StatusInternalServerError
			}
			seeds = append(seeds, indexer.VerseKey(vol, book, ch, v))
		}
		if len(seeds) == 0 {
			return nil, "not found", http.StatusNotFound
		}
		if p.Verse == 0 {
			seeds = append(seeds, indexer.ChapterKey(vol, p.Book, p.Chapter))
		}
		return seeds, "", 0
	}

	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if typ == "" || err != nil {
		return nil, "provide reference, or type (talks, manuals, study_aids) and id", http.StatusBadRequest
	}
	switch typ {
	case "talks", "manuals":
		var content string
		if err := s.DB.Pool.QueryRow(ctx, `SELECT content FROM `+typ+` WHERE id = $1`, id).Scan(&content); err != nil {
			return nil, "not found", http.StatusNotFound
		}
		key := indexer.TalkKey
		if typ == "manuals" {
			key = indexer.ManualKey
		}
		n := len(indexer.Paragraphs(content))
		seeds := make([]string, n)
		for i := range n {
			seeds[i] = key(id, i)
		}
		return seeds, "", 0
	case "study_aids":
		var aidType, slug string
		if err := s.DB.Pool.QueryRow(ctx, `SELECT aid_type, slug FROM study_aids WHERE id = $1`, id).Scan(&aidType, &slug); err != nil {
			return nil, "not found", http.StatusNotFound
		}
		return []string{indexer.AidKey(aidType, slug)}, "", 0
	}
	return nil, fmt.Sprintf("type %q has no links (talks, manuals, study_aids)", typ), http.StatusBadRequest
}

// walkRelated runs the walk and resolves the results for display.
func (s *Server) walkRelated(ctx context.Context, seeds []string, hops int, prefixes []string, limit int) ([]relatedResult, error) {
	// walk(key, depth, edge, rev, through): edge/rev are the FIRST hop's link
	// and direction, carried along; through is the depth-1 node on a depth-2
	// row. UNION removes duplicate rows, so links counts distinct routes.
	const q = `
WITH RECURSIVE walk(key, depth, edge, rev, through) AS (
    SELECT k, 0, ''::text, false, ''::text FROM unnest($1::text[]) k
  UNION
    SELECT e.dst, w.depth + 1,
           CASE WHEN w.depth = 0 THEN e.edge_type ELSE w.edge END,
           CASE WHEN w.depth = 0 THEN e.reverse ELSE w.rev END,
           CASE WHEN w.depth = 0 THEN '' ELSE w.key END
    FROM walk w JOIN graph_edges e ON e.src = w.key
    WHERE w.depth < $2
)
SELECT key, depth, edge, rev, through, links FROM (
    SELECT DISTINCT ON (key) key, depth, edge, rev, through,
           count(*) OVER (PARTITION BY key) AS links
    FROM walk
    WHERE depth > 0
      AND key <> ALL($1::text[])
      AND split_part(key, ':', 1) = ANY($3::text[])
    ORDER BY key, depth, edge, through
) r
ORDER BY depth, links DESC, key
LIMIT $4`
	rows, err := s.DB.Pool.Query(ctx, q, seeds, hops, prefixes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []relatedResult
	var throughKeys []string
	for rows.Next() {
		var (
			res     relatedResult
			edge    string
			rev     bool
			through string
		)
		if err := rows.Scan(&res.Key, &res.Hops, &edge, &rev, &through, &res.Links); err != nil {
			return nil, err
		}
		dir := "out"
		if rev {
			dir = "in"
		}
		res.Via = &relatedVia{Edge: edge, Direction: dir}
		if through != "" {
			res.Via.Through = &relatedResult{Key: through}
			throughKeys = append(throughKeys, through)
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(out)+len(throughKeys))
	for _, r := range out {
		keys = append(keys, r.Key)
	}
	keys = append(keys, throughKeys...)
	info, err := s.resolveNodes(ctx, keys)
	if err != nil {
		return nil, err
	}
	for i := range out {
		fill(&out[i], info[out[i].Key])
		if t := out[i].Via.Through; t != nil {
			fill(t, info[t.Key])
			t.Text = "" // the in-between passage is named, not quoted
		}
	}
	return out, nil
}

// fill copies resolved display fields onto r, keeping r's walk fields.
func fill(r *relatedResult, d relatedResult) {
	r.Kind, r.ID, r.Reference, r.Title, r.Speaker = d.Kind, d.ID, d.Reference, d.Title, d.Speaker
	r.Year, r.Month, r.Paragraph, r.Text = d.Year, d.Month, d.Paragraph, d.Text
}

// resolveNodes looks up display fields for node keys, a batched query per kind.
// A key whose row is missing still resolves to its kind and a readable label.
func (s *Server) resolveNodes(ctx context.Context, keys []string) (map[string]relatedResult, error) {
	out := map[string]relatedResult{}
	var vVol, vBook []string
	var vCh, vV []int32
	talkIDs, manualIDs := map[int64]bool{}, map[int64]bool{}
	var aidTypes, aidSlugs []string
	for _, k := range keys {
		if _, done := out[k]; done {
			continue
		}
		kind, rest, _ := strings.Cut(k, ":")
		switch kind {
		case "v":
			vol, book, ch, v, ok := parseVerseKey(rest)
			if !ok {
				continue
			}
			out[k] = relatedResult{Kind: "verse", Reference: fmt.Sprintf("%s %d:%d", indexer.BookDisplayName(book), ch, v)}
			vVol, vBook, vCh, vV = append(vVol, vol), append(vBook, book), append(vCh, int32(ch)), append(vV, int32(v))
		case "c":
			parts := strings.Split(rest, "/")
			if len(parts) == 3 {
				out[k] = relatedResult{Kind: "chapter", Reference: indexer.BookDisplayName(parts[1]) + " " + parts[2]}
			}
		case "t", "m":
			id, p, ok := parseParagraphKey(rest)
			if !ok {
				continue
			}
			pp := p
			kindName := "talk"
			if kind == "m" {
				kindName = "manual"
				manualIDs[id] = true
			} else {
				talkIDs[id] = true
			}
			out[k] = relatedResult{Kind: kindName, ID: id, Paragraph: &pp}
		case "tg", "bd", "gs", "jst":
			out[k] = relatedResult{Kind: "study_aid", Title: strings.ToUpper(kind) + " " + rest}
			aidTypes, aidSlugs = append(aidTypes, kind), append(aidSlugs, rest)
		}
	}

	if len(vVol) > 0 {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT s.id, s.volume, s.book, s.chapter, s.verse, s.reference, s.text
			FROM unnest($1::text[], $2::text[], $3::int[], $4::int[]) AS k(volume, book, chapter, verse)
			JOIN scriptures s USING (volume, book, chapter, verse)`, vVol, vBook, vCh, vV)
		if err != nil {
			return nil, fmt.Errorf("resolve verses: %w", err)
		}
		for rows.Next() {
			var d relatedResult
			var vol, book string
			var ch, v int
			if err := rows.Scan(&d.ID, &vol, &book, &ch, &v, &d.Reference, &d.Text); err != nil {
				rows.Close()
				return nil, err
			}
			d.Kind = "verse"
			out[indexer.VerseKey(vol, book, ch, v)] = d
		}
		rows.Close()
	}

	for table, ids := range map[string]map[int64]bool{"talks": talkIDs, "manuals": manualIDs} {
		if len(ids) == 0 {
			continue
		}
		list := make([]int64, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sel := `SELECT id, title, speaker, year, month, content FROM talks WHERE id = ANY($1)`
		if table == "manuals" {
			sel = `SELECT id, title, '', 0, '', content FROM manuals WHERE id = ANY($1)`
		}
		rows, err := s.DB.Pool.Query(ctx, sel, list)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", table, err)
		}
		type row struct {
			title, speaker, month string
			year                  int
			paras                 []string
		}
		got := map[int64]row{}
		for rows.Next() {
			var id int64
			var rw row
			var content string
			if err := rows.Scan(&id, &rw.title, &rw.speaker, &rw.year, &rw.month, &content); err != nil {
				rows.Close()
				return nil, err
			}
			rw.paras = indexer.Paragraphs(content)
			got[id] = rw
		}
		rows.Close()
		for k, d := range out {
			if (table == "talks" && d.Kind != "talk") || (table == "manuals" && d.Kind != "manual") {
				continue
			}
			rw, ok := got[d.ID]
			if !ok {
				continue
			}
			d.Title, d.Speaker, d.Year, d.Month = rw.title, rw.speaker, rw.year, rw.month
			if d.Paragraph != nil && *d.Paragraph < len(rw.paras) {
				d.Text = rw.paras[*d.Paragraph]
			}
			out[k] = d
		}
	}

	if len(aidTypes) > 0 {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT a.id, a.aid_type, a.slug, a.title
			FROM unnest($1::text[], $2::text[]) AS k(aid_type, slug)
			JOIN study_aids a USING (aid_type, slug)`, aidTypes, aidSlugs)
		if err != nil {
			return nil, fmt.Errorf("resolve study aids: %w", err)
		}
		for rows.Next() {
			var d relatedResult
			var typ, slug, title string
			if err := rows.Scan(&d.ID, &typ, &slug, &title); err != nil {
				rows.Close()
				return nil, err
			}
			d.Kind = "study_aid"
			d.Title = aidPrefix[typ] + title
			out[indexer.AidKey(typ, slug)] = d
		}
		rows.Close()
	}
	return out, nil
}

// parseVerseKey splits "bofm/ether/12:27".
func parseVerseKey(rest string) (vol, book string, ch, v int, ok bool) {
	path, verse, found := strings.Cut(rest, ":")
	parts := strings.Split(path, "/")
	if !found || len(parts) != 3 {
		return "", "", 0, 0, false
	}
	ch, err1 := strconv.Atoi(parts[2])
	v, err2 := strconv.Atoi(verse)
	if err1 != nil || err2 != nil {
		return "", "", 0, 0, false
	}
	return parts[0], parts[1], ch, v, true
}

// parseParagraphKey splits "123#4".
func parseParagraphKey(rest string) (id int64, p int, ok bool) {
	a, b, found := strings.Cut(rest, "#")
	if !found {
		return 0, 0, false
	}
	id, err1 := strconv.ParseInt(a, 10, 64)
	p, err2 := strconv.Atoi(b)
	return id, p, err1 == nil && err2 == nil
}
