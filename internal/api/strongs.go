package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Strong's concordance (v3): the lexicon, a reverse search by English word,
// and the KJV's word-by-word tagging. Tables from migration 005, filled by
// scripts/strongs. Every response carries the attribution.

const strongsAttribution = "Strong's Concordance (James Strong, 1890; public domain) via OpenScriptures (CC BY 4.0 / CC BY-SA). " +
	"Modern glosses: STEPBible TBESH/TBESG, Tyndale House (CC BY 4.0). KJV with Strong's numbers: CrossWire Bible Society's " +
	"KJV module (GPL; OT Strong's from The Bible Foundation; NT from the KJV2003 Project). Glosses are a starting point " +
	"for study: read the fuller definition and the verse in context."

type strongsEntry struct {
	Number     string `json:"number"`
	Lang       string `json:"lang"`
	Lemma      string `json:"lemma"`
	Translit   string `json:"translit"`
	Pron       string `json:"pron,omitempty"`
	StrongsDef string `json:"strongs_def,omitempty"`
	KJVDef     string `json:"kjv_def,omitempty"`
	Derivation string `json:"derivation,omitempty"`
	StepGloss  string `json:"step_gloss,omitempty"`
	StepDef    string `json:"step_def,omitempty"`
}

// strongsWord is one tagged word or phrase of a KJV verse.
type strongsWord struct {
	Word    string         `json:"word"`
	Numbers []string       `json:"numbers"`
	Lexicon []strongsBrief `json:"lexicon,omitempty"`
}

type strongsBrief struct {
	Number   string `json:"number"`
	Lemma    string `json:"lemma"`
	Translit string `json:"translit"`
	Gloss    string `json:"gloss"`
}

var strongsNumRe = regexp.MustCompile(`^([GgHh])0*(\d{1,5})[a-zA-Z]?$`)

// canonStrongs turns "g0026", "H07225" or "G26" into "G26"; "" when it is not a number.
func canonStrongs(s string) string {
	m := strongsNumRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[2] == "0" {
		return ""
	}
	return strings.ToUpper(m[1]) + m[2]
}

// strongsLoaded answers 503 (and returns false) while the tables are empty:
// migration 005 creates them at boot, and scripts/strongs fills them after.
func (s *Server) strongsLoaded(w http.ResponseWriter, r *http.Request) bool {
	var ok bool
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM kjv_words) AND EXISTS (SELECT 1 FROM strongs_lexicon)`).Scan(&ok); err != nil {
		log.Printf("strongs: loaded check: %v", err)
		http.Error(w, "Strong's lookup failed", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "Strong's data has not been loaded on this server yet (scripts/strongs)", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// GET /api/strongs/define?number=G26
func (s *Server) handleStrongsDefine(w http.ResponseWriter, r *http.Request) {
	n := canonStrongs(r.URL.Query().Get("number"))
	if n == "" {
		http.Error(w, "give a Strong's number like H7225 (Hebrew) or G26 (Greek)", http.StatusBadRequest)
		return
	}
	if !s.strongsLoaded(w, r) {
		return
	}
	var e strongsEntry
	err := s.DB.Pool.QueryRow(r.Context(), `
		SELECT number, lang, lemma, translit, pron, strongs_def, kjv_def, derivation, step_gloss, step_def
		FROM strongs_lexicon WHERE number = $1`, n).Scan(
		&e.Number, &e.Lang, &e.Lemma, &e.Translit, &e.Pron, &e.StrongsDef, &e.KJVDef, &e.Derivation, &e.StepGloss, &e.StepDef)
	if err != nil {
		http.Error(w, fmt.Sprintf("no Strong's entry %s", n), http.StatusNotFound)
		return
	}
	var verses int
	_ = s.DB.Pool.QueryRow(r.Context(),
		`SELECT count(DISTINCT (book, chapter, verse)) FROM kjv_words WHERE numbers @> ARRAY[$1]::text[]`, n).Scan(&verses)
	writeJSON(w, http.StatusOK, map[string]any{"entry": e, "kjv_verses": verses, "source": strongsAttribution})
}

var strongsWordRe = regexp.MustCompile(`^[\p{L}][\p{L} '\-]{0,40}$`)

// GET /api/strongs/search?word=love&limit=20 — reverse lookup by English word,
// gloss or transliteration: exact gloss or transliteration first, then a whole
// word in the KJV usage, then the gloss containing it, then the definition.
func (s *Server) handleStrongsSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("word")))
	if !strongsWordRe.MatchString(q) {
		http.Error(w, "give an English word or a transliteration (letters, spaces, apostrophes, hyphens)", http.StatusBadRequest)
		return
	}
	limit := 20
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = min(l, 100)
	}
	if !s.strongsLoaded(w, r) {
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT number, lang, lemma, translit, step_gloss, kjv_def, score FROM (
		  SELECT *, CASE
		    WHEN lower(step_gloss) = $1 OR lower(translit) = $1 THEN 100
		    WHEN lower(kjv_def) ~ ('\m' || $1 || '\M') THEN 60
		    WHEN strpos(lower(step_gloss), $1) > 0 THEN 40
		    WHEN lower(strongs_def) ~ ('\m' || $1 || '\M') THEN 20
		  END AS score
		  FROM strongs_lexicon
		) x
		WHERE score IS NOT NULL
		ORDER BY score DESC, lang DESC, (substring(number FROM 2))::int
		LIMIT $2`, q, limit)
	if err != nil {
		log.Printf("strongs search: %v", err)
		http.Error(w, "Strong's search failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type hit struct {
		strongsEntry
		Score int `json:"score"`
	}
	var out []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.Number, &h.Lang, &h.Lemma, &h.Translit, &h.StepGloss, &h.KJVDef, &h.Score); err != nil {
			log.Printf("strongs search: %v", err)
			http.Error(w, "Strong's search failed", http.StatusInternalServerError)
			return
		}
		out = append(out, h)
	}
	if out == nil {
		out = []hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"word": q, "count": len(out), "results": out, "source": strongsAttribution})
}

// GET /api/strongs/verse?reference=John+3:16 (a verse or a range of KJV verses)
func (s *Server) handleStrongsVerse(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("reference"))
	p, ok := parseReference(ref)
	if !ok || p.Verse == 0 {
		http.Error(w, fmt.Sprintf("give a KJV verse or range, e.g. 'John 3:16' (got %q)", ref), http.StatusBadRequest)
		return
	}
	end := max(p.EndVerse, p.Verse)
	if end-p.Verse+1 > maxRangeVerses {
		http.Error(w, fmt.Sprintf("verse range exceeds limit of %d verses", maxRangeVerses), http.StatusBadRequest)
		return
	}
	if !s.strongsLoaded(w, r) {
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(),
		`SELECT id, volume, book, chapter, verse, reference, text, file_path FROM scriptures
		 WHERE book = $1 AND chapter = $2 AND verse BETWEEN $3 AND $4 ORDER BY verse`, p.Book, p.Chapter, p.Verse, end)
	if err != nil {
		log.Printf("strongs verse: %v", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	var verses []verseRow
	for rows.Next() {
		var v verseRow
		if err := rows.Scan(&v.ID, &v.Volume, &v.Book, &v.Chapter, &v.Verse, &v.Reference, &v.Text, &v.FilePath); err != nil {
			rows.Close()
			http.Error(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		verses = append(verses, v)
	}
	rows.Close()
	if len(verses) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if verses[0].Volume != "ot" && verses[0].Volume != "nt" {
		http.Error(w, "Strong's numbers cover the KJV Bible (Old and New Testaments) only", http.StatusBadRequest)
		return
	}
	tagged, err := s.fetchStrongs(r.Context(), verses)
	if err != nil {
		log.Printf("strongs verse: %v", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	type out struct {
		Reference string        `json:"reference"`
		Text      string        `json:"text"`
		Words     []strongsWord `json:"words"`
	}
	res := make([]out, 0, len(verses))
	for _, v := range verses {
		res = append(res, out{Reference: v.Reference, Text: v.Text, Words: tagged[v.Verse]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"verses": res, "source": strongsAttribution})
}

// fetchStrongs returns each verse's tagged words (by verse number), with a
// brief lexicon entry per number. Verses outside the KJV Bible get nothing.
// All verses must share book and chapter, as every caller's range does.
func (s *Server) fetchStrongs(ctx context.Context, verses []verseRow) (map[int][]strongsWord, error) {
	out := map[int][]strongsWord{}
	if len(verses) == 0 || (verses[0].Volume != "ot" && verses[0].Volume != "nt") {
		return out, nil
	}
	lo, hi := verses[0].Verse, verses[len(verses)-1].Verse
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT w.verse, w.word, w.numbers,
		       coalesce(array_agg(l.number ORDER BY array_position(w.numbers, l.number)) FILTER (WHERE l.number IS NOT NULL), '{}'),
		       coalesce(array_agg(l.lemma ORDER BY array_position(w.numbers, l.number)) FILTER (WHERE l.number IS NOT NULL), '{}'),
		       coalesce(array_agg(l.translit ORDER BY array_position(w.numbers, l.number)) FILTER (WHERE l.number IS NOT NULL), '{}'),
		       coalesce(array_agg(coalesce(nullif(l.step_gloss, ''), l.kjv_def) ORDER BY array_position(w.numbers, l.number)) FILTER (WHERE l.number IS NOT NULL), '{}')
		FROM kjv_words w
		LEFT JOIN strongs_lexicon l ON l.number = ANY (w.numbers)
		WHERE w.book = $1 AND w.chapter = $2 AND w.verse BETWEEN $3 AND $4
		GROUP BY w.verse, w.position, w.word, w.numbers
		ORDER BY w.verse, w.position`, verses[0].Book, verses[0].Chapter, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var sw strongsWord
		var nums, lemmas, translits, glosses []string
		if err := rows.Scan(&v, &sw.Word, &sw.Numbers, &nums, &lemmas, &translits, &glosses); err != nil {
			return nil, err
		}
		for i := range nums {
			sw.Lexicon = append(sw.Lexicon, strongsBrief{Number: nums[i], Lemma: lemmas[i], Translit: translits[i], Gloss: glosses[i]})
		}
		out[v] = append(out[v], sw)
	}
	return out, rows.Err()
}

// addStrongs adds "strongs" (verse number -> tagged words) and its attribution
// to a gospel_get response for KJV verses; other volumes get an empty map and a
// note. It writes the error itself and returns false when the lookup fails.
func (s *Server) addStrongs(w http.ResponseWriter, r *http.Request, resp map[string]any, verses []verseRow) bool {
	tagged, err := s.fetchStrongs(r.Context(), verses)
	if err != nil {
		log.Printf("strongs for get: %v", err)
		http.Error(w, "strongs lookup failed", http.StatusInternalServerError)
		return false
	}
	resp["strongs"] = tagged
	if len(verses) > 0 && verses[0].Volume != "ot" && verses[0].Volume != "nt" {
		resp["strongs_note"] = "Strong's numbers cover the KJV Bible (Old and New Testaments) only"
	} else {
		resp["strongs_source"] = strongsAttribution
	}
	return true
}
