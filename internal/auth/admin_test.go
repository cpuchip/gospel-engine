package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cpuchip/gospel-engine/internal/db"
)

func TestRequireAdmin(t *testing.T) {
	cases := []struct {
		name    string
		devMode bool
		ctx     func(context.Context) context.Context
		want    int
	}{
		{"admin token passes", false, withToken(&db.APIToken{ID: 2, IsAdmin: true}), http.StatusOK},
		{"ordinary token refused", false, withToken(&db.APIToken{ID: 6, IsAdmin: false}), http.StatusForbidden},
		{"no token refused", false, func(c context.Context) context.Context { return c }, http.StatusForbidden},
		{"internally trusted but no token refused", false, WithInternalTrusted, http.StatusForbidden},
		{"dev mode bypasses", true, func(c context.Context) context.Context { return c }, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			h := RequireAdmin(tc.devMode)(next)
			req := httptest.NewRequest(http.MethodGet, "/api/admin/tokens", nil)
			req = req.WithContext(tc.ctx(req.Context()))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func withToken(tok *db.APIToken) func(context.Context) context.Context {
	return func(c context.Context) context.Context {
		return context.WithValue(c, tokenContextKey, tok)
	}
}
