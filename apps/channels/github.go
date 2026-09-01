package channels

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// github.go is the GitHub transport: an issue or pull request is a room, its
// comments are the thread, and a comment that mentions the App is a turn. The
// integrations GitHub webhook normalizes the delivery and sends it here; the
// reply is an issue comment posted with the installation's token
// (integrations planeChatSend). One room per issue is what lets an org bind an
// agent to a repository's issues the way it binds one to a Slack channel.

// githubDoor is the send path; tests spy it, prod never repoints.
var githubDoor = func(ctx context.Context, org, room, text string) (string, error) {
	return post(ctx, org, plane.ChatSendIn{Provider: "github", Room: room, Text: text})
}

var githubTransport = transport{
	id:        "github",
	caps:      capabilities{DM: false, Group: false, Thread: true},
	normalize: githubNormalize,
	send:      githubEgress,
}

// githubNormalize maps a GitHub Inbound (ExternalID = installation id, Channel =
// "owner/repo#N", DedupeKey = comment id) into the envelope. Every issue is a
// thread: there is no direct message on GitHub and the comments under an issue
// are one conversation.
func githubNormalize(ev plane.ChannelsIngestIn) (Message, bool) {
	if ev.Channel == "" {
		return Message{}, false
	}
	return Message{
		Channel:     "github",
		Account:     strings.ToLower(ev.ExternalID),
		Sender:      Sender{ExternalID: ev.User, Org: ev.Org},
		Room:        Room{ID: ev.Channel, Kind: RoomThread},
		Text:        ev.Text,
		Idempotency: ev.DedupeKey,
	}, true
}

// githubEgress posts an issue comment through the org's own installation.
func githubEgress(ctx context.Context, _ *cloud.Service[state], org string, m Message) (Delivery, error) {
	id, err := githubDoor(ctx, org, m.Room.ID, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: time.Now().Unix()}, nil
}
