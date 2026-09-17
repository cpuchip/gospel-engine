package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// TestAdminFlowAgainstPostgres runs only when GOSPEL_TEST_DATABASE_URL points at
// a disposable Postgres. It drives the real router, auth middleware and token
// store end to end. Run integration tests with -p 1: the db package's test
// temporarily rewinds a migration on the same database.
//
// The invariant under test: an ordinary token cannot reach /api/admin/*, and no
// token minted over the API is ever an admin, even when the request asks.
func TestAdminFlowAgainstPostgres(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	router := (&Server{Cfg: &config.Config{}, DB: d}).Router()

	_, rawAdmin, err := d.CreateAPIToken(ctx, "system:itest", "itest-admin", nil, 0, true)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	_, rawUser, err := d.CreateAPIToken(ctx, "ibeco:1", "itest-user", nil, 0, false)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Ordinary token: every admin route refuses it.
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/tokens"},
		{http.MethodGet, "/api/admin/tokens"},
		{http.MethodDelete, "/api/admin/tokens/1"},
		{http.MethodPost, "/api/admin/reindex"},
	} {
		if rec := call(rt.method, rt.path, rawUser, `{"name":"nope"}`); rec.Code != http.StatusForbidden {
			t.Errorf("ordinary token %s %s = %d, want 403", rt.method, rt.path, rec.Code)
		}
	}

	// Admin token mints, and asking for admin in the body is ignored.
	rec := call(http.MethodPost, "/api/admin/tokens", rawAdmin,
		`{"name":"minted-by-admin","external_user":"ibeco:1","is_admin":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin mint = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token db.APIToken `json:"token"`
		Raw   string      `json:"raw"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode mint response: %v", err)
	}
	if out.Token.IsAdmin {
		t.Error("mint response claims the new token is admin")
	}
	minted, err := d.ValidateAPIToken(ctx, out.Raw)
	if err != nil || minted == nil {
		t.Fatalf("validate minted: tok=%v err=%v", minted, err)
	}
	if minted.IsAdmin {
		t.Fatal("a token minted over the API validated as admin")
	}

	// The minted token cannot mint in turn.
	if rec := call(http.MethodPost, "/api/admin/tokens", out.Raw, `{"name":"second-generation"}`); rec.Code != http.StatusForbidden {
		t.Errorf("minted token mint = %d, want 403", rec.Code)
	}

	// Admin can list.
	if rec := call(http.MethodGet, "/api/admin/tokens", rawAdmin, ""); rec.Code != http.StatusOK {
		t.Errorf("admin list = %d, want 200", rec.Code)
	}
}
