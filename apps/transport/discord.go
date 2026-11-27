package transport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// discord.go is the Discord transport: envelope normalization from the
// ingress client and egress through provider.SendDiscord (token custody
// stays in integrations).

// errNoRoute rejects egress to a channel the org has no inbound-learned route
// for. routes.go maps it to 409 — the send needs a prior allowed inbound
// message from that channel.
var errNoRoute = errors.New("transport: no reply route for this channel")

// discordEndpoint is the send path; tests spy it, prod never repoints.
var discordEndpoint = func(ctx context.Context, org, channelID, replyTo, text string) (string, error) {
	return post(ctx, org, client.ChatSendIn{Provider: "discord", Channel: channelID, ReplyTo: replyTo, Text: text})
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
// only, so every channel is a group.
func discordNormalize(ev client.TransportIngestIn) (Message, bool) {
	in := ev
	return Message{
		Transport:   "discord",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Channel:     Channel{ID: in.Channel, Kind: ChannelGroup},
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
	_, ok, err := s.State.store.routeFor(ctx, org, "discord", m.Channel.ID, now)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errNoRoute
	}
	id, err := discordEndpoint(ctx, org, m.Channel.ID, m.ReplyTo, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: now}, nil
}
