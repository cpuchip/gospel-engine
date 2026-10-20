package api

import (
	"context"
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

// A 404 names the part of the reference the library does not have, so a
// caller (or a model reading the error) can correct the reference rather
// than guess whether the book, the chapter or the verse was wrong. A 400
// for an unparseable reference is unchanged.
func TestNotFoundSaysWhat(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, graph_edges RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	// /api/related answers 503 until a graph exists; one edge is enough here.
	if _, err := d.Pool.Exec(ctx, `INSERT INTO graph_edges (src, dst, edge_type, reverse) VALUES ('v:bofm:ether:12:27', 'v:bofm:ether:15:34', 'footnote', false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path) VALUES
		('bofm','ether',12,27,'Ether 12:27','And if men come unto me...','a'),
		('bofm','ether',15,34,'Ether 15:34','Now the last words...','b')`); err != nil {
		t.Fatal(err)
	}
	router := (&Server{Cfg: &config.Config{}, DB: d}).Router()
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	cases := []struct {
		ref, want string
		code      int
	}{
		{"Ether 99:1", "Ether has 15 chapters", http.StatusNotFound},
		{"Ether 12:99", "Ether 12 has 27 verses", http.StatusNotFound},
		{"Ether 12:50-60", "Ether 12 has 27 verses", http.StatusNotFound},
		{"Ether 13:1", "Ether 13 is not in the library", http.StatusNotFound},
		{"Ether 99", "Ether has 15 chapters", http.StatusNotFound},
		{"Fakebook 1:1", "could not parse reference", http.StatusBadRequest},
	}
	for _, path := range []string{"/api/get?reference=", "/api/related?reference="} {
		for _, c := range cases {
			rec := get(path + url.QueryEscape(c.ref))
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("%s%s: %d %q; want %d containing %q", path, c.ref, rec.Code, strings.TrimSpace(rec.Body.String()), c.code, c.want)
			}
		}
	}
}
