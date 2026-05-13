---
phase: 1.5d
status: ready
repo: gospel-engine-v2
created: 2026-05-13
---

# Phase 1.5d — Speaker extraction fix

**Binding problem.** `gospel_search` results show `"speaker": "🎧 Listen to Audio"` for talks indexed since the church.org redesign added an audio-link line. Confirmed across multiple decades of talks. Search results are technically correct but humanly useless — speaker is the most common filter intent.

## Root cause

Modern talk markdown (`gospel-library/eng/general-conference/{year}/{month}/{slug}.md`) is now:

```
# Title

🎧 [Listen to Audio](https://...mp3)

# Title

By President Dallin H. Oaks

President of The Church of Jesus Christ of Latter-day Saints

{body...}
```

`indexer.go::parseTalkHeader` finds the first `# ` heading, then takes the first non-empty non-heading line that follows. That line is `🎧 [Listen to Audio](...)`. After `cleanInlineMarkdown` strips the link wrapper, we get `🎧 Listen to Audio`.

## Change

### `internal/indexer/indexer.go::parseTalkHeader`

Rewrite to:

1. Find the first `# ` heading (title).
2. Skip subsequent lines that are:
   - Empty
   - Start with `🎧` (audio-link prefix)
   - Match `^\[Listen to Audio\]` (audio link without emoji)
   - Are another `# ` heading (the duplicated H1 case)
3. The next non-skipped line is the speaker candidate.
4. If it starts with `By ` (case-insensitive), strip the prefix and use the rest as speaker.
5. Otherwise, take the line as-is (older format fallback) — this is what the original parser did.
6. Apply `cleanInlineMarkdown` to the speaker.
7. `contentStart` is the line AFTER the speaker.

If steps 1–5 fail to produce a non-empty speaker, log to a parse-failures file (see below) and leave the existing DB value alone (don't overwrite with empty).

### Speaker re-extraction job

Don't re-walk disk. The `talks` table has `content` AND `file_path`. Add an admin endpoint or a one-shot CLI command:

```
POST /api/admin/reparse-speakers
```

Implementation: `SELECT id, file_path, content FROM talks` → for each row, re-run `parseTalkHeader` on the file (or on `content` if we have the raw markdown stored — check schema), `UPDATE talks SET speaker = $1 WHERE id = $2` only when the new value differs AND is non-empty.

### Parse-failures log

When `parseTalkHeader` cannot extract a speaker (or extracts something obviously wrong — heuristic: result is empty, contains `Listen to Audio`, or longer than 200 chars), append to `/data/logs/speaker-parse-failures.log`:

```
2026-05-13T15:42:11Z  file=eng/general-conference/1971/04/foo.md  raw_first_line="..."  fallback_value="..."
```

This is the corpus future heuristic / LLM cleanup will work from. Path configurable via env var (`GOSPEL_LOG_DIR`, default `/data/logs`).

## Verify

Local docker:

1. Re-parse a known-bad talk by ID (Wu's 2026-04 talk): record now shows `speaker = "Elder Wan-Liang Wu"`.
2. Sample 5 talks across decades (1971, 1985, 2001, 2015, 2026); spot-check speakers in DB.
3. Re-parse run: count rows changed, count failures logged.
4. `tail /data/logs/speaker-parse-failures.log` shows entries for genuine edge cases.
5. MCP: `mcp_gospel-engine_gospel_search { query: "give away all my sins", mode: "hybrid" }` returns Wu's talk with correct speaker after rebuild + reparse.

## Commit checkpoints

1. `fix(indexer): parseTalkHeader skips audio link + duplicated H1, strips 'By ' prefix`
2. `feat(indexer): log speaker-parse failures to GOSPEL_LOG_DIR for later cleanup`
3. `feat(api): POST /api/admin/reparse-speakers endpoint (re-runs parser, no full reindex)`

## Effort

Half session, including local re-parse + verification.
