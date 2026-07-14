package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/embed"
)

// stubEmbedServer is an OpenAI-compatible /embeddings backend whose availability
// can be flipped at runtime to reproduce the real-world failure that motivated
// the fix: the backend is down when the engine boots, then comes up later. While
// `up` is false it answers 503 (so Ping fails); while true it returns a valid
// embedding (so Ping succeeds). hits counts every request so a test can assert
// how many probes actually fired.
func stubEmbedServer(t *testing.T, up *atomic.Bool, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if !up.Load() {
			http.Error(w, "embedding backend down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSemanticReprobeRecoversWithoutRestart is the primary oracle for the bug:
// the engine starts with a dead embedding backend (semantic gated off), the
// backend later comes up, and after the cooldown a semantic request enables
// semantic WITHOUT a process restart. It also proves the cooldown gate holds the
// gate off until the interval elapses.
func TestSemanticReprobeRecoversWithoutRestart(t *testing.T) {
	var up atomic.Bool // false: the backend is down at boot (boot ping failed)
	srv := stubEmbedServer(t, &up, nil)

	client := embed.New(srv.URL, "test-model", 2*time.Second)
	const cooldown = 60 * time.Millisecond
	// embedEnabled=false models a failed boot ping — semantic starts gated off.
	s := NewSearcher(nil, client, false, cooldown, "both", "")
	ctx := context.Background()

	// Backend down: the gate is off and no client is handed to the semantic path,
	// so /api/search?mode=semantic returns the "disabled" error.
	if s.SemanticEnabled() {
		t.Fatal("gate should start disabled after a failed boot ping")
	}
	if c := s.semanticClient(ctx); c != nil {
		t.Fatal("semanticClient must return nil while the backend is down (probe #1 fails)")
	}

	// Backend recovers — but the cooldown since probe #1 has not elapsed, so the
	// gate must stay off. This is the cadence guard that prevents hammering a
	// still-flapping backend on every request.
	up.Store(true)
	if c := s.semanticClient(ctx); c != nil {
		t.Fatal("semanticClient must stay nil before the re-probe cooldown elapses")
	}
	if s.SemanticEnabled() {
		t.Fatal("gate must not enable before the cooldown elapses")
	}

	// After the cooldown, the next semantic request re-probes, the now-healthy
	// backend answers, and semantic is enabled from then on — no restart.
	time.Sleep(cooldown + 20*time.Millisecond)
	if c := s.semanticClient(ctx); c == nil {
		t.Fatal("semanticClient must return a client after the backend recovered and the cooldown elapsed")
	}
	if !s.SemanticEnabled() {
		t.Fatal("gate should be enabled after a successful re-probe — /api/health reports exactly this bool")
	}

	// Latched on: once enabled the gate stays enabled and takes the fast path even
	// if the backend blips (a transient blip falls back per-query for hybrid, or
	// errors for pure-semantic — it does not re-disable the gate).
	up.Store(false)
	if c := s.semanticClient(ctx); c == nil {
		t.Fatal("once enabled the gate must stay enabled on the fast path")
	}
}

// TestSemanticNoReprobeStaysDeadWithInfiniteCooldown is the inverse oracle. With
// an effectively-infinite cooldown, re-probing never fires after the first failed
// probe — which is exactly the original bug's behavior (probe once at boot, never
// again). It proves the recovery in the test above comes from the re-probe, not
// from luck: suppress the re-probe and a recovered backend stays gated off.
func TestSemanticNoReprobeStaysDeadWithInfiniteCooldown(t *testing.T) {
	var up atomic.Bool // down at boot
	srv := stubEmbedServer(t, &up, nil)

	client := embed.New(srv.URL, "test-model", 2*time.Second)
	// A cooldown far longer than the test == "never re-probe within the run".
	s := NewSearcher(nil, client, false, time.Hour, "both", "")
	ctx := context.Background()

	// Probe #1 fails (backend down) and claims the (1-hour) cooldown window.
	if c := s.semanticClient(ctx); c != nil {
		t.Fatal("expected nil while the backend is down")
	}

	// Backend recovers, but no further probe is permitted within the hour, so the
	// gate stays off no matter how many requests arrive — the pre-fix behavior.
	up.Store(true)
	for i := 0; i < 5; i++ {
		if c := s.semanticClient(ctx); c != nil {
			t.Fatal("with re-probing suppressed, a recovered backend must NOT re-enable — proves the fix, not chance, drives recovery")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.SemanticEnabled() {
		t.Fatal("gate must stay disabled while re-probing is suppressed")
	}
}

// TestSemanticReprobeConcurrentSingleProbe proves the gate is safe and correct
// under the concurrent load /api/search actually serves: a burst of simultaneous
// semantic requests against a down backend must fire exactly ONE probe (the
// cooldown is claimed under the lock before the network round-trip), not one per
// request. Run under `go test -race` this also proves the gate is data-race free.
func TestSemanticReprobeConcurrentSingleProbe(t *testing.T) {
	var up atomic.Bool // down: every probe fails, so none latches the gate on
	var hits atomic.Int64
	srv := stubEmbedServer(t, &up, &hits)

	client := embed.New(srv.URL, "test-model", 2*time.Second)
	// Long cooldown so the whole burst shares a single probe window.
	s := NewSearcher(nil, client, false, time.Hour, "both", "")
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.semanticClient(ctx)
		}()
	}
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Fatalf("a concurrent burst must serialize to exactly 1 backend probe, got %d", got)
	}
	if s.SemanticEnabled() {
		t.Fatal("gate must remain disabled: every probe hit a down backend")
	}
}
