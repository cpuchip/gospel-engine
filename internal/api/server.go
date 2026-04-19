// Package api wires the HTTP handlers using chi.
package api

import (
	"context"
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
// /api/get?ref=1+Nephi+3:7  OR  /api/get?type=talk&id=42
// ============================================================================

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))

	if ref != "" {
		s.getByReference(w, r, ref)
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
	http.Error(w, "provide either ref= or (type= and id=)", http.StatusBadRequest)
}

func (s *Server) getByReference(w http.ResponseWriter, r *http.Request, ref string) {
	row := s.DB.Pool.QueryRow(r.Context(),
		`SELECT id, volume, book, chapter, verse, reference, text, file_path
		 FROM scriptures WHERE reference = $1 LIMIT 1`, ref)
	var (
		id                                      int64
		volume, book, reference, text, filePath string
		chapter, verse                          int
	)
	if err := row.Scan(&id, &volume, &book, &chapter, &verse, &reference, &text, &filePath); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source_type": "scriptures",
		"id":          id,
		"volume":      volume,
		"book":        book,
		"chapter":     chapter,
		"verse":       verse,
		"reference":   reference,
		"text":        text,
		"file_path":   filePath,
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
