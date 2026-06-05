package search

import "testing"

func TestWebURLFromFilePath(t *testing.T) {
	cases := map[string]string{
		"/data/gospel-library/eng/scriptures/bofm/mosiah/2.md":          "https://www.churchofjesuschrist.org/study/scriptures/bofm/mosiah/2?lang=eng",
		"eng/general-conference/2020/04/11nelson.md":                    "https://www.churchofjesuschrist.org/study/general-conference/2020/04/11nelson?lang=eng",
		"/data/gospel-library/eng/scriptures/dc-testament/dc/121.md":    "https://www.churchofjesuschrist.org/study/scriptures/dc-testament/dc/121?lang=eng",
		"/data/books/lectures-on-faith/1.md":                            "", // not gospel-library
		"":                                                              "",
	}
	for in, want := range cases {
		if got := webURLFromFilePath(in); got != want {
			t.Errorf("webURLFromFilePath(%q) = %q, want %q", in, got, want)
		}
	}
}
