package search

import (
	"context"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
	"github.com/pgvector/pgvector-go"
)

// The vector arm of a speaker-filtered search returns only that speaker's
// paragraphs, nearest first, even when another speaker's paragraph is the
// nearest row in the whole table.
func TestNearestSpeakerFilter(t *testing.T) {
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
	if _, err := d.Pool.Exec(ctx, `INSERT INTO talks (year, month, speaker, title, content, file_path) VALUES
		(2021, '10', 'Elder David A. Bednar', 'Covenants', 'x', 'a'),
		(2021, '04', 'President Russell M. Nelson', 'Faith', 'y', 'b')`); err != nil {
		t.Fatal(err)
	}
	// unit vectors along different axes; the query sits closest to Nelson's row
	vec := func(axis int, w float32) pgvector.Vector {
		v := make([]float32, 768)
		v[axis] = w
		v[0] += 1 - w
		return pgvector.NewVector(v)
	}
	rows := []struct {
		talk int64
		text string
		v    pgvector.Vector
	}{
		{2, "nelson near", vec(1, 0.1)},
		{1, "bednar mid", vec(1, 0.5)},
		{1, "bednar far", vec(2, 0.9)},
		{2, "nelson far", vec(3, 0.9)},
	}
	for i, r := range rows {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO embeddings (source_type, source_id, layer, content, embedding) VALUES ('talks', $1, 'p'||$2::text, $3, $4)`,
			r.talk, i, r.text, r.v); err != nil {
			t.Fatal(err)
		}
	}
	s := &Searcher{DB: d}
	q := vec(0, 1)
	got, err := s.nearest(ctx, q, Options{Speaker: "bednar", Sources: []Source{SourceTalks}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].text != "bednar mid" || got[1].text != "bednar far" {
		t.Errorf("speaker=bednar: %+v; want bednar mid then bednar far", got)
	}
	all, err := s.nearest(ctx, q, Options{Sources: []Source{SourceTalks}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].text != "nelson near" {
		t.Errorf("no speaker: %+v; want all four, nelson near first", all)
	}
}
