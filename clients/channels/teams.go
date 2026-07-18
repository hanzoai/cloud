package channels

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// teams.go is the Teams transport: envelope normalization from the ingress
// seam and egress through the ONE existing Bot Connector send path
// (integrations.SendTeams).

// teamsDoor is the send door; tests spy it, prod never repoints.
var teamsDoor = integrations.SendTeams

var teamsTransport = transport{
	id:        "teams",
	caps:      capabilities{DM: true, Group: true},
	normalize: teamsNormalize,
	send:      teamsEgress,
}

// teamsNormalize maps a Teams Inbound (ExternalID = AAD tenant id, User =
// aadObjectId or from.id, Channel = conversation id, no ThreadID) into the
// envelope. Bot Framework contract: channel/group-chat conversation ids are
// 19:...@thread.*; personal chats are a:.... Unknown shapes classify DM —
// the fail-safe direction, since dmPolicy defaults to pairing (strictest).
func teamsNormalize(ev integrations.IngressEvent) (Message, bool) {
	in := ev.In
	kind := RoomDM
	if strings.HasPrefix(in.Channel, "19:") {
		kind = RoomGroup
	}
	return Message{
		Channel:     "teams",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Room:        Room{ID: in.Channel, Kind: kind},
		Text:        in.Text,
		Idempotency: in.DedupeKey,
	}, true
}

// teamsEgress sends via the Bot Connector at the stored reply root. The
// serviceURL is learned ONLY from JWT-verified inbound (IngressEvent.
// ReplyRoot); nothing else may mint it — that is both the security invariant
// (no attacker-chosen serviceURL) and the tenancy gate (an org can drive only
// conversations it was messaged from).
func teamsEgress(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error) {
	root, ok, err := s.State.store.routeFor(ctx, org, "teams", m.Room.ID)
	if err != nil {
		return Delivery{}, err
	}
	if !ok || root == "" {
		return Delivery{}, errNoRoute
	}
	if err := teamsDoor(ctx, root, m.Room.ID, renderText(m)); err != nil {
		return Delivery{}, err
	}
	// The Bot Connector activity id is not surfaced by the existing helper —
	// accepted tradeoff; the receipt carries the send time only.
	return Delivery{MessageID: "", Timestamp: time.Now().Unix()}, nil
}
