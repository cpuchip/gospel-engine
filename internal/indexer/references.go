package indexer

import (
	"context"
	"fmt"
	"time"
)

// RepairResult is the summary returned by RepairReferences.
type RepairResult struct {
	Books    int           `json:"books"`
	Changed  int64         `json:"changed"`
	Duration time.Duration `json:"-"`
}

// RepairReferences rewrites scriptures.reference from the current book-name
// map, only where it differs ("1-cor 1:27" -> "1 Corinthians 1:27"). It is
// the cheap pass for a map change: an incremental reindex skips unchanged
// files, so their rows would otherwise keep the old display. No re-embed:
// embeddings are of the verse text, not the reference.
func (idx *Indexer) RepairReferences(ctx context.Context) (*RepairResult, error) {
	start := time.Now()
	res := &RepairResult{}
	rows, err := idx.DB.Pool.Query(ctx, `SELECT DISTINCT book FROM scriptures`)
	if err != nil {
		return res, fmt.Errorf("select books: %w", err)
	}
	var books []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return res, err
		}
		books = append(books, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, b := range books {
		tag, err := idx.DB.Pool.Exec(ctx, `
			UPDATE scriptures
			SET reference = $2 || ' ' || chapter::text || ':' || verse::text
			WHERE book = $1
			  AND reference IS DISTINCT FROM $2 || ' ' || chapter::text || ':' || verse::text
		`, b, BookDisplayName(b))
		if err != nil {
			return res, fmt.Errorf("update %s: %w", b, err)
		}
		res.Changed += tag.RowsAffected()
	}
	res.Books = len(books)
	res.Duration = time.Since(start)
	return res, nil
}
