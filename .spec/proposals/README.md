---
created: 2026-05-13
last_updated: 2026-05-13
parent: scripture-study/.spec/proposals/gospel-engine/phase1.5-ergonomics.md
---

# gospel-engine-v2 Rollup — Phase 1.5 + Research

Waterfall ratified 2026-05-13 with Michael (steward + visionary).

## Order of execution

| # | Phase | Touches | Reindex? | Repo |
|---|-------|---------|----------|------|
| 1 | [phase-1.5a — mode enum docs](phase-1.5a-mode-enum-docs.md) | docs only | no | scripture-study |
| 2 | [phase-1.5b — handleGet rewrite](phase-1.5b-handleget-rewrite.md) | server + MCP schema | no | gospel-engine-v2 |
| 3 | [phase-1.5c — cross-refs (opt-in)](phase-1.5c-cross-references.md) | server + MCP schema | no | gospel-engine-v2 |
| 4 | [phase-1.5d — speaker fix](phase-1.5d-speaker-fix.md) | indexer + admin job | targeted (talks UPDATE) | gospel-engine-v2 |
| 5 | [phase-1.5e — study aids](phase-1.5e-study-aids.md) | schema + indexer + search | full walk for `tg/bd/gs/jst` | gospel-engine-v2 |
| 6 | **single reindex** in local docker → prod (admin POST) | — | yes | gospel-engine-v2 |
| 7 | [phase-3-research — v3 architecture spike](phase-3-research-v3-architecture.md) | research memo only | no | gospel-engine-v2/research/ |

## Deferred (NOT in this rollup)

- **Phase 2 — TITSW migration to v2.** Multi-session, LLM-heavy. Scope retained in the [parent v1 spec](../../../../.spec/proposals/gospel-engine/main.md). Tackle after Phase 1.5 + Phase 3 research land.

## Dev rules for this rollup

- **Local dev loop:** `docker-compose.local.yml` against local PG. Local copy of `/gospel-library/` mounted as `/data/gospel-library`. Reindex via the admin endpoint after each indexer phase.
- **Commit cadence:** at least one git commit per phase, ideally a few (one per logical change inside a phase). Commits stay local until end of rollup.
- **Push trigger:** single `git push` at the end of all 1.5 phases. Dokploy auto-deploys. Then call the prod admin reindex endpoint once.
- **Ratify each phase before moving on:** complete + verify Phase N before starting Phase N+1.
