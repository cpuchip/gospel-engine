package api

import "testing"

// The parser accepts the forms a reader types or dictates: the Church's own
// abbreviations (1 Ne. 3:7, Moro. 10:4, JS—H 1:17), common shorter ones
// (Eth 12:27, Mt 5:14), and spoken ordinals (First Nephi 3:7). It still
// fails closed on anything it cannot name: a wrong verse is worse than none.
func TestParseReferenceForms(t *testing.T) {
	cases := []struct {
		in   string
		want parsedRef
	}{
		{"1 Nephi 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"1 Ne. 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"1 Ne 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"1Ne 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"First Nephi 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"1st Nephi 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"I Nephi 3:7", parsedRef{Book: "1-ne", Chapter: 3, Verse: 7}},
		{"Third Nephi 11:10-11", parsedRef{Book: "3-ne", Chapter: 11, Verse: 10, EndVerse: 11}},
		{"Eth 12:27", parsedRef{Book: "ether", Chapter: 12, Verse: 27}},
		{"Ether 12:27", parsedRef{Book: "ether", Chapter: 12, Verse: 27}},
		{"Moro. 10:4", parsedRef{Book: "moro", Chapter: 10, Verse: 4}},
		{"Hel. 5:12", parsedRef{Book: "hel", Chapter: 5, Verse: 12}},
		{"W of M 1:7", parsedRef{Book: "w-of-m", Chapter: 1, Verse: 7}},
		{"Words of Mormon 1:7", parsedRef{Book: "w-of-m", Chapter: 1, Verse: 7}},
		{"A of F 1:13", parsedRef{Book: "a-of-f", Chapter: 1, Verse: 13}},
		{"JS—H 1:17", parsedRef{Book: "js-h", Chapter: 1, Verse: 17}},
		{"JS-M 1:22", parsedRef{Book: "js-m", Chapter: 1, Verse: 22}},
		{"Joseph Smith—History 1:17", parsedRef{Book: "js-h", Chapter: 1, Verse: 17}},
		{"D&C 93:24-30", parsedRef{Book: "dc", Chapter: 93, Verse: 24, EndVerse: 30}},
		{"Doctrine and Covenants 93:24", parsedRef{Book: "dc", Chapter: 93, Verse: 24}},
		{"Matt 5:14", parsedRef{Book: "matt", Chapter: 5, Verse: 14}},
		{"Mt 5:14", parsedRef{Book: "matt", Chapter: 5, Verse: 14}},
		{"Jn 3:16", parsedRef{Book: "john", Chapter: 3, Verse: 16}},
		{"1 Cor. 13:4", parsedRef{Book: "1-cor", Chapter: 13, Verse: 4}},
		{"Second Corinthians 12:9", parsedRef{Book: "2-cor", Chapter: 12, Verse: 9}},
		{"Philip. 4:13", parsedRef{Book: "philip", Chapter: 4, Verse: 13}},
		{"Phil 4:13", parsedRef{Book: "philip", Chapter: 4, Verse: 13}},
		{"Ps. 23:1", parsedRef{Book: "ps", Chapter: 23, Verse: 1}},
		{"Psalm 23", parsedRef{Book: "ps", Chapter: 23}},
		{"Gen. 1:1", parsedRef{Book: "gen", Chapter: 1, Verse: 1}},
		{"Song of Songs 2:1", parsedRef{Book: "song", Chapter: 2, Verse: 1}},
		{"Mosiah 4", parsedRef{Book: "mosiah", Chapter: 4}},
		{"Mos. 4:27", parsedRef{Book: "mosiah", Chapter: 4, Verse: 27}},
		{"Abr. 3:22", parsedRef{Book: "abr", Chapter: 3, Verse: 22}},
	}
	for _, c := range cases {
		got, ok := parseReference(c.in)
		if !ok || got != c.want {
			t.Errorf("parseReference(%q) = %+v, %v; want %+v", c.in, got, ok, c.want)
		}
	}
}

func TestParseReferenceFailsClosed(t *testing.T) {
	for _, in := range []string{"", "Ether", "Fakebook 1:1", "Mor 1:1", "Ne 3:7", "Ether 0:1", "Ether 12:0", "Nephi 3:7"} {
		if got, ok := parseReference(in); ok {
			t.Errorf("parseReference(%q) = %+v; want a parse failure", in, got)
		}
	}
}
