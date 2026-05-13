package indexer

import (
	"context"
	"fmt"
	"os"
	"time"
)

// ReparseResult is the summary returned by ReparseSpeakers.
type ReparseResult struct {
	Total     int           `json:"total"`
	Changed   int           `json:"changed"`
	Unchanged int           `json:"unchanged"`
	Failed    int           `json:"failed"`
	Missing   int           `json:"missing"` // file_path no longer on disk
	Duration  time.Duration `json:"-"`
}

// ReparseSpeakers iterates every row in the talks table, re-reads the
// markdown from file_path, runs the current parseTalkHeader against it,
// and updates the speaker column when (a) the new value differs from the
// current value AND (b) the new value is not a known-bad shape (empty,
// over-long, or 'Listen to Audio'-tainted). Bad shapes are logged via the
// same speaker-parse-failures.log channel used at index time.
//
// This intentionally does NOT touch title or content -- a full reindex is
// the right tool for that. Reparse is the cheap, safe, pure-text pass that
// fixes the bug introduced in production by the previous parser.
func (idx *Indexer) ReparseSpeakers(ctx context.Context) (*ReparseResult, error) {
	start := time.Now()
	res := &ReparseResult{}

	rows, err := idx.DB.Pool.Query(ctx, `SELECT id, file_path, speaker FROM talks`)
	if err != nil {
		return res, fmt.Errorf("select talks: %w", err)
	}

	type talkRef struct {
		id       int64
		filePath string
		speaker  string
	}
	var refs []talkRef
	for rows.Next() {
		var t talkRef
		if err := rows.Scan(&t.id, &t.filePath, &t.speaker); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan talks: %w", err)
		}
		refs = append(refs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("rows.Err: %w", err)
	}
	res.Total = len(refs)

	for _, t := range refs {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		body, err := os.ReadFile(t.filePath)
		if err != nil {
			res.Missing++
			continue
		}
		_, newSpeaker, _ := parseTalkHeader(string(body))

		if looksLikeSpeakerFailure(newSpeaker) {
			res.Failed++
			idx.logSpeakerFailure(t.filePath, string(body), newSpeaker)
			continue
		}
		if newSpeaker == t.speaker {
			res.Unchanged++
			continue
		}
		if _, err := idx.DB.Pool.Exec(ctx,
			`UPDATE talks SET speaker = $1 WHERE id = $2`,
			newSpeaker, t.id); err != nil {
			return res, fmt.Errorf("update talk %d: %w", t.id, err)
		}
		res.Changed++
	}

	res.Duration = time.Since(start)
	return res, nil
}
