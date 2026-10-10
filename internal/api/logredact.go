package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// redactedParams are query parameters whose values never reach the request
// log: credentials a client might put in a URL, and OAuth's one-time code.
var redactedParams = map[string]bool{"key": true, "token": true, "access_token": true, "code": true, "state": true}

// redactURI is the request line the log may show: under /auth/ the query is
// dropped whole (the OAuth callback carries code and state); elsewhere the
// values of redactedParams are replaced.
func redactURI(u *url.URL) string {
	p := u.EscapedPath()
	if u.RawQuery == "" {
		return p
	}
	if strings.HasPrefix(u.Path, "/auth/") {
		return p + "?[redacted]"
	}
	q := u.Query()
	changed := false
	for k := range q {
		if redactedParams[strings.ToLower(k)] {
			q[k] = []string{"REDACTED"}
			changed = true
		}
	}
	if !changed {
		return p + "?" + u.RawQuery
	}
	return p + "?" + q.Encode()
}

type origRequestKey struct{}

// redactingLogger is chi's request logger fed a copy of each request whose
// URL is redacted; the handler chain still receives the original request.
func redactingLogger(next http.Handler) http.Handler {
	logged := middleware.Logger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orig, _ := r.Context().Value(origRequestKey{}).(*http.Request)
		if orig == nil {
			next.ServeHTTP(w, r)
			return
		}
		// keep the context the logger added (its entry, for Recoverer)
		next.ServeHTTP(w, orig.WithContext(r.Context()))
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shown := redactURI(r.URL)
		cp := r.WithContext(context.WithValue(r.Context(), origRequestKey{}, r))
		cp.RequestURI = shown
		u := *r.URL
		if parsed, err := url.ParseRequestURI(shown); err == nil {
			u.RawQuery = parsed.RawQuery
		}
		cp.URL = &u
		logged.ServeHTTP(w, cp)
	})
}
