package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cpuchip/gospel-engine/internal/testdb"
)

// TestTokenAdminAgainstPostgres runs only when GOSPEL_TEST_DATABASE_URL points at
// a disposable Postgres with pgvector (the image production uses). It exercises
// what unit tests cannot: the real migration SQL, and whether the token queries
// actually load is_admin. If ValidateAPIToken stopped selecting is_admin, every
// admin token would silently validate as ordinary and /api/admin/* would lock
// out its only legitimate caller, with every unit test still green.
func TestTokenAdminAgainstPostgres(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open (applies every migration): %v", err)
	}
	defer d.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// Rewind migration 003 so it runs against rows shaped like production's,
	// including a look-alike of the service token that an ordinary token could
	// have minted before the role existed.
	exec(`DELETE FROM schema_migrations WHERE version = 3`)
	exec(`ALTER TABLE api_tokens DROP COLUMN IF EXISTS is_admin`)
	exec(`DELETE FROM api_tokens`)
	for _, row := range []struct {
		id         int
		user, name string
	}{
		{1, "owner", "local-mcp"},
		{2, "system:ibeco.me", "ibeco.me-service"},
		{3, "system:ibeco.me", "ibeco.me-service"}, // look-alike, must NOT be promoted
		{4, "ibeco:99", "some-user-token"},
	} {
		exec(`INSERT INTO api_tokens (id, external_user, name, prefix, token_hash)
		      VALUES ($1, $2, $3, 'stdy_0000000', 'unused')`, row.id, row.user, row.name)
	}
	exec(`SELECT setval(pg_get_serial_sequence('api_tokens', 'id'), 4)`)

	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("applying 003 to production-shaped rows: %v", err)
	}
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate should be a no-op: %v", err)
	}

	admins := func() []int64 {
		t.Helper()
		rows, err := d.Pool.Query(ctx, `SELECT id FROM api_tokens WHERE is_admin ORDER BY id`)
		if err != nil {
			t.Fatalf("query admins: %v", err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	if got, want := admins(), []int64{2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admin tokens after migration = %v, want %v (only the pinned service token)", got, want)
	}
	if n, err := d.CountLiveAdminTokens(ctx); err != nil || n != 1 {
		t.Fatalf("live admin count after migration = %d (err %v), want 1", n, err)
	}

	// Real create + validate round trip for both kinds of token.
	userTok, rawUser, err := d.CreateAPIToken(ctx, "ibeco:1", "ordinary", nil, 0, false)
	if err != nil {
		t.Fatalf("create ordinary: %v", err)
	}
	adminTok, rawAdmin, err := d.CreateAPIToken(ctx, "", "admin", nil, 0, true)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if n, err := d.CountLiveAdminTokens(ctx); err != nil || n != 2 {
		t.Fatalf("live admin count after minting one = %d (err %v), want 2", n, err)
	}
	exec(`UPDATE api_tokens SET revoked = TRUE WHERE id = $1`, adminTok.ID)
	if n, err := d.CountLiveAdminTokens(ctx); err != nil || n != 1 {
		t.Fatalf("live admin count after revoking it = %d (err %v), want 1 (revoked must not count)", n, err)
	}
	exec(`UPDATE api_tokens SET revoked = FALSE WHERE id = $1`, adminTok.ID)
	for _, c := range []struct {
		label     string
		raw       string
		wantAdmin bool
	}{{"ordinary", rawUser, false}, {"admin", rawAdmin, true}} {
		got, err := d.ValidateAPIToken(ctx, c.raw)
		if err != nil || got == nil {
			t.Fatalf("validate %s: tok=%v err=%v", c.label, got, err)
		}
		if got.IsAdmin != c.wantAdmin {
			t.Errorf("validate %s: IsAdmin = %v, want %v", c.label, got.IsAdmin, c.wantAdmin)
		}
	}

	listed, err := d.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[int64]bool{}
	for _, tok := range listed {
		seen[tok.ID] = tok.IsAdmin
	}
	if seen[userTok.ID] || !seen[adminTok.ID] || !seen[2] || seen[3] {
		t.Errorf("list is_admin wrong: ordinary=%v admin=%v service=%v look-alike=%v",
			seen[userTok.ID], seen[adminTok.ID], seen[2], seen[3])
	}
}
