package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/ratelimit"
)

// The database is nil on purpose: any request that reaches the token lookup
// panics, so a clean 401 proves malformed credentials are rejected up front.
func TestMiddlewareRejectsMalformedWithoutLookup(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next handler must not be reached")
	})
	h := Middleware(nil, false, nil)(next)
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

// Spend limits ordinary tokens and never an admin token (ibeco.me's service
// token is one, and carries every ibeco.me reader's lookups).
func TestSpendLimitsOrdinaryNotAdmin(t *testing.T) {
	lim := ratelimit.New()
	user := &db.APIToken{ID: 1, RateLimit: 1}
	admin := &db.APIToken{ID: 2, RateLimit: 1, IsAdmin: true}
	if !Spend(httptest.NewRecorder(), lim, user) {
		t.Fatal("first ordinary request refused")
	}
	rec := httptest.NewRecorder()
	if Spend(rec, lim, user) {
		t.Error("second ordinary request at 1/min allowed")
	}
	if rec.Header().Get("Retry-After") != "60" {
		t.Errorf("Retry-After = %q, want 60", rec.Header().Get("Retry-After"))
	}
	for i := 0; i < 5; i++ {
		if !Spend(httptest.NewRecorder(), lim, admin) {
			t.Fatalf("admin request %d limited", i+1)
		}
	}
}
