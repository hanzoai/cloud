// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package meet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// answers is a workspace authority that ANSWERS — the thing production has and no
// test did. Every IAM-lane test before this one stopped at an unanswerable ask, so
// the rules PAST the ask (a machine credential holds no seat; a guest holds no
// seat) were never reached and could be deleted without failing anything.
//
// It records what it was asked, which is the other half of the proof: a rule that
// refuses BEFORE the ask must leave this untouched. "Refused" and "refused for the
// right reason" are different results, and only the second one survives a mutation.
// It answers per WORKSPACE, because the rows do: a member of A is not a member of
// B, and that difference IS the tenant boundary meet enforces. An authority that
// said yes to any workspace would make the boundary untestable at the endpoint.
type answers struct {
	// mu guards the counters below. The real authority is another PROCESS, so it is
	// asked concurrently the moment two requests are in flight — which is exactly
	// what the two-replica race in record_test.go models, and what made this the
	// first fixture in the package to need a lock.
	mu      sync.Mutex
	row     func(workspace, subject string) plane.Member // what the rows say about one workspace
	list    plane.Spaces                                 // what they say about all of them
	err     error                                        // or why they cannot be read
	asked   int                                          // how many times the authority was consulted
	subject string                                       // the subject it was consulted about
	saw     string                                       // the workspace it was consulted about
}

func (a *answers) member(_ context.Context, workspace, subject string) (*plane.Member, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked++
	a.subject, a.saw = subject, workspace
	if a.err != nil {
		return nil, a.err
	}
	if a.row == nil {
		return &plane.Member{}, nil
	}
	m := a.row(workspace, subject)
	return &m, nil
}

func (a *answers) workspaces(_ context.Context, subject string) (*plane.Spaces, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked++
	a.subject = subject
	if a.err != nil {
		return nil, a.err
	}
	s := a.list
	return &s, nil
}

// holds is the authority for a person who holds exactly these roles, keyed by
// workspace. BOTH answers come off the one map, so the lobby's offer and the
// mint's grant cannot be handed different rows and drift apart.
func holds(roles map[string]string) *answers {
	a := &answers{list: plane.Spaces{Account: account}}
	a.row = func(workspace, _ string) plane.Member {
		role, ok := roles[workspace]
		return plane.Member{Member: ok, Role: role, Account: account}
	}
	for ws, role := range roles {
		a.list.Items = append(a.list.Items, plane.Space{UUID: ws, Role: role})
	}
	return a
}

// anyone says yes to every question. It is what proves a refusal came from a RULE
// rather than from an unanswerable ask: with it in place, whatever is still
// refused was refused before the authority was ever reached.
func anyone() *answers {
	return &answers{
		row: func(string, string) plane.Member {
			return plane.Member{Member: true, Role: token.RoleOwner, Account: account}
		},
		list: plane.Spaces{Account: account, Items: []plane.Space{{UUID: workspaceA, Role: token.RoleOwner}}},
	}
}

// human is the attestation the boundary mints for a real person: an org and a
// verified `sub`.
var human = principal.Principal{Org: org, User: "ada", Subject: ada}

// onLane runs fn inside a request carrying an attested principal — the ONE way to
// model what the boundary produces, since principal.Mint is the boundary's own call.
func onLane(t *testing.T, p principal.Principal, fn func(*zip.Ctx)) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error {
		principal.Mint(c, p)
		fn(c)
		return c.String(http.StatusOK, "ok")
	})
	if _, err := app.Test(httptest.NewRequest(http.MethodGet, "/probe", nil)); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

// TestIAMLaneSeatsAHumanAndRefusesAMachine is the test the machine-credential rule
// never had, and the one that makes deleting it fail.
//
// The boundary stamps an org AND a user for an sk- API key, so a lane selected on
// "has an org and a user" would put a machine on the lane whose whole question is
// which human is in this room — and LiveKit seats whatever identity it is handed,
// evicting the live participant on a duplicate. A key principal carries no `sub`.
//
// Both halves are asserted, and the second is what pins the RULE rather than the
// outcome: the authority must never even be CONSULTED about a machine. Neuter
// `p.Subject != ""` in admits and the machine reaches admitsMember, which asks an
// authority that says "member, owner" — so it is seated, and both assertions fail.
func TestIAMLaneSeatsAHumanAndRefusesAMachine(t *testing.T) {
	const seat = "550e8400-e29b-41d4-a716-446655440000"

	t.Run("a human is seated", func(t *testing.T) {
		a := anyone()
		st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
		var j joiner
		var ok bool
		onLane(t, human, func(c *zip.Ctx) { j, ok = st.admits(c, roomIn(workspaceA)) })
		if !ok {
			t.Fatal("the IAM lane refused a privileged member with an authority that admits them")
		}
		if j.account != seat {
			t.Errorf("account = %q, want the AUTHORITY's %q — meet must never derive its own", j.account, seat)
		}
		if a.asked != 1 || a.subject != human.Subject {
			t.Errorf("authority asked %d times about %q, want 1 about %q", a.asked, a.subject, human.Subject)
		}
	})

	t.Run("a machine is refused before anything is asked", func(t *testing.T) {
		for _, p := range machinePrincipals {
			a := anyone()
			st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
			var ok bool
			onLane(t, p, func(c *zip.Ctx) { _, ok = st.admits(c, roomIn(workspaceA)) })
			if ok {
				t.Fatalf("SECURITY: %+v was seated by an authority that answers", p)
			}
			if a.asked != 0 {
				t.Errorf("SECURITY: the authority was consulted about %+v — the exclusion is not what refused it", p)
			}
		}
	})
}

// TestLobbyOffersToAHumanAndRefusesAMachine is the same proof on the READ, which
// is where a gate quietly gets relaxed. Same shape, same mutation: neuter the
// subject requirement and a machine reads a tenant's workspace list — which is the
// one string needed to name a room in it.
func TestLobbyOffersToAHumanAndRefusesAMachine(t *testing.T) {
	const seat = "550e8400-e29b-41d4-a716-446655440000"
	rows := plane.Spaces{
		Account: seat,
		Name:    "Ada",
		Items:   []plane.Space{{UUID: workspaceA, Name: "Acme", Role: token.RoleOwner}},
	}

	t.Run("a human is offered their workspaces", func(t *testing.T) {
		a := &answers{list: rows}
		st := state{authority: a}
		var sp plane.Spaces
		var ok bool
		onLane(t, human, func(c *zip.Ctx) { sp, ok = st.spaces(c) })
		if !ok {
			t.Fatal("the IAM lane refused a human with an authority that answers")
		}
		if sp.Account != seat || len(sp.Items) != 1 || sp.Items[0].UUID != workspaceA {
			t.Fatalf("spaces = %+v, want the authority's answer verbatim", sp)
		}
		if a.asked != 1 || a.subject != human.Subject {
			t.Errorf("authority asked %d times about %q, want 1 about %q", a.asked, a.subject, human.Subject)
		}
	})

	t.Run("a machine is refused before anything is asked", func(t *testing.T) {
		for _, p := range machinePrincipals {
			a := &answers{list: rows}
			st := state{authority: a}
			var ok bool
			onLane(t, p, func(c *zip.Ctx) { _, ok = st.spaces(c) })
			if ok {
				t.Fatalf("SECURITY: %+v read a tenant's workspace list", p)
			}
			if a.asked != 0 {
				t.Errorf("SECURITY: the authority was consulted about %+v — the exclusion is not what refused it", p)
			}
		}
	})
}

// TestTheAuthoritysRoleDecides pins the rules that live PAST the ask, which is the
// whole class the missing peer was hiding. The role comes from the rows, not from
// anything the caller signed, and an unprivileged one holds no seat and is not
// offered a room.
func TestTheAuthoritysRoleDecides(t *testing.T) {
	for _, tc := range []struct {
		role    string
		admits  bool
		offered bool
	}{
		{token.RoleOwner, true, true},
		{token.RoleAdmin, true, true},
		{token.RoleMember, true, true},
		{token.RoleGuest, false, false},
		{"", false, false},
		{"auditor", false, false}, // a role invented tomorrow starts without a seat
	} {
		a := holds(map[string]string{workspaceA: tc.role})
		st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}

		var admitted, read bool
		var sp plane.Spaces
		onLane(t, human, func(c *zip.Ctx) {
			_, admitted = st.admits(c, roomIn(workspaceA))
			sp, read = st.spaces(c)
		})
		if admitted != tc.admits {
			t.Errorf("role %q: admits = %v, want %v", tc.role, admitted, tc.admits)
		}
		// The read admits every member and lets session() narrow, so what is pinned
		// here is the OFFER after narrowing — the same predicate the mint uses.
		if !read {
			t.Fatalf("role %q: the lobby read was refused outright", tc.role)
		}
		offered := 0
		for _, w := range sp.Items {
			if privileged(w.Role) {
				offered++
			}
		}
		if (offered == 1) != tc.offered {
			t.Errorf("role %q: offered %d workspace(s), want offered=%v", tc.role, offered, tc.offered)
		}
		if admitted != tc.offered {
			t.Errorf("role %q: OFFER=%v but ADMIT=%v — the lobby and the mint disagree on the IAM lane",
				tc.role, tc.offered, admitted)
		}
	}
}

// TestNotAMemberIsRefusedOnBothDoors: an authority that answers "no row" is a
// refusal, and it is one the AUTHORITY made — distinct from an authority that
// could not be reached, which is the next test.
func TestNotAMemberIsRefusedOnBothDoors(t *testing.T) {
	a := &answers{}
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
	onLane(t, human, func(c *zip.Ctx) {
		if _, ok := st.admits(c, roomIn(workspaceA)); ok {
			t.Error("a caller with no member row was seated")
		}
		sp, ok := st.spaces(c)
		if !ok {
			t.Error("a caller with no rows should read an EMPTY lobby, not be refused one")
		}
		if len(sp.Items) != 0 {
			t.Errorf("spaces = %+v, want empty", sp.Items)
		}
	})
}

// TestAnUnreachableAuthorityIsARefusal. An authority that cannot answer is never
// an assumption — on the mint because a seat would be granted on nothing, and on
// the lobby because "you have no workspaces" is a LIE when the truth is "team is
// down", and it sends someone to ask for an invite they already have.
func TestAnUnreachableAuthorityIsARefusal(t *testing.T) {
	a := &answers{err: context.DeadlineExceeded}
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
	onLane(t, human, func(c *zip.Ctx) {
		if _, ok := st.admits(c, roomIn(workspaceA)); ok {
			t.Error("SECURITY: an unreachable authority admitted a caller")
		}
		if _, ok := st.spaces(c); ok {
			t.Error("an unreachable authority produced a lobby answer; it must refuse")
		}
	})
}

// TestTheIAMLaneNeverWidensTheRoom. The authority is asked about the room's OWN
// workspace and nothing else, so a room naming another tenant cannot be answered
// with this caller's membership somewhere else. The QUESTION is what is pinned:
// an authority that answers "yes" to everything must still not seat this caller,
// because the workspace it was asked about is the room's.
//
// It also pins what a malformed room name actually does, which an authority that
// could never answer had made unobservable — and which the suite described WRONGLY
// until this test could see it. strings.Cut returns the WHOLE string when there is
// no separator, so:
//
//   - "_standup"      -> workspace "" -> refused by the guard, nothing is asked;
//   - "no-separator"  -> workspace "no-separator" -> ASKED, and refused because no
//     tenant has a workspace by that name.
//
// Both are refusals and both are fail-closed, but only the first is a refusal
// meet makes on its own. Saying so is the difference between a test that documents
// the program and one that flatters it.
func TestTheIAMLaneNeverWidensTheRoom(t *testing.T) {
	// A room in ANOTHER workspace asks about THAT workspace, never the caller's.
	a := anyone()
	st := state{apiKey: apiKey, apiSecret: apiSecret, authority: a}
	onLane(t, human, func(c *zip.Ctx) { _, _ = st.admits(c, roomIn(workspaceB)) })
	if a.saw != workspaceB {
		t.Errorf("the authority was asked about %q for a room in %q", a.saw, workspaceB)
	}

	// An EMPTY leading segment is refused by meet itself.
	a = anyone()
	st.authority = a
	var ok bool
	onLane(t, human, func(c *zip.Ctx) { _, ok = st.admits(c, "_standup") })
	if ok {
		t.Error("SECURITY: a room with an empty workspace segment was admitted")
	}
	if a.asked != 0 {
		t.Errorf("the authority was consulted about a room with no workspace segment (asked about %q)", a.saw)
	}

	// A name with no separator at all IS asked about — as itself — and no tenant
	// holds a workspace by that name, so the rows refuse it.
	a = holds(nil) // the honest answer for a workspace nobody has
	st.authority = a
	onLane(t, human, func(c *zip.Ctx) { _, ok = st.admits(c, "no-separator") })
	if ok {
		t.Error("a room naming a workspace nobody has was admitted")
	}
	if a.saw != "no-separator" {
		t.Errorf("the authority was asked about %q, want the room name itself", a.saw)
	}
}

// TestAStateWithNoAuthorityAsksTheRealPeer proves rows() is nil-safe by
// construction: every load() failure path returns a state carrying only a reason,
// and a lobby read on such a deploy must answer like production (a refusal,
// because there is no peer) rather than panic.
func TestAStateWithNoAuthorityAsksTheRealPeer(t *testing.T) {
	var zero state
	if _, isPeer := zero.rows().(peer); !isPeer {
		t.Fatal("a state with no authority must ask the real peer")
	}
	st := state{reason: "unconfigured"}
	onLane(t, human, func(c *zip.Ctx) {
		if _, ok := st.spaces(c); ok {
			t.Error("an unconfigured deploy with no plane peer answered a lobby read")
		}
	})
}
