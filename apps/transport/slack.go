package transport

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// slack.go is the Slack transport: envelope normalization from the ingress
// client and egress through the ONE existing chat.postMessage path, reached over
// the plane (provider.SendSlackAt).

// slackEndpoint is the send path; tests spy it, prod never repoints.
var slackEndpoint = func(ctx context.Context, org, transport, threadTS, edit, text string, ctl client.Controls) (string, error) {
	return post(ctx, org, client.ChatSendIn{Provider: "slack", Channel: transport, ReplyTo: threadTS, Edit: edit, Text: text, Controls: ctl})
}

var slackTransport = transport{
	id:        "slack",
	caps:      capabilities{DM: true, Group: true, Thread: true, Edit: true},
	normalize: slackNormalize,
	send:      slackEgress,
}

// slackNormalize maps a Slack Inbound (ExternalID = team id, DedupeKey =
// event_id) into the envelope. Slack conversation-id contract: D* = IM,
// C* = public channel, G* = private/mpim — a D-prefixed conversation is a DM;
// a threaded event (thread_ts set) is a thread; everything else is a group.
//
// ThreadID and MessageID stay SEPARATE all the way through: the first is the
// thread the person spoke in, empty when they spoke in the channel, and the second
// is their message's own ts. turn.go needs both to answer where they are.
func slackNormalize(ev client.TransportIngestIn) (Message, bool) {
	in := ev
	kind := ChannelGroup
	switch {
	case strings.HasPrefix(in.Channel, "D"):
		kind = ChannelDM
	case in.ThreadID != "":
		kind = ChannelThread
	}
	return Message{
		Transport:   "slack",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Channel:     Channel{ID: in.Channel, Kind: kind, Public: in.Public},
		Text:        in.Text,
		ReplyTo:     in.ThreadID,
		ID:          in.MessageID,
		Idempotency: in.DedupeKey,
	}, true
}

// slackEgress posts via the org's OWN custodied bot token. Tenancy fails
// closed inside SendSlackAt via TokenFor(org, "slack"): no per-org token, no
// send — transport never sees a token, so no extra binding gate is needed.
func slackEgress(ctx context.Context, _ *cloud.Service[state], org string, m Message) (Delivery, error) {
	id, err := slackEndpoint(ctx, org, m.Channel.ID, m.ReplyTo, m.Edit, renderText(m), m.Controls)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: time.Now().Unix()}, nil
}
