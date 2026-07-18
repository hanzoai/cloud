package channels

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// discord.go is the Discord transport: envelope normalization from the
// ingress seam and egress through integrations.SendDiscord (token custody
// stays in integrations).

// errNoRoute rejects egress to a room the org has no inbound-learned route
// for. routes.go maps it to 409 — the send needs a prior allowed inbound
// message from that room.
var errNoRoute = errors.New("channels: no reply route for this room")

// discordDoor is the send door; tests spy it, prod never repoints.
var discordDoor = integrations.SendDiscord

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
func discordNormalize(ev integrations.IngressEvent) (Message, bool) {
	in := ev.In
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
	_, ok, err := s.State.store.routeFor(ctx, org, "discord", m.Room.ID)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errNoRoute
	}
	id, err := discordDoor(ctx, m.Room.ID, m.ReplyTo, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: time.Now().Unix()}, nil
}
