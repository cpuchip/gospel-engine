package mcpserver

import (
	"net/http"
	"sort"
	"testing"
)

// Every tool here only reads the engine's own corpus. The MCP library's
// defaults advertise tools as destructive and open-world, so each hint is
// asserted explicitly rather than trusting absence.
func TestToolsAdvertiseReadOnly(t *testing.T) {
	s := New(http.NotFoundHandler(), "test")
	tools := s.mcp.ListTools()

	var names []string
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"gospel_get", "gospel_list", "gospel_search"}
	if len(names) != len(want) {
		t.Fatalf("registered tools = %v, want %v", names, want)
	}

	for _, name := range want {
		st, ok := tools[name]
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		a := st.Tool.Annotations
		for label, got := range map[string]*bool{
			"readOnlyHint":    a.ReadOnlyHint,
			"destructiveHint": a.DestructiveHint,
			"idempotentHint":  a.IdempotentHint,
			"openWorldHint":   a.OpenWorldHint,
		} {
			if got == nil {
				t.Errorf("%s: %s unset (library default applies)", name, label)
			}
		}
		if a.ReadOnlyHint != nil && !*a.ReadOnlyHint {
			t.Errorf("%s: readOnlyHint = false, want true", name)
		}
		if a.DestructiveHint != nil && *a.DestructiveHint {
			t.Errorf("%s: destructiveHint = true, want false", name)
		}
		if a.IdempotentHint != nil && !*a.IdempotentHint {
			t.Errorf("%s: idempotentHint = false, want true", name)
		}
		if a.OpenWorldHint != nil && *a.OpenWorldHint {
			t.Errorf("%s: openWorldHint = true, want false", name)
		}
	}
}
