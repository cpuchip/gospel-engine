// Command gospel-engine is the hosted study.ibeco.me server: PostgreSQL-backed
// gospel search with REST API, token auth, and MCP binary distribution.
package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cpuchip/gospel-engine/internal/api"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/embed"
	"github.com/cpuchip/gospel-engine/internal/indexer"
	"github.com/cpuchip/gospel-engine/internal/mcpserver"
	"github.com/cpuchip/gospel-engine/internal/search"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log.Printf("gospel-engine starting (version=%s, listen=%s, dev=%v)",
		cfg.Version, cfg.ListenAddr, cfg.DevMode)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Database ---
	connectCtx, connectCancel := context.WithTimeout(rootCtx, 30*time.Second)
	database, err := db.Open(connectCtx, cfg.DatabaseURL)
	connectCancel()
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer database.Close()
	log.Printf("db connected; schema migrations applied")

	// --- Embedding client (best-effort) ---
	// The client is always constructed and kept so /api/health can live-ping the
	// backend and the searcher can re-probe it. A startup ping decides whether
	// semantic search is enabled *now*: on failure the searcher gates semantic
	// off, but re-probes on a cadence (EMBED_REPROBE_SECONDS) and enables it
	// automatically once the backend recovers — no restart required.
	embedder := embed.New(cfg.EmbeddingURL, cfg.EmbeddingModel, cfg.EmbedRequestTimeo)
	pingCtx, pingCancel := context.WithTimeout(rootCtx, 5*time.Second)
	embedOK := embedder.Ping(pingCtx) == nil
	pingCancel()
	if embedOK {
		log.Printf("embedding server OK (model=%s)", cfg.EmbeddingModel)
	} else {
		log.Printf("WARN: embedding server unreachable at boot — keyword search works now; semantic will re-probe every %s and enable itself once the backend recovers (model=%s)", cfg.EmbedReprobe, cfg.EmbeddingModel)
	}

	// --- Indexer (always constructed; backgrounded only when configured) ---
	idx := indexer.New(database, cfg.GospelLibraryPath, cfg.BooksPath)
	idx.LogDir = cfg.LogDir

	if cfg.IndexOnStartup {
		go func() {
			log.Printf("indexer starting (gospel=%s, books=%s)", cfg.GospelLibraryPath, cfg.BooksPath)
			res, err := idx.IndexAll(rootCtx)
			if err != nil {
				log.Printf("indexer error: %v", err)
			}
			if res != nil {
				log.Printf("indexer done: scriptures=%d chapters=%d talks=%d manuals=%d books=%d skipped=%d errors=%d (%s)",
					res.ScripturesIndexed, res.ChaptersIndexed, res.TalksIndexed,
					res.ManualsIndexed, res.BooksIndexed, res.Skipped, res.Errors, res.Duration)
			}

			// --- Embedding pass (only if embedder is healthy and bulk loading is enabled) ---
			if embedOK && cfg.BulkLoadEmbeds {
				log.Printf("embed pass starting")
				eres, err := idx.EmbedAll(rootCtx, embedder)
				if err != nil {
					log.Printf("embed pass error: %v", err)
				}
				if eres != nil {
					log.Printf("embed pass done: verses=%d paragraphs=%d errors=%d (%s)",
						eres.Verses, eres.Paragraphs, eres.Errors, eres.Duration)
				}
			} else if !embedOK {
				log.Printf("embed pass skipped: embedding server unavailable")
			} else {
				log.Printf("embed pass skipped: BULK_LOAD_EMBEDDINGS=false")
			}
		}()
	}

	// --- HTTP server ---
	srv := &api.Server{
		Cfg:      cfg,
		DB:       database,
		Searcher: search.NewSearcher(database, embedder, embedOK, cfg.EmbedReprobe, cfg.LinkMode, cfg.GospelLibraryPath),
		Embed:    embedder,
		Indexer:  idx,
		Started:  time.Now(),
	}
	apiRouter := srv.Router()

	// MCP-over-HTTP at /mcp (streamable HTTP) — lets a remote bridge dial
	// gospel-engine directly, exa-search / dnd-tools style, with no local
	// stdio binary. Tools proxy in-process to the same router above, so
	// semantic search and all JSON shapes match the REST API exactly. The
	// endpoint accepts a live stdy_ API token or the legacy GOSPEL_MCP_KEY, via
	// ?key= or an Authorization: Bearer header; see mountMCP.
	mcpSrv := mcpserver.New(apiRouter, cfg.Version)
	mcpKey := os.Getenv("GOSPEL_MCP_KEY")
	if mcpKey == "" {
		log.Printf("MCP-over-HTTP enabled at /mcp (stdy_ API tokens only; legacy GOSPEL_MCP_KEY unset)")
	} else {
		log.Printf("MCP-over-HTTP enabled at /mcp (key-gated: stdy_ API tokens or legacy GOSPEL_MCP_KEY)")
	}
	validateToken := func(ctx context.Context, raw string) (bool, error) {
		tok, err := database.ValidateAPIToken(ctx, raw)
		if err != nil || tok == nil {
			return false, err
		}
		database.TouchAPITokenIfStale(tok)
		return true, nil
	}
	rootHandler := mountMCP(apiRouter, mcpSrv.HTTPHandler(), mcpKey, validateToken)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds a stalled request body; Go clears the read deadline
		// once the body is read, so long-lived MCP streams are unaffected.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	// Shutdown plumbing.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("shutting down...")
		ctx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = httpSrv.Shutdown(ctx)
		cancel()
	}()

	log.Printf("listening on %s", cfg.ListenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// maxMCPBody caps an MCP request body. JSON-RPC messages here are a few KB.
const maxMCPBody = 1 << 20

// tokenValidator reports whether raw is a live (unrevoked, unexpired) API token.
// A non-nil error means the lookup failed, not that the token is invalid.
type tokenValidator func(ctx context.Context, raw string) (bool, error)

// mountMCP returns a handler that routes /mcp* to the streamable-HTTP MCP
// handler and everything else to the API router. A request to /mcp is let
// through when its credential is either the legacy shared GOSPEL_MCP_KEY or a
// live per-user `stdy_` API token (revocable, expirable, same store as the
// REST bearer tokens). The credential may arrive as ?key=... (remote
// connectors such as claude.ai can only carry it in the URL) or as an
// `Authorization: Bearer` header. Everything else fails closed: no credential,
// an empty one, an unknown one, or an unset legacy key with no valid token is
// 401; a failed token lookup is 500, never a pass.
func mountMCP(apiRouter, mcpHandler http.Handler, key string, validate tokenValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" && !strings.HasPrefix(r.URL.Path, "/mcp/") {
			apiRouter.ServeHTTP(w, r)
			return
		}
		serveMCP := func() {
			// The MCP library reads whole request bodies; cap them.
			r.Body = http.MaxBytesReader(w, r.Body, maxMCPBody)
			mcpHandler.ServeHTTP(w, r)
		}
		supplied := r.URL.Query().Get("key")
		if supplied == "" {
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				supplied = strings.TrimSpace(h[len("Bearer "):])
			}
		}
		switch {
		case supplied == "":
			// fall through to reject
		case key != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(key)) == 1:
			serveMCP()
			return
		case validate != nil && db.LooksLikeAPIToken(supplied):
			ok, err := validate(r.Context(), supplied)
			if err != nil {
				http.Error(w, `{"error":"auth lookup failed"}`, http.StatusInternalServerError)
				return
			}
			if ok {
				serveMCP()
				return
			}
		}
		http.Error(w, `{"error":"missing or invalid key"}`, http.StatusUnauthorized)
	})
}
