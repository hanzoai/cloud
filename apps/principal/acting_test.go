// Copyright © 2026 Hanzo AI. MIT License.

package principal

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

// AN ORG IS A TENANT ONLY WHERE A TRUSTED DOOR PARKED IT, and this is the
// property that makes that safe to rely on: the DEFAULT IS REFUSAL.
//
// zip.CallerOf hands back a plain string whether it read the request's X-Org-Id
// or a caller something stated in-process, and the identity boundary deliberately
// restores an unvalidated caller's own X-Org-Id for the data path. So the caller
// alone cannot be trusted, and nothing here tries: Acting reads the slot the
// trusted writers park — the boundary for a validated request (WithOrg), and
// WithActing for a call that states its own tenant in-process.
//
// The alternative was to mark the DOOR and admit a stated caller wherever the
// mark was absent. That fails OPEN: an app that serves HTTP without installing
// the boundary has no mark, and a forged header there reads exactly like an
// in-process statement. This way round, a writer that forgets to park is refused.
func TestActingTrustsOnlyAParkedTenant(t *testing.T) {
	stated := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	for _, tc := range []struct {
		what     string
		ctx      context.Context
		resolved bool
	}{
		// The boundary parked it for a validated request.
		{"a validated principal", context.WithValue(context.Background(), orgKey{}, "acme"), true},
		// An in-process caller stated it AND parked it — what plane.For does.
		{"a parked in-process tenant", WithActing(stated, "acme"), true},
		// The same caller with nothing parked. This is the shape a forged header
		// takes once zip has read it, so it must not resolve.
		{"a caller with nothing parked", stated, false},
		{"nothing at all", context.Background(), false},
	} {
		org, err := Acting(tc.ctx)
		if (err == nil) != tc.resolved {
			t.Errorf("%s: Acting = %q, %v — want resolved=%v", tc.what, org, err, tc.resolved)
		}
		if err == nil && org != "acme" {
			t.Errorf("%s: Acting resolved %q, want acme", tc.what, org)
		}
	}
}

// WithActing refuses rather than trims, because trimming is how two distinct
// orgs end up sharing one namespace. A refused park leaves the context alone —
// it does not park a folded neighbour's name.
func TestWithActingWillNotFoldOneOrgOntoAnother(t *testing.T) {
	for _, org := range []string{" acme", "acme ", "", "   ", "AC ME"} {
		got, err := Acting(WithActing(context.Background(), org))
		if err == nil {
			t.Errorf("a stated org %q was parked as %q, want refused", org, got)
		}
	}
	// The canonical name itself still parks.
	if got, err := Acting(WithActing(context.Background(), "acme")); err != nil || got != "acme" {
		t.Errorf("Acting = %q, %v — want acme", got, err)
	}
}
