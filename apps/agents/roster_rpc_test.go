package agents

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// mountedForTest installs a mounted agents service over per-test stores, which
// is what the plane handler resolves through, and restores the previous one.
func mountedForTest(t *testing.T) *state {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{stores: testStores(t)}}
	t.Cleanup(func() { mounted = prev })
	return &mounted.State
}

// seedAgent plants one ready agent in an org's own store.
func seedAgent(t *testing.T, st *state, org, id, name string) {
	t.Helper()
	seedAgentStatus(t, st, org, id, name, "ready")
}

// seedAgentStatus plants one agent carrying a specific registry status.
func seedAgentStatus(t *testing.T, st *state, org, id, name, status string) {
	t.Helper()
	now := time.Now().Unix()
	if err := storeOf(t, st, org).Create(context.Background(), Agent{
		ID: id, Org: org, Name: name, Status: status,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed agent %q: %v", id, err)
	}
}

// numFields reports how many fields a wire type declares.
func numFields[T any]() int { return reflect.TypeFor[T]().NumField() }

// TestRosterAnswersTheCallersOwnOrg is the tenancy contract of the op, driven
// through planeRoster itself: the org comes from the caller's plane identity, so
// two orgs asking the same question get their own answers.
func TestRosterAnswersTheCallersOwnOrg(t *testing.T) {
	st := mountedForTest(t)
	seedAgent(t, st, "acme", "a-1", "helper")
	seedAgent(t, st, "other", "a-2", "stranger")

	got, err := planeRoster(cloud.For(context.Background(), "acme"), &plane.RosterIn{})
	if err != nil {
		t.Fatalf("planeRoster: %v", err)
	}
	if len(got.Agents) != 1 || got.Agents[0].ID != "a-1" {
		t.Fatalf("agents = %+v, want only acme's own", got.Agents)
	}
}

// TestRosterRefusesAnAnonymousCaller fails CLOSED. A roster read arriving with no
// principal must fail rather than pick a tenant — the same rule the session ops
// hold, and the reason RosterIn has no org field to fall back to.
func TestRosterRefusesAnAnonymousCaller(t *testing.T) {
	mountedForTest(t)
	if _, err := planeRoster(context.Background(), &plane.RosterIn{}); err == nil {
		t.Fatal("planeRoster answered a caller with no org")
	}
}

// TestRosterInNamesNoTenant is the structural half, and it is the one that
// survives a careless edit: the cross-tenant read is unrepresentable rather than
// merely refused, because there is no field to put an org in. This is the same
// rule TestNoPlaneInputCanNameAnOrg states fleet-wide, held here beside the type
// it was just added to.
func TestRosterInNamesNoTenant(t *testing.T) {
	if n := numFields[plane.RosterIn](); n != 0 {
		t.Errorf("plane.RosterIn has %d field(s); it must have none — "+
			"a roster read that can name its own tenant enumerates somebody else's agents", n)
	}
}

// TestRosterCarriesStatusVerBATIM pins the split of responsibility: the wire
// carries the registry status as-is and the CALLER decides what counts as live.
// Folding that into a bool here would move a policy — which statuses project as a
// workspace member — to the wrong side of the boundary.
func TestRosterCarriesStatusVerbatim(t *testing.T) {
	st := mountedForTest(t)
	seedAgentStatus(t, st, "acme", "a-archived", "old", "archived")

	got, err := planeRoster(cloud.For(context.Background(), "acme"), &plane.RosterIn{})
	if err != nil {
		t.Fatalf("planeRoster: %v", err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(got.Agents))
	}
	if got.Agents[0].Status != "archived" {
		t.Errorf("status = %q, want the registry value verbatim", got.Agents[0].Status)
	}
}

// TestRosterAnswersAnEmptyListNotNull keeps "this org has no agents" and "the
// call failed" distinguishable on the wire. A nil slice marshals to null and
// reads as neither.
func TestRosterAnswersAnEmptyListNotNull(t *testing.T) {
	mountedForTest(t)
	got, err := planeRoster(cloud.For(context.Background(), "empty-org"), &plane.RosterIn{})
	if err != nil {
		t.Fatalf("planeRoster: %v", err)
	}
	if got.Agents == nil {
		t.Fatal("agents = nil; an org with no agents must answer [] so absence is not confusable with failure")
	}
	if len(got.Agents) != 0 {
		t.Fatalf("agents = %+v, want none", got.Agents)
	}
}

// TestRosterOpIsDeclaredOnThePlane proves the op is REACHABLE by the name its
// generated client calls, not merely that the handler compiles. exposeRoster runs
// at Mount; this asserts the registration landed under the contract's op id.
//
// ResetPlane first, because the plane is a process-wide app and another test in
// this package mounts the subsystem: registering twice is two declarations of one
// address, which zip refuses outright rather than letting the second win. That
// refusal is the framework working — it is the same guard that stops two apps
// claiming one route — so the test yields to it instead of being made tolerant.
func TestRosterOpIsDeclaredOnThePlane(t *testing.T) {
	mountedForTest(t)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeRoster()
	var found bool
	for _, op := range cloud.Plane().Commands() {
		if op.OperationID == plane.AgentsRoster {
			found = true
			if !strings.Contains(strings.ToLower(op.Summary), "agents") {
				t.Errorf("summary = %q, want it to say what the op answers", op.Summary)
			}
		}
	}
	if !found {
		t.Fatalf("no plane op declared as %q — the generated client would call a name nothing answers", plane.AgentsRoster)
	}
}
