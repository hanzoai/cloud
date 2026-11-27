package cloud

// AN ORG NOBODY CONFIGURED IS STILL HELD.
//
// Once the edge stopped counting validated callers by address, "no rule" would
// have meant "no ceiling" for every org nobody configured — one address's backstop
// per process was all that held it. An unconfigured org now carries a default
// derived from the per-IP knob, counted once for the deployment, and a validated
// caller with no org meets the same default by who it is.

import (
	"testing"

	"github.com/hanzoai/cloud/apps/gateway/edge"
)

// THE DEFAULT CAP IS ITS OWN KNOB. Nobody writing the platform's org_rpm leaves
// every org at DefaultOrgRPM, and the anonymous per-IP cap — cut in the middle of a
// flood, or moved to a per-minute window — does not move it.
//
// MUTATION: derive Cap from per_ip_rpm and window_sec and each row disagrees.
func TestTheDefaultCapIsNotThePerIPKnob(t *testing.T) {
	for _, p := range []edge.Policy{{}, {PerIPRPM: 100, WindowSec: 1}, {PerIPRPM: 10, WindowSec: 1}, {PerIPRPM: 100, WindowSec: 60}} {
		if got := staticPol(t, p).Cap("acme"); got != edge.DefaultOrgRPM {
			t.Fatalf("per_ip_rpm %d window_sec %d: Cap(acme) = %d, want %d", p.PerIPRPM, p.WindowSec, got, edge.DefaultOrgRPM)
		}
	}
	if got := staticPol(t, edge.Policy{PerIPRPM: 10, WindowSec: 1, OrgRPM: 90000}).Cap("acme"); got != 90000 {
		t.Fatalf("the platform's org_rpm: Cap(acme) = %d, want 90000", got)
	}
}

// orgPolicy is a policy store with platform as its boot policy and row written for
// org, as the gateway config route would.
func orgPolicy(t *testing.T, platform edge.Policy, org string, row edge.Policy) *edge.Store {
	t.Helper()
	st, err := edge.New(t.TempDir(), "admin", platform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Put(t.Context(), org, row); err != nil {
		t.Fatal(err)
	}
	return st
}
