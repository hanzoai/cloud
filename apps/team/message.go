package team

// The messages in a room.
//
// A room surface that lists rooms and cannot read one is a directory, not a
// conversation: hanzo.ai could name every channel the org has and had nothing to
// draw when a reader opened one, because the whole message surface lived behind
// the transactor's own websocket protocol. These two ops are the same second
// DOOR the room listing is — the SAME Chunter documents, read and written
// through the SAME applyTx path the Team client uses, so a message posted here
// arrives in an open client live and one typed there is read here with no sync.
//
// THE WRITE IS ATTRIBUTED TO A PERSON, which is the whole reason it does not
// reuse `principal.Acting` like the reads beside it. A message carries an
// author, and the only honest author is the team account the caller's verified
// credential resolves to — `sessionOf`, the same resolution the files and
// billing planes use. A caller with an org but no team account has nothing to
// sign a message with and is refused rather than having one invented for them.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// How many messages one read answers, newest last. A room that has run for a
// year is not a page, and a reader opening one wants the end of it — so the
// window is the TAIL, and a caller who needs more than this needs paging, which
// is a second parameter and not a bigger number.
const messageMax = 200

// The longest message this door accepts, in bytes of plain text. It is the
// bound the Team client's own composer keeps, and it is here so a caller
// driving the API by hand cannot write a document the client then cannot render.
const messageBytes = 16 << 10

// teamMessages is one room's tail, oldest first — the order a conversation is
// read in, which is the opposite of the order it is paged in.
type teamMessages struct {
	// Messages are the room's, oldest first, at most `messageMax` of them.
	Messages []teamMessage `json:"messages"`
}

// teamMessage is one thing somebody said.
type teamMessage struct {
	// ID is the message document's own id.
	ID string `json:"id"`
	// Room is the room it was said in — the same id the room listing answers
	// with, so a caller holding a message can name its room without a second read.
	Room string `json:"room"`
	// Author is the team account uuid that wrote it. It is an ACCOUNT and not a
	// display name: what to call somebody is the roster's answer, and copying it
	// onto every message is how the two come to disagree. An agent's messages
	// carry the account derived from its id, so the same field answers for both.
	Author string `json:"author"`
	// Text is the message as PLAIN TEXT. The document stores markup; this is the
	// same `plainText` reduction the agent responder reads a prompt with, so a
	// caller never has to parse the client's markup to know what was said.
	Text string `json:"text"`
	// CreatedOn is unix MILLIseconds, which is what the platform stamps.
	CreatedOn int64 `json:"createdOn"`
}

// teamMessageRead names one room to read.
type teamMessageRead struct {
	// ID is the room, from the path. The URL is the authority.
	ID string `json:"id"`
	// Space names the space holding the room, and is required for the reason the
	// bind op requires it: a room id is unique within a space and not across the
	// org, so searching every space for a match would make the answer depend on
	// iteration order.
	Space string `json:"space"`
}

// teamMessageWrite is one thing to say.
type teamMessageWrite struct {
	// ID is the room to say it in, from the path.
	ID string `json:"id"`
	// Space names the space holding the room. Body-only: a query string may not
	// redirect a write.
	Space string `json:"space" url:"-"`
	// Text is what to say, as plain text. It is wrapped in the client's markup on
	// the way in, so a caller writes words rather than HTML.
	Text string `json:"text" url:"-"`
}

// registerMessages declares the message surface as TYPED ops.
//
// The group is built here, from the one prefix constant, because a typed op's
// path is its group's prefix composed with its leaf and cmd/zipdoc resolves that
// prefix from the assignment in the SAME file. Same shape room.go and bots.go
// use; nothing is installed on it, so a second group at one address is an
// ADDRESS and not a second middleware chain.
func (b *roomBridge) registerMessages(app cloud.Router) {
	g := app.Group(teamPrefix)
	zip.Get(g, "/rooms/:id/messages", b.listMessages)
	zip.Post(g, "/rooms/:id/messages", b.sendMessage, zip.WithStatus(http.StatusCreated))
}

// ListMessages returns the tail of one room's conversation, oldest first.
//
// It reads the SAME Chunter documents the transactor serves, so a message typed
// in the Team client is here with no sync. A room the caller's org does not own
// answers 404 rather than 403, so a probe learns nothing about what exists.
//
// Example: {"messages": [{"id": "7a1c", "room": "6543", "author": "9f2…", "text": "shipped", "createdOn": 1756598400000}]}
func (b *roomBridge) listMessages(ctx context.Context, in *teamMessageRead) (*teamMessages, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	room, ws, err := b.roomOwned(ctx, org, in.ID, in.Space)
	if err != nil {
		return nil, err
	}
	_ = room
	docs, err := b.trans.store.byClasses(org, ws, []string{clChatMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: messages of space %s: %v", ws, err)
	}
	out := make([]teamMessage, 0, len(docs))
	for _, doc := range docs {
		if str(doc["attachedTo"]) != in.ID {
			continue
		}
		out = append(out, messageOf(in.ID, doc))
	}
	// Oldest first, by the platform's own stamp, then by id — two messages can
	// share a millisecond and a list that reshuffles between two identical reads
	// is one no client can diff.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedOn != out[j].CreatedOn {
			return out[i].CreatedOn < out[j].CreatedOn
		}
		return out[i].ID < out[j].ID
	})
	// The TAIL, so a long room answers with its end rather than its beginning.
	if len(out) > messageMax {
		out = out[len(out)-messageMax:]
	}
	return &teamMessages{Messages: out}, nil
}

// SendMessage says one thing in a room, as the caller.
//
// The write goes through the SAME applyTx path the Team client's own messages
// take and is broadcast to every connected client of the space, so a message
// sent here appears live in an open room rather than on the next reload. It
// answers the message as the store now HOLDS it.
//
// Example: {"space": "0e3c…", "text": "deploying now"}
func (b *roomBridge) sendMessage(ctx context.Context, in *teamMessageWrite) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	// THE AUTHOR IS THE CREDENTIAL'S, never the body's. `sessionOf` resolves the
	// team account from the verified session or IAM credential, which is the one
	// thing a caller cannot choose for themselves.
	account, org, err := sessionOf(ctx, b.ident)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, zip.ErrBadRequest("a message needs something to say")
	}
	if len(text) > messageBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge,
			"team: a message is at most %d bytes of text", messageBytes)
	}
	room, ws, err := b.roomOwned(ctx, org, in.ID, in.Space)
	if err != nil {
		return nil, err
	}
	id := newMsgID()
	tx := attachedCreateTx(id, clChatMessage, in.ID, in.ID, str(room["_class"]),
		"messages", "hanzo:"+account, map[string]any{"message": htmlMarkup(text)})
	raw, err := json.Marshal(tx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "team: message: %v", err)
	}
	sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, space: ws, account: account}
	_, applied := sess.applyTx(raw)
	if len(applied) == 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "team: the message was not applied")
	}
	b.trans.hub.broadcast(ws, applied)
	// Re-read rather than projecting what we just sent: the answer is what the
	// store now holds, which is the only thing a caller can rely on.
	doc, err := b.trans.store.get(org, ws, id)
	if err != nil || doc == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: message after send: %v", err)
	}
	v := messageOf(in.ID, doc)
	return &v, nil
}

// roomOwned resolves (room document, space) for a room the caller's ORG owns,
// answering 404 for anything else.
//
// One function, because the read and the write must agree about what a caller
// may reach: two copies of an ownership rule is two rules, and the one that
// drifts is the one nobody is looking at.
func (b *roomBridge) roomOwned(ctx context.Context, org, id, space string) (map[string]any, string, error) {
	id = strings.TrimSpace(id)
	ws := strings.TrimSpace(space)
	if id == "" || ws == "" {
		return nil, "", zip.ErrBadRequest("room id and space are required")
	}
	owned, err := b.accounts.SpacesForOrg(ctx, org)
	if err != nil {
		return nil, "", zip.Errorf(http.StatusInternalServerError, "team: spaces: %v", err)
	}
	if !ownsSpace(owned, ws) {
		// 404, not 403: a caller who may not touch this space must not learn
		// whether it exists.
		return nil, "", zip.ErrNotFound("room not found")
	}
	doc, err := b.trans.store.get(org, ws, id)
	if err != nil {
		return nil, "", zip.Errorf(http.StatusBadGateway, "team: room: %v", err)
	}
	if doc == nil || !isRoom(str(doc["_class"])) {
		return nil, "", zip.ErrNotFound("room not found")
	}
	return doc, ws, nil
}

// messageOf projects one stored ChatMessage document.
func messageOf(room string, doc map[string]any) teamMessage {
	m := teamMessage{
		ID:        str(doc["_id"]),
		Room:      room,
		Author:    stripHanzo(str(firstNonNil(doc["createdBy"], doc["modifiedBy"]))),
		CreatedOn: asInt64(firstNonNil(doc["createdOn"], doc["modifiedOn"])),
	}
	m.Text = plainText(str(doc["message"]))
	if m.CreatedOn == 0 {
		m.CreatedOn = time.Now().UnixMilli()
	}
	return m
}
