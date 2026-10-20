package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

func TestBookCollectionAllowed(t *testing.T) {
	open := &Indexer{}
	if !open.bookCollectionAllowed("creators-playbook") || !open.bookCollectionAllowed("misc") {
		t.Error("an empty allowlist must admit every collection")
	}
	idx := &Indexer{BooksCollections: []string{"lecture-on-faith"}}
	if !idx.bookCollectionAllowed("lecture-on-faith") {
		t.Error("a listed collection was refused")
	}
	for _, c := range []string{"creators-playbook", "debugging", "misc", "Lecture-On-Faith", ""} {
		if idx.bookCollectionAllowed(c) {
			t.Errorf("%q admitted by an allowlist that names only lecture-on-faith", c)
		}
	}
}

// TestBooksAllowlistSkipsOtherCollections: with an allowlist, folders it does
// not name are never read into books or index_metadata; without one every
// folder is (GOSPEL_TEST_DATABASE_URL, local only; destructive; run with -p 1).
func TestBooksAllowlistSkipsOtherCollections(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	truncate := func() {
		if _, err := d.Pool.Exec(ctx, `TRUNCATE books, embeddings, index_metadata RESTART IDENTITY CASCADE`); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(truncate) // runs before the pool closes; the api package expects an empty books table

	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lecture-on-faith/lecture-1.md", `# Lecture First

Faith is the first principle.
`)
	write("creators-playbook/01-intro.md", `# Introduction

A third-party book that must not enter the public index.
`)
	write("debugging/rule-1.md", `# Understand the System

Another third-party book.
`)
	write("stray.md", `# A file at the root

Lands in the misc collection.
`)

	collections := func() map[string]int {
		rows, err := d.Pool.Query(ctx, `SELECT collection, count(*) FROM books GROUP BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]int{}
		for rows.Next() {
			var c string
			var n int
			if err := rows.Scan(&c, &n); err != nil {
				t.Fatal(err)
			}
			got[c] = n
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	metadata := func() int {
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM index_metadata WHERE content_type = 'books'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	same := func(got, want map[string]int) bool {
		if len(got) != len(want) {
			return false
		}
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}
		return true
	}

	truncate()
	idx := &Indexer{DB: d, BooksRoot: root, BooksCollections: []string{"lecture-on-faith"}}
	if err := idx.indexBooks(ctx, &Result{}); err != nil {
		t.Fatal(err)
	}
	if got := collections(); !same(got, map[string]int{"lecture-on-faith": 1}) {
		t.Errorf("allowlisted index holds %v, want lecture-on-faith only", got)
	}
	if n := metadata(); n != 1 {
		t.Errorf("index_metadata holds %d books rows, want 1", n)
	}

	truncate()
	idx.BooksCollections = nil
	if err := idx.indexBooks(ctx, &Result{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"lecture-on-faith": 1, "creators-playbook": 1, "debugging": 1, "misc": 1}
	if got := collections(); !same(got, want) {
		t.Errorf("open index holds %v, want %v", got, want)
	}
	if n := metadata(); n != 4 {
		t.Errorf("index_metadata holds %d books rows, want 4", n)
	}
}
