package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/cpuchip/gospel-engine/internal/citations"
	"github.com/cpuchip/gospel-engine/internal/indexer"
)

// /api/citations?reference=Ether+12:27 — who has cited a verse (or a range),
// answered live by the BYU Scripture Citation Index and kept for a day. Every
// response names the source and links back to it.
func (s *Server) handleCitations(w http.ResponseWriter, r *http.Request) {
	if s.Citations == nil {
		http.Error(w, "citation lookups are not configured", http.StatusServiceUnavailable)
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("reference"))
	if ref == "" {
		ref = strings.TrimSpace(r.URL.Query().Get("ref"))
	}
	p, ok := parseReference(ref)
	if !ok || p.Verse == 0 {
		http.Error(w, fmt.Sprintf("give a verse or verse range, e.g. 'Ether 12:27' or 'D&C 93:24-30' (got %q)", ref), http.StatusBadRequest)
		return
	}
	verses := fmt.Sprint(p.Verse)
	display := fmt.Sprintf("%s %d:%d", indexer.BookDisplayName(p.Book), p.Chapter, p.Verse)
	if p.EndVerse > p.Verse {
		verses += fmt.Sprintf("-%d", p.EndVerse)
		display += fmt.Sprintf("-%d", p.EndVerse)
	}
	res, err := s.Citations.Lookup(r.Context(), p.Book, p.Chapter, verses, display)
	if errors.Is(err, citations.ErrUnknownBook) {
		http.Error(w, fmt.Sprintf("%s is not covered by the citation index", indexer.BookDisplayName(p.Book)), http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("citations %s: %v", display, err)
		http.Error(w, "the BYU Scripture Citation Index did not answer; try again later ("+citations.SourceURL+")", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
