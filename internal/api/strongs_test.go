package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/auth"
	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

func TestCanonStrongs(t *testing.T) {
	for in, want := range map[string]string{
		"G26": "G26", "g0026": "G26", "H07225": "H7225", " h430 ": "H430", "H0853a": "H853",
		"G0": "", "26": "", "X26": "", "G123456": "", "": "",
	} {
		if got := canonStrongs(in); got != want {
			t.Errorf("canonStrongs(%q) = %q, want %q", in, got, want)
		}
	}
}

// Bad input is refused before any database work (DB is nil here).
func TestStrongsRejectsBadParams(t *testing.T) {
	router := (&Server{Cfg: &config.Config{}}).Router()
	for _, target := range []string{
		"/api/strongs/define?number=love",
		"/api/strongs/define?number=",
		"/api/strongs/search?word=" + url.QueryEscape("lo(ve"),
		"/api/strongs/search?word=",
		"/api/strongs/verse?reference=" + url.QueryEscape("John 3"),
		"/api/strongs/verse?reference=nowhere",
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

// TestStrongsAgainstPostgres seeds a verse and its tagging by hand (no corpus
// needed) and drives the Strong's endpoints and gospel_get strongs=true
// through the real router (GOSPEL_TEST_DATABASE_URL, local only; destructive;
// run with -p 1).
func TestStrongsAgainstPostgres(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`TRUNCATE kjv_words, strongs_lexicon, scriptures RESTART IDENTITY`)
	router := (&Server{Cfg: &config.Config{}, DB: d}).Router()
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/api/strongs/define?number=G26"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("define before the load: status %d, want 503", rec.Code)
	}

	exec(`INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path) VALUES
		('nt','john',3,16,'John 3:16','For God so loved the world...','x'),
		('bofm','ether',12,27,'Ether 12:27','And if men come unto me...','y')`)
	exec(`INSERT INTO strongs_lexicon (number, lang, lemma, translit, strongs_def, kjv_def, step_gloss) VALUES
		('G25','greek','ἀγαπάω','agapáō','to love (in a social or moral sense)','(be-)love(-ed)','to love'),
		('G26','greek','ἀγάπη','agápē','love, i.e. affection or benevolence','(feast of) charity, love','love'),
		('G2316','greek','θεός','theós','a deity','God, god','God'),
		('G3588','greek','ὁ','ho','the definite article','the','the')`)
	exec(`INSERT INTO kjv_words (volume, book, chapter, verse, position, word, numbers) VALUES
		('nt','john',3,16,0,'For','{}'),
		('nt','john',3,16,1,'God','{G3588,G2316}'),
		('nt','john',3,16,2,'so loved','{G25}')`)

	rec := get("/api/strongs/define?number=g0026")
	var def struct {
		Entry     strongsEntry `json:"entry"`
		KJVVerses int          `json:"kjv_verses"`
		Source    string       `json:"source"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &def) != nil || def.Entry.Lemma != "ἀγάπη" || !strings.Contains(def.Source, "CrossWire") {
		t.Errorf("define G26: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/api/strongs/define?number=G9999"); rec.Code != http.StatusNotFound {
		t.Errorf("define G9999: status %d, want 404", rec.Code)
	}

	rec = get("/api/strongs/search?word=love")
	var srch struct {
		Results []struct {
			Number string `json:"number"`
			Score  int    `json:"score"`
		} `json:"results"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &srch) != nil || len(srch.Results) < 2 || srch.Results[0].Number != "G26" {
		t.Errorf("search love: %d %s", rec.Code, rec.Body.String())
	}

	rec = get("/api/strongs/verse?reference=" + url.QueryEscape("John 3:16"))
	var vr struct {
		Verses []struct {
			Reference string        `json:"reference"`
			Words     []strongsWord `json:"words"`
		} `json:"verses"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &vr) != nil || len(vr.Verses) != 1 {
		t.Fatalf("verse John 3:16: %d %s", rec.Code, rec.Body.String())
	}
	w := vr.Verses[0].Words
	if len(w) != 3 || w[0].Word != "For" || len(w[0].Numbers) != 0 ||
		len(w[1].Lexicon) != 2 || w[1].Lexicon[0].Number != "G3588" || w[1].Lexicon[1].Gloss != "God" ||
		w[2].Lexicon[0].Translit != "agapáō" {
		t.Errorf("John 3:16 words: %+v", w)
	}
	if rec := get("/api/strongs/verse?reference=" + url.QueryEscape("Ether 12:27")); rec.Code != http.StatusBadRequest {
		t.Errorf("Strong's for a Book of Mormon verse: status %d, want 400", rec.Code)
	}

	rec = get("/api/get?reference=" + url.QueryEscape("John 3:16") + "&strongs=true")
	var g struct {
		Strongs map[string][]strongsWord `json:"strongs"`
		Source  string                   `json:"strongs_source"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &g) != nil || len(g.Strongs["16"]) != 3 || g.Source == "" {
		t.Errorf("gospel_get strongs: %d %s", rec.Code, rec.Body.String())
	}
}
