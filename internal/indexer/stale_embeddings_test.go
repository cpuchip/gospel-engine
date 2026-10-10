package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// embedState compares every embedded row with what the embedder would write
// for its current text: stale = a stored embedding whose text differs,
// missing = a paragraph with no embedding, orphan = an embedding past the
// row's last paragraph.
type embedState struct{ stale, missing, orphan int }

func readEmbedState(t *testing.T, ctx context.Context, d *db.DB) embedState {
	t.Helper()
	var s embedState
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM scriptures x JOIN embeddings e
		ON e.source_type = 'scriptures' AND e.source_id = x.id AND e.layer = 'verse'
		WHERE e.content <> x.text`).Scan(&s.stale); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"talks", "manuals", "books"} {
		type row struct {
			id   int64
			text string
		}
		var rs []row
		rows, err := d.Pool.Query(ctx, `SELECT id, content FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.text); err != nil {
				t.Fatal(err)
			}
			rs = append(rs, r)
		}
		rows.Close()
		for _, r := range rs {
			got := map[string]string{}
			er, err := d.Pool.Query(ctx, `SELECT layer, content FROM embeddings WHERE source_type = $1 AND source_id = $2`, table, r.id)
			if err != nil {
				t.Fatal(err)
			}
			for er.Next() {
				var layer, content string
				if err := er.Scan(&layer, &content); err != nil {
					t.Fatal(err)
				}
				got[layer] = content
			}
			er.Close()
			want := splitParagraphs(r.text)
			matched := 0
			for p, para := range want {
				c, ok := got[paragraphLayer("paragraph", p)]
				switch {
				case !ok:
					s.missing++
				case c != para:
					s.stale++
					matched++
				default:
					matched++
				}
			}
			s.orphan += len(got) - matched
		}
	}
	return s
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paragraphsOf(prefix string, n int) []string {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, prefix+" paragraph "+string(rune('A'+i))+" with some words in it.")
	}
	return ps
}

// TestEditedSourceIsReembedded: editing a verse, a talk and a manual and
// indexing again leaves no stale, missing or orphan embeddings, and a file
// whose index failed is not recorded as indexed (GOSPEL_TEST_DATABASE_URL,
// local only; destructive; run with -p 1).
func TestEditedSourceIsReembedded(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, talks, manuals, books, embeddings, index_metadata RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	eng := filepath.Join(root, "eng")
	chapter := filepath.Join(eng, "scriptures", "bofm", "1-ne", "3.md")
	talk := filepath.Join(eng, "general-conference", "2099", "04", "11test.md")
	manual := filepath.Join(eng, "manual", "test-manual", "lesson-1.md")
	broken := filepath.Join(eng, "scriptures", "bofm", "1-ne", "4.md")

	verses := func(seven string) string {
		return "# 1 Nephi 3\n\n**6.** Therefore go, my son.\n\n**7.** " + seven + "\n\n**8.** And my father was glad.\n"
	}
	talkBody := func(paras []string) string {
		return "# A Test Talk\n\nBy Elder Test Speaker\n\nOf the Quorum of the Twelve Apostles\n\n" + strings.Join(paras, "\n\n") + "\n"
	}
	talkParas := paragraphsOf("Talk", 5)
	manualParas := paragraphsOf("Manual", 12)

	writeFile(t, chapter, verses("I will go and do the things which the Lord hath commanded."))
	writeFile(t, talk, talkBody(talkParas))
	writeFile(t, manual, "# Lesson 1\n\n"+strings.Join(manualParas, "\n\n")+"\n")
	writeFile(t, broken, "# 1 Nephi 4\n\n**1.** A verse Postgres refuses: \x00\n")

	idx := New(d, root, "")
	var failAt, calls atomic.Int32
	emb := fakeEmbedder(t, &failAt, &calls)
	pass := func() {
		t.Helper()
		if _, err := idx.IndexAll(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := idx.EmbedAll(ctx, emb); err != nil {
			t.Fatal(err)
		}
	}

	pass()
	if s := readEmbedState(t, ctx, d); s != (embedState{}) {
		t.Fatalf("after the first pass: %+v; want all zero", s)
	}
	var recorded int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM index_metadata WHERE file_path = $1`, broken).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("a file whose index failed was recorded as indexed, so no later pass retries it")
	}

	// The edits: verse 7's words; the talk's first paragraph plus two new
	// paragraphs; the manual cut from 12 paragraphs to 3.
	time.Sleep(10 * time.Millisecond) // a distinct mtime even where sizes match
	writeFile(t, chapter, verses("I will go and do the things which the Lord hath commanded, said Nephi."))
	talkParas[0] = "Edited " + talkParas[0]
	writeFile(t, talk, talkBody(append(talkParas, "A new paragraph one.", "A new paragraph two.")))
	writeFile(t, manual, "# Lesson 1\n\n"+strings.Join(manualParas[:3], "\n\n")+"\n")

	pass()
	if s := readEmbedState(t, ctx, d); s != (embedState{}) {
		t.Errorf("after editing and indexing again: %+v; want all zero", s)
	}
}

// TestRepairFindsDrift: drift written before the reindex cleared changed rows
// (a stale verse, a stale talk paragraph, a shrunk manual, a partial book) is
// found by RepairEmbeddings and re-embedded; an embed of text the row no
// longer holds writes nothing (GOSPEL_TEST_DATABASE_URL, local only;
// destructive; run with -p 1).
func TestRepairFindsDrift(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, talks, manuals, books, embeddings, index_metadata RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	idx := New(d, "", "")
	var failAt, calls atomic.Int32
	emb := fakeEmbedder(t, &failAt, &calls)

	var verseID, talkID, manualID, bookID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path)
		VALUES ('bofm','1-ne',3,7,'1 Nephi 3:7','I will go and do.','x.md') RETURNING id`).Scan(&verseID); err != nil {
		t.Fatal(err)
	}
	talkParas := paragraphsOf("Talk", 4)
	if err := d.Pool.QueryRow(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path)
		VALUES (2099,'04','S','T',$1,'t.md') RETURNING id`, strings.Join(talkParas, "\n\n")).Scan(&talkID); err != nil {
		t.Fatal(err)
	}
	manualParas := paragraphsOf("Manual", 6)
	if err := d.Pool.QueryRow(ctx, `INSERT INTO manuals (content_type, collection_id, title, content, file_path)
		VALUES ('manual','c','T',$1,'m.md') RETURNING id`, strings.Join(manualParas, "\n\n")).Scan(&manualID); err != nil {
		t.Fatal(err)
	}
	bookParas := paragraphsOf("Book", 5)
	if err := d.Pool.QueryRow(ctx, `INSERT INTO books (collection, section, title, content, file_path)
		VALUES ('c','s','T',$1,'b.md') RETURNING id`, strings.Join(bookParas, "\n\n")).Scan(&bookID); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.EmbedAll(ctx, emb); err != nil {
		t.Fatal(err)
	}
	if s := readEmbedState(t, ctx, d); s != (embedState{}) {
		t.Fatalf("after the first pass: %+v; want all zero", s)
	}

	// Drift as the old reindex left it: text rewritten under kept embeddings.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE scriptures SET text = 'I will go and do the things.' WHERE id = $1`, verseID)
	talkParas[2] = "Edited " + talkParas[2]
	exec(`UPDATE talks SET content = $1 WHERE id = $2`, strings.Join(talkParas, "\n\n"), talkID)
	exec(`UPDATE manuals SET content = $1 WHERE id = $2`, strings.Join(manualParas[:2], "\n\n"), manualID)
	exec(`DELETE FROM embeddings WHERE source_type = 'books' AND source_id = $1 AND layer IN ('paragraph_3','paragraph_4')`, bookID)
	before := readEmbedState(t, ctx, d)
	if before.stale != 2 || before.orphan != 4 || before.missing != 2 {
		t.Fatalf("drift setup: %+v; want stale 2, orphan 4, missing 2", before)
	}

	// An embed of text the row no longer holds writes nothing.
	if _, err := idx.embedRow(ctx, emb, "talks", "paragraph", talkID, "Some older text.", true); !errors.Is(err, errRowChanged) {
		t.Errorf("embedRow with text the row no longer holds: err %v; want errRowChanged", err)
	}
	// The guard is tried on a verse with no embedding yet, so only the text
	// check can stop the write.
	var bareID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path)
		VALUES ('bofm','1-ne',3,8,'1 Nephi 3:8','And my father was glad.','x.md') RETURNING id`).Scan(&bareID); err != nil {
		t.Fatal(err)
	}
	if ok, err := idx.embedVerse(ctx, emb, bareID, "Older words of verse 8.", false); err != nil || ok {
		t.Errorf("embedVerse with text the verse no longer holds: wrote %v, err %v; want nothing written", ok, err)
	}
	var bare int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE source_type = 'scriptures' AND source_id = $1`, bareID).Scan(&bare); err != nil || bare != 0 {
		t.Errorf("verse 8 has %d embeddings after a refused embed (err %v); want 0", bare, err)
	}
	if ok, err := idx.embedVerse(ctx, emb, bareID, "And my father was glad.", false); err != nil || !ok {
		t.Errorf("embedVerse with the verse's own text: wrote %v, err %v; want written", ok, err)
	}
	if s := readEmbedState(t, ctx, d); s != before {
		t.Fatalf("a refused embed changed the stored embeddings: %+v, was %+v", s, before)
	}

	dry, err := idx.RepairEmbeddings(ctx, emb, true)
	if err != nil {
		t.Fatal(err)
	}
	if wantDry := (EmbedRepairResult{Checked: 3, Partial: 1, Stale: 1, Shrunk: 1, StaleVerses: 1}); *dry != wantDry {
		t.Errorf("dry run result %+v; want %+v", *dry, wantDry)
	}
	if s := readEmbedState(t, ctx, d); s != before {
		t.Fatalf("a dry run changed the stored embeddings: %+v, was %+v", s, before)
	}

	res, err := idx.RepairEmbeddings(ctx, emb, false)
	if err != nil {
		t.Fatal(err)
	}
	want := EmbedRepairResult{Checked: 3, Partial: 1, Stale: 1, Shrunk: 1, StaleVerses: 1, Repaired: 4}
	if *res != want {
		t.Errorf("repair result %+v; want %+v", *res, want)
	}
	if s := readEmbedState(t, ctx, d); s != (embedState{}) {
		t.Errorf("after repair: %+v; want all zero", s)
	}
}

// TestEmbedWaitsOutReindex: an embed of a row that a reindex is rewriting
// waits for the reindex to commit, then writes nothing for the old text
// (GOSPEL_TEST_DATABASE_URL, local only; destructive; run with -p 1).
func TestEmbedWaitsOutReindex(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE scriptures, chapters, talks, manuals, books, embeddings, index_metadata RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	idx := New(d, "", "")
	var failAt, calls atomic.Int32
	emb := fakeEmbedder(t, &failAt, &calls)

	oldTalk := strings.Join(paragraphsOf("Talk", 3), "\n\n")
	var talkID, verseID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path)
		VALUES (2099,'04','S','T',$1,'t.md') RETURNING id`, oldTalk).Scan(&talkID); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `INSERT INTO scriptures (volume, book, chapter, verse, reference, text, file_path)
		VALUES ('bofm','1-ne',3,7,'1 Nephi 3:7','I will go and do.','x.md') RETURNING id`).Scan(&verseID); err != nil {
		t.Fatal(err)
	}

	// waitForLockWaiter returns once another backend is waiting on a lock, or
	// after 3 s (an embed that does not wait never shows up).
	waitForLockWaiter := func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n)
			if n > 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// rewrite holds a reindex's transaction open (row locked, text changed,
	// embeddings cleared) while embed runs, then commits.
	rewrite := func(lockSQL, updateSQL, sourceType string, id int64, newText string, embed func() error) error {
		t.Helper()
		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, lockSQL, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, updateSQL, newText, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM embeddings WHERE source_type = $1 AND source_id = $2`, sourceType, id); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- embed() }()
		waitForLockWaiter()
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return <-done
	}
	count := func(sourceType string, id int64) int {
		var n int
		_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE source_type = $1 AND source_id = $2`, sourceType, id).Scan(&n)
		return n
	}

	err = rewrite(`SELECT 1 FROM talks WHERE id = $1 FOR UPDATE`, `UPDATE talks SET content = $1 WHERE id = $2`,
		"talks", talkID, oldTalk+"\n\nA new paragraph.", func() error {
			_, err := idx.embedRow(ctx, emb, "talks", "paragraph", talkID, oldTalk, false)
			return err
		})
	if !errors.Is(err, errRowChanged) || count("talks", talkID) != 0 {
		t.Errorf("embedRow racing a reindex: err %v, %d embeddings; want errRowChanged and 0", err, count("talks", talkID))
	}

	var wrote bool
	err = rewrite(`SELECT 1 FROM scriptures WHERE id = $1 FOR UPDATE`, `UPDATE scriptures SET text = $1 WHERE id = $2`,
		"scriptures", verseID, "I will go and do the things.", func() error {
			var err error
			wrote, err = idx.embedVerse(ctx, emb, verseID, "I will go and do.", false)
			return err
		})
	if err != nil || wrote || count("scriptures", verseID) != 0 {
		t.Errorf("embedVerse racing a reindex: wrote %v, err %v, %d embeddings; want nothing written", wrote, err, count("scriptures", verseID))
	}
}

// TestLinkOnlyEditKeepsEmbeddings: a change that leaves every paragraph the
// same once the embedder has cleaned it (a link target, here) keeps the row's
// embeddings (GOSPEL_TEST_DATABASE_URL, local only; destructive; run with -p 1).
func TestLinkOnlyEditKeepsEmbeddings(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE manuals, embeddings RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	idx := New(d, "", "")
	var failAt, calls atomic.Int32
	emb := fakeEmbedder(t, &failAt, &calls)
	body := func(target string) string {
		return "# Lesson\n\nRead [Alma 32](" + target + ") and ponder it.\n\nA second paragraph."
	}
	upsert := func(text string) {
		t.Helper()
		if err := idx.upsertText(ctx, "manuals", "m.md", text, `
			INSERT INTO manuals (content_type, collection_id, title, content, file_path)
			VALUES ('manual', 'c', 'Lesson', $1, 'm.md')
			ON CONFLICT (file_path) DO UPDATE SET content = EXCLUDED.content`, text); err != nil {
			t.Fatal(err)
		}
	}
	upsert(body("../../scriptures/bofm/alma/32.md"))
	if _, err := idx.EmbedAll(ctx, emb); err != nil {
		t.Fatal(err)
	}
	var before, after int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE source_type = 'manuals'`).Scan(&before)
	upsert(body("https://www.churchofjesuschrist.org/study/scriptures/bofm/alma/32"))
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM embeddings WHERE source_type = 'manuals'`).Scan(&after)
	if before == 0 || after != before {
		t.Errorf("a link-only edit: %d embeddings before, %d after; want the same, nonzero", before, after)
	}
}
