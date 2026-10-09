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
	// LinkMode controls how search results reference their source:
	// "web" (canonical churchofjesuschrist.org URL only), "fs" (file_path only),
	// or "both" (default). Env GOSPEL_LINK_MODE.
	LinkMode       string
	BooksPath      string // /data/books
	EmbeddingsPath string // /data/embeddings (pre-computed JSONL files)

	// MCP binaries served at /download/gospel-mcp-{os}-{arch}
	MCPBinariesPath string // /opt/mcp-binaries
	Version         string // build version (set by ldflags)

	// LogDir is where parser-failure / diagnostic append-only logs land
	// (e.g. speaker-parse-failures.log). Empty = logging disabled.
	LogDir string

	// Behavior
	IndexOnStartup    bool
	BulkLoadEmbeds    bool
	EmbedRequestTimeo time.Duration
	// EmbedReprobe is the minimum interval between recovery re-probes of the
	// embedding backend when semantic search is gated off (backend was down at
	// boot, or a prior probe failed). A semantic/hybrid request re-probes at most
	// once per interval and enables semantic on success — so the backend
	// recovering never requires a process restart. Env EMBED_REPROBE_SECONDS.
	EmbedReprobe time.Duration
	// CitationContact is the address the engine names in its User-Agent when it
	// asks the BYU Scripture Citation Index, so the index can reach a person.
	// Env CITATION_CONTACT.
	CitationContact string
	// Google sign-in for the key page (internal/signin): the same OAuth client
	// ibeco.me uses. Unset GoogleClientID leaves sign-in off. Env
	// GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, GOOGLE_REDIRECT_URL; COOKIE_SECURE
	// (default true) is false only for local http development.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	CookieSecure       bool
	// EmbedTaskPrefixes names the query/document prefix preset sent to the
	// embedding model (embed.SetTaskPrefixes): "" or "none" = text as-is (the
	// engine before v3), "nomic-v1.5" = nomic's card prefixes. It must match how
	// the live embeddings table was built. Env EMBED_TASK_PREFIXES.
	EmbedTaskPrefixes string
	DevMode           bool // disables auth — local dev only
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:         env("LISTEN_ADDR", ":8080"),
		DatabaseURL:        env("GOSPEL_DB", "postgres://gospel:gospel@localhost:5432/gospel?sslmode=disable"),
		EmbeddingURL:       env("EMBEDDING_URL", "http://localhost:1234/v1"),
		EmbeddingModel:     env("EMBEDDING_MODEL", "nomic-embed-text-v1.5"),
		GospelLibraryPath:  env("GOSPEL_LIBRARY_PATH", "/data/gospel-library"),
		LinkMode:           env("GOSPEL_LINK_MODE", "both"),
		BooksPath:          env("BOOKS_PATH", "/data/books"),
		EmbeddingsPath:     env("EMBEDDINGS_PATH", "/data/embeddings"),
		MCPBinariesPath:    env("MCP_BINARIES_PATH", "/opt/mcp-binaries"),
		Version:            env("VERSION", "dev"),
		LogDir:             env("GOSPEL_LOG_DIR", "/data/logs"),
		IndexOnStartup:     envBool("INDEX_ON_STARTUP", true),
		BulkLoadEmbeds:     envBool("BULK_LOAD_EMBEDDINGS", true),
		EmbedRequestTimeo:  time.Duration(envInt("EMBED_TIMEOUT_SECONDS", 60)) * time.Second,
		EmbedReprobe:       time.Duration(envInt("EMBED_REPROBE_SECONDS", 60)) * time.Second,
		CitationContact:    env("CITATION_CONTACT", "stuffleberryco+privacy@gmail.com"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		CookieSecure:       envBool("COOKIE_SECURE", true),
		EmbedTaskPrefixes:  env("EMBED_TASK_PREFIXES", ""),
		DevMode:            envBool("DEV_MODE", false),
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
