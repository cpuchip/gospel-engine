package testdb

import "testing"

func TestIsLocal(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"postgres://u:p@localhost:55499/gospel?sslmode=disable", true},
		{"postgres://u:p@127.0.0.1:5432/gospel", true},
		{"postgres://u:p@[::1]:5432/gospel", true},
		{"postgres://u:p@engine.ibeco.me:5432/gospel", false},
		{"postgres://u:p@204.12.235.154:5432/gospel", false},
		{"postgres://u:p@localhost.example.com:5432/gospel", false},
		{"host=localhost dbname=gospel", false}, // key=value DSN: fail safe
		{"", false},
	}
	for _, c := range cases {
		if got := IsLocal(c.raw); got != c.want {
			t.Errorf("IsLocal(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}
