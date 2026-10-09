package indexer

import (
	"bufio"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVersesFromText(t *testing.T) {
	for _, c := range []struct {
		text string
		ch   int
		want []int
	}{
		{"Heb. 11:1", 11, []int{1}},
		{"Alma 32:21, 26", 32, []int{21, 26}},
		{"Ether 12:6–9", 12, []int{6, 7, 8, 9}},
		{"Ether 12:6-9", 12, []int{6, 7, 8, 9}},
		{"Ps. 119:1–176", 119, []int{1}},                      // a long range gives its first verse
		{"Moses 6:21–7:69", 6, []int{21}},                     // cross-chapter range linked to its first chapter
		{"Moses 6:21–7:69", 7, []int{69}},                     // ... and to its second
		{"D&C 21:1 (1–3)", 21, []int{1}},                      // a parenthesis is context
		{"Gen. 1", 1, nil},                                    // no verse: chapter-level
		{"1 Ne. 1:1; 21:2", 1, []int{1, 21}},                  // "1:" inside "21:" is not chapter 1; the loader's quirk on a two-reference link kept (21)
		{"JST Gen. 18:23 (Gen. 18:22 note a)", 18, []int{22}}, // JST text, KJV verse from the parenthesis
		{"JST Gen. 50:24–38", 50, nil},                        // JST numbering with no KJV parenthesis
		{"Isa. 29:13–14; 2 Ne. 27:25", 29, []int{13, 14, 2}},  // the same quirk: the loader's output, reproduced
	} {
		if got := versesFromText(c.text, c.ch); !reflect.DeepEqual(got, c.want) {
			t.Errorf("versesFromText(%q, %d) = %v, want %v", c.text, c.ch, got, c.want)
		}
	}
}

// TestBuildCrossReferencesCorpus rebuilds the table's rows from a real
// gospel-library checkout and compares them, line for line, with the file the
// one-time loader produced (69,572 rows on 2026-10-09). Both paths come from
// the environment so the test skips where the corpus is absent.
//
//	GOSPEL_LIBRARY_ROOT=…/gospel-library GOSPEL_XREF_GOLDEN=…/cross_references.tsv go test ./internal/indexer -run Corpus
func TestBuildCrossReferencesCorpus(t *testing.T) {
	root, golden := os.Getenv("GOSPEL_LIBRARY_ROOT"), os.Getenv("GOSPEL_XREF_GOLDEN")
	if root == "" || golden == "" {
		t.Skip("set GOSPEL_LIBRARY_ROOT and GOSPEL_XREF_GOLDEN to compare against the loader's file")
	}
	rows, sum, err := BuildCrossReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if sum.UnmatchedSources != 0 || sum.UnmatchedTargets != 0 {
		t.Errorf("unmatched sources %d, targets %d; want 0, 0", sum.UnmatchedSources, sum.UnmatchedTargets)
	}
	f, err := os.Open(golden)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var want []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		want = append(want, strings.TrimRight(sc.Text(), "\r"))
	}
	if len(rows) != len(want) {
		t.Errorf("rows = %d, want %d", len(rows), len(want))
	}
	shown := 0
	for i := 0; i < len(rows) && i < len(want) && shown < 10; i++ {
		if got := rows[i].TSV(); got != want[i] {
			t.Errorf("row %d:\n got  %s\n want %s", i, got, want[i])
			shown++
		}
	}
	t.Logf("chapters %d, rows %d, by type %v, skipped %v, %s", sum.Chapters, sum.Rows, sum.ByType, sum.Skipped, sum.Duration)
}
