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
	"syscall"
	"time"

	"github.com/cpuchip/gospel-engine/internal/api"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/embed"
	"github.com/cpuchip/gospel-engine/internal/indexer"
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
	embedder := embed.New(cfg.EmbeddingURL, cfg.EmbeddingModel, cfg.EmbedRequestTimeo)
	pingCtx, pingCancel := context.WithTimeout(rootCtx, 5*time.Second)
	if err := embedder.Ping(pingCtx); err != nil {
		log.Printf("WARN: embedding server unreachable (%v) — keyword search will work, semantic will not", err)
		embedder = nil
	} else {
		log.Printf("embedding server OK (model=%s)", cfg.EmbeddingModel)
	}
	pingCancel()

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
			if embedder != nil && cfg.BulkLoadEmbeds {
				log.Printf("embed pass starting")
				eres, err := idx.EmbedAll(rootCtx, embedder)
				if err != nil {
					log.Printf("embed pass error: %v", err)
				}
				if eres != nil {
					log.Printf("embed pass done: verses=%d paragraphs=%d errors=%d (%s)",
						eres.Verses, eres.Paragraphs, eres.Errors, eres.Duration)
				}
			} else if embedder == nil {
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
		Searcher: search.NewSearcher(database, embedder),
		Indexer:  idx,
		Started:  time.Now(),
	}
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Router(),
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
