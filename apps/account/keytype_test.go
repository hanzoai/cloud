package account

import "testing"

// A KEY'S TYPE IS WHAT THE DOORS ENFORCE, NOT WHAT THE RECORD SAYS.
//
// cloud.IsPublishableKey reads the prefix, and it decides whether a key may
// become a principal at all: a pk- returns nil at the identity middleware and
// authenticates nothing, whatever any other field claims. So a listing that types
// a key from IAM's stored scope can hand a holder a key labelled `secret` that
// every gate treats as publishable — a credential the console presents as working
// which silently works for nothing.
//
// Not hypothetical. Measured on this cluster the day this was written: two rows
// scoped non-publish carrying pk- prefixes, one minted that morning.
func TestKeyTypeFollowsThePrefixThatEnforces(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  userKey
		want bool
	}{
		{
			// THE BUG. Scope says one thing, the prefix says another, and the prefix
			// is what the identity middleware obeys.
			name: "a pk- scoped non-publish is publishable, because every door says so",
			row:  userKey{AccessKey: "pk-live-7ad0000000", Scope: "read"},
			want: true,
		},
		{
			name: "the ordinary publishable row",
			row:  userKey{AccessKey: "pk-live-3480000000", Scope: iamScopePublish},
			want: true,
		},
		{
			name: "the ordinary secret row",
			row:  userKey{AccessKey: "sk-live-0000000000", Scope: "read"},
			want: false,
		},
		{
			// THE OPPOSITE DISAGREEMENT, and it must NOT resolve the same way. Calling
			// this publishable would print a confidential key's full value into a
			// listing, so the scope may never promote an sk- on its own.
			name: "an sk- scoped publish stays secret — a label cannot expose a secret",
			row:  userKey{AccessKey: "sk-live-0000000001", Scope: iamScopePublish},
			want: false,
		},
		{
			name: "an unrecognised key is secret, the safe default",
			row:  userKey{AccessKey: "something-else", Scope: "read"},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publishable(tc.row); got != tc.want {
				t.Fatalf("publishable(%q, scope=%q) = %v, want %v",
					tc.row.AccessKey, tc.row.Scope, got, tc.want)
			}
		})
	}
}
