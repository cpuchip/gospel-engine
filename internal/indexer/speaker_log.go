package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// looksLikeSpeakerFailure returns true when parseTalkHeader produced a
// value we know to be wrong or suspect, so the caller can log it for
// later cleanup. Heuristics:
//
//   - empty speaker
//   - residual audio-link content (the previous parser bug, kept here as
//     defense-in-depth in case a new church.org variant slips past)
//   - implausibly long (real names + titles never exceed ~120 chars in the
//     corpus; 200 is a deliberately loose ceiling)
func looksLikeSpeakerFailure(speaker string) bool {
	if speaker == "" {
		return true
	}
	if strings.Contains(speaker, "Listen to Audio") {
		return true
	}
	if len(speaker) > 200 {
		return true
	}
	return false
}

var speakerLogMu sync.Mutex

// logSpeakerFailure appends one record to ${LogDir}/speaker-parse-failures.log.
// Best-effort: if LogDir is unset or the write fails, the function is silent
// rather than blocking indexing on a logging problem.
func (idx *Indexer) logSpeakerFailure(path, fullContent, fallback string) {
	if idx.LogDir == "" {
		return
	}
	if err := os.MkdirAll(idx.LogDir, 0o755); err != nil {
		return
	}

	logPath := filepath.Join(idx.LogDir, "speaker-parse-failures.log")

	rawFirstLine := firstNonEmptyLineAfterTitle(fullContent)
	relPath := path
	if idx.GospelLibraryRoot != "" {
		if rel, err := filepath.Rel(idx.GospelLibraryRoot, path); err == nil {
			relPath = filepath.ToSlash(rel)
		}
	}

	line := fmt.Sprintf("%s  file=%s  raw_first_line=%q  fallback_value=%q\n",
		time.Now().UTC().Format(time.RFC3339),
		relPath,
		rawFirstLine,
		fallback,
	)

	speakerLogMu.Lock()
	defer speakerLogMu.Unlock()

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// firstNonEmptyLineAfterTitle returns the first non-empty trimmed line that
// follows the first H1, for diagnostic purposes. Returns "" if the file has
// no H1 or nothing after it.
func firstNonEmptyLineAfterTitle(s string) string {
	lines := strings.Split(s, "\n")
	pastTitle := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !pastTitle {
			if strings.HasPrefix(t, "# ") {
				pastTitle = true
			}
			continue
		}
		if t != "" {
			return t
		}
	}
	return ""
}
