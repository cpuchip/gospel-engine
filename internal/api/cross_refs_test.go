package api

import "testing"

func TestXrefLabel(t *testing.T) {
	v := 27
	for _, c := range []struct {
		x                  xrefRow
		verseRef, aidTitle string
		want               string
	}{
		{xrefRow{ReferenceType: "footnote", TargetBook: "ether", TargetChapter: 12, TargetVerse: &v}, "Ether 12:27", "", "Ether 12:27"},
		{xrefRow{ReferenceType: "footnote", TargetBook: "ps", TargetChapter: 119}, "", "", "Psalms 119"},
		{xrefRow{ReferenceType: "footnote", TargetBook: "1-cor", TargetChapter: 1, TargetVerse: &v}, "", "", "1 Corinthians 1:27"},
		{xrefRow{ReferenceType: "tg", TargetVolume: "tg", TargetBook: "grace"}, "", "Grace", "TG Grace"},
		{xrefRow{ReferenceType: "bd", TargetVolume: "bd", TargetBook: "aaron"}, "", "Aaron", "BD Aaron"},
		{xrefRow{ReferenceType: "jst", TargetVolume: "jst", TargetBook: "jst-rev", TargetChapter: 2}, "", "JST, Revelation 2", "JST, Revelation 2"},
		{xrefRow{ReferenceType: "tg", TargetVolume: "tg", TargetBook: "grace"}, "", "", "TG grace"}, // aid row not indexed
		{xrefRow{ReferenceType: "jst", TargetVolume: "jst", TargetBook: "jst-rev", TargetChapter: 2}, "", "", "JST jst-rev 2"},
	} {
		if got := xrefLabel(c.x, c.verseRef, c.aidTitle); got != c.want {
			t.Errorf("xrefLabel(%+v) = %q, want %q", c.x, got, c.want)
		}
	}
}
