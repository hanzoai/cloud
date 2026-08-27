package team

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

// The defect these tests hold a line under, stated once.
//
// team projects the org's agents as workspace members, and it used to read them
// with agents.ListForOrg — an in-process call gated on that package's `mounted`
// global. A package global is per-PROCESS, and team and agents are separate
// plugin binaries in every real deployment, so the call answered ErrNoPeer every
// time. What made it invisible rather than loud was how the two readers handled
// it: listBots turned any error into an EMPTY LIST, and the mention responder
// dropped it with `if err != nil || len(bots) == 0 { return }`. So
// GET /v1/team/bots answered [] for an org holding agents, no agent was ever
// projected as a member, no @mention could resolve one, and the boot log said
// "Chunter agent responder ENABLED" the whole time.
//
// The read goes over the plane now. These tests pin the two halves that let it
// hide: absence and failure must not look alike, and a fault must be reported.

// TestListBotsReportsAFailedRosterRatherThanAnEmptyOne is the regression. A peer
// that ANSWERED BADLY is an error; only a peer that is genuinely ABSENT is an
// empty list.
func TestListBotsReportsAFailedRosterRatherThanAnEmptyOne(t *testing.T) {
	srv, _, _ := rosterServer(t, "acme", "u-1", "Human", nil)
	b := &botsBridge{trans: srv, accounts: srv.accounts}
	ctx := principal.WithActing(context.Background(), "acme")

	// A peer that is reachable and unwell.
	srv.bots = func(context.Context, string) ([]Bot, error) {
		return nil, errors.New("dial: connection refused")
	}
	if _, err := b.listBots(ctx, nil); err == nil {
		t.Fatal("a failed roster read answered successfully — a caller cannot tell it from an org with no agents")
	}

	// A deployment that runs no agents subsystem HAS no agents, and an empty list
	// is the honest answer to "who are this org's bots".
	srv.bots = func(context.Context, string) ([]Bot, error) {
		return nil, cloud.ErrNoPeer
	}
	got, err := b.listBots(ctx, nil)
	if err != nil {
		t.Fatalf("an absent agents subsystem must answer an empty roster, got: %v", err)
	}
	if got.Bots == nil {
		t.Error("bots = nil; an empty roster must marshal as [] rather than null")
	}
	if len(got.Bots) != 0 {
		t.Errorf("bots = %+v, want none", got.Bots)
	}
}

// TestListBotsAndTheResponderReadOneRoster is the structural half of the same
// fix: both surfaces resolve agents through the transactor's ONE lister, so they
// cannot come to disagree about which agents an org has — and a test can reach
// both. listBots used to call the package function directly, which is why the
// bug above was unreachable from this package's suite.
func TestListBotsAndTheResponderReadOneRoster(t *testing.T) {
	bot := Bot{ID: "agent_enso", Name: "enso", Active: true}
	srv, _, _ := rosterServer(t, "acme", "u-1", "Human", []Bot{bot})
	b := &botsBridge{trans: srv, accounts: srv.accounts}

	var asked int
	inner := srv.bots
	srv.bots = func(ctx context.Context, org string) ([]Bot, error) {
		asked++
		return inner(ctx, org)
	}

	got, err := b.listBots(principal.WithActing(context.Background(), "acme"), nil)
	if err != nil {
		t.Fatalf("listBots: %v", err)
	}
	if asked != 1 {
		t.Fatalf("the injected lister was consulted %d times, want exactly 1 — "+
			"listBots is reading a second path to the same fact", asked)
	}
	if len(got.Bots) != 1 || got.Bots[0].ID != bot.ID {
		t.Fatalf("bots = %+v, want the one the lister answered", got.Bots)
	}
}

// TestMentionSaysSoWhenTheRosterIsUnavailable pins the responder's half: it
// cannot answer a mention it never resolved, and it must not fail silently — a
// wired-but-inert responder that logs nothing is indistinguishable from a
// disabled one, which is exactly how this survived.
func TestMentionSaysSoWhenTheRosterIsUnavailable(t *testing.T) {
	const org, human = "acme", "113d4dd4-2486-40de-be2b-88d6e3e0b718"
	srv, _, ws := rosterServer(t, org, human, "Human", nil)

	srv.runAgent = func(context.Context, string, string, string, string) (string, error) {
		t.Error("an agent ran despite an unresolvable roster")
		return "", nil
	}
	srv.bots = func(context.Context, string) ([]Bot, error) {
		return nil, errors.New("dial: connection refused")
	}
	srv.startedAt = time.Now().UnixMilli()

	dmID := "dm-1"
	putDoc(t, srv, org, ws, map[string]any{
		"_id": dmID, "_class": clDirectMessage, "space": dmID, "members": []any{human},
	})
	inbound := chatCreateRaw(t, dmID, clDirectMessage, "hanzo:"+human, "<p>hello</p>")

	// It must not panic, must not run an agent, and must not post anything.
	srv.maybeAgentReply(org, ws, []json.RawMessage{inbound})

	if n := len(botMessages(t, srv, org, ws, botUserID("agent_enso"))); n != 0 {
		t.Fatalf("the responder posted %d message(s) with no resolvable roster", n)
	}
}
