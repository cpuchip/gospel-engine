package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var (
	sharedKey  = strings.Repeat("0123456789abcdef", 4)
	liveToken  = "stdy_" + strings.Repeat("a", 64)
	deadToken  = "stdy_" + strings.Repeat("b", 64)
	errorToken = "stdy_" + strings.Repeat("e", 64)
)

// fakeValidator accepts only liveToken, errors on errorToken, and counts calls.
type fakeValidator struct{ calls int }

func (f *fakeValidator) validate(_ context.Context, raw string) (bool, error) {
	f.calls++
	switch raw {
	case liveToken:
		return true, nil
	case errorToken:
		return false, errors.New("db down")
	}
	return false, nil
}

func marker(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Reached", name)
		w.WriteHeader(http.StatusOK)
	})
}

func TestMountMCP(t *testing.T) {
	nul64 := strings.Repeat("%00", 64)
	cases := []struct {
		name       string
		method     string // "" = POST
		envKey     string
		target     string
		auth       string // raw Authorization header value
		wantStatus int
		wantReach  string // "mcp", "api", or "" for rejected
		wantCalls  int    // validator calls expected
	}{
		{"no key", "", sharedKey, "/mcp", "", 401, "", 0},
		{"empty key", "", sharedKey, "/mcp?key=", "", 401, "", 0},
		{"shared key", "", sharedKey, "/mcp?key=" + sharedKey, "", 200, "mcp", 0},
		{"wrong key", "", sharedKey, "/mcp?key=nope", "", 401, "", 0},
		{"prefix of shared key", "", sharedKey, "/mcp?key=" + sharedKey[:20], "", 401, "", 0},
		{"live stdy token in query", "", sharedKey, "/mcp?key=" + liveToken, "", 200, "mcp", 1},
		{"revoked or unknown stdy token", "", sharedKey, "/mcp?key=" + deadToken, "", 401, "", 1},
		{"live token works with legacy key unset", "", "", "/mcp?key=" + liveToken, "", 200, "mcp", 1},
		{"fail closed: legacy key unset, empty key", "", "", "/mcp?key=", "", 401, "", 0},
		{"fail closed: legacy key unset, no key", "", "", "/mcp", "", 401, "", 0},
		{"lookup error is 500 not open", "", sharedKey, "/mcp?key=" + errorToken, "", 500, "", 1},
		{"subpath is gated", "", sharedKey, "/mcp/sse?key=nope", "", 401, "", 0},
		{"subpath with live token", "", sharedKey, "/mcp/sse?key=" + liveToken, "", 200, "mcp", 1},
		{"bearer header live token", "", sharedKey, "/mcp", "Bearer " + liveToken, 200, "mcp", 1},
		{"bearer header dead token", "", sharedKey, "/mcp", "Bearer " + deadToken, 401, "", 1},
		{"non-mcp path untouched", "", sharedKey, "/api/health", "", 200, "api", 0},
		{"lookalike path not gated", "", sharedKey, "/mcpx", "", 200, "api", 0},
		// Added after independent review:
		{"GET without credential", http.MethodGet, sharedKey, "/mcp", "", 401, "", 0},
		{"DELETE without credential", http.MethodDelete, sharedKey, "/mcp", "", 401, "", 0},
		{"GET with live token", http.MethodGet, sharedKey, "/mcp?key=" + liveToken, "", 200, "mcp", 1},
		{"wrong query key beats valid bearer", "", sharedKey, "/mcp?key=nope", "Bearer " + liveToken, 401, "", 0},
		{"empty query key falls back to bearer", "", sharedKey, "/mcp?key=", "Bearer " + liveToken, 200, "mcp", 1},
		{"shared key as bearer", "", sharedKey, "/mcp", "Bearer " + sharedKey, 200, "mcp", 0},
		{"lowercase bearer scheme rejected", "", sharedKey, "/mcp", "bearer " + liveToken, 401, "", 0},
		{"basic auth rejected", "", sharedKey, "/mcp", "Basic dXNlcjpwYXNz", 401, "", 0},
		{"percent-encoded path is gated", "", sharedKey, "/%6Dcp", "", 401, "", 0},
		{"uppercase path goes to api", "", sharedKey, "/MCP", "", 200, "api", 0},
		{"NUL-byte token never reaches validator", "", sharedKey, "/mcp?key=stdy_" + nul64, "", 401, "", 0},
		{"short stdy token never reaches validator", "", sharedKey, "/mcp?key=stdy_abc", "", 401, "", 0},
		{"uppercase hex token never reaches validator", "", sharedKey, "/mcp?key=stdy_" + strings.Repeat("A", 64), "", 401, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fv := &fakeValidator{}
			h := mountMCP(marker("api"), marker("mcp"), tc.envKey, fv.validate)
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, tc.target, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("X-Reached"); got != tc.wantReach {
				t.Errorf("reached = %q, want %q", got, tc.wantReach)
			}
			if fv.calls != tc.wantCalls {
				t.Errorf("validator calls = %d, want %d", fv.calls, tc.wantCalls)
			}
		})
	}
}

// TestMountMCPBodyLimit proves the MCP handler sees a capped body: a body of
// exactly maxMCPBody reads cleanly, one byte more is a MaxBytesError.
func TestMountMCPBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		size    int
		wantErr bool
	}{{maxMCPBody, false}, {maxMCPBody + 1, true}} {
		var readErr error
		reader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})
		h := mountMCP(marker("api"), reader, sharedKey, nil)
		req := httptest.NewRequest(http.MethodPost, "/mcp?key="+sharedKey, bytes.NewReader(make([]byte, tc.size)))
		h.ServeHTTP(httptest.NewRecorder(), req)
		var mbe *http.MaxBytesError
		if got := errors.As(readErr, &mbe); got != tc.wantErr {
			t.Errorf("body %d bytes: MaxBytesError = %v, want %v (err=%v)", tc.size, got, tc.wantErr, readErr)
		}
	}
}
