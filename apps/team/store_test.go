package team

import "testing"

// TestSegTraversalGuard is the fix for Red's LOW #3: seg() must neutralize the
// dot-only path components "." and ".." (which are inside the allowed
// [A-Za-z0-9_.-] class and would otherwise pass through) so a tenant/workspace/
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
