// Copyright © 2026 Hanzo AI. MIT License.

package channels

import (
	"testing"

	"github.com/hanzoai/cloud/apps/integrations"
)

// The listing must agree with /v1/integrations about whether a workspace is
// connected, whatever composite key the install filed the row under.
//
// This is the defect it was written for: Slack WAS connected — /v1/integrations
// reported the workspace, its team id and nine scopes — while this listing said
// `connected: false` for the same install, because it asked for the row filed
// under the exact key (user "", label ""). One install, two answers, and a send
// refused on the strength of the wrong one.
func TestOrgConnectionDoesNotGuessTheKey(t *testing.T) {
	// An org-held row with a NON-empty label — the shape the exact-key lookup missed.
	conns := []integrations.Connection{
		{Org: "hanzo", User: "", Provider: "slack", Label: "workspace", ExternalID: "T03258TMTMK", AccountLabel: "The Foundation"},
	}
	got, ok := pickOrgConnection(conns)
	if !ok {
		t.Fatal("an org-held connection with a label was not found — the listing would say disconnected while integrations says connected")
	}
	if got.AccountLabel != "The Foundation" {
		t.Errorf("account = %q, want the workspace", got.AccountLabel)
	}

	// A channel posts as the WORKSPACE: the org-held row wins over a member's
	// personal link, whatever order the store returned them in.
	mixed := []integrations.Connection{
		{Org: "hanzo", User: "z", Provider: "slack", AccountLabel: "z's personal"},
		{Org: "hanzo", User: "", Provider: "slack", AccountLabel: "The Foundation"},
	}
	got, ok = pickOrgConnection(mixed)
	if !ok || got.AccountLabel != "The Foundation" {
		t.Errorf("picked %q, want the org-held workspace — a channel must not post as a person", got.AccountLabel)
	}

	// Nothing connected stays nothing: absent is not "the first row".
	if _, ok := pickOrgConnection(nil); ok {
		t.Error("reported connected with no connections")
	}
}
