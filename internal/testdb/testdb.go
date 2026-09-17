// Package testdb guards the integration tests that delete and rewrite
// api_tokens. Only tests import it, so it never ships in the binary.
package testdb

import (
	"net/url"
	"os"
	"testing"
)

const (
	envURL         = "GOSPEL_TEST_DATABASE_URL"
	envAllowRemote = "GOSPEL_TEST_ALLOW_REMOTE_DB"
)

// URL returns the database URL for a destructive integration test. It skips the
// test when the variable is unset, and refuses any database that is not on this
// machine unless GOSPEL_TEST_ALLOW_REMOTE_DB=1: pointed at a real deployment,
// these tests would wipe its tokens. The URL is never printed, since it usually
// carries a password.
func URL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv(envURL)
	if raw == "" {
		t.Skipf("set %s to a disposable local Postgres to run this test", envURL)
	}
	if !IsLocal(raw) && os.Getenv(envAllowRemote) != "1" {
		t.Fatalf("refusing to run a destructive token test against a non-local database; use localhost or set %s=1", envAllowRemote)
	}
	return raw
}

// IsLocal reports whether a postgres:// URL names this machine. Anything that
// does not parse to a loopback host, including key=value DSNs, counts as not
// local, so the guard fails safe.
func IsLocal(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
