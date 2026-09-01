package channels

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// linear.go is the Linear transport: an issue is a room, its comments the
// thread, and a comment that mentions @hanzo is a turn. The integrations Linear
// webhook normalizes the delivery; the reply is a comment posted with the key of
// the person who bound the organization (integrations planeChatSend).

// linearDoor is the send path; tests spy it, prod never repoints.
var linearDoor = func(ctx context.Context, org, room, text string) (string, error) {
	return post(ctx, org, plane.ChatSendIn{Provider: "linear", Room: room, Text: text})
}

var linearTransport = transport{
	id:        "linear",
	caps:      capabilities{DM: false, Group: false, Thread: true},
	normalize: linearNormalize,
	send:      linearEgress,
}

// linearNormalize maps a Linear Inbound (ExternalID = organization id, Channel =
// issue id, DedupeKey = comment id) into the envelope. Every issue is a thread.
func linearNormalize(ev plane.ChannelsIngestIn) (Message, bool) {
	if ev.Channel == "" {
		return Message{}, false
	}
	return Message{
		Channel:     "linear",
		Account:     strings.ToLower(ev.ExternalID),
		Sender:      Sender{ExternalID: ev.User, Org: ev.Org},
		Room:        Room{ID: ev.Channel, Kind: RoomThread},
		Text:        ev.Text,
		Idempotency: ev.DedupeKey,
	}, true
}

// linearEgress posts a comment on the issue.
func linearEgress(ctx context.Context, _ *cloud.Service[state], org string, m Message) (Delivery, error) {
	id, err := linearDoor(ctx, org, m.Room.ID, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: time.Now().Unix()}, nil
}
