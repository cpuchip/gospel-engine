package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The presets put the right prefix on each side, and an unknown name is refused.
func TestTaskPrefixes(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = append(got, in.Input)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL, "m", 0)

	for _, name := range []string{"", "none"} {
		if err := c.SetTaskPrefixes(name); err != nil {
			t.Fatal(err)
		}
		got = nil
		_, _ = c.EmbedQuery(ctx, "faith")
		_, _ = c.EmbedDocument(ctx, "Faith is things hoped for.")
		if got[0] != "faith" || got[1] != "Faith is things hoped for." {
			t.Errorf("preset %q sent %q", name, got)
		}
	}
	if err := c.SetTaskPrefixes("nomic-v1.5"); err != nil {
		t.Fatal(err)
	}
	got = nil
	_, _ = c.EmbedQuery(ctx, "faith")
	_, _ = c.EmbedDocument(ctx, "Faith is things hoped for.")
	_, _ = c.Embed(ctx, "test")
	want := []string{"search_query: faith", "search_document: Faith is things hoped for.", "test"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nomic-v1.5 call %d sent %q, want %q", i, got[i], want[i])
		}
	}
	if err := c.SetTaskPrefixes("nomic"); err == nil {
		t.Error("unknown preset accepted")
	}
}
