package channels

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// discord.go is the Discord transport: envelope normalization from the
// ingress client and egress through integrations.SendDiscord (token custody
// stays in integrations).

// errNoRoute rejects egress to a room the org has no inbound-learned route
// for. routes.go maps it to 409 — the send needs a prior allowed inbound
// message from that room.
var errNoRoute = errors.New("channels: no reply route for this room")

// discordEndpoint is the send path; tests spy it, prod never repoints.
var discordEndpoint = func(ctx context.Context, org, channelID, replyTo, text string) (string, error) {
	return post(ctx, org, plane.ChatSendIn{Provider: "discord", Room: channelID, ReplyTo: replyTo, Text: text})
}

// DM:false is honest: the interactions ingress is guild-scoped only.
var discordTransport = transport{
	id:        "discord",
	caps:      capabilities{Group: true},
	normalize: discordNormalize,
	send:      discordEgress,
}

// discordNormalize maps a Discord Inbound (ExternalID = guild id, DedupeKey =
// interaction id) into the envelope. The ingress is guild slash commands
// only, so every room is a group.
func discordNormalize(ev plane.ChannelsIngestIn) (Message, bool) {
	in := ev
	return Message{
		Channel:     "discord",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Room:        Room{ID: in.Channel, Kind: RoomGroup},
		Text:        in.Text,
		Idempotency: in.DedupeKey,
	}, true
}

// discordEgress sends via the shared bot after the tenancy gate: a
// channel_route row exists only after an ALLOWED inbound interaction in that
// channel, so route presence IS the org's verified send capability
// (reply_root is "" for discord; presence is the datum).
func discordEgress(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error) {
	now := time.Now().Unix()
	_, ok, err := s.State.store.routeFor(ctx, org, "discord", m.Room.ID, now)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errNoRoute
	}
	id, err := discordEndpoint(ctx, org, m.Room.ID, m.ReplyTo, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: now}, nil
}
