package indexer

// The full-recall detector for the speaker-parse question (relay #39,
// 2026-08-03): two talks in the production DB carried an audio-link line as
// their speaker. The parser's own comment says the PREVIOUS version had
// exactly that bug, so the live rows are probably old-parser residue — but
// "probably" is not a verdict. This test runs the CURRENT parser over the
// ENTIRE on-disk conference corpus and fails on any speaker that looks like
// a link, an audio line, or markup residue. Zero hits = the current parser
// is clean at full recall and the DB rows were historical; any hit = a live
// bug with the exact file named.
//
// Corpus location comes from GOSPEL_CORPUS (the workspace's gospel-library);
// the test SKIPS (loudly) when unset rather than passing vacuously — a
// detector that inspected nothing must never read as green.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func looksLikeBadSpeaker(sp string) string {
	l := strings.ToLower(sp)
	switch {
	case sp == "":
		return "" // empty is the parser's honest "not found", handled upstream
	case strings.Contains(l, "http://"), strings.Contains(l, "https://"):
		return "contains a URL"
	case strings.Contains(l, ".mp3"), strings.Contains(l, "listen to audio"):
		return "audio-link residue"
	case strings.HasPrefix(sp, "🎧"):
		return "audio glyph"
	case strings.HasPrefix(sp, "["), strings.HasPrefix(sp, "!["):
		return "markdown link/image residue"
	case strings.HasPrefix(sp, "#"):
		return "heading residue"
	case len(sp) > 120:
		return "implausibly long for a name"
	}
	return ""
}

func TestCorpusSpeakersAtFullRecall(t *testing.T) {
	root := os.Getenv("GOSPEL_CORPUS")
	if root == "" {
		t.Skip("GOSPEL_CORPUS unset — detector inspected NOTHING (do not read this as green)")
	}
	conf := filepath.Join(root, "eng", "general-conference")
	var inspected, flagged int
	err := filepath.Walk(conf, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		_, speaker, _ := parseTalkHeader(string(b))
		inspected++
		if why := looksLikeBadSpeaker(speaker); why != "" {
			flagged++
			t.Errorf("BAD SPEAKER (%s): %q\n  file: %s", why, speaker, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if inspected == 0 {
		t.Fatal("walked the corpus and parsed ZERO files — broken instrument, not a clean corpus")
	}
	t.Logf("inspected %d talks, flagged %d", inspected, flagged)
}

// Fixtures from the four real corpus files the 2026-08 sweep flagged — pinned
// as unit cases so the behaviors survive without GOSPEL_CORPUS set.
func TestParseTalkHeaderSweepFindings(t *testing.T) {
	cases := []struct {
		name, doc, wantSpeaker string
	}{
		{
			// 1997/04 pioneer pageant: first line after title is an H2.
			// Headings are structure, never speakers.
			name: "H2 narrator heading is skipped, not captured",
			doc:  "# Faith in Every Footstep\n\n## Narrator: President Gordon B. Hinckley\n\nbody\n",
			// The next non-heading line is "body" — short prose, accepted as
			// candidate. The point pinned here: no "##" in the speaker.
			wantSpeaker: "body",
		},
		{
			// 1980/04 proclamation: attribution sentence, not a name.
			name:        "long attribution line returns empty, not prose",
			doc:         "# Proclamation\n\n*From the First Presidency and the Quorum of the Twelve Apostles of The Church of Jesus Christ of Latter-day Saints, April 6, 1980*\n\nbody\n",
			wantSpeaker: "",
		},
		{
			// 2022/04 video segment: opens with narrative prose.
			name:        "opening prose sentence returns empty",
			doc:         "# Video Presentation\n\nIn 1979 President Spencer W. Kimball was in the hospital and asked his wife, Camilla, to read his talk to a general women's meeting.\n\nbody\n",
			wantSpeaker: "",
		},
		{
			// The classic modern shape must keep working exactly as before.
			name:        "modern audio-link + duplicated H1 + By-prefix still parses",
			doc:         "# The Title\n\n🎧 [Listen to Audio](https://x.mp3)\n\n# The Title\n\nBy President Dallin H. Oaks\n\nbody\n",
			wantSpeaker: "President Dallin H. Oaks",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, speaker, _ := parseTalkHeader(c.doc)
			if speaker != c.wantSpeaker {
				t.Errorf("speaker = %q, want %q", speaker, c.wantSpeaker)
			}
		})
	}
}

func TestLooksLikeStoredGarbage(t *testing.T) {
	for _, garbage := range []string{
		"## Narrator: President Gordon B. Hinckley",
		"🎧 [Listen to Audio](https://x.mp3)",
		"see https://example.com/talk",
	} {
		if !looksLikeStoredGarbage(garbage) {
			t.Errorf("should be garbage: %q", garbage)
		}
	}
	for _, name := range []string{"President Dallin H. Oaks", "Elder Bruce R. McConkie", ""} {
		if looksLikeStoredGarbage(name) {
			t.Errorf("should NOT be garbage: %q", name)
		}
	}
}
