// Copyright © 2026 Hanzo AI. MIT License.

package provider

import "testing"

// Which account an org ACTS AS, decided in one place.
//
// The connection key is (org, user, provider, label): user is "" only when the org
// owns the connection, and label is "" only for a single-account provider. A caller
// that looked up the row under an exact (user "", label "") key therefore found
// nothing whenever the install wrote either field — so this choice is made from the
// org's whole set instead of guessed from a key.
func TestOrgHeldPicksTheWorkspaceNotAPerson(t *testing.T) {
	// Org-held, but with a label — the shape an exact-key lookup missed.
	one, ok := orgHeld([]Connection{
		{Org: "hanzo", User: "", Provider: "slack", Label: "workspace",
			ExternalID: "T03258TMTMK", AccountLabel: "The Foundation"},
	})
	if !ok {
		t.Fatal("an org-held connection carrying a label was not found")
	}
	if one.AccountLabel != "The Foundation" {
		t.Errorf("account = %q, want the workspace", one.AccountLabel)
	}

	// A member's personal link must never win: an app acting for the org posts as
	// the WORKSPACE. Order must not decide it.
	for _, set := range [][]Connection{
		{{User: "z", AccountLabel: "z's personal"}, {User: "", AccountLabel: "The Foundation"}},
		{{User: "", AccountLabel: "The Foundation"}, {User: "z", AccountLabel: "z's personal"}},
	} {
		got, ok := orgHeld(set)
		if !ok || got.AccountLabel != "The Foundation" {
			t.Errorf("picked %q, want the org-held workspace", got.AccountLabel)
		}
	}

	// Absent is absent — never "the first row of an empty set".
	if _, ok := orgHeld(nil); ok {
		t.Error("reported an account with nothing connected")
	}
}

// An org holds a named account only through its own row for it: any case of the
// name, never another account, never a member's personal link.
func TestOrgAccountIsTheOrgsOwnRowForThatAccount(t *testing.T) {
	set := []Connection{
		{Org: "hanzo", User: "", Provider: "github", Label: "ZooAI", ExternalID: "1"},
		{Org: "hanzo", User: "", Provider: "github", Label: "hanzoai", ExternalID: "2"},
		{Org: "hanzo", User: "z", Provider: "github", Label: "luxfi", ExternalID: "3"},
	}
	if c, ok := orgAccount(set, "zooai"); !ok || c.ExternalID != "1" {
		t.Errorf("zooai: %+v %v, want the org's ZooAI row", c, ok)
	}
	if _, ok := orgAccount(set, "luxfi"); ok {
		t.Error("a member's personal link answered for the org")
	}
	if _, ok := orgAccount(set, "zoo"); ok {
		t.Error("an account the org does not hold answered")
	}
}
