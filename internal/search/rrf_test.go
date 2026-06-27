package search

import (
	"math"
	"testing"
)

// TestRRFMergeIsRankBasedNotScoreBlended proves the hybrid fusion is genuine
// Reciprocal Rank Fusion — score = Σ 1/(k+rank) with k=60 — and NOT a
// weighted-linear blend of the retrievers' raw scores.
//
// The proof rests on rrfMerge consuming only each list's *position*. We hand it
// results whose Score fields are absurd, inverted values: the FTS top hit carries
// a huge score, the doc that appears in both lists carries the smallest scores. A
// weighted-linear blend (w1·ftsScore + w2·semScore — what the substrate's
// world_entity_hybrid does as 0.45·lex + 0.55·sem) would let that huge raw score
// dominate and rank it #1. Genuine RRF ignores the magnitudes and rewards the doc
// that ranks well in BOTH lists. That ordering difference is the discriminator.
func TestRRFMergeIsRankBasedNotScoreBlended(t *testing.T) {
	// FTS list, already sorted best-first: A (rank 1), B (rank 2).
	// A's raw score is enormous on purpose; B's is tiny.
	fts := []Result{
		{SourceType: "scriptures", SourceID: 1, Score: 999.0}, // A — fts rank 1
		{SourceType: "scriptures", SourceID: 2, Score: 0.001}, // B — fts rank 2
	}
	// Semantic list, best-first: B (rank 1), C (rank 2). C is absent from FTS.
	sem := []Result{
		{SourceType: "scriptures", SourceID: 2, Score: 0.002}, // B — sem rank 1
		{SourceType: "scriptures", SourceID: 3, Score: 0.500}, // C — sem rank 2
	}

	got := rrfMerge(fts, sem, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 unique docs after fusion, got %d", len(got))
	}

	// --- Property 1: rank-based, not score-blended (the inverse hypothesis) ---
	// B appears in BOTH lists, so its reciprocal ranks sum to the highest RRF
	// score — even though its raw scores are the smallest in the input. Under a
	// weighted-linear blend, A's score of 999 would swamp everything and A would
	// rank #1. It does not. That is the falsifiable difference between real RRF
	// and a score blend.
	if got[0].SourceID != 2 {
		t.Fatalf("expected B (id=2, present in both lists) at rank #1 under RRF; got id=%d. "+
			"A weighted-linear score blend would rank A (id=1, raw score 999) first — "+
			"this fusion would NOT be rank-based.", got[0].SourceID)
	}

	// --- Property 2: a doc that is absent from FTS still surfaces ---
	// C is semantic-only. Real RRF lets single-list docs contribute, so C must
	// appear in the merged output (it would never appear in an FTS-gated path).
	var sawC bool
	for _, r := range got {
		if r.SourceID == 3 {
			sawC = true
		}
	}
	if !sawC {
		t.Fatal("C (id=3, semantic-only) did not surface; under RRF a doc present in only one " +
			"retriever must still contribute to the fused list")
	}

	// --- Property 3: exact RRF scores (locks k=60 and the 1-based rank formula) ---
	const k = 60.0
	wantA := 1.0 / (k + 1)         // fts rank 1 only
	wantB := 1.0/(k+2) + 1.0/(k+1) // fts rank 2 + sem rank 1
	wantC := 1.0 / (k + 2)         // sem rank 2 only
	byID := map[int64]float64{}
	for _, r := range got {
		byID[r.SourceID] = r.Score
	}
	for id, want := range map[int64]float64{1: wantA, 2: wantB, 3: wantC} {
		if math.Abs(byID[id]-want) > 1e-9 {
			t.Errorf("id=%d fused score = %.12f, want %.12f (Σ 1/(k+rank), k=60)", id, byID[id], want)
		}
	}

	// --- Property 4: rank ordering within the tail (fts rank 1 beats sem rank 2) ---
	// A (1/61) edges out C (1/62): a higher rank in one list outranks a lower
	// rank in the other, independent of the raw scores.
	posA, posC := -1, -1
	for i, r := range got {
		switch r.SourceID {
		case 1:
			posA = i
		case 3:
			posC = i
		}
	}
	if posA >= posC {
		t.Errorf("expected A (fts rank 1, 1/61) before C (sem rank 2, 1/62); got posA=%d posC=%d", posA, posC)
	}
}

// TestRRFMergeSingleListDegenerate confirms that when only one retriever returns
// hits, RRF degrades to that list's order (each doc scores 1/(k+rank)).
func TestRRFMergeSingleListDegenerate(t *testing.T) {
	only := []Result{
		{SourceType: "talks", SourceID: 10, Score: 0.9},
		{SourceType: "talks", SourceID: 11, Score: 0.8},
		{SourceType: "talks", SourceID: 12, Score: 0.7},
	}
	got := rrfMerge(only, nil, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(got))
	}
	wantOrder := []int64{10, 11, 12}
	for i, want := range wantOrder {
		if got[i].SourceID != want {
			t.Fatalf("position %d: got id=%d, want id=%d (single-list RRF must preserve input order)", i, got[i].SourceID, want)
		}
	}
	const k = 60.0
	if math.Abs(got[0].Score-1.0/(k+1)) > 1e-9 {
		t.Errorf("top single-list score = %.12f, want %.12f", got[0].Score, 1.0/(k+1))
	}
}
