// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// A PLANE OP IS REACHED WITH A STATED CALLER AND NO REQUEST, and payingOrg has
// to resolve its tenant from that or it resolves nothing.
//
// principal.OrgFrom cannot do it alone here, by construction: plane.For sets
// zip.Caller{Org} and leaves User empty, and OrgFrom composes validated-ness AND
// an org — "no validated principal, the org claim is untrusted". So every typed
// money op behind billing's door answered
//
//	403  "tier: no validated org on the call"
//
// to a caller the door had just admitted. Measured in production, where it read
// as ai's tier_cache defaulting every org to the lowest tier.
//
// callerOrg is the rule the other plane ops in this package already read by, and
// the one plane.Ask documents: the org rides the CALLER. This asserts payingOrg
// reads it too, and still refuses when nobody stated one.
//
// Co-residency is checked AFTER the tenant, which is what makes this testable
// with no commerce embed: unresolved says "no org on the call", resolved says
// "not co-resident". So the second message is the PASS.
func TestPayingOrgReadsTheStatedCaller(t *testing.T) {
	for _, tc := range []struct {
		what     string
		ctx      context.Context
		resolved bool
	}{
		{"a caller stating its org", cloud.For(context.Background(), "hanzo"), true},
		{"a caller stating an empty org", cloud.For(context.Background(), ""), false},
		{"no caller at all", context.Background(), false},
	} {
		_, err := payingOrg(tc.ctx, "probe")
		if err == nil {
			t.Errorf("%s: payingOrg succeeded with no commerce co-resident", tc.what)
			continue
		}
		noOrg := strings.Contains(err.Error(), "no org on the call")
		if noOrg == tc.resolved {
			t.Errorf("%s: %v — want tenant-resolved=%v", tc.what, err, tc.resolved)
		}
	}
}

// The tenant is never an argument and never a header a caller can set on this
// path: it is whatever the CALLER states, and the only thing that states one is
// the door that already validated it. Naming a different org here reaches a
// different tenant's books, so what the op reads must be exactly what was
// stamped — no fallback to anything a request could carry.
func TestPayingOrgReadsOnlyWhatWasStamped(t *testing.T) {
	_, err := payingOrg(cloud.For(context.Background(), "acme"), "probe")
	if err == nil {
		t.Fatal("payingOrg succeeded with no commerce co-resident")
	}
	if strings.Contains(err.Error(), "no org on the call") {
		t.Fatalf("the stamped org was not read: %v", err)
	}
	// It got past the tenant and stopped on the embed, which is the only other
	// thing in its way here.
	if !strings.Contains(err.Error(), "co-resident") {
		t.Fatalf("expected the co-residency refusal after the tenant resolved, got: %v", err)
	}
}
