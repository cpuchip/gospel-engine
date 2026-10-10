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
	"errors"
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

// ErrEmbedRunning is returned by EmbedAll while another pass is in progress.
var ErrEmbedRunning = errors.New("an embed pass is already running")

// EmbedAll generates and stores embeddings for any scripture verse, talk
// paragraph, manual paragraph, or book paragraph that doesn't already have
// one in the `embeddings` table. Safe to re-run.
func (idx *Indexer) EmbedAll(ctx context.Context, embedder *embed.Client) (*EmbedResult, error) {
	if embedder == nil {
		return nil, fmt.Errorf("embed: no client configured")
	}
	if !idx.embedding.CompareAndSwap(false, true) {
		return nil, ErrEmbedRunning
	}
	defer idx.embedding.Store(false)
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
		ok, err := idx.embedVerse(ctx, embedder, j.id, j.text, false)
		if err != nil {
			log.Printf("embed verses: id=%d failed: %v", j.id, err)
			continue
		}
		if ok {
			count++
		}
		if (i+1)%logEvery == 0 {
			log.Printf("embed verses: %d / %d", i+1, len(jobs))
		}
	}
	log.Printf("embed verses: done (%d inserted)", count)
	return count, nil
}

// embedVerse embeds one verse and writes it only while the verse still has
// that text: the FOR SHARE waits out a reindex that is changing the verse,
// then finds the new text and writes nothing, so the old text's vector never
// lands after the reindex cleared it. The verse is locked before its
// embedding is touched, the order the reindex uses, so the two cannot
// deadlock. replace deletes the verse's existing embedding in the same
// transaction (the repair path). It reports whether a row was written.
func (idx *Indexer) embedVerse(ctx context.Context, embedder *embed.Client, id int64, text string, replace bool) (bool, error) {
	vec, err := embedder.EmbedDocument(ctx, text)
	if err != nil {
		return false, err
	}
	tx, err := idx.DB.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var current string
	if err := tx.QueryRow(ctx, `SELECT text FROM scriptures WHERE id = $1 FOR SHARE`, id).Scan(&current); err != nil {
		return false, fmt.Errorf("re-read verse: %w", err)
	}
	if current != text {
		return false, nil
	}
	if replace {
		if _, err := tx.Exec(ctx, `DELETE FROM embeddings WHERE source_type = 'scriptures' AND source_id = $1 AND layer = 'verse'`, id); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO embeddings (source_type, source_id, layer, content, embedding, model)
		VALUES ('scriptures', $1, 'verse', $2, $3, $4)
		ON CONFLICT (source_type, source_id, layer) DO NOTHING
	`, id, text, pgvector.NewVector(vec), embedder.Model)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, tx.Commit(ctx)
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
		n, err := idx.embedRow(ctx, embedder, tableName, layer, j.id, j.text, false)
		if err != nil {
			if errCount != nil {
				*errCount++
			}
			log.Printf("embed %s: id=%d: %v (row left for the next pass)", tableName, j.id, err)
		}
		totalInserted += n
		if (i+1)%50 == 0 {
			log.Printf("embed %s: %d / %d source rows (%d paragraphs inserted)", tableName, i+1, len(jobs), totalInserted)
		}
	}
	log.Printf("embed %s: done (%d paragraphs inserted across %d rows)", tableName, totalInserted, len(jobs))
	return totalInserted, nil
}

// paragraphLayer is the embeddings layer of paragraph p: "paragraph" for the
// first, "paragraph_N" after (UNIQUE (source_type, source_id, layer) holds one
// row per paragraph that way).
func paragraphLayer(layer string, p int) string {
	if p == 0 {
		return layer
	}
	return fmt.Sprintf("%s_%d", layer, p)
}

// errRowChanged: the row was rewritten while it was being embedded; the next
// pass embeds the new text.
var errRowChanged = errors.New("row changed while it was being embedded")

// embedRow embeds every paragraph of one source row FIRST, then writes them
// all in one transaction, so a row is either wholly embedded or not at all.
// Writing paragraph by paragraph, a restart mid-row (a deploy during an index
// pass, 2026-10-09: talk 8565 stopped at 21 of 40) left the row partial, and
// the next pass skipped it for good because it tests only the first paragraph.
// replace deletes the row's existing paragraph embeddings inside the same
// transaction (the repair path); otherwise existing rows are left alone.
func (idx *Indexer) embedRow(ctx context.Context, embedder *embed.Client, tableName, layer string, id int64, text string, replace bool) (int, error) {
	paragraphs := splitParagraphs(text)
	vecs := make([]pgvector.Vector, len(paragraphs))
	for p, para := range paragraphs {
		vec, err := embedder.EmbedDocument(ctx, para)
		if err != nil {
			return 0, fmt.Errorf("paragraph %d of %d: %w", p, len(paragraphs), err)
		}
		vecs[p] = pgvector.NewVector(vec)
	}
	tx, err := idx.DB.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// The row must still hold the text just embedded. FOR SHARE waits out a
	// reindex that is rewriting it; that reindex has cleared the row's
	// embeddings, and writing the old text's vectors now would undo it.
	var current string
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT content FROM %s WHERE id = $1 FOR SHARE`, tableName), id).Scan(&current); err != nil {
		return 0, fmt.Errorf("re-read row: %w", err)
	}
	if current != text {
		return 0, errRowChanged
	}
	if replace {
		if _, err := tx.Exec(ctx, `
			DELETE FROM embeddings WHERE source_type = $1 AND source_id = $2
			  AND (layer = $3 OR layer LIKE $3 || '\_%')`, tableName, id, layer); err != nil {
			return 0, fmt.Errorf("clear partial row: %w", err)
		}
	}
	n := 0
	for p, para := range paragraphs {
		tag, err := tx.Exec(ctx, `
			INSERT INTO embeddings (source_type, source_id, layer, content, embedding, model)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (source_type, source_id, layer) DO NOTHING
		`, tableName, id, paragraphLayer(layer, p), para, vecs[p], embedder.Model)
		if err != nil {
			return 0, fmt.Errorf("insert paragraph %d: %w", p, err)
		}
		n += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// EmbedRepairResult reports RepairEmbeddings. Partial, Stale and Shrunk count
// talk, manual and book rows; StaleVerses counts verses.
type EmbedRepairResult struct {
	Checked     int `json:"checked"`
	Partial     int `json:"partial"`
	Stale       int `json:"stale"`
	Shrunk      int `json:"shrunk"`
	StaleVerses int `json:"stale_verses"`
	Repaired    int `json:"repaired"`
	Failed      int `json:"failed"`
}

// rowDrift compares a row's stored paragraph embeddings (layer -> text) with
// the paragraphs the embedder makes of its current text: "shrunk" when a
// stored layer has no paragraph, "stale" when a stored text differs, "partial"
// when a paragraph has no layer, "" when they agree.
func rowDrift(want []string, got map[string]string) string {
	matched, stale, missing := 0, false, false
	for p, para := range want {
		c, ok := got[paragraphLayer("paragraph", p)]
		switch {
		case !ok:
			missing = true
		case c != para:
			stale = true
			matched++
		default:
			matched++
		}
	}
	switch {
	case len(got) > matched:
		return "shrunk"
	case stale:
		return "stale"
	case missing:
		return "partial"
	}
	return ""
}

// RepairEmbeddings re-embeds, whole and in one transaction each, every talk,
// manual and book row whose paragraph embeddings disagree with its current
// text (partial, stale or shrunk, judged by the embedder's own splitter), and
// every verse whose embedding was made from other text. These are left by
// writes from before the reindex cleared changed rows' embeddings. Rows with
// no embeddings at all are left to the ordinary pass. It reads every row's
// content and embedded text once, so it is an admin action, not part of each
// pass. dry counts what it would repair and changes nothing.
func (idx *Indexer) RepairEmbeddings(ctx context.Context, embedder *embed.Client, dry bool) (*EmbedRepairResult, error) {
	res := &EmbedRepairResult{}
	for _, table := range []string{"talks", "manuals", "books"} {
		rows, err := idx.DB.Pool.Query(ctx, fmt.Sprintf(`
			SELECT t.id, t.content, array_agg(e.layer), array_agg(e.content)
			FROM %s t
			JOIN embeddings e ON e.source_type = $1 AND e.source_id = t.id
			 AND (e.layer = 'paragraph' OR e.layer LIKE 'paragraph\_%%')
			GROUP BY t.id, t.content`, table), table)
		if err != nil {
			return res, fmt.Errorf("scan %s: %w", table, err)
		}
		type drifted struct {
			id   int64
			text string
		}
		var todo []drifted
		for rows.Next() {
			var id int64
			var text string
			var layers, contents []string
			if err := rows.Scan(&id, &text, &layers, &contents); err != nil {
				rows.Close()
				return res, err
			}
			res.Checked++
			got := make(map[string]string, len(layers))
			for i, l := range layers {
				got[l] = contents[i]
			}
			switch rowDrift(splitParagraphs(text), got) {
			case "partial":
				res.Partial++
			case "stale":
				res.Stale++
			case "shrunk":
				res.Shrunk++
			default:
				continue
			}
			if !dry {
				todo = append(todo, drifted{id, text})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
		for _, d := range todo {
			if _, err := idx.embedRow(ctx, embedder, table, "paragraph", d.id, d.text, true); err != nil {
				res.Failed++
				log.Printf("embed repair %s id=%d: %v", table, d.id, err)
				continue
			}
			res.Repaired++
			log.Printf("embed repair %s id=%d: re-embedded whole", table, d.id)
		}
	}

	rows, err := idx.DB.Pool.Query(ctx, `
		SELECT s.id, s.text FROM scriptures s
		JOIN embeddings e ON e.source_type = 'scriptures' AND e.source_id = s.id AND e.layer = 'verse'
		WHERE e.content <> s.text`)
	if err != nil {
		return res, fmt.Errorf("scan verses: %w", err)
	}
	type verse struct {
		id   int64
		text string
	}
	var verses []verse
	for rows.Next() {
		var v verse
		if err := rows.Scan(&v.id, &v.text); err != nil {
			rows.Close()
			return res, err
		}
		verses = append(verses, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.StaleVerses = len(verses)
	if dry {
		return res, nil
	}
	for _, v := range verses {
		ok, err := idx.embedVerse(ctx, embedder, v.id, v.text, true)
		if err != nil || !ok {
			res.Failed++
			log.Printf("embed repair verse id=%d: wrote=%v err=%v", v.id, ok, err)
			continue
		}
		res.Repaired++
	}
	return res, nil
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
