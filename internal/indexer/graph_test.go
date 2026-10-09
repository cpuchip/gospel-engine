package indexer

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLinkTargets(t *testing.T) {
	talk := "/data/gospel-library/eng/general-conference/2024/10/18oaks.md"
	tg := "/data/gospel-library/eng/scriptures/tg/faith.md"
	ch := "/data/gospel-library/eng/scriptures/bofm/ether/12.md"
	for _, c := range []struct {
		src, text, href string
		want            []string
	}{
		{talk, "Luke 18:22", "../../../scriptures/nt/luke/18.md", []string{"v:nt/luke/18:22"}},
		{talk, "Matthew 22:36–40", "../../../scriptures/nt/matt/22.md",
			[]string{"v:nt/matt/22:36", "v:nt/matt/22:37", "v:nt/matt/22:38", "v:nt/matt/22:39", "v:nt/matt/22:40"}},
		{talk, "Mosiah 4", "../../../scriptures/bofm/mosiah/4.md", []string{"c:bofm/mosiah/4"}},
		{talk, "a manual", "../../../manual/come-follow-me/1.md", nil},
		{talk, "a title page", "../../../scriptures/bofm/title-page.md", nil},
		{tg, "Assurance", "assurance.md", []string{"tg:assurance"}},
		{tg, "Heb. 11:1", "../nt/heb/11.md", []string{"v:nt/heb/11:1"}},
		{ch, "TG Weakness", "../../tg/weakness.md", []string{"tg:weakness"}},
		{ch, "JST Gen. 50:24", "../../jst/jst-gen/50.md", []string{"jst:jst-gen/50"}},
		{`C:\lib\eng\scriptures\bofm\ether\12.md`, "Moro. 7:33", "../../bofm/moro/7.md", []string{"v:bofm/moro/7:33"}},
	} {
		if got := LinkTargets(c.src, c.text, c.href); !reflect.DeepEqual(got, c.want) {
			t.Errorf("LinkTargets(%q, %q) = %v, want %v", c.text, c.href, got, c.want)
		}
	}
}

// paragraphLinks must number paragraphs exactly as splitParagraphs does, or a
// talk node would name the wrong passage.
func TestParagraphLinksMatchSplitParagraphs(t *testing.T) {
	content := strings.Join([]string{
		"First paragraph cites [Alma 32:21](../../../scriptures/bofm/alma/32.md).",
		"   ",
		"<sup>[1](#fn-1)</sup>", // cleans to nothing: not a paragraph
		"Second, no links.",
		"Third cites [Ether 12:27](../../../scriptures/bofm/ether/12.md) and [Moro. 10:4](../../../scriptures/bofm/moro/10.md).",
	}, "\n\n")
	paras := splitParagraphs(content)
	if len(paras) != 3 {
		t.Fatalf("splitParagraphs kept %d, want 3: %q", len(paras), paras)
	}
	var got []string
	paragraphLinks(content, func(p int, text, _ string) {
		if !strings.Contains(paras[p], strings.SplitN(text, " ", 2)[0]) {
			t.Errorf("link %q placed in paragraph %d: %q", text, p, paras[p])
		}
		got = append(got, text)
	})
	if want := []string{"Alma 32:21", "Ether 12:27", "Moro. 10:4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v, want %v", got, want)
	}
}

func TestXrefTargetKey(t *testing.T) {
	v := 27
	for _, c := range []struct {
		r    XrefRow
		want string
	}{
		{XrefRow{ReferenceType: "footnote", TargetVolume: "bofm", TargetBook: "ether", TargetChapter: 12, TargetVerse: &v}, "v:bofm/ether/12:27"},
		{XrefRow{ReferenceType: "footnote", TargetVolume: "ot", TargetBook: "ps", TargetChapter: 119}, "c:ot/ps/119"},
		{XrefRow{ReferenceType: "tg", TargetVolume: "tg", TargetBook: "weakness"}, "tg:weakness"},
		{XrefRow{ReferenceType: "jst", TargetVolume: "jst", TargetBook: "jst-gen", TargetChapter: 50}, "jst:jst-gen/50"},
	} {
		if got := xrefTargetKey(c.r); got != c.want {
			t.Errorf("xrefTargetKey = %q, want %q", got, c.want)
		}
	}
}

// TestGraphParagraphsCorpus walks a real library's talks and manuals the way
// the indexer stores them (parseTalkHeader for talks, the whole file for
// manuals), and checks every link lands in the paragraph the engine stores
// at that index: the link's text must appear in splitParagraphs(content)[p].
func TestGraphParagraphsCorpus(t *testing.T) {
	root := os.Getenv("GOSPEL_LIBRARY_ROOT")
	if root == "" {
		t.Skip("set GOSPEL_LIBRARY_ROOT")
	}
	counts := map[string]int{}
	misplaced, beyondCap := 0, 0
	for _, sub := range []string{"general-conference", "manual"} {
		err := filepath.WalkDir(filepath.Join(root, "eng", sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			content := string(b)
			if sub == "general-conference" {
				_, _, content = parseTalkHeader(content)
			}
			paras := splitParagraphs(content)
			var full []string // the same paragraphs, uncapped
			for _, raw := range strings.Split(content, "\n\n") {
				if c := cleanInlineMarkdown(strings.TrimSpace(raw)); c != "" {
					full = append(full, c)
				}
			}
			paragraphLinks(content, func(i int, text, href string) {
				dsts := LinkTargets(p, text, href)
				counts[sub] += len(dsts)
				if len(dsts) == 0 || strings.Contains(paras[i], cleanInlineMarkdown(text)) {
					return
				}
				// splitParagraphs caps a stored paragraph at 2,000 runes; a
				// link past the cap is in the right paragraph, its text cut.
				if len([]rune(paras[i])) == 2000 && strings.Contains(full[i], cleanInlineMarkdown(text)) {
					beyondCap++
					return
				}
				if misplaced < 5 {
					t.Errorf("%s: link %q not in paragraph %d", p, text, i)
				}
				misplaced++
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if misplaced > 0 {
		t.Errorf("%d links misplaced", misplaced)
	}
	t.Logf("edges by source: %v; links past the 2,000-rune cap of their paragraph: %d", counts, beyondCap)
}
