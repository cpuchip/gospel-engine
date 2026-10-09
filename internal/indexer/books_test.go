package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

// librarySlugs is every book folder under gospel-library/eng/scriptures/{ot,nt,
// bofm,dc-testament,pgp} (listed 2026-10-09). A slug missing from bookNames
// prints as itself in search results and cross-references ("1-cor 1:27").
var librarySlugs = []string{
	"1-chr", "1-kgs", "1-sam", "2-chr", "2-kgs", "2-sam", "amos", "dan", "deut", "eccl", "esth", "ex", "ezek",
	"ezra", "gen", "hab", "hag", "hosea", "isa", "jer", "job", "joel", "jonah", "josh", "judg", "lam", "lev",
	"mal", "micah", "nahum", "neh", "num", "obad", "prov", "ps", "ruth", "song", "zech", "zeph",
	"1-cor", "1-jn", "1-pet", "1-thes", "1-tim", "2-cor", "2-jn", "2-pet", "2-thes", "2-tim", "3-jn", "acts",
	"col", "eph", "gal", "heb", "james", "john", "jude", "luke", "mark", "matt", "philem", "philip", "rev",
	"rom", "titus",
	"1-ne", "2-ne", "3-ne", "4-ne", "alma", "enos", "ether", "hel", "jacob", "jarom", "morm", "moro", "mosiah",
	"omni", "w-of-m",
	"dc", "od",
	"abr", "a-of-f", "js-h", "js-m", "moses",
}

func TestBookNamesComplete(t *testing.T) {
	if len(librarySlugs) != 88 {
		t.Fatalf("librarySlugs has %d entries, want 88 (39 OT + 27 NT + 15 BofM + 2 D&C + 5 PGP)", len(librarySlugs))
	}
	for _, s := range librarySlugs {
		if _, ok := bookNames[s]; !ok {
			t.Errorf("no display name for book slug %q", s)
		}
	}
	if got := formatReference("1-cor", 1, 27); got != "1 Corinthians 1:27" {
		t.Errorf("formatReference(1-cor) = %q", got)
	}
}

// TestBookNamesCoverLibrary walks a real checkout when GOSPEL_LIBRARY_ROOT is
// set, so a book folder added to the library later fails here, not in search.
func TestBookNamesCoverLibrary(t *testing.T) {
	root := os.Getenv("GOSPEL_LIBRARY_ROOT")
	if root == "" {
		t.Skip("set GOSPEL_LIBRARY_ROOT to check the map against a library checkout")
	}
	n := 0
	for _, v := range xrefVolumes {
		ents, err := os.ReadDir(filepath.Join(root, "eng", "scriptures", v))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if !e.IsDir() || e.Name()[0] == '.' { // a tool's dot-folder is not a book
				continue
			}
			n++
			if _, ok := bookNames[e.Name()]; !ok {
				t.Errorf("library book %s/%s has no display name", v, e.Name())
			}
		}
	}
	t.Logf("%d library books checked", n)
}
