// Package auth provides bearer-token middleware for the API.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/cpuchip/gospel-engine/internal/db"
)

type contextKey string

const (
	tokenContextKey contextKey = "api_token"
)

// Middleware returns an HTTP middleware that validates `Authorization: Bearer stdy_…`.
// devMode bypasses auth entirely (local testing only).
func Middleware(database *db.DB, devMode bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if devMode {
				next.ServeHTTP(w, r)
				return
			}
			raw := extractBearer(r)
			if raw == "" {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}
			tok, err := database.ValidateAPIToken(r.Context(), raw)
			if err != nil {
				http.Error(w, "auth lookup failed", http.StatusInternalServerError)
				return
			}
			if tok == nil {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			// Best-effort touch (don't block the request).
			go database.TouchAPIToken(context.Background(), tok.ID)

			ctx := context.WithValue(r.Context(), tokenContextKey, tok)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext returns the APIToken associated with the request, if any.
func FromContext(ctx context.Context) *db.APIToken {
	v, _ := ctx.Value(tokenContextKey).(*db.APIToken)
	return v
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
