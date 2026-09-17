package auth

import "net/http"

// RequireAdmin allows a request through only when Middleware attached an admin
// token to its context. It must run after Middleware. A request with no token
// in context is refused even if it was marked internally trusted, so the
// in-process MCP tool calls can never reach an admin route. devMode bypasses
// the check, matching Middleware (local testing only).
func RequireAdmin(devMode bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if devMode {
				next.ServeHTTP(w, r)
				return
			}
			if tok := FromContext(r.Context()); tok != nil && tok.IsAdmin {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "admin token required", http.StatusForbidden)
		})
	}
}
