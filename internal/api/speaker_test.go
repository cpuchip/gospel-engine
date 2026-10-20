package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/auth"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/search"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// speaker narrows a search to one speaker's talks, matched as a substring of
// the name the corpus stores ("Elder David A. Bednar"), and implies
// sources=talks. Keyword mode is exercised here; the vector path has its own
// test in the search package.
func TestSearchSpeakerFilter(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE talks, scriptures RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path) VALUES
		(2021, '10', 'Elder David A. Bednar', 'With the Power of God in Great Glory', 'Honoring covenants arms us with righteousness and faith.', 'a'),
		(2021, '04', 'President Russell M. Nelson', 'Christ Is Risen; Faith in Him Will Move Mountains', 'Faith in Jesus Christ is the foundation of all belief.', 'b')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path) VALUES
		('bofm','moro',7,39,'Moroni 7:39','ye have faith in Christ because of your meekness','c')`); err != nil {
		t.Fatal(err)
	}
	router := (&Server{Cfg: &config.Config{}, DB: d, Searcher: &search.Searcher{DB: d}}).Router()
	get := func(target string) []search.Result {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
		}
		var out struct {
			Results []search.Result `json:"results"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Results
	}
	all := get("/api/search?q=faith&mode=keyword")
	if len(all) != 3 {
		t.Fatalf("without a speaker: %d results, want 3 (two talks and a verse): %+v", len(all), all)
	}
	got := get("/api/search?q=faith&mode=keyword&speaker=bednar")
	if len(got) != 1 || got[0].SourceType != "talks" || got[0].Speaker != "Elder David A. Bednar" {
		t.Errorf("speaker=bednar: %+v; want only the Bednar talk", got)
	}
	// speaker implies talks even when the caller asked for every source
	got = get("/api/search?q=faith&mode=keyword&speaker=Nelson&sources=scriptures,talks")
	if len(got) != 1 || got[0].Speaker != "President Russell M. Nelson" {
		t.Errorf("speaker=Nelson with scriptures asked for: %+v; want only the Nelson talk", got)
	}
}
