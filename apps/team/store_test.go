package team

import (
	"errors"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// TestSegTraversalGuard is the fix for Red's LOW #3: seg() must neutralize the
// dot-only path components "." and ".." (which are inside the allowed
// [A-Za-z0-9_.-] class and would otherwise pass through) so a tenant/space/
// blobId segment can never be current/parent-dir traversal. '/' is already killed
// by the class, so a slash-bearing value collapses to underscores in place and
// cannot escape its box.
func TestSegTraversalGuard(t *testing.T) {
	cases := map[string]string{
		".":            "_", // current dir → neutralized
		"..":           "_", // parent dir → neutralized (the whole point)
		"":             "_", // empty → placeholder
		"a/b":          "a_b",
		"../etc":       ".._etc", // not exactly "..": slash killed, stays a filename
		"../../secret": ".._.._secret",
		"normal-org.1": "normal-org.1", // dots/hyphens preserved when not dot-only
		"o1234":        "o1234",
	}
	for in, want := range cases {
		if got := seg(in); got != want {
			t.Errorf("seg(%q) = %q, want %q", in, got, want)
		}
		// Belt: a sanitized segment is never a traversal component.
		if got := seg(in); got == "." || got == ".." {
			t.Errorf("seg(%q) = %q is still a traversal component", in, got)
		}
	}
}

// A DOCUMENT IS STORED AS WRITTEN, AND ONE POSTGRESQL WOULD REFUSE IS REFUSED ON BOTH
// ENGINES. A message that carries a NUL (a JSON string spells it \u0000) is the same
// error on SQLite, which would have kept it, and on hanzo-sql, which cannot; the
// space is left as it was.
func TestADocumentOneEngineWouldRefuseIsRefusedOnBoth(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t.TempDir())
		t.Cleanup(func() { _ = st.Close() })
		const org, space = "acme", "s1"
		if err := st.put(org, space, map[string]any{"_id": "m1", "_class": "chunter:class:ChatMessage", "message": "a\x00b"}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a message holding a NUL: %v, want ErrText", err)
		}
		if doc, err := st.get(org, space, "m1"); err != nil || doc != nil {
			t.Fatalf("the refused message was stored: %v (%v)", doc, err)
		}
		if err := st.put(org, space, map[string]any{"_id": "m2", "_class": "chunter:class:ChatMessage", "message": "héllo \\u0000"}); err != nil {
			t.Fatalf("a message that spells the escape out: %v", err)
		}
		doc, err := st.get(org, space, "m2")
		if err != nil || doc["message"] != "héllo \\u0000" {
			t.Fatalf("the message came back as %v (%v)", doc, err)
		}
	})
}
