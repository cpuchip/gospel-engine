package api

import (
	"regexp"
	"strings"
)

// cleanMarkup returns a talk's or manual's content as plain prose: the
// corpus keeps HTML footnote markers and anchors and relative markdown links
// inside the text, which a model reads past but a knowledge base has to
// strip. Headings, emphasis and the footnote texts themselves stay; only the
// markers, anchors, link targets and stray tags go.
func cleanMarkup(s string) string {
	s = supRe.ReplaceAllString(s, "")
	s = anchorRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = tagRe.ReplaceAllString(s, "")
	s = strings.NewReplacer(`\[`, "[", `\]`, "]").Replace(s)
	s = blankRunRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s) + "\n"
}

var (
	supRe      = regexp.MustCompile(`<sup>.*?</sup>`)
	anchorRe   = regexp.MustCompile(`<a\s+id="[^"]*"\s*>\s*</a>`)
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	tagRe      = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	blankRunRe = regexp.MustCompile(`\n{3,}`)
)
