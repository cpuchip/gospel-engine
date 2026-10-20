package api

import "testing"

// A talk as the corpus stores it, cut down: a footnote marker, an escaped
// bracket, a heading, the footnote anchor and a relative link.
const talkSample = "\nOf the Quorum of the Twelve Apostles\n\n" +
	"He explained “that God had a work for \\[Joseph] to do”<sup>[2](#fn-2)</sup> and then instructed him.\n\n" +
	"## Teachings of the Prophet Joseph Smith\n\n" +
	"The lessons Joseph Smith learned *influenced* every aspect of his ministry.\n\n\n\n" +
	"---\n\n## Footnotes\n\n" +
	"<a id=\"fn-2\"></a>**2.** [Joseph Smith—History 1:33](../../../scriptures/pgp/js-h/1.md).\n"

const talkClean = "Of the Quorum of the Twelve Apostles\n\n" +
	"He explained “that God had a work for [Joseph] to do” and then instructed him.\n\n" +
	"## Teachings of the Prophet Joseph Smith\n\n" +
	"The lessons Joseph Smith learned *influenced* every aspect of his ministry.\n\n" +
	"---\n\n## Footnotes\n\n" +
	"**2.** Joseph Smith—History 1:33.\n"

func TestCleanMarkup(t *testing.T) {
	if got := cleanMarkup(talkSample); got != talkClean {
		t.Errorf("cleanMarkup:\n got  %q\n want %q", got, talkClean)
	}
	if got := cleanMarkup("plain text\n"); got != "plain text\n" {
		t.Errorf("plain text changed: %q", got)
	}
}
