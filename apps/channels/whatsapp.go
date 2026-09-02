package channels

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// whatsapp.go is the WhatsApp transport: envelope normalization from the
// ingress client and egress through the Cloud API, with token custody staying
// in integrations exactly as it does for slack and telegram.
//
// WhatsApp IS a direct-message channel and nothing else here. The Cloud API
// addresses a conversation by the person's phone number — there is no room a
// third party joins, no thread, and no group the business API can post into
// unprompted. So the room id IS the sender, and Group is false rather than
// aspirational: a capability this transport advertises is one routes.go will
// let an org try.
//
// The 24-hour rule is the reason egress is gated on a route the same way
// discord's is, and it is stricter than a convention: outside 24 hours since
// the person's last inbound message, Meta refuses free-form text and accepts
// only a pre-approved template. A route row exists only after an ALLOWED
// inbound message, so route presence is both the tenancy check and the
// evidence that a window was open. It can still have closed since — that
// answer comes from the API, and it surfaces as the send error rather than
// being guessed at here.
var whatsappTransport = transport{
	id:        "whatsapp",
	caps:      capabilities{DM: true},
	normalize: whatsappNormalize,
	send:      whatsappEgress,
}

// whatsappNormalize maps a WhatsApp Inbound into the envelope. ExternalID is
// the business phone-number id that received the message — the account, since
// one org can hold several numbers — and Channel is the sender's number, which
// is also the only address a reply can go to.
//
// The wamid does two jobs and is filed under both: it dedupes the delivery, and
// it is the message a reply quotes (Meta renders `context.message_id` as a
// quoted reply). Telegram files its triggering message id the same way.
func whatsappNormalize(ev plane.ChannelsIngestIn) (Message, bool) {
	in := ev
	if strings.TrimSpace(in.Channel) == "" {
		return Message{}, false
	}
	return Message{
		Channel: "whatsapp",
		Account: strings.ToLower(in.ExternalID),
		Sender:  Sender{ExternalID: in.User, Org: ev.Org},
		// Kind is DM unconditionally: the Cloud API has no other room to be.
		Room:        Room{ID: in.Channel, Kind: RoomDM},
		Text:        in.Text,
		ReplyTo:     in.DedupeKey,
		Idempotency: in.DedupeKey,
	}, true
}

// whatsappEndpoint is the send path; tests spy it, prod never repoints.
var whatsappEndpoint = func(ctx context.Context, org, to, replyTo, text string) (string, error) {
	return post(ctx, org, plane.ChatSendIn{Provider: "whatsapp", Room: to, ReplyTo: replyTo, Text: text})
}

// whatsappEgress sends after the tenancy gate. A channel_route row exists only
// after an ALLOWED inbound message from that number, so route presence IS the
// org's verified capability to reply to it — the same rule discord uses, and
// the one the 24-hour window makes load-bearing rather than merely tidy.
func whatsappEgress(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error) {
	now := time.Now().Unix()
	_, ok, err := s.State.store.routeFor(ctx, org, "whatsapp", m.Room.ID, now)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errNoRoute
	}
	id, err := whatsappEndpoint(ctx, org, m.Room.ID, m.ReplyTo, renderText(m))
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: now}, nil
}
