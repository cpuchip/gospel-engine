// Package search implements keyword (FTS), semantic (vector), and hybrid
// search across all gospel content tables.
package search

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/embed"
	"github.com/pgvector/pgvector-go"
)

// Mode is the search mode requested by the caller.
type Mode string

const (
	ModeKeyword  Mode = "keyword"
	ModeSemantic Mode = "semantic"
	ModeHybrid   Mode = "hybrid" // RRF merge of keyword + semantic
)

// Source narrows results to a content type. Empty = all.
type Source string

const (
	SourceScriptures Source = "scriptures"
	SourceTalks      Source = "talks"
	SourceManuals    Source = "manuals"
	SourceBooks      Source = "books"
	SourceStudyAids  Source = "study_aids"
)

// Result is a single search hit.
type Result struct {
	SourceType string  `json:"source_type"`
	SourceID   int64   `json:"source_id"`
	Title      string  `json:"title,omitempty"`
	Reference  string  `json:"reference,omitempty"`
	Snippet    string  `json:"snippet"`
	FilePath   string  `json:"file_path,omitempty"`
	WebURL     string  `json:"web_url,omitempty"`
	Score      float64 `json:"score"`
	Speaker    string  `json:"speaker,omitempty"`
	Year       int     `json:"year,omitempty"`
	Month      string  `json:"month,omitempty"`
	AidType    string  `json:"aid_type,omitempty"`
	Slug       string  `json:"slug,omitempty"`
}

// Searcher orchestrates keyword + semantic search.
type Searcher struct {
	DB *db.DB

	// embed is the embeddings client, retained even while semantic search is
	// gated off so the gate below can re-probe the backend and recover without a
	// process restart. Nil only when no embedding backend is configured at all.
	embed *embed.Client

	// Semantic gate. embedEnabled latches true once the backend answers a probe;
	// from then on semantic search runs directly. While it is false, a semantic
	// or hybrid request re-probes the backend at most once per reprobeCooldown and
	// enables the gate on success — so a backend that was down at boot recovers on
	// the query cadence instead of needing a restart. Guarded by embedMu because
	// /api/search is served concurrently.
	embedMu         sync.Mutex
	embedEnabled    bool
	lastProbe       time.Time
	reprobeCooldown time.Duration

	// LinkMode controls how a result's source is referenced: "web" (canonical
	// churchofjesuschrist.org URL only), "fs" (file_path only), or "both"
	// (default — both fields). LibraryPath is kept for future path-based needs.
	LinkMode    string
	LibraryPath string
}

// NewSearcher constructs a Searcher. e is the embeddings client (nil only when
// no embedding backend is configured). embedEnabled is the caller's startup
// probe result: true means semantic search is live immediately, false means it
// is gated off until a re-probe succeeds. reprobeCooldown is the minimum
// interval between recovery re-probes while the gate is off (<=0 defaults to
// 60s). linkMode is "web" | "fs" | "both" ("" = both).
func NewSearcher(d *db.DB, e *embed.Client, embedEnabled bool, reprobeCooldown time.Duration, linkMode, libraryPath string) *Searcher {
	if reprobeCooldown <= 0 {
		reprobeCooldown = 60 * time.Second
	}
	return &Searcher{
		DB:              d,
		embed:           e,
		embedEnabled:    embedEnabled,
		reprobeCooldown: reprobeCooldown,
		LinkMode:        linkMode,
		LibraryPath:     libraryPath,
	}
}

// SemanticEnabled reports whether semantic search is currently live. Thread-safe.
// /api/health reads this, so the health endpoint reflects the live gate state:
// enabled flips true after a recovery re-probe, with no restart.
func (s *Searcher) SemanticEnabled() bool {
	s.embedMu.Lock()
	defer s.embedMu.Unlock()
	return s.embedEnabled
}

// semanticClient returns the embeddings client to use for a semantic query, or
// nil when semantic search is unavailable. When the gate is off it lazily
// re-probes the backend at most once per reprobeCooldown; a successful probe
// latches the gate on so every later query uses semantic directly. Thread-safe:
// under a burst of concurrent requests only one probe fires per cooldown, and
// the network round-trip happens outside the lock so it never blocks other
// searches (they observe the cooldown and skip).
func (s *Searcher) semanticClient(ctx context.Context) *embed.Client {
	s.embedMu.Lock()
	if s.embedEnabled {
		c := s.embed
		s.embedMu.Unlock()
		return c
	}
	if s.embed == nil {
		s.embedMu.Unlock()
		return nil
	}
	if !s.lastProbe.IsZero() && time.Since(s.lastProbe) < s.reprobeCooldown {
		s.embedMu.Unlock()
		return nil // still cooling down since the last failed probe
	}
	s.lastProbe = time.Now() // claim this cooldown window before releasing the lock
	client := s.embed
	s.embedMu.Unlock()

	if err := client.Ping(ctx); err != nil {
		return nil // backend still down; next request retries after the cooldown
	}
	s.embedMu.Lock()
	alreadyOn := s.embedEnabled
	s.embedEnabled = true
	s.embedMu.Unlock()
	if !alreadyOn {
		log.Printf("semantic search re-enabled: embedding backend recovered")
	}
	return client
}

// Options are the search parameters from the API caller.
type Options struct {
	Query   string
	Mode    Mode
	Sources []Source
	Limit   int
}

// Search runs the requested search, then decorates each result's source links
// according to LinkMode (web URL / file path / both).
func (s *Searcher) Search(ctx context.Context, opt Options) ([]Result, error) {
	res, err := s.searchRaw(ctx, opt)
	if err != nil {
		return nil, err
	}
	return s.decorate(res), nil
}

// decorate applies LinkMode to each result's source reference.
func (s *Searcher) decorate(res []Result) []Result {
	if s.LinkMode == "fs" {
		return res // file_path only — no web URL
	}
	for i := range res {
		if u := webURLFromFilePath(res[i].FilePath); u != "" {
			res[i].WebURL = u
		}
		if s.LinkMode == "web" {
			res[i].FilePath = "" // surface only the canonical web URL
		}
	}
	return res
}

// webURLFromFilePath maps a gospel-library markdown path to its canonical
// churchofjesuschrist.org study URL: <lang>/<study-path>.md →
// .../study/<study-path>?lang=<lang> (e.g. eng/scriptures/bofm/mosiah/2.md →
// https://www.churchofjesuschrist.org/study/scriptures/bofm/mosiah/2?lang=eng).
// Returns "" for paths outside gospel-library (e.g. books), which have no
// canonical web home — works whether the path is absolute or relative.
func webURLFromFilePath(fp string) string {
	// Books (and other non-gospel-library sources) have no canonical web home.
	if fp == "" || strings.Contains(fp, "/books/") || strings.HasPrefix(fp, "books/") {
		return ""
	}
	// Reduce to "<lang>/<study-path>.md", whether the path was absolute
	// (…/gospel-library/eng/…) or already relative (eng/…).
	const marker = "gospel-library/"
	if i := strings.Index(fp, marker); i >= 0 {
		fp = fp[i+len(marker):]
	} else {
		fp = strings.TrimPrefix(fp, "/")
	}
	p := strings.TrimSuffix(fp, ".md")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "https://www.churchofjesuschrist.org/study/" + parts[1] + "?lang=" + parts[0]
}

// searchRaw runs the requested search and returns merged, sorted results.
func (s *Searcher) searchRaw(ctx context.Context, opt Options) ([]Result, error) {
	if opt.Limit <= 0 || opt.Limit > 100 {
		opt.Limit = 20
	}
	if opt.Mode == "" {
		opt.Mode = ModeHybrid
	}
	if len(opt.Sources) == 0 {
		opt.Sources = []Source{SourceScriptures, SourceTalks, SourceManuals, SourceBooks, SourceStudyAids}
	}

	switch opt.Mode {
	case ModeKeyword:
		return s.keyword(ctx, opt)
	case ModeSemantic:
		client := s.semanticClient(ctx)
		if client == nil {
			return nil, fmt.Errorf("semantic search disabled (embedding backend unavailable)")
		}
		return s.semantic(ctx, client, opt)
	case ModeHybrid:
		kw, err := s.keyword(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("keyword: %w", err)
		}
		client := s.semanticClient(ctx)
		if client == nil {
			return kw, nil
		}
		sem, err := s.semantic(ctx, client, opt)
		if err != nil {
			// Fall back to keyword-only on semantic failure.
			return kw, nil
		}
		return rrfMerge(kw, sem, opt.Limit), nil
	default:
		return nil, fmt.Errorf("unknown search mode: %s", opt.Mode)
	}
}

// keyword runs a parallel-style FTS query across the requested sources.
func (s *Searcher) keyword(ctx context.Context, opt Options) ([]Result, error) {
	wantSet := sourceSet(opt.Sources)
	var all []Result

	if wantSet[SourceScriptures] {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT id, reference, text, file_path,
			       ts_rank(tsv, plainto_tsquery('english', $1)) AS rank
			FROM scriptures
			WHERE tsv @@ plainto_tsquery('english', $1)
			ORDER BY rank DESC
			LIMIT $2
		`, opt.Query, opt.Limit)
		if err != nil {
			return nil, fmt.Errorf("scriptures fts: %w", err)
		}
		for rows.Next() {
			var r Result
			r.SourceType = "scriptures"
			if err := rows.Scan(&r.SourceID, &r.Reference, &r.Snippet, &r.FilePath, &r.Score); err != nil {
				rows.Close()
				return nil, err
			}
			all = append(all, r)
		}
		rows.Close()
	}

	if wantSet[SourceTalks] {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT id, title, speaker, year, month, file_path, content,
			       ts_rank(tsv, plainto_tsquery('english', $1)) AS rank
			FROM talks
			WHERE tsv @@ plainto_tsquery('english', $1)
			ORDER BY rank DESC
			LIMIT $2
		`, opt.Query, opt.Limit)
		if err != nil {
			return nil, fmt.Errorf("talks fts: %w", err)
		}
		for rows.Next() {
			var (
				r       Result
				content string
			)
			r.SourceType = "talks"
			if err := rows.Scan(&r.SourceID, &r.Title, &r.Speaker, &r.Year, &r.Month, &r.FilePath, &content, &r.Score); err != nil {
				rows.Close()
				return nil, err
			}
			r.Snippet = snippet(content, 240)
			all = append(all, r)
		}
		rows.Close()
	}

	if wantSet[SourceManuals] {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT id, title, file_path, content,
			       ts_rank(tsv, plainto_tsquery('english', $1)) AS rank
			FROM manuals
			WHERE tsv @@ plainto_tsquery('english', $1)
			ORDER BY rank DESC
			LIMIT $2
		`, opt.Query, opt.Limit)
		if err != nil {
			return nil, fmt.Errorf("manuals fts: %w", err)
		}
		for rows.Next() {
			var (
				r       Result
				content string
			)
			r.SourceType = "manuals"
			if err := rows.Scan(&r.SourceID, &r.Title, &r.FilePath, &content, &r.Score); err != nil {
				rows.Close()
				return nil, err
			}
			r.Snippet = snippet(content, 240)
			all = append(all, r)
		}
		rows.Close()
	}

	if wantSet[SourceBooks] {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT id, title, file_path, content,
			       ts_rank(tsv, plainto_tsquery('english', $1)) AS rank
			FROM books
			WHERE tsv @@ plainto_tsquery('english', $1)
			ORDER BY rank DESC
			LIMIT $2
		`, opt.Query, opt.Limit)
		if err != nil {
			return nil, fmt.Errorf("books fts: %w", err)
		}
		for rows.Next() {
			var (
				r       Result
				content string
			)
			r.SourceType = "books"
			if err := rows.Scan(&r.SourceID, &r.Title, &r.FilePath, &content, &r.Score); err != nil {
				rows.Close()
				return nil, err
			}
			r.Snippet = snippet(content, 240)
			all = append(all, r)
		}
		rows.Close()
	}

	if wantSet[SourceStudyAids] {
		rows, err := s.DB.Pool.Query(ctx, `
			SELECT id, aid_type, slug, title, file_path, content,
			       ts_rank(tsv, plainto_tsquery('english', $1)) AS rank
			FROM study_aids
			WHERE tsv @@ plainto_tsquery('english', $1)
			ORDER BY rank DESC
			LIMIT $2
		`, opt.Query, opt.Limit)
		if err != nil {
			return nil, fmt.Errorf("study_aids fts: %w", err)
		}
		for rows.Next() {
			var (
				r       Result
				content string
			)
			r.SourceType = "study_aids"
			if err := rows.Scan(&r.SourceID, &r.AidType, &r.Slug, &r.Title, &r.FilePath, &content, &r.Score); err != nil {
				rows.Close()
				return nil, err
			}
			r.Snippet = snippet(content, 240)
			all = append(all, r)
		}
		rows.Close()
	}

	// Sort by descending FTS rank, then truncate.
	sortByScore(all)
	if len(all) > opt.Limit {
		all = all[:opt.Limit]
	}
	return all, nil
}

// semantic runs a single nearest-neighbor query against the embeddings table,
// then enriches each row with metadata from the appropriate source table. The
// caller passes the embeddings client resolved by semanticClient (never nil).
func (s *Searcher) semantic(ctx context.Context, client *embed.Client, opt Options) ([]Result, error) {
	vec, err := client.Embed(ctx, opt.Query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	wantSet := sourceSet(opt.Sources)

	// Fetch a few extra rows so per-source filtering doesn't starve us.
	fetch := opt.Limit * 4
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT source_type, source_id, content,
		       1 - (embedding <=> $1) AS similarity
		FROM embeddings
		ORDER BY embedding <=> $1
		LIMIT $2
	`, pgvector.NewVector(vec), fetch)
	if err != nil {
		return nil, fmt.Errorf("embedding ANN query: %w", err)
	}
	type ann struct {
		typ        string
		id         int64
		text       string
		similarity float64
	}
	var hits []ann
	for rows.Next() {
		var a ann
		if err := rows.Scan(&a.typ, &a.id, &a.text, &a.similarity); err != nil {
			rows.Close()
			return nil, err
		}
		if !wantSet[Source(a.typ)] {
			continue
		}
		hits = append(hits, a)
		if len(hits) >= opt.Limit {
			break
		}
	}
	rows.Close()

	// Enrich.
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		r := Result{SourceType: h.typ, SourceID: h.id, Snippet: snippet(h.text, 240), Score: h.similarity}
		if err := s.enrich(ctx, &r); err != nil {
			// Best-effort enrichment — don't drop the result.
			_ = err
		}
		out = append(out, r)
	}
	return out, nil
}

// enrich fills in title/reference/file_path from the source table.
func (s *Searcher) enrich(ctx context.Context, r *Result) error {
	switch r.SourceType {
	case "scriptures":
		return s.DB.Pool.QueryRow(ctx,
			`SELECT reference, file_path FROM scriptures WHERE id = $1`, r.SourceID,
		).Scan(&r.Reference, &r.FilePath)
	case "talks":
		return s.DB.Pool.QueryRow(ctx,
			`SELECT title, speaker, year, month, file_path FROM talks WHERE id = $1`, r.SourceID,
		).Scan(&r.Title, &r.Speaker, &r.Year, &r.Month, &r.FilePath)
	case "manuals":
		return s.DB.Pool.QueryRow(ctx,
			`SELECT title, file_path FROM manuals WHERE id = $1`, r.SourceID,
		).Scan(&r.Title, &r.FilePath)
	case "books":
		return s.DB.Pool.QueryRow(ctx,
			`SELECT title, file_path FROM books WHERE id = $1`, r.SourceID,
		).Scan(&r.Title, &r.FilePath)
	case "study_aids":
		return s.DB.Pool.QueryRow(ctx,
			`SELECT aid_type, slug, title, file_path FROM study_aids WHERE id = $1`, r.SourceID,
		).Scan(&r.AidType, &r.Slug, &r.Title, &r.FilePath)
	}
	return nil
}

// rrfMerge implements Reciprocal Rank Fusion over two ranked lists.
// k=60 is the standard constant.
func rrfMerge(a, b []Result, limit int) []Result {
	const k = 60.0
	type key struct {
		typ string
		id  int64
	}
	scores := map[key]float64{}
	first := map[key]Result{}

	for rank, r := range a {
		k1 := key{r.SourceType, r.SourceID}
		scores[k1] += 1.0 / (k + float64(rank+1))
		if _, ok := first[k1]; !ok {
			first[k1] = r
		}
	}
	for rank, r := range b {
		k1 := key{r.SourceType, r.SourceID}
		scores[k1] += 1.0 / (k + float64(rank+1))
		if _, ok := first[k1]; !ok {
			first[k1] = r
		}
	}

	merged := make([]Result, 0, len(scores))
	for k1, sc := range scores {
		r := first[k1]
		r.Score = sc
		merged = append(merged, r)
	}
	sortByScore(merged)
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func sortByScore(rs []Result) {
	// Insertion sort: result sets are tiny (~20 items).
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Score > rs[j-1].Score; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func snippet(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

func sourceSet(ss []Source) map[Source]bool {
	m := make(map[Source]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
