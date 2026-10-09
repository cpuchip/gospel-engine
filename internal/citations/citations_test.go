package citations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sample mirrors the index's answer shape (read from a live response for
// Ether 12:27 on 2026-10-09): a <ul class="referencesblock"> of <li> items,
// conference items with watch/listen links, a Journal of Discourses item
// without. The talks here are invented.
const sample = `<div class="versenum">27<!--comment
    --></div>
        <ul class="referencesblock"><!-- comment
                --><li><a href="javascript:void(0);" class="refcounter" onclick="getTalk('8851', '145818');"><!--comment
                    --><div class="reference referencewatch referencelisten">2026-A:0, Test Speaker One</div><div class="talktitle talktitlewatch talktitlelisten">Weak Things &amp; Strong</div></a><a href="javascript:void(0);" onclick="watchTalk('8851', 'https://www.churchofjesuschrist.org/study/general-conference/2026/04/99test?lang=eng');" class="watchlink"><div class="watchtalk">Watch</div></a><!--comment
                --></li><!--comment
                --><li><a href="javascript:void(0);" class="refcounter" onclick="getTalk('42', '7');"><!--comment
                    --><div class="reference">JD 19:81b, Test Speaker Two</div><div class="talktitle">A Discourse</div></a></li>
        </ul>`

func TestParse(t *testing.T) {
	got, err := Parse(sample, "https://scriptures.byu.edu")
	if err != nil {
		t.Fatal(err)
	}
	want := []Citation{
		{Reference: "2026-A:0", Speaker: "Test Speaker One", Title: "Weak Things & Strong",
			IndexURL: "https://scriptures.byu.edu/#:t2293$145818:",
			TalkURL:  "https://www.churchofjesuschrist.org/study/general-conference/2026/04/99test?lang=eng"},
		{Reference: "JD 19:81b", Speaker: "Test Speaker Two", Title: "A Discourse",
			IndexURL: "https://scriptures.byu.edu/#:t2a$7:"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d citations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("citation %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

// An item with ids but no title means the page changed: refuse, don't guess.
func TestParseRefusesAChangedShape(t *testing.T) {
	broken := strings.Replace(sample, `<div class="talktitle">A Discourse</div>`, ``, 1)
	if _, err := Parse(broken, "https://scriptures.byu.edu"); err == nil {
		t.Error("an item without a title parsed")
	}
	if got, err := Parse(`<p>no citations</p>`, "https://scriptures.byu.edu"); err != nil || len(got) != 0 {
		t.Errorf("an empty answer gave %v, %v; want none, nil", got, err)
	}
}

func TestLookupCachesForADay(t *testing.T) {
	var hits atomic.Int32
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotPath, gotUA = r.URL.Path+"?"+r.URL.RawQuery, r.UserAgent()
		fmt.Fprint(w, sample)
	}))
	defer srv.Close()
	c := New("someone@example.org", "test")
	c.BaseURL = srv.URL
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	r1, err := c.Lookup(ctx, "ether", 12, "27", "Ether 12:27")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Cached || r1.Count != 2 || r1.Source != Source || r1.Reference != "Ether 12:27" {
		t.Errorf("first lookup: %+v", r1)
	}
	if want := "/citation_index/citation_ajax/Any/1830/2027/all/s/f/218/12?verses=27"; gotPath != want {
		t.Errorf("upstream path %q, want %q (through the current year)", gotPath, want)
	}
	if !strings.Contains(gotUA, "someone@example.org") || !strings.HasPrefix(gotUA, "gospel-engine/") {
		t.Errorf("User-Agent %q", gotUA)
	}
	now = now.Add(23 * time.Hour)
	r2, _ := c.Lookup(ctx, "ether", 12, "27", "Ether 12:27")
	if !r2.Cached || hits.Load() != 1 {
		t.Errorf("within a day: cached %v, upstream hits %d; want true, 1", r2.Cached, hits.Load())
	}
	now = now.Add(2 * time.Hour)
	r3, _ := c.Lookup(ctx, "ether", 12, "27", "Ether 12:27")
	if r3.Cached || hits.Load() != 2 {
		t.Errorf("after a day: cached %v, upstream hits %d; want false, 2", r3.Cached, hits.Load())
	}
	if _, err := c.Lookup(ctx, "tg", 1, "1", "x"); err != ErrUnknownBook {
		t.Errorf("unknown book: %v", err)
	}
}

func TestCacheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sample) }))
	defer srv.Close()
	c := New("", "test")
	c.BaseURL, c.MaxItems = srv.URL, 3
	for v := 1; v <= 10; v++ {
		if _, err := c.Lookup(context.Background(), "ether", 12, fmt.Sprint(v), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(c.cache); n > 3 {
		t.Errorf("cache holds %d entries, cap 3", n)
	}
}

// TestParseLiveSample parses a saved real answer when CITATION_SAMPLE names
// one (the index's data stays out of the repo), and checks every item parsed.
func TestParseLiveSample(t *testing.T) {
	p := os.Getenv("CITATION_SAMPLE")
	if p == "" {
		t.Skip("set CITATION_SAMPLE to a saved citation_ajax answer")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(string(b), "https://scriptures.byu.edu")
	if err != nil {
		t.Fatal(err)
	}
	items := strings.Count(string(b), "getTalk(")
	talks, untitled := 0, 0
	for _, c := range got {
		// a title can be genuinely empty in the index (TPJS entries carry
		// <div class="talktitle"></div>); speaker and link never are
		if c.Speaker == "" || c.IndexURL == "" {
			t.Errorf("incomplete citation %+v", c)
		}
		if c.Title == "" {
			untitled++
		}
		if c.TalkURL != "" {
			talks++
		}
	}
	t.Logf("%d without a title in the index", untitled)
	if len(got) != items {
		t.Errorf("parsed %d of %d items", len(got), items)
	}
	t.Logf("%d citations, %d with a talk link; first %+v", len(got), talks, got[0])
}
