package indexer

import "testing"

func TestParseTalkHeader(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantTitle   string
		wantSpeaker string
	}{
		{
			name: "modern church.org with audio link, duplicated H1, By prefix",
			input: "# Give Away All My Sins\n\n" +
				"🎧 [Listen to Audio](https://media.churchofjesuschrist.org/foo.mp3)\n\n" +
				"# Give Away All My Sins\n\n" +
				"By Elder Wan-Liang Wu\n\n" +
				"Of the Quorum of the Seventy\n\n" +
				"Body paragraph 1.\n",
			wantTitle:   "Give Away All My Sins",
			wantSpeaker: "Elder Wan-Liang Wu",
		},
		{
			name: "audio link without emoji",
			input: "# Title\n\n" +
				"[Listen to Audio](https://x.mp3)\n\n" +
				"By President Dallin H. Oaks\n\n" +
				"Body.\n",
			wantTitle:   "Title",
			wantSpeaker: "President Dallin H. Oaks",
		},
		{
			name: "older format, no audio, no By prefix",
			input: "# Title\n\n" +
				"Elder Bruce R. McConkie\n\n" +
				"Of the Quorum of the Twelve Apostles\n\n" +
				"Body.\n",
			wantTitle:   "Title",
			wantSpeaker: "Elder Bruce R. McConkie",
		},
		{
			name: "lowercase by prefix",
			input: "# Title\n\n" +
				"by elder so-and-so\n\n" +
				"Body.\n",
			wantTitle:   "Title",
			wantSpeaker: "elder so-and-so",
		},
		{
			name: "speaker with italic emphasis",
			input: "# Title\n\n" +
				"By *Elder X*\n\n" +
				"Body.\n",
			wantTitle:   "Title",
			wantSpeaker: "*Elder X*", // "By " stripped; * intact (cleanInlineMarkdown leaves emphasis alone)
		},
		{
			name: "no speaker present",
			input: "# Title\n\n" +
				"🎧 [Listen to Audio](https://x.mp3)\n",
			wantTitle:   "Title",
			wantSpeaker: "",
		},
		{
			name: "modern with parenthetical citation epigraph between H1 and By",
			input: "# All Who Have Endured Valiantly\n\n" +
				"🎧 [Listen to Audio](https://x.mp3)\n\n" +
				"# All Who Have Endured Valiantly\n\n" +
				"([Doctrine and Covenants 121:29](../../../scriptures/dc-testament/dc/121.md))\n\n" +
				"By Elder David A. Bednar\n\n" +
				"Of the Quorum of the Twelve Apostles\n\n" +
				"Body.\n",
			wantTitle:   "All Who Have Endured Valiantly",
			wantSpeaker: "Elder David A. Bednar",
		},
		{
			name:        "no title at all",
			input:       "Just some prose.\n",
			wantTitle:   "",
			wantSpeaker: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, speaker, _ := parseTalkHeader(c.input)
			if title != c.wantTitle {
				t.Errorf("title: got %q, want %q", title, c.wantTitle)
			}
			if speaker != c.wantSpeaker {
				t.Errorf("speaker: got %q, want %q", speaker, c.wantSpeaker)
			}
		})
	}
}
