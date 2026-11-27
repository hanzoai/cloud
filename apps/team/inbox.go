package team

// inbox.go is a person's INBOX: the notifications the write path's triggers
// (notify.go) filed for them — mentioned in a channel, messaged directly, a reply
// or comment on something they follow, assigned something — read, marked read
// and archived through the same documents the Team client's Inbox shows.
//
// A notification is an InboxNotification row whose `user` is the account it is
// for. Reading one is setting `isViewed`, archiving one is setting `archived`,
// and both are ordinary updates through the client's write path, so the badge in
// an open Team client moves the moment a native client clears it.

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// inboxMax bounds one inbox read, newest first.
const inboxMax = 200

// Why a notification was filed, as teamInboxItem.Reason publishes it.
const (
	reasonMention  = "mention"
	reasonDirect   = "dm"
	reasonReply    = "reply"
	reasonComment  = "comment"
	reasonAssigned = "assigned"
	reasonOther    = "other"
)

// teamInboxRead filters the caller's inbox.
type teamInboxRead struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty"`
	// Archived lists the archived notifications instead of the live ones.
	Archived bool `json:"archived,omitempty"`
}

// teamInbox is the caller's notifications, newest first.
type teamInbox struct {
	// Items are the notifications, newest first, at most 200.
	Items []teamInboxItem `json:"items"`
	// Unread is how many of the caller's live (unarchived) notifications are
	// unread — the number a badge shows, counted over all of them rather than
	// the page.
	Unread int `json:"unread"`
}

// teamInboxItem is one notification and what it is about.
type teamInboxItem struct {
	// ID is the notification's own id — what read and archive address.
	ID string `json:"id"`
	// Space is the space uuid it was filed in.
	Space string `json:"space"`
	// Reason is why it was filed: "mention", "dm" (a direct message), "reply" (in
	// a thread the caller is in), "comment" (on something the caller follows),
	// "assigned", or "other".
	Reason string `json:"reason"`
	// Read reports that the caller has seen it.
	Read bool `json:"read"`
	// Archived reports that the caller archived it.
	Archived bool `json:"archived"`
	// CreatedOn is when it was filed, unix milliseconds.
	CreatedOn int64 `json:"createdOn"`
	// Channel is the channel it is about, when it is about a channel.
	Channel string `json:"channel,omitempty"`
	// Doc is the document it is about, when it is about a document.
	Doc string `json:"doc,omitempty"`
	// Thread is the message whose thread it is about, for a reply.
	Thread string `json:"thread,omitempty"`
	// Message is the message that caused it, as the message ops answer it.
	// Absent when that message has since been deleted, or is in a channel the
	// caller can no longer see.
	Message *teamMessage `json:"message,omitempty"`
}

// teamInboxAt names one notification.
type teamInboxAt struct {
	// ID is the notification, from the path.
	ID string `json:"id"`
	// Space is the space uuid it was filed in. Body-only.
	Space string `json:"space" url:"-"`
}

// teamInboxAll names the inbox to clear.
type teamInboxAll struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty"`
}

// teamInboxCleared says how much a read-all changed.
type teamInboxCleared struct {
	// Read is how many notifications this call marked read.
	Read int `json:"read"`
}

// registerInbox declares the inbox ops. The group is built here so cmd/zipdoc
// can resolve the prefix from this file.
func (b *channelBridge) registerInbox(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/inbox", b.listInbox)
	g.Post("/inbox/read", b.readAll)
	g.Post("/inbox/:id/read", b.readOne)
	g.Post("/inbox/:id/archive", b.archiveOne)
}

// ListInbox returns the caller's notifications, newest first, each with the
// channel or document it is about and the message that caused it.
//
// These are the rows the Team client's Inbox reads, so a notification cleared
// there is cleared here. Only the caller's own notifications are ever listed.
//
// Example: {"items": [{"id": "notif:7a1c:9f2…", "space": "0e3c…", "reason": "mention", "read": false, "archived": false, "createdOn": 1756598400000, "channel": "6543", "message": {"id": "7a1c", "channel": "6543", "author": "b1…", "text": "@Ada can you look?", "mentions": ["9f2…"], "createdOn": 1756598400000, "replies": 0, "reactions": [], "files": []}}], "unread": 1}
func (b *channelBridge) listInbox(ctx context.Context, in *teamInboxRead) (*teamInbox, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	mine, err := b.notes(s)
	if err != nil {
		return nil, err
	}
	out := &teamInbox{Items: []teamInboxItem{}}
	x := b.extras(s.org, s.ws)
	for _, n := range mine {
		if !truthy(n["archived"]) && !truthy(n["isViewed"]) {
			out.Unread++
		}
		if truthy(n["archived"]) != in.Archived || len(out.Items) >= inboxMax {
			continue
		}
		out.Items = append(out.Items, b.itemOf(s, n, x))
	}
	return out, nil
}

// ReadNotification marks one of the caller's notifications read and answers it.
// Another person's notification is a 404, as one that does not exist is.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) readOne(ctx context.Context, in *teamInboxAt) (*teamInboxItem, error) {
	return b.markOne(ctx, in, "isViewed")
}

// ArchiveNotification archives one of the caller's notifications — read, and
// out of the live inbox — and answers it. It is listed again with
// `archived=true`.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) archiveOne(ctx context.Context, in *teamInboxAt) (*teamInboxItem, error) {
	return b.markOne(ctx, in, "archived")
}

// ReadAllNotifications marks every live notification of the caller's read, and
// says how many it changed.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) readAll(ctx context.Context, in *teamInboxAll) (*teamInboxCleared, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	mine, err := b.notes(s)
	if err != nil {
		return nil, err
	}
	var txes []map[string]any
	contexts := map[string]bool{}
	for _, n := range mine {
		if truthy(n["archived"]) || truthy(n["isViewed"]) {
			continue
		}
		txes = append(txes, updateTx(str(n["_id"]), str(n["_class"]), str(n["space"]), s.social(),
			map[string]any{"isViewed": true}))
		contexts[str(n["docNotifyContext"])] = true
	}
	read := len(txes)
	txes = append(txes, b.viewed(s, contexts)...)
	if len(txes) > 0 {
		if _, err := apply(b.session(s), b.trans, s.ws, txes...); err != nil {
			return nil, err
		}
	}
	return &teamInboxCleared{Read: read}, nil
}

// markOne sets one flag on one of the caller's notifications. Archiving also
// marks it read, as the Team client's archive does.
func (b *channelBridge) markOne(ctx context.Context, in *teamInboxAt, flag string) (*teamInboxItem, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	n, err := b.note(s, in.ID)
	if err != nil {
		return nil, err
	}
	ops := map[string]any{"isViewed": true}
	if flag == "archived" {
		ops["archived"] = true
	}
	txes := []map[string]any{updateTx(str(n["_id"]), str(n["_class"]), str(n["space"]), s.social(), ops)}
	txes = append(txes, b.viewed(s, map[string]bool{str(n["docNotifyContext"]): true})...)
	if _, err := apply(b.session(s), b.trans, s.ws, txes...); err != nil {
		return nil, err
	}
	after, err := b.note(s, in.ID)
	if err != nil {
		return nil, err
	}
	v := b.itemOf(s, after, b.extras(s.org, s.ws))
	return &v, nil
}

// viewed stamps the caller's notify contexts as seen now — the other half of
// "read" in the Team client, which draws a conversation's unread marker from
// the context rather than from the notifications.
func (b *channelBridge) viewed(s seat, contexts map[string]bool) []map[string]any {
	now := time.Now().UnixMilli()
	var out []map[string]any
	for id := range contexts {
		if id == "" {
			continue
		}
		c, _ := b.trans.store.get(s.org, s.ws, id)
		if c == nil || str(c["user"]) != s.account {
			continue
		}
		out = append(out, updateTx(id, clDocNotifyContext, str(c["space"]), s.social(),
			map[string]any{"lastViewedTimestamp": now}))
	}
	return out
}

// notes is every notification filed for the caller in their space, newest
// first.
func (b *channelBridge) notes(s seat) ([]map[string]any, error) {
	docs, err := b.trans.store.byClasses(s.org, s.ws, b.trans.hier.candidates(clInboxNotification))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: inbox: %v", err)
	}
	var mine []map[string]any
	for _, d := range docs {
		if b.trans.hier.isDerived(str(d["_class"]), clInboxNotification) && str(d["user"]) == s.account {
			mine = append(mine, d)
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		a, c := asInt64(mine[i]["createdOn"]), asInt64(mine[j]["createdOn"])
		if a != c {
			return a > c
		}
		return str(mine[i]["_id"]) > str(mine[j]["_id"])
	})
	return mine, nil
}

// note resolves one of the caller's own notifications, answering 404 for
// anything else — including somebody else's.
func (b *channelBridge) note(s seat, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, zip.ErrBadRequest("notification id is required")
	}
	n, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: notification: %v", err)
	}
	if n == nil || !b.trans.hier.isDerived(str(n["_class"]), clInboxNotification) || str(n["user"]) != s.account {
		return nil, zip.ErrNotFound("notification not found")
	}
	return n, nil
}

// itemOf projects one notification with what it is about.
func (b *channelBridge) itemOf(s seat, n map[string]any, x extras) teamInboxItem {
	v := teamInboxItem{
		ID: str(n["_id"]), Space: s.ws, Reason: reasonOther,
		Read: truthy(n["isViewed"]), Archived: truthy(n["archived"]),
		CreatedOn: asInt64(n["createdOn"]),
	}
	objClass, obj := str(n["objectClass"]), str(n["objectId"])
	switch {
	case isChannel(objClass):
		v.Channel = obj
	case objClass == clDocument:
		v.Doc = obj
	}
	if str(n["attachedToClass"]) == clDocUpdateMessage {
		v.Reason = reasonAssigned
		return v
	}
	msg, _, err := b.message(s, str(n["attachedTo"]))
	if err != nil {
		return v
	}
	m := messageOf(msg, x)
	v.Message = &m
	v.Channel, v.Doc, v.Thread = m.Channel, m.Doc, m.Thread
	switch {
	case slices.Contains(m.Mentions, s.account):
		v.Reason = reasonMention
	case m.Thread != "":
		v.Reason = reasonReply
	case objClass == clDirectMessage:
		v.Reason = reasonDirect
	case m.Doc != "":
		v.Reason = reasonComment
	}
	return v
}
