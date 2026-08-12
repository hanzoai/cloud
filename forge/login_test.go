package forge

// login_test.go pins the forge's own username rule.
//
// The rule is not ours: this deployment registers through OIDC with
// oauth2_client.USERNAME=email, so the forge's login is the address run through
// its NormalizeUserName. These cases are taken from that function
// (models/user/user.go) so a drift shows up here rather than as "unknown actor"
// against a live forge.

import "testing"

func TestLogin_IsTheEmailLocalPartFoldedTheForgesWay(t *testing.T) {
	for _, tc := range []struct{ email, want string }{
		// The case that shipped broken: a real user, and the login the forge holds.
		{"a@hanzo.ai", "a"},
		{"z@hanzo.ai", "z"},
		{"first.last@hanzo.ai", "first.last"},
		// Already a bare login (no @) — a no-op, which is what makes the fallback
		// path safe to pass through here too.
		{"a", "a"},
		// Only the FIRST @ splits.
		{"a@b@hanzo.ai", "a"},
		// Diacritics fold to ASCII; Æ expands rather than vanishing.
		{"café@hanzo.ai", "cafe"},
		{"Æon@hanzo.ai", "AEon"},
		// Quotes and accents are deleted, joining what is either side.
		{"o'brien@hanzo.ai", "obrien"},
		{"a`b@hanzo.ai", "ab"},
		// Whitespace and ~ + become a hyphen.
		{"a b@hanzo.ai", "a-b"},
		{"a+tag@hanzo.ai", "a-tag"},
		{"a~b@hanzo.ai", "a-b"},
		// Nothing to derive is EMPTY, never a guess: the caller refuses on it.
		{"", ""},
		{"   ", ""},
		{"@hanzo.ai", ""},
	} {
		if got := Login(tc.email); got != tc.want {
			t.Errorf("Login(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

// Case is LEFT ALONE. The forge stores the name as given and enforces
// uniqueness on a lowercased copy, and Sudo resolves case-insensitively
// (models/user/user.go GetUserByName on lower_name) — so folding case here
// would be a transformation the forge does not make, for no gain.
func TestLogin_DoesNotFoldCase(t *testing.T) {
	if got := Login("Zoe@hanzo.ai"); got != "Zoe" {
		t.Fatalf("Login = %q, want the address's own case", got)
	}
}
