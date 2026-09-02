package channels

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// slack.go is the Slack transport: envelope normalization from the ingress
// client and egress through the ONE existing chat.postMessage path, reached over
// the plane (integrations.SendSlackAt).

// slackDoor is the send path; tests spy it, prod never repoints.
var slackDoor = func(ctx context.Context, org, channel, threadTS, text string) error {
	_, err := post(ctx, org, plane.ChatSendIn{Provider: "slack", Room: channel, ReplyTo: threadTS, Text: text})
	return err
}

var slackTransport = transport{
	id:        "slack",
	caps:      capabilities{DM: true, Group: true, Thread: true},
	normalize: slackNormalize,
	send:      slackEgress,
}

// slackNormalize maps a Slack Inbound (ExternalID = team id, DedupeKey =
// event_id) into the envelope. Slack conversation-id contract: D* = IM,
// C* = public channel, G* = private/mpim — a D-prefixed conversation is a DM;
// a threaded event (thread_ts set) is a thread; everything else is a group.
func slackNormalize(ev plane.ChannelsIngestIn) (Message, bool) {
	in := ev
	kind := RoomGroup
	switch {
	case strings.HasPrefix(in.Channel, "D"):
		kind = RoomDM
	case in.ThreadID != "":
		kind = RoomThread
	}
	return Message{
		Channel:     "slack",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Room:        Room{ID: in.Channel, Kind: kind},
		Text:        in.Text,
		ReplyTo:     in.ThreadID,
		Idempotency: in.DedupeKey,
	}, true
}

// slackEgress posts via the org's OWN custodied bot token. Tenancy fails
// closed inside SendSlackAt via TokenFor(org, "slack"): no per-org token, no
// send — channels never sees a token, so no extra binding gate is needed.
func slackEgress(ctx context.Context, _ *cloud.Service[state], org string, m Message) (Delivery, error) {
	if err := slackDoor(ctx, org, m.Room.ID, m.ReplyTo, renderText(m)); err != nil {
		return Delivery{}, err
	}
	// chat.postMessage's ts is not surfaced by the existing helper — accepted
	// tradeoff; the receipt carries the send time only.
	return Delivery{MessageID: "", Timestamp: time.Now().Unix()}, nil
}
