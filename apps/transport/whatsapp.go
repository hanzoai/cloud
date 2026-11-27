package transport

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// whatsapp.go is the WhatsApp transport: envelope normalization from the
// ingress client and egress through the Cloud API, with token custody staying
// in integrations exactly as it does for slack and telegram.
//
// WhatsApp carries a direct message and a group. A direct message is addressed
// by the person's number, and that number is the channel id. A group message
// names a group_id, and the channel id is that group: a reply goes back to the
// group with recipient_type group, not to the person who spoke. There is still
// no thread.
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
	caps:      capabilities{DM: true, Group: true},
	normalize: whatsappNormalize,
	send:      whatsappEgress,
}

// whatsappNormalize maps a WhatsApp Inbound into the envelope. ExternalID is
// the business phone-number id that received the message — the account, since
// one org can hold several numbers. Channel is the reply address: the sender's
// number for a direct message, the group id when the delivery named one.
//
// The wamid does two jobs and is filed under both: it dedupes the delivery, and
// it is the message a reply quotes (Meta renders `context.message_id` as a
// quoted reply). Telegram files its triggering message id the same way.
//
// Attachments are media the adapter already fetched. They ride the envelope
// and, in Text, the turn: the agent reads Text, and the file line there is the
// same slackFile a Slack message carries.
func whatsappNormalize(ev client.TransportIngestIn) (Message, bool) {
	in := ev
	if strings.TrimSpace(in.Channel) == "" {
		return Message{}, false
	}
	kind := ChannelDM
	if in.Group {
		kind = ChannelGroup
	}
	var atts []Attachment
	for _, a := range in.Attachments {
		atts = append(atts, Attachment{Kind: AttachmentKind(a.Kind), URL: a.URL, MIME: a.MIME})
	}
	return Message{
		Transport:   "whatsapp",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Channel:     Channel{ID: in.Channel, Kind: kind},
		Text:        in.Text,
		Attachments: atts,
		ReplyTo:     in.DedupeKey,
		Idempotency: in.DedupeKey,
	}, true
}

// whatsappEndpoint is the send path; tests spy it, prod never repoints.
var whatsappEndpoint = func(ctx context.Context, org, to, replyTo, text string, group bool) (string, error) {
	return post(ctx, org, client.ChatSendIn{
		Provider: "whatsapp", Channel: to, ReplyTo: replyTo, Text: text, Group: group,
	})
}

// whatsappEgress sends after the tenancy gate. A channel_route row exists only
// after an ALLOWED inbound message from that number, so route presence IS the
// org's verified capability to reply to it — the same rule discord uses, and
// the one the 24-hour window makes load-bearing rather than merely tidy.
func whatsappEgress(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error) {
	now := time.Now().Unix()
	_, ok, err := s.State.store.routeFor(ctx, org, "whatsapp", m.Channel.ID, now)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, errNoRoute
	}
	id, err := whatsappEndpoint(ctx, org, m.Channel.ID, m.ReplyTo, renderText(m), m.Channel.Kind == ChannelGroup)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{MessageID: id, Timestamp: now}, nil
}
