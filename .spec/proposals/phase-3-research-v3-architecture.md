---
phase: 3-research
status: ready
repo: gospel-engine-v2
depends_on: phase-1.5 complete
created: 2026-05-13
---

# Phase 3 (Research) — v3 Architecture Investigation

**Binding problem.** Two RAG architecture ideas have been proposed for evaluation:

- **Proxy-pointer RAG** (Towards Data Science article — claims 100% accuracy with smarter retrieval). Some structural similarity to v2's hybrid keyword+semantic flow.
- **LightRAG** (alternative graph-aware RAG framework).

Both predate any decision about a v3. The question is: does v2 already do enough of this implicitly, or is there a structural change worth making?

This phase produces ONE memo. No code. Decision output is whether to continue iterating on v2 or draft a v3 architecture proposal.

## Inputs

- Article: https://towardsdatascience.com/proxy-pointer-rag-structure-meets-scale-100-accuracy-with-smarter-retrieval/
- LightRAG framework docs (URL TBD by research agent — start from project repo / paper)
- Current v2 search code: `scripts/gospel-engine-v2/internal/search/`
- Current v2 schema: `scripts/gospel-engine-v2/internal/db/`
- Hybrid search RRF implementation (per v2-hosted.md)

## Deliverable

Single file at `scripts/gospel-engine-v2/research/v3-architecture-evaluation.md`:

1. **Proxy-pointer RAG** — faithful 1-paragraph summary + diagram-in-words.
2. **LightRAG** — same shape.
3. **Map against v2 current architecture** — table: which mechanisms we already have (keyword FTS, pgvector HNSW, RRF reranking, cross-references graph), which we don't (proxy structures, light-weight graph traversal at query time, etc.).
4. **Gap analysis** — what would change if we adopted each idea? Cost vs. benefit for our corpus (~42K verses, ~10K talks, ~85K cross-refs).
5. **Recommendation** — one of: (a) v2 already covers it, no change; (b) discrete v2.x enhancements worth filing as Phase 1.6/1.7; (c) v3 is warranted, draft architecture proposal.

## Constraints

- **No code changes** in this phase.
- Use the project's existing research agent (Sonnet/Opus). Cap research-agent budget at 1 session.
- Keep the memo concrete: every claim about proxy-pointer or LightRAG must cite the source article/section.

## Verify

1. Memo exists at the deliverable path.
2. Memo includes the mapping table.
3. Memo ends with a single labeled recommendation (a/b/c).
4. Decision recorded in this phase file's frontmatter (`decision: a|b|c` after the memo lands).

## Commit checkpoints

1. `research: v3 architecture evaluation memo (proxy-pointer + lightrag)`

## Effort

One research-agent session.

## Out of scope

- Drafting the v3 proposal (that's the next phase if recommendation is c).
- Benchmark runs (would be Phase 3b).
- Any change to v2 code.
