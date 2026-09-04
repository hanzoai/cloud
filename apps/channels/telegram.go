package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// telegram.go is the Telegram transport: envelope normalization from the
// ingress client and egress through the ONE existing Bot API send path
// (integrations.SendTelegram).

// errRoomNotBound rejects egress to a room the caller's org has no verified
// binding for. routes.go maps it to 403 — the send is authenticated but the
// org holds no capability over that room.
var errRoomNotBound = errors.New("channels: room not bound to org")

// telegramEndpoint is the sender; tests spy it, prod never repoints.
var telegramEndpoint = func(ctx context.Context, org string, chatID, replyTo int64, text string) error {
	room := strconv.FormatInt(chatID, 10)
	reply := ""
	if replyTo != 0 {
		reply = strconv.FormatInt(replyTo, 10)
	}
	_, err := post(ctx, org, client.ChatSendIn{Provider: "telegram", Room: room, ReplyTo: reply, Text: text})
	return err
}

var telegramTransport = transport{
	id:        "telegram",
	caps:      capabilities{DM: true, Group: true},
	normalize: telegramNormalize,
	send:      telegramEgress,
}

// telegramNormalize maps a Telegram Inbound (ExternalID = Channel = decimal
// chat id, User = from.id, ThreadID = triggering message id, DedupeKey =
// update_id) into the envelope. Telegram's ThreadID is the message to reply
// under, so it maps to ReplyTo, never RoomThread.
func telegramNormalize(ev client.ChannelsIngestIn) (Message, bool) {
	in := ev
	kind, ok := telegramRoomKind(in.Channel)
	if !ok {
		return Message{}, false
	}
	return Message{
		Channel:     "telegram",
		Account:     strings.ToLower(in.ExternalID),
		Sender:      Sender{ExternalID: in.User, Org: ev.Org},
		Room:        Room{ID: in.Channel, Kind: kind},
		Text:        in.Text,
		ReplyTo:     in.ThreadID,
		Idempotency: in.DedupeKey,
	}, true
}

// telegramRoomKind classifies a chat id. Bot API contract: group/supergroup
// chat ids are negative, private-chat ids positive. Unparseable (or zero)
// ids are unclassifiable — the event is dropped rather than guessed.
func telegramRoomKind(chatID string) (RoomKind, bool) {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil || id == 0 {
		return "", false
	}
	if id < 0 {
		return RoomGroup, true
	}
	return RoomDM, true
}

// telegramEgress sends via the shared bot after the tenancy gate: the
// chat→org bind is the isolation root — the global bot token never fires for
// an unbound or foreign chat. This is also the connected-check: an org that
// never onboarded Telegram has no bind and fails closed.
func telegramEgress(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error) {
	// TENANCY, checked HERE against a table this process owns. A channel_route row
	// exists only after an ALLOWED inbound in that chat, so its presence is this
	// org's verified capability to send there — the same datum discord uses.
	//
	// It used to ask integrations.OrgForExternalID, an in-process call to another
	// PROCESS: nil global, "not bound", every telegram send refused. Failing closed
	// is why that looked like nothing rather than an outage.
	//
	// The bind is ALSO verified on the answering side, where the global bot token
	// lives. Both, deliberately: one process holding the token and another deciding
	// who may spend it is exactly where a single check becomes a single point of
	// failure.
	now := time.Now().Unix()
	if _, ok, err := s.State.store.routeFor(ctx, org, "telegram", m.Room.ID, now); err != nil {
		return Delivery{}, err
	} else if !ok {
		return Delivery{}, errRoomNotBound
	}
	chatID, err := strconv.ParseInt(m.Room.ID, 10, 64)
	if err != nil {
		return Delivery{}, fmt.Errorf("telegram: invalid room id %q", m.Room.ID)
	}
	// Best-effort reply threading: an unparseable ReplyTo degrades to a
	// top-level send rather than failing the message.
	replyTo, _ := strconv.ParseInt(m.ReplyTo, 10, 64)
	if err := telegramEndpoint(ctx, org, chatID, replyTo, renderText(m)); err != nil {
		return Delivery{}, err
	}
	// sendMessage's message id is not surfaced by the existing helper —
	// accepted tradeoff; the receipt carries the send time only.
	return Delivery{MessageID: "", Timestamp: now}, nil
}
