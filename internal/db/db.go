// Package db wraps a PostgreSQL connection pool and runs embedded migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps the pgx connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Open creates a new pool, registers pgvector type codecs, and runs migrations.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour

	// Bootstrap: ensure the vector extension exists before opening the pool,
	// so the AfterConnect hook can register its type codecs without racing
	// concurrent connections trying to create the same extension.
	bootstrap, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("bootstrap connect: %w", err)
	}
	if _, err := bootstrap.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		bootstrap.Close(ctx)
		return nil, fmt.Errorf("creating vector extension: %w", err)
	}
	bootstrap.Close(ctx)

	// Register pgvector type codecs on every new pool connection.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvector.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	// Verify connection.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging db: %w", err)
	}

	d := &DB{Pool: pool}
	if err := d.Migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return d, nil
}

// Close releases the pool.
func (d *DB) Close() {
	d.Pool.Close()
}

// Migrate runs every embedded migration file whose version isn't already
// recorded in schema_migrations.
func (d *DB) Migrate(ctx context.Context) error {
	// Bootstrap: create schema_migrations if missing.
	_, err := d.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("bootstrap schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}

	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// Filename is NNN_description.sql
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) != 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		migs = append(migs, mig{version: v, name: e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	// Find which versions are already applied.
	applied := map[int]bool{}
	rows, err := d.Pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("querying applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", m.name, err)
		}

		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("applying %s: %w", m.name, err)
		}
		// File itself usually inserts the version row; ensure it's there.
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`,
			m.version,
		); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("recording version %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", m.name, err)
		}
	}
	return nil
}
