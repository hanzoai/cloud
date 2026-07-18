package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// telegram.go is the Telegram transport: envelope normalization from the
// ingress seam and egress through the ONE existing Bot API send path
// (integrations.SendTelegram).

// errRoomNotBound rejects egress to a room the caller's org has no verified
// binding for. routes.go maps it to 403 — the send is authenticated but the
// org holds no capability over that room.
var errRoomNotBound = errors.New("channels: room not bound to org")

// telegramDoor is the send door; tests spy it, prod never repoints.
var telegramDoor = integrations.SendTelegram

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
func telegramNormalize(ev integrations.IngressEvent) (Message, bool) {
	in := ev.In
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
func telegramEgress(ctx context.Context, _ *cloud.Service[state], org string, m Message) (Delivery, error) {
	boundOrg, ok := integrations.OrgForExternalID("telegram", m.Room.ID)
	if !ok || boundOrg != org {
		return Delivery{}, errRoomNotBound
	}
	chatID, err := strconv.ParseInt(m.Room.ID, 10, 64)
	if err != nil {
		return Delivery{}, fmt.Errorf("telegram: invalid room id %q", m.Room.ID)
	}
	// Best-effort reply threading: an unparseable ReplyTo degrades to a
	// top-level send rather than failing the message.
	replyTo, _ := strconv.ParseInt(m.ReplyTo, 10, 64)
	if err := telegramDoor(ctx, chatID, replyTo, renderText(m)); err != nil {
		return Delivery{}, err
	}
	// sendMessage's message id is not surfaced by the existing helper —
	// accepted tradeoff; the receipt carries the send time only.
	return Delivery{MessageID: "", Timestamp: time.Now().Unix()}, nil
}
