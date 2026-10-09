package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/auth"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/indexer"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// Bad parameters are refused before any database work (DB is nil here, so
// reaching a query would panic).
func TestRelatedRejectsBadParams(t *testing.T) {
	router := (&Server{Cfg: &config.Config{}}).Router()
	for _, target := range []string{
		"/api/related?reference=Ether+12:27&hops=3",
		"/api/related?reference=Ether+12:27&hops=x",
		"/api/related?reference=Ether+12:27&limit=0",
		"/api/related?reference=Ether+12:27&kinds=videos",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", target, rec.Code)
		}
	}
}

func TestParseNodeKeys(t *testing.T) {
	vol, book, ch, v, ok := parseVerseKey("bofm/ether/12:27")
	if !ok || vol != "bofm" || book != "ether" || ch != 12 || v != 27 {
		t.Errorf("parseVerseKey = %q %q %d %d %v", vol, book, ch, v, ok)
	}
	for _, bad := range []string{"bofm/ether/12", "ether/12:27", "bofm/ether/x:1"} {
		if _, _, _, _, ok := parseVerseKey(bad); ok {
			t.Errorf("parseVerseKey(%q) ok", bad)
		}
	}
	id, p, ok := parseParagraphKey("123#4")
	if !ok || id != 123 || p != 4 {
		t.Errorf("parseParagraphKey = %d %d %v", id, p, ok)
	}
	if _, _, ok := parseParagraphKey("123"); ok {
		t.Error("parseParagraphKey without # ok")
	}
}

// TestRelatedAgainstPostgres indexes a copy of a real library's scriptures,
// adds one talk that cites Ether 12:27, rebuilds the graph, and drives
// /api/related through the real router (GOSPEL_TEST_DATABASE_URL, local only,
// and GOSPEL_LIBRARY_ROOT; destructive; run with -p 1).
func TestRelatedAgainstPostgres(t *testing.T) {
	dsn := testdb.URL(t)
	lib := os.Getenv("GOSPEL_LIBRARY_ROOT")
	if lib == "" {
		t.Skip("set GOSPEL_LIBRARY_ROOT to a gospel-library checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, study_aids, cross_references, index_metadata, talks, manuals, graph_edges RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	root := t.TempDir()
	copyTree(t, filepath.Join(lib, "eng", "scriptures"), filepath.Join(root, "eng", "scriptures"))
	talkPath := filepath.Join(root, "eng", "general-conference", "2099", "10", "itest.md")
	content := "Opening words with no links.\n\nWeakness becomes strength, as in [Ether 12:27](../../../scriptures/bofm/ether/12.md)."
	var talkID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path)
		VALUES (2099, '10', 'Test Speaker', 'Test Talk', $1, $2) RETURNING id`, content, talkPath).Scan(&talkID); err != nil {
		t.Fatal(err)
	}
	// Before any build, the endpoint says so instead of answering "no links".
	{
		req := httptest.NewRequest(http.MethodGet, "/api/related?reference="+url.QueryEscape("Ether 12:27"), nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		(&Server{Cfg: &config.Config{}, DB: d}).Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("related before the graph exists: status %d, want 503", rec.Code)
		}
	}
	idx := indexer.New(d, root, "")
	res, err := idx.IndexAll(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	t.Logf("indexed verses=%d xrefs=%d graph links=%d errors=%d", res.ScripturesIndexed, res.XrefRows, res.GraphEdges, res.Errors)
	if res.GraphEdges == 0 {
		t.Fatal("index pass did not rebuild the graph")
	}

	router := (&Server{Cfg: &config.Config{}, DB: d}).Router()
	type body struct {
		Seeds   []string        `json:"seeds"`
		Results []relatedResult `json:"results"`
	}
	get := func(q url.Values) body {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/related?"+q.Encode(), nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		start := time.Now()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", q.Encode(), rec.Code, rec.Body.String())
		}
		var b body
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d results in %s", q.Encode(), len(b.Results), time.Since(start))
		return b
	}

	// One hop from Ether 12:27: its footnotes (out) and the test talk (in).
	b := get(url.Values{"reference": {"Ether 12:27"}, "limit": {"200"}})
	var sawTalk, sawTG, sawVerse bool
	for _, r := range b.Results {
		switch {
		case r.Kind == "talk" && r.ID == talkID:
			sawTalk = true
			if r.Via.Edge != "talk_cites" || r.Via.Direction != "in" || r.Paragraph == nil || *r.Paragraph != 1 ||
				!strings.Contains(r.Text, "Ether 12:27") || r.Title != "Test Talk" {
				t.Errorf("talk result wrong: %+v via %+v", r, r.Via)
			}
		case r.Kind == "study_aid" && strings.HasPrefix(r.Title, "TG "):
			sawTG = true
			if r.Via.Edge != "footnote" || r.Via.Direction != "out" {
				t.Errorf("TG result via %+v", r.Via)
			}
		case r.Kind == "verse":
			sawVerse = true
			if r.Text == "" || r.Reference == "" {
				t.Errorf("verse result unresolved: %+v", r)
			}
		}
		if r.Hops != 1 {
			t.Errorf("hops=1 walk returned a %d-hop result", r.Hops)
		}
	}
	if !sawTalk || !sawTG || !sawVerse {
		t.Errorf("one hop: talk %v, TG %v, verse %v; want all", sawTalk, sawTG, sawVerse)
	}

	// kinds filter
	for _, r := range get(url.Values{"reference": {"Ether 12:27"}, "kinds": {"talks"}}).Results {
		if r.Kind != "talk" {
			t.Errorf("kinds=talks returned %s", r.Kind)
		}
	}

	// Two hops reaches farther, and names what it went through.
	two := get(url.Values{"reference": {"Ether 12:27"}, "hops": {"2"}, "limit": {"200"}})
	far := 0
	for _, r := range two.Results {
		if r.Hops == 2 {
			far++
			if r.Via.Through == nil || r.Via.Through.Kind == "" {
				t.Errorf("2-hop result without a resolved through: %+v", r)
			}
		}
	}
	if far == 0 {
		t.Error("hops=2 found nothing beyond one hop")
	}

	// A two-hop walk from a whole long chapter is refused, not run.
	{
		req := httptest.NewRequest(http.MethodGet, "/api/related?hops=2&reference="+url.QueryEscape("Psalms 119"), nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("hops=2 from Psalms 119 (177 seeds): status %d, want 400", rec.Code)
		}
	}
	// One hop from a chapter counts each verse that links a target as its own route.
	ch := get(url.Values{"reference": {"Ether 12"}, "limit": {"5"}})
	if len(ch.Results) == 0 || ch.Results[0].Links < 2 {
		t.Errorf("top one-hop result from Ether 12 has links %v, want several seed routes", ch.Results)
	}

	// Seeding from the talk finds the verse it cites.
	found := false
	for _, r := range get(url.Values{"type": {"talks"}, "id": {strconv.FormatInt(talkID, 10)}}).Results {
		if r.Kind == "verse" && r.Reference == "Ether 12:27" && r.Via.Direction == "out" {
			found = true
		}
	}
	if !found {
		t.Error("talk seed did not reach Ether 12:27")
	}
}
