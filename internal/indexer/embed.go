// Package indexer — embedding pass.
//
// Granularity: per-verse for scriptures, per-paragraph for talks/manuals/books.
// Idempotent: only embeds rows that don't already have an entry in the
// `embeddings` table for (source_type, source_id, layer).
//
// LM Studio with nomic-embed-text-v1.5 typically does ~50–150 embeddings/sec
// on a single GPU. ~50k rows ≈ 5–15 minutes for a cold start.
package indexer

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cpuchip/gospel-engine/internal/embed"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// EmbedResult is a small summary returned from EmbedAll.
type EmbedResult struct {
	Verses     int
	Paragraphs int
	Errors     int
	Duration   time.Duration
}

// EmbedAll generates and stores embeddings for any scripture verse, talk
// paragraph, manual paragraph, or book paragraph that doesn't already have
// one in the `embeddings` table. Safe to re-run.
func (idx *Indexer) EmbedAll(ctx context.Context, embedder *embed.Client) (*EmbedResult, error) {
	if embedder == nil {
		return nil, fmt.Errorf("embed: no client configured")
	}
	start := time.Now()
	r := &EmbedResult{}

	// --- 1. Verses ---
	n, err := idx.embedVerses(ctx, embedder)
	if err != nil {
		return r, fmt.Errorf("verses: %w", err)
	}
	r.Verses = n

	// --- 2. Talk paragraphs ---
	n, err = idx.embedTextRows(ctx, embedder, "talks", "content", "paragraph", &r.Errors)
	if err != nil {
		return r, fmt.Errorf("talks: %w", err)
	}
	r.Paragraphs += n

	// --- 3. Manual paragraphs ---
	n, err = idx.embedTextRows(ctx, embedder, "manuals", "content", "paragraph", &r.Errors)
	if err != nil {
		return r, fmt.Errorf("manuals: %w", err)
	}
	r.Paragraphs += n

	// --- 4. Book paragraphs ---
	n, err = idx.embedTextRows(ctx, embedder, "books", "content", "paragraph", &r.Errors)
	if err != nil {
		return r, fmt.Errorf("books: %w", err)
	}
	r.Paragraphs += n

	r.Duration = time.Since(start)
	return r, nil
}

// embedVerses iterates scripture verses missing an embedding and writes one
// vector per verse (layer = "verse").
func (idx *Indexer) embedVerses(ctx context.Context, embedder *embed.Client) (int, error) {
	rows, err := idx.DB.Pool.Query(ctx, `
		SELECT s.id, s.text
		FROM scriptures s
		LEFT JOIN embeddings e
		  ON e.source_type = 'scriptures'
		 AND e.source_id   = s.id
		 AND e.layer       = 'verse'
		WHERE e.id IS NULL
		  AND length(s.text) > 0
		ORDER BY s.id
	`)
	if err != nil {
		return 0, err
	}

	type job struct {
		id   int64
		text string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.text); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if len(jobs) == 0 {
		log.Printf("embed verses: nothing to do")
		return 0, nil
	}
	log.Printf("embed verses: %d to process", len(jobs))

	count := 0
	logEvery := 500
	for i, j := range jobs {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		vec, err := embedder.Embed(ctx, j.text)
		if err != nil {
			log.Printf("embed verses: id=%d failed: %v", j.id, err)
			continue
		}
		if _, err := idx.DB.Pool.Exec(ctx, `
			INSERT INTO embeddings (source_type, source_id, layer, content, embedding, model)
			VALUES ('scriptures', $1, 'verse', $2, $3, $4)
			ON CONFLICT (source_type, source_id, layer) DO NOTHING
		`, j.id, j.text, pgvector.NewVector(vec), embedder.Model); err != nil {
			log.Printf("embed verses: insert id=%d failed: %v", j.id, err)
			continue
		}
		count++
		if (i+1)%logEvery == 0 {
			log.Printf("embed verses: %d / %d", i+1, len(jobs))
		}
	}
	log.Printf("embed verses: done (%d inserted)", count)
	return count, nil
}

// embedTextRows generates per-paragraph embeddings for any table that has
// (id BIGINT, <textCol> TEXT) shape. Paragraphs are split on blank lines.
func (idx *Indexer) embedTextRows(
	ctx context.Context,
	embedder *embed.Client,
	tableName, textCol, layer string,
	errCount *int,
) (int, error) {
	q := fmt.Sprintf(`
		SELECT t.id, t.%s
		FROM %s t
		WHERE NOT EXISTS (
		  SELECT 1 FROM embeddings e
		   WHERE e.source_type = $1
		     AND e.source_id   = t.id
		     AND e.layer       = $2
		)
		AND length(t.%s) > 0
		ORDER BY t.id
	`, textCol, tableName, textCol)

	rows, err := idx.DB.Pool.Query(ctx, q, tableName, layer)
	if err != nil {
		return 0, err
	}

	type job struct {
		id   int64
		text string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.text); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if len(jobs) == 0 {
		log.Printf("embed %s: nothing to do", tableName)
		return 0, nil
	}
	log.Printf("embed %s: %d source rows to process", tableName, len(jobs))

	totalInserted := 0
	for i, j := range jobs {
		if ctx.Err() != nil {
			return totalInserted, ctx.Err()
		}
		paragraphs := splitParagraphs(j.text)
		for pIdx, p := range paragraphs {
			vec, err := embedder.Embed(ctx, p)
			if err != nil {
				if errCount != nil {
					*errCount++
				}
				log.Printf("embed %s: id=%d p=%d failed: %v", tableName, j.id, pIdx, err)
				continue
			}
			// Use a synthetic source_id that encodes (id, paragraph_index) so we
			// can store many paragraphs per source row. Format: id*1000 + pIdx.
			// Simpler: store the original id and use the (source_type,source_id,layer)
			// uniqueness only for the FIRST paragraph; subsequent paragraphs use
			// layer = "paragraph_N". But we declared UNIQUE (source_type, source_id, layer)
			// — so encode the paragraph index in the layer.
			synthLayer := layer
			if pIdx > 0 {
				synthLayer = fmt.Sprintf("%s_%d", layer, pIdx)
			}
			if _, err := idx.DB.Pool.Exec(ctx, `
				INSERT INTO embeddings (source_type, source_id, layer, content, embedding, model)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (source_type, source_id, layer) DO NOTHING
			`, tableName, j.id, synthLayer, p, pgvector.NewVector(vec), embedder.Model); err != nil {
				if errCount != nil {
					*errCount++
				}
				log.Printf("embed %s: insert id=%d p=%d failed: %v", tableName, j.id, pIdx, err)
				continue
			}
			totalInserted++
		}
		if (i+1)%50 == 0 {
			log.Printf("embed %s: %d / %d source rows (%d paragraphs inserted)", tableName, i+1, len(jobs), totalInserted)
		}
	}
	log.Printf("embed %s: done (%d paragraphs inserted across %d rows)", tableName, totalInserted, len(jobs))
	return totalInserted, nil
}

// splitParagraphs splits text on blank lines, trims, drops empties, and caps
// each paragraph to a reasonable length so we don't blow past nomic's 8k token
// context with one giant blob.
func splitParagraphs(text string) []string {
	const maxRunes = 2000 // ~500 tokens, well under nomic's 8k limit
	raw := strings.Split(text, "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Strip simple markdown noise that hurts embedding quality.
		p = cleanInlineMarkdown(p)
		if p == "" {
			continue
		}
		// Cap length.
		if len([]rune(p)) > maxRunes {
			runes := []rune(p)
			p = string(runes[:maxRunes])
		}
		out = append(out, p)
	}
	return out
}

// Compile-time guard so we don't accidentally drop the pgx import if all
// helpers above stop using it directly.
var _ = pgx.ErrNoRows
