package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The database is nil on purpose: any request that reaches the token lookup
// panics, so a clean 401 proves malformed credentials are rejected up front.
func TestMiddlewareRejectsMalformedWithoutLookup(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next handler must not be reached")
	})
	h := Middleware(nil, false)(next)
	for _, auth := range []string{
		"",
		"Basic dXNlcjpwYXNz",
		"Bearer stdy_abc",
		"Bearer stdy_" + strings.Repeat("\x00", 64),
		"Bearer stdy_" + strings.Repeat("A", 64),
		"Bearer not-a-token-at-all",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/search?q=x", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q: status = %d, want 401", auth, rec.Code)
		}
	}
}
