---
phase: 1.5a
status: ready
repo: scripture-study
created: 2026-05-13
---

# Phase 1.5a — Mode enum + tool docs fix

**Binding problem.** `.github/copilot-instructions.md` and `CLAUDE.md` document `gospel_search { mode: "combined" }`. The server's MCP schema enum is actually `["keyword", "semantic", "hybrid"]`. Every session that follows the docs and calls `mode: "combined"` burns one tool call on `Your input to the tool was invalid`.

## Change

In `.github/copilot-instructions.md`:
- Locate the MCP-tools table and any other text that mentions the search mode parameter.
- Replace `"combined"` with `"hybrid"` everywhere.
- While in the file, audit any other claims about `gospel_search` / `gospel_get` parameters against what the server actually accepts (e.g., the `ref` vs `reference` mention — once Phase 1.5b ships, both are valid; until then, document `reference`).

In `CLAUDE.md`:
- Same edits as above.

No code changes. No rebuild. No reindex.

## Verify

1. `grep_search` for `"combined"` across `.github/` and `CLAUDE.md` — zero matches in mode-context.
2. Live call `mcp_gospel-engine_gospel_search { query: "test", mode: "hybrid", limit: 1 }` succeeds.
3. Live call with `mode: "combined"` returns the existing invalid-value error (confirms the docs were the lie, not the server).

## Commit checkpoints

- Single commit: `docs: gospel_search mode 'combined' → 'hybrid' (matches server enum)`

## Effort

15 minutes.
