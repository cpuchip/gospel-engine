package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/embed"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

func TestParagraphLayer(t *testing.T) {
	if paragraphLayer("paragraph", 0) != "paragraph" || paragraphLayer("paragraph", 7) != "paragraph_7" {
		t.Error("paragraph layer names changed: existing embeddings would stop matching")
	}
}

// fakeEmbedder returns a 768-d vector per text and fails the Nth call when failAt > 0.
func fakeEmbedder(t *testing.T, failAt *atomic.Int32, calls *atomic.Int32) *embed.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if f := failAt.Load(); f > 0 && n == f {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		v := make([]float32, 768)
		v[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": v}}})
	}))
	t.Cleanup(srv.Close)
	return embed.New(srv.URL, "test-model", 5*time.Second)
}

// TestEmbedRowIsAtomic: a failure mid-row writes nothing; the next pass embeds
// the row whole; a partial row from the old writes is found and repaired
// (GOSPEL_TEST_DATABASE_URL, local only; destructive; run with -p 1).
func TestEmbedRowIsAtomic(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE talks, embeddings RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	var paras []string
	for i := 0; i < 6; i++ {
		paras = append(paras, fmt.Sprintf("Paragraph number %d of the test talk.", i))
	}
	var id int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path)
		VALUES (2099,'10','S','T',$1,'t.md') RETURNING id`, strings.Join(paras, "\n\n")).Scan(&id); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE source_type='talks' AND source_id=$1`, id).Scan(&n)
		return n
	}
	idx := New(d, "", "")
	var failAt, calls atomic.Int32
	emb := fakeEmbedder(t, &failAt, &calls)

	failAt.Store(4) // the 4th paragraph's embed call fails
	errs := 0
	if _, err := idx.embedTextRows(ctx, emb, "talks", "content", "paragraph", &errs); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 || errs != 1 {
		t.Fatalf("after a failure mid-row: %d rows written, %d errors; want 0, 1", n, errs)
	}
	failAt.Store(0)
	if _, err := idx.embedTextRows(ctx, emb, "talks", "content", "paragraph", &errs); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 6 {
		t.Fatalf("next pass: %d rows, want all 6", n)
	}

	// A partial row as the old one-by-one writes could leave it: paragraphs 0..2 only.
	if _, err := d.Pool.Exec(ctx, `DELETE FROM embeddings WHERE source_id=$1 AND layer IN ('paragraph_3','paragraph_4','paragraph_5')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.embedTextRows(ctx, emb, "talks", "content", "paragraph", &errs); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Fatalf("the ordinary pass should not see the partial row (that is the defect): %d rows", n)
	}
	res, err := idx.RepairEmbeddings(ctx, emb)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 6 || res.Partial != 1 || res.Repaired != 1 {
		t.Errorf("repair: %d rows, result %+v; want 6, 1 partial repaired", n, res)
	}
}
