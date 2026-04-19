// Package config holds runtime configuration loaded from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// HTTP
	ListenAddr string

	// Database
	DatabaseURL string

	// Embedding (LM Studio / OpenAI-compatible)
	EmbeddingURL   string // e.g. http://host.docker.internal:1234/v1
	EmbeddingModel string // nomic-embed-text-v1.5 — MUST match what produced bulk embeddings

	// Content paths (mounted read-only into container)
	GospelLibraryPath string // /data/gospel-library
	BooksPath         string // /data/books
	EmbeddingsPath    string // /data/embeddings (pre-computed JSONL files)

	// MCP binaries served at /download/gospel-mcp-{os}-{arch}
	MCPBinariesPath string // /opt/mcp-binaries
	Version         string // build version (set by ldflags)

	// Behavior
	IndexOnStartup    bool
	BulkLoadEmbeds    bool
	EmbedRequestTimeo time.Duration
	DevMode           bool // disables auth — local dev only
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:        env("LISTEN_ADDR", ":8080"),
		DatabaseURL:       env("GOSPEL_DB", "postgres://gospel:gospel@localhost:5432/gospel?sslmode=disable"),
		EmbeddingURL:      env("EMBEDDING_URL", "http://localhost:1234/v1"),
		EmbeddingModel:    env("EMBEDDING_MODEL", "nomic-embed-text-v1.5"),
		GospelLibraryPath: env("GOSPEL_LIBRARY_PATH", "/data/gospel-library"),
		BooksPath:         env("BOOKS_PATH", "/data/books"),
		EmbeddingsPath:    env("EMBEDDINGS_PATH", "/data/embeddings"),
		MCPBinariesPath:   env("MCP_BINARIES_PATH", "/opt/mcp-binaries"),
		Version:           env("VERSION", "dev"),
		IndexOnStartup:    envBool("INDEX_ON_STARTUP", true),
		BulkLoadEmbeds:    envBool("BULK_LOAD_EMBEDDINGS", true),
		EmbedRequestTimeo: time.Duration(envInt("EMBED_TIMEOUT_SECONDS", 60)) * time.Second,
		DevMode:           envBool("DEV_MODE", false),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("GOSPEL_DB is required")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
