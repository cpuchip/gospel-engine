package db

import (
	"strings"
	"testing"
	"time"
)

func TestLooksLikeAPIToken(t *testing.T) {
	hex64 := strings.Repeat("0123456789abcdef", 4)
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"minted shape", TokenPrefix + hex64, true},
		{"uppercase hex is not what we mint", TokenPrefix + strings.Repeat("A", 64), false},
		{"too short", TokenPrefix + hex64[:63], false},
		{"too long", TokenPrefix + hex64 + "0", false},
		{"wrong prefix", "stdx_" + hex64, false},
		{"NUL bytes", TokenPrefix + strings.Repeat("\x00", 64), false},
		{"non-hex tail", TokenPrefix + hex64[:63] + "g", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := LooksLikeAPIToken(c.raw); got != c.want {
			t.Errorf("%s: LooksLikeAPIToken = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTouchDue(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second)
	old := now.Add(-2 * time.Minute)
	exact := now.Add(-touchInterval)
	if !touchDue(nil, now) {
		t.Error("never-used token should be due")
	}
	if touchDue(&recent, now) {
		t.Error("token used 30s ago should not be due")
	}
	if !touchDue(&old, now) {
		t.Error("token used 2m ago should be due")
	}
	if !touchDue(&exact, now) {
		t.Error("token used exactly one interval ago should be due")
	}
}
