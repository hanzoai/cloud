package brand

import "testing"

// TestOneIssuerRuleForBothHosts pins the disagreement Issuer was extracted to end.
//
// A pinned issuer with a trailing slash was trimmed by package cloud and NOT by the
// light host, which had spelled the rule again inline because it is built not to
// link package cloud. An OIDC issuer is compared as a literal string, so those are
// two different issuers: a token stamped by one fails validation against the other.
func TestOneIssuerRuleForBothHosts(t *testing.T) {
	for _, tc := range []struct{ pinned, id, want string }{
		{"https://hanzo.id/", "hanzo", "https://hanzo.id"},    // the trailing slash is the bug
		{"  https://hanzo.id  ", "hanzo", "https://hanzo.id"}, // and so is the whitespace
		{"https://iam.example/", "lux", "https://iam.example"},
		{"", "hanzo", IssuerFor("hanzo")}, // unpinned falls to the brand's own
		{"   ", "lux", IssuerFor("lux")},  // blank is unpinned, not a pin of ""
	} {
		if got := Issuer(tc.pinned, tc.id); got != tc.want {
			t.Errorf("Issuer(%q, %q) = %q, want %q", tc.pinned, tc.id, got, tc.want)
		}
	}

	// It never answers empty. An empty issuer flows to IAMBase(), which the sk- API
	// key resolver dials, and an empty base reads as "unresolved key" — every API-key
	// request on the fleet authenticating as nobody, with no error and no log line.
	if got := Issuer("", "no-such-brand"); got == "" {
		t.Error("Issuer answered empty for an unknown brand; every API-key request would authenticate as nobody")
	}
}
