// Command gospel-engine is the hosted study.ibeco.me server: PostgreSQL-backed
// gospel search with REST API, token auth, and MCP binary distribution.
package main

import (
	"context"
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
	// endpoint is gated by ?key=<GOSPEL_MCP_KEY>; if the key is unset the
	// endpoint stays mounted but rejects every request (fail closed).
	mcpSrv := mcpserver.New(apiRouter, cfg.Version)
	mcpKey := os.Getenv("GOSPEL_MCP_KEY")
	if mcpKey == "" {
		log.Printf("WARN: GOSPEL_MCP_KEY unset — /mcp endpoint will reject all requests (fail closed)")
	} else {
		log.Printf("MCP-over-HTTP enabled at /mcp (key-gated)")
	}
	rootHandler := mountMCP(apiRouter, mcpSrv.HTTPHandler(), mcpKey)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 10 * time.Second,
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

// mountMCP returns a handler that routes /mcp* to the streamable-HTTP MCP
// handler (gated by ?key=) and everything else to the API router. The auth
// model mirrors dnd-tools: the bridge's http transport carries the key in the
// URL (?key=...), like exa-search. An empty key fails closed — every /mcp
// request is rejected — so a misconfigured deploy never exposes the tools
// unauthenticated.
func mountMCP(apiRouter, mcpHandler http.Handler, key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/") {
			if key == "" || r.URL.Query().Get("key") != key {
				http.Error(w, `{"error":"missing or invalid key"}`, http.StatusUnauthorized)
				return
			}
			mcpHandler.ServeHTTP(w, r)
			return
		}
		apiRouter.ServeHTTP(w, r)
	})
}
