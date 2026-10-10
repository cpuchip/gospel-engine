package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

func TestRedactURI(t *testing.T) {
	for in, want := range map[string]string{
		"/api/search?q=faith":                          "/api/search?q=faith",
		"/auth/google/callback?state=abc&code=4/0Axyz": "/auth/google/callback?[redacted]",
		"/api/search?q=faith&key=stdy_secret":          "/api/search?key=REDACTED&q=faith",
		"/api/get?reference=Ether+12:27":               "/api/get?reference=Ether+12:27",
		"/keys":                                        "/keys",
	} {
		u, _ := url.ParseRequestURI(in)
		if got := redactURI(u); got != want {
			t.Errorf("redactURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// The log line never carries the code; the handler still sees it.
func TestRedactingLoggerKeepsTheRealRequest(t *testing.T) {
	var buf bytes.Buffer
	saved := middleware.DefaultLogger
	middleware.DefaultLogger = middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: log.New(&buf, "", 0), NoColor: true})
	defer func() { middleware.DefaultLogger = saved }()
	var seen string
	h := redactingLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("code")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/google/callback?state=s1&code=SECRETCODE", nil))
	if seen != "SECRETCODE" {
		t.Errorf("handler saw code %q", seen)
	}
	if strings.Contains(buf.String(), "SECRETCODE") || strings.Contains(buf.String(), "s1") || !strings.Contains(buf.String(), "/auth/google/callback") {
		t.Errorf("log line: %q", buf.String())
	}
}
