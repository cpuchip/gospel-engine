package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/indexer"
	"github.com/cpuchip/gospel-engine/internal/ratelimit"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// copyTree copies src into dst (files and folders only).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// TestV3FixesAgainstPostgres drives the v3 fix list on a disposable Postgres
// and a copy of a real library's scriptures (GOSPEL_TEST_DATABASE_URL and
// GOSPEL_LIBRARY_ROOT; destructive, local only, run with -p 1):
//
//   - an index pass rebuilds cross_references from the footnotes (69,572 rows
//     on the 2026-10-09 library) through the real IndexAll wiring;
//   - no verse reference prints a slug, and RepairReferences fixes one that does;
//   - /api/get with cross_refs labels aid rows from study_aids ("TG …") and
//     never prints the old "grace 0" fallback;
//   - a key with rate_limit 2 gets 429 with Retry-After on its third request.
func TestV3FixesAgainstPostgres(t *testing.T) {
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
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, study_aids, cross_references, index_metadata, api_tokens RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	root := t.TempDir()
	copyTree(t, filepath.Join(lib, "eng", "scriptures"), filepath.Join(root, "eng", "scriptures"))
	idx := indexer.New(d, root, "")
	res, err := idx.IndexAll(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	t.Logf("indexed chapters=%d verses=%d aids=%d xrefs=%d errors=%d in %s",
		res.ChaptersIndexed, res.ScripturesIndexed, res.StudyAidsIndexed, res.XrefRows, res.Errors, res.Duration)
	if res.Errors != 0 {
		t.Errorf("index errors = %d", res.Errors)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_references`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 69572 || res.XrefRows != n {
		t.Errorf("cross_references = %d (result says %d), want 69572", n, res.XrefRows)
	}

	// References: none slug-shaped ("1-cor 1:27", "gen 1:1").
	slugRe := `^([a-z]|[0-9]-)`
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM scriptures WHERE reference ~ $1`, slugRe).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d verse references still print a slug", n)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE scriptures SET reference = '1-cor 1:27' WHERE book = '1-cor' AND chapter = 1 AND verse = 27`); err != nil {
		t.Fatal(err)
	}
	rr, err := idx.RepairReferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := d.Pool.QueryRow(ctx, `SELECT reference FROM scriptures WHERE book = '1-cor' AND chapter = 1 AND verse = 27`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if rr.Changed != 1 || ref != "1 Corinthians 1:27" {
		t.Errorf("repair changed %d, reference %q; want 1, \"1 Corinthians 1:27\"", rr.Changed, ref)
	}

	// The real router, a real token, a real limiter.
	srv := &Server{Cfg: &config.Config{}, DB: d, Limiter: ratelimit.New()}
	router := srv.Router()
	_, raw, err := d.CreateAPIToken(ctx, "system:itest", "itest-v3", nil, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/api/get?reference=" + url.QueryEscape("Ether 12:27") + "&cross_refs=true")
	if rec.Code != 200 {
		t.Fatalf("get Ether 12:27: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Xrefs []xrefRow `json:"cross_references"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Xrefs) != 14 {
		t.Errorf("Ether 12:27 has %d cross-references, want 14", len(body.Xrefs))
	}
	tg := 0
	for _, x := range body.Xrefs {
		t.Logf("  %-9s %s", x.ReferenceType, x.Reference)
		if strings.HasSuffix(x.Reference, " 0") || (x.Reference != "" && x.Reference[0] >= 'a' && x.Reference[0] <= 'z') {
			t.Errorf("unlabelled target %+v", x)
		}
		if x.ReferenceType == "tg" {
			tg++
			if !strings.HasPrefix(x.Reference, "TG ") {
				t.Errorf("tg row labelled %q", x.Reference)
			}
		}
	}
	if tg == 0 {
		t.Error("no Topical Guide rows on Ether 12:27")
	}

	// Rate limit: the token allows 2 per minute and has spent 1.
	if rec := get("/api/get?reference=" + url.QueryEscape("Ether 12:27")); rec.Code == http.StatusTooManyRequests {
		t.Fatal("second request limited")
	}
	rec = get("/api/get?reference=" + url.QueryEscape("Ether 12:27"))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third request: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}
