// Package api wires the HTTP handlers using chi.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cpuchip/gospel-engine/internal/auth"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/search"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed web/index.html
var indexHTML []byte

// Server holds the dependencies the handlers need.
type Server struct {
	Cfg      *config.Config
	DB       *db.DB
	Searcher *search.Searcher
	Started  time.Time
}

// Router builds the full chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Public — no auth required.
	r.Get("/", s.handleIndex)
	r.Get("/api/health", s.handleHealth)
	r.Get("/api/version", s.handleVersion)
	r.Get("/download/{filename}", s.handleDownload)

	// Authenticated API.
	r.Group(func(g chi.Router) {
		g.Use(auth.Middleware(s.DB, s.Cfg.DevMode))
		g.Get("/api/search", s.handleSearch)
		g.Get("/api/get", s.handleGet)
		g.Get("/api/list", s.handleList)
	})

	// Admin (also auth-gated; later we'll add a role check).
	r.Group(func(g chi.Router) {
		g.Use(auth.Middleware(s.DB, s.Cfg.DevMode))
		g.Post("/api/admin/tokens", s.handleCreateToken)
		g.Get("/api/admin/tokens", s.handleListTokens)
		g.Delete("/api/admin/tokens/{id}", s.handleRevokeToken)
		g.Post("/api/admin/reindex", s.handleReindex)
	})

	return r
}

// ============================================================================
// / — landing page (embedded HTML, no auth)
// ============================================================================

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(indexHTML)
}

// ============================================================================
// /api/health
// ============================================================================

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := true
	if err := s.DB.Pool.Ping(r.Context()); err != nil {
		dbOK = false
	}
	out := map[string]any{
		"status":  "ok",
		"version": s.Cfg.Version,
		"uptime":  time.Since(s.Started).String(),
		"db_ok":   dbOK,
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================================
// /api/version
// ============================================================================

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.Cfg.Version,
		"api":      "v1",
		"binaries": s.listMCPBinaries(),
	})
}

// ============================================================================
// /api/search
// ============================================================================

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	mode := search.Mode(strings.ToLower(r.URL.Query().Get("mode")))
	limitStr := r.URL.Query().Get("limit")
	limit, _ := strconv.Atoi(limitStr)

	var sources []search.Source
	for _, s := range strings.Split(r.URL.Query().Get("sources"), ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			sources = append(sources, search.Source(s))
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	results, err := s.Searcher.Search(ctx, search.Options{
		Query: q, Mode: mode, Sources: sources, Limit: limit,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("search failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   q,
		"mode":    string(mode),
		"results": results,
	})
}

// ============================================================================
// /api/get?reference=1+Nephi+3:7   (single verse)
// /api/get?reference=D%26C+93:24-30 (verse range, capped at maxRangeVerses)
// /api/get?reference=Mosiah+4       (chapter)
// /api/get?ref=...                  (back-compat alias for reference=)
// /api/get?type=talk&id=42          (direct lookup by table id)
// ============================================================================

// maxRangeVerses caps verse-range responses so a stray "Genesis 1:1-1000"
// doesn't dump a whole chapter through the JSON encoder.
const maxRangeVerses = 50

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		// Accept the schema-correct name too. Server-side both work; agents
		// hit either depending on which doc page they read most recently.
		ref = strings.TrimSpace(r.URL.Query().Get("reference"))
	}
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	// cross_refs is opt-in (Phase 1.5c). Only meaningful for verse / range
	// responses; silently ignored for type=talk/manual/book and chapter-only
	// scripture lookups.
	crossRefs := r.URL.Query().Get("cross_refs") == "true"

	if ref != "" {
		s.getByReference(w, r, ref, crossRefs)
		return
	}
	if typ != "" && idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		s.getByID(w, r, typ, id)
		return
	}
	http.Error(w, "provide either reference= or (type= and id=)", http.StatusBadRequest)
}

// verseRow is the per-verse payload returned in `verses[]`.
type verseRow struct {
	ID        int64  `json:"id"`
	Volume    string `json:"volume"`
	Book      string `json:"book"`
	Chapter   int    `json:"chapter"`
	Verse     int    `json:"verse"`
	Reference string `json:"reference"`
	Text      string `json:"text"`
	FilePath  string `json:"file_path"`
}

func (s *Server) getByReference(w http.ResponseWriter, r *http.Request, ref string, crossRefs bool) {
	parsed, ok := parseReference(ref)
	if !ok {
		http.Error(w, fmt.Sprintf("could not parse reference %q (try '1 Nephi 3:7', 'D&C 93:24-30', or 'Mosiah 4')", ref), http.StatusBadRequest)
		return
	}

	switch {
	case parsed.Verse > 0 && parsed.EndVerse > 0:
		s.getVerseRange(w, r, ref, parsed, crossRefs)
	case parsed.Verse > 0:
		s.getSingleVerse(w, r, ref, parsed, crossRefs)
	default:
		// Chapter-only refs skip cross_refs (out of scope per Phase 1.5c).
		s.getChapter(w, r, ref, parsed)
	}
}

func (s *Server) getSingleVerse(w http.ResponseWriter, r *http.Request, queryRef string, p parsedRef, crossRefs bool) {
	row := s.DB.Pool.QueryRow(r.Context(),
		`SELECT id, volume, book, chapter, verse, reference, text, file_path
		 FROM scriptures WHERE book = $1 AND chapter = $2 AND verse = $3 LIMIT 1`,
		p.Book, p.Chapter, p.Verse)
	var v verseRow
	if err := row.Scan(&v.ID, &v.Volume, &v.Book, &v.Chapter, &v.Verse, &v.Reference, &v.Text, &v.FilePath); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{
		"source_type":     "scriptures",
		"reference_query": queryRef,
		"verses":          []verseRow{v},
	}
	if crossRefs {
		xrefs, err := s.fetchCrossRefs(r.Context(), []verseRow{v})
		if err != nil {
			http.Error(w, fmt.Sprintf("cross_refs failed: %v", err), http.StatusInternalServerError)
			return
		}
		resp["cross_references"] = xrefs
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getVerseRange(w http.ResponseWriter, r *http.Request, queryRef string, p parsedRef, crossRefs bool) {
	if p.EndVerse < p.Verse {
		http.Error(w, "end verse is before start verse", http.StatusBadRequest)
		return
	}
	if p.EndVerse-p.Verse+1 > maxRangeVerses {
		http.Error(w, fmt.Sprintf("verse range exceeds limit of %d verses", maxRangeVerses), http.StatusBadRequest)
		return
	}

	rows, err := s.DB.Pool.Query(r.Context(),
		`SELECT id, volume, book, chapter, verse, reference, text, file_path
		 FROM scriptures
		 WHERE book = $1 AND chapter = $2 AND verse BETWEEN $3 AND $4
		 ORDER BY verse`,
		p.Book, p.Chapter, p.Verse, p.EndVerse)
	if err != nil {
		http.Error(w, fmt.Sprintf("query failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var verses []verseRow
	for rows.Next() {
		var v verseRow
		if err := rows.Scan(&v.ID, &v.Volume, &v.Book, &v.Chapter, &v.Verse, &v.Reference, &v.Text, &v.FilePath); err != nil {
			continue
		}
		verses = append(verses, v)
	}
	if len(verses) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{
		"source_type":     "scriptures",
		"reference_query": queryRef,
		"verses":          verses,
	}
	if crossRefs {
		xrefs, err := s.fetchCrossRefs(r.Context(), verses)
		if err != nil {
			http.Error(w, fmt.Sprintf("cross_refs failed: %v", err), http.StatusInternalServerError)
			return
		}
		resp["cross_references"] = xrefs
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getChapter(w http.ResponseWriter, r *http.Request, queryRef string, p parsedRef) {
	row := s.DB.Pool.QueryRow(r.Context(),
		`SELECT id, volume, book, chapter, title, full_content, file_path
		 FROM chapters WHERE book = $1 AND chapter = $2 LIMIT 1`,
		p.Book, p.Chapter)
	var (
		id                                              int64
		volume, book, title, fullContent, filePath      string
		chapter                                         int
	)
	if err := row.Scan(&id, &volume, &book, &chapter, &title, &fullContent, &filePath); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source_type":     "chapters",
		"reference_query": queryRef,
		"chapter": map[string]any{
			"id":           id,
			"volume":       volume,
			"book":         book,
			"chapter":      chapter,
			"title":        title,
			"full_content": fullContent,
			"file_path":    filePath,
		},
	})
}

func (s *Server) getByID(w http.ResponseWriter, r *http.Request, typ string, id int64) {
	var (
		query string
		out   = map[string]any{"source_type": typ, "id": id}
	)
	switch typ {
	case "scriptures":
		query = `SELECT volume, book, chapter, verse, reference, text, file_path FROM scriptures WHERE id=$1`
		var v0, v1, ref, text, fp string
		var ch, vs int
		if err := s.DB.Pool.QueryRow(r.Context(), query, id).Scan(&v0, &v1, &ch, &vs, &ref, &text, &fp); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out["volume"], out["book"], out["chapter"], out["verse"] = v0, v1, ch, vs
		out["reference"], out["text"], out["file_path"] = ref, text, fp
	case "talks":
		query = `SELECT year, month, speaker, title, content, file_path FROM talks WHERE id=$1`
		var year int
		var month, sp, ti, ct, fp string
		if err := s.DB.Pool.QueryRow(r.Context(), query, id).Scan(&year, &month, &sp, &ti, &ct, &fp); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out["year"], out["month"], out["speaker"], out["title"], out["content"], out["file_path"] = year, month, sp, ti, ct, fp
	case "manuals":
		var ti, ct, fp, coll string
		if err := s.DB.Pool.QueryRow(r.Context(),
			`SELECT collection_id, title, content, file_path FROM manuals WHERE id=$1`, id,
		).Scan(&coll, &ti, &ct, &fp); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out["collection"], out["title"], out["content"], out["file_path"] = coll, ti, ct, fp
	case "books":
		var coll, sec, ti, ct, fp string
		if err := s.DB.Pool.QueryRow(r.Context(),
			`SELECT collection, section, title, content, file_path FROM books WHERE id=$1`, id,
		).Scan(&coll, &sec, &ti, &ct, &fp); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out["collection"], out["section"], out["title"], out["content"], out["file_path"] = coll, sec, ti, ct, fp
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ============================================================================
// /api/list?type=scriptures|talks|manuals|books&...
// ============================================================================

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	switch typ {
	case "scriptures":
		// volumes summary
		rows, err := s.DB.Pool.Query(r.Context(), `
			SELECT volume, COUNT(DISTINCT book) AS books, COUNT(*) AS verses
			FROM scriptures GROUP BY volume ORDER BY volume`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var v string
			var b, vs int
			if err := rows.Scan(&v, &b, &vs); err != nil {
				continue
			}
			items = append(items, map[string]any{"volume": v, "books": b, "verses": vs})
		}
		writeJSON(w, 200, map[string]any{"type": "scriptures", "items": items})
	case "talks":
		rows, err := s.DB.Pool.Query(r.Context(), `
			SELECT year, month, COUNT(*) AS talks
			FROM talks GROUP BY year, month ORDER BY year DESC, month DESC LIMIT 50`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var y, n int
			var m string
			if err := rows.Scan(&y, &m, &n); err != nil {
				continue
			}
			items = append(items, map[string]any{"year": y, "month": m, "talks": n})
		}
		writeJSON(w, 200, map[string]any{"type": "talks", "items": items})
	case "manuals":
		rows, err := s.DB.Pool.Query(r.Context(), `
			SELECT collection_id, COUNT(*) AS pages
			FROM manuals GROUP BY collection_id ORDER BY collection_id LIMIT 200`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var c string
			var n int
			if err := rows.Scan(&c, &n); err != nil {
				continue
			}
			items = append(items, map[string]any{"collection": c, "pages": n})
		}
		writeJSON(w, 200, map[string]any{"type": "manuals", "items": items})
	case "books":
		rows, err := s.DB.Pool.Query(r.Context(), `
			SELECT collection, COUNT(*) AS sections
			FROM books GROUP BY collection ORDER BY collection`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var items []map[string]any
		for rows.Next() {
			var c string
			var n int
			if err := rows.Scan(&c, &n); err != nil {
				continue
			}
			items = append(items, map[string]any{"collection": c, "sections": n})
		}
		writeJSON(w, 200, map[string]any{"type": "books", "items": items})
	case "":
		// Stats.
		stats := map[string]any{}
		_ = s.DB.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM scriptures`).Scan(&stats)
		writeJSON(w, 200, map[string]any{"type": "stats", "stats": s.collectStats(r.Context())})
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
	}
}

func (s *Server) collectStats(ctx context.Context) map[string]int {
	stats := map[string]int{}
	for table, key := range map[string]string{
		"scriptures": "scriptures",
		"chapters":   "chapters",
		"talks":      "talks",
		"manuals":    "manuals",
		"books":      "books",
		"embeddings": "embeddings",
	} {
		var n int
		_ = s.DB.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
		stats[key] = n
	}
	return stats
}

// ============================================================================
// /download/gospel-mcp-{os}-{arch}
// ============================================================================

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if !strings.HasPrefix(filename, "gospel-mcp-") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Prevent path traversal.
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, `/\`) {
		http.Error(w, "bad filename", http.StatusBadRequest)
		return
	}
	full := filepath.Join(s.Cfg.MCPBinariesPath, filename)
	http.ServeFile(w, r, full)
}

func (s *Server) listMCPBinaries() []string {
	matches, _ := filepath.Glob(filepath.Join(s.Cfg.MCPBinariesPath, "gospel-mcp-*"))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, filepath.Base(m))
	}
	return out
}

// ============================================================================
// admin
// ============================================================================

type createTokenReq struct {
	ExternalUser  string `json:"external_user"`
	Name          string `json:"name"`
	RateLimit     int    `json:"rate_limit"`
	ExpiresInDays int    `json:"expires_in_days"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var exp *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().UTC().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		exp = &t
	}
	tok, raw, err := s.DB.CreateAPIToken(r.Context(), req.ExternalUser, req.Name, exp, req.RateLimit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 201, map[string]any{
		"token": tok,
		"raw":   raw, // shown once
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.DB.ListAPITokens(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": tokens})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.DB.RevokeAPIToken(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	// Long-running — fire and forget; return 202.
	go func() {
		// In a real deploy, write progress to a status table or log.
	}()
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"started"}`))
}

// ============================================================================
// helpers
// ============================================================================

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
