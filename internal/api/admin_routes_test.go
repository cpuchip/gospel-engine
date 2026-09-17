package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cpuchip/gospel-engine/internal/auth"
	"github.com/cpuchip/gospel-engine/internal/config"
)

// Admin routes must refuse a request that passed authentication but carries no
// admin token. The in-process MCP tool calls are exactly that shape (trusted
// context, no token), so this also pins that MCP can never reach an admin route.
// DB is nil: a request that got past the admin gate would hit the handler and
// fail differently, so a clean 403 proves the gate ran first.
func TestAdminRoutesRequireAdminToken(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	router := s.Router()
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/tokens"},
		{http.MethodGet, "/api/admin/tokens"},
		{http.MethodDelete, "/api/admin/tokens/1"},
		{http.MethodPost, "/api/admin/reindex"},
		{http.MethodPost, "/api/admin/reparse-speakers"},
	} {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{"name":"x"}`))
		req = req.WithContext(auth.WithInternalTrusted(req.Context()))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, rec.Code)
		}
	}
}
