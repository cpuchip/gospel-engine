// Package auth provides bearer-token middleware for the API.
package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/ratelimit"
)

type contextKey string

const (
	tokenContextKey    contextKey = "api_token"
	internalTrustedKey contextKey = "internal_trusted"
)

// WithInternalTrusted marks a context as pre-authorized, so an in-process call
// to the router (e.g. from the MCP-over-HTTP tool handlers, which carry their
// own ?key= gate at /mcp) skips bearer validation. It must only ever be set on
// requests the server constructs itself — never derived from inbound headers.
func WithInternalTrusted(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalTrustedKey, true)
}

func isInternalTrusted(ctx context.Context) bool {
	v, _ := ctx.Value(internalTrustedKey).(bool)
	return v
}

// Middleware returns an HTTP middleware that validates `Authorization: Bearer stdy_…`
// and spends one request from the token's rate-limit bucket (429 with
// Retry-After when it is empty; lim nil disables limiting). devMode bypasses
// auth entirely (local testing only); in-process trusted calls were already
// counted where they entered (/mcp) and are not counted again.
func Middleware(database *db.DB, devMode bool, lim *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if devMode || isInternalTrusted(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			raw := extractBearer(r)
			if raw == "" {
				http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
				return
			}
			if !db.LooksLikeAPIToken(raw) {
				http.Error(w, "invalid token", http.StatusUnauthorized)
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
			if !Spend(w, lim, tok) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			// Best-effort, rate-limited touch (never blocks the request).
			database.TouchAPITokenIfStale(tok)

			ctx := context.WithValue(r.Context(), tokenContextKey, tok)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Spend takes one request from tok's bucket. When the bucket is empty it sets
// Retry-After on w and returns false; the caller writes the 429 body in its
// own error format. A nil limiter always allows, and so does an admin token:
// admin tokens are minted only inside the container (the operator's own), and
// one of them is ibeco.me's service token, which carries every ibeco.me
// reader's scripture lookups; a per-key bucket there would throttle them all
// together.
func Spend(w http.ResponseWriter, lim *ratelimit.Limiter, tok *db.APIToken) bool {
	if lim == nil || tok == nil || tok.IsAdmin {
		return true
	}
	ok, wait := lim.Allow(tok.ID, tok.RateLimit)
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
	}
	return ok
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
