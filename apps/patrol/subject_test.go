package patrol

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/hanzoai/cloud/sdk/bus"
)

// subject_test.go asks the feed's addressing the only question that matters
// about it: does one subject belong to one org. The feed is the whole live
// picture — every incident, every feed row, every checkpoint — and two orgs on
// one subject is not a leak of a row, it is a subscription to a tenant.

// TestOneOrgHasOneSubject measures bus.addressable for injectivity, which its
// own comment claims. It has two branches, and only one of them is guarded.
// namespace.Sanitize refuses to be the identity on a name shaped like its own
// disambiguation (looksSuffixed), precisely so a folded name cannot land on a
// verbatim one. addressable short-circuits to the org ITSELF whenever it is a
// bare NATS token, before Sanitize is ever called — and that branch carries no
// such guard, so the image of the fold and the image of the pass-through
// overlap. The suffix is sha256 of the target's own org id, which is public: an
// attacker computes the colliding name offline and asks IAM for an org by that
// name.
func TestOneOrgHasOneSubject(t *testing.T) {
	for _, target := range []string{"hanzo.ai", "acme.security", "north.side"} {
		sum := sha256.Sum256([]byte(target))
		twin := fold(target) + "-" + hex.EncodeToString(sum[:8])
		if twin == target {
			continue
		}
		if a, b := bus.Phys(target, Module), bus.Phys(twin, Module); a == b {
			t.Errorf("orgs %q and %q both address the patrol feed at %q: a tenant that registers "+
				"the second name subscribes to the first one's incidents, feed rows and rounds "+
				"as they happen", target, twin, a)
		}
	}
}

// fold is what namespace.Sanitize does to the visible punctuation in an org id
// before it appends the hash: lowercase, and everything that is not [a-z0-9-]
// becomes a dash.
func fold(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}
