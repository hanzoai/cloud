package team

// The messages in a channel.
//
// A channel surface that lists channels and cannot read one is a directory, not a
// conversation: hanzo.ai could name every channel the org has and had nothing to
// draw when a reader opened one, because the whole message surface lived behind
// the transactor's own websocket protocol. These ops are the same second
// SURFACE the channel listing is — the SAME Chunter documents, read and written
// through the SAME applyTx path the Team client uses, so a message posted here
// arrives in an open client live and one typed there is read here with no sync.
//
// THE WRITE IS ATTRIBUTED TO A PERSON. A message carries an author, and the only
// honest author is the team account the caller's verified credential resolves to
// — `enter`, the same resolution every team surface uses. A caller with an org
// but no team account has nothing to sign a message with and is refused rather
// than having one invented for them.
//
// The model's own vocabulary, which every op here writes in:
//
//   - a message is a chunter ChatMessage attached to its channel (collection
//     "messages"), or to a document (collection "comments");
//   - a reply is a ThreadMessage attached to the message it answers (collection
//     "replies"), naming the channel it lives in as its objectId;
//   - a reaction is an activity Reaction attached to the message (collection
//     "reactions"), carrying the emoji and who reacted;
//   - a file is an attachment Attachment attached to the message (collection
//     "attachments"), naming a blob uploaded through /v1/team/files/{space}.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// How many messages one read answers, newest last. A channel that has run for a
// year is not a page, and a reader opening one wants the end of it — so the
// window is the TAIL, and a caller who needs more than this needs paging, which
// is a second parameter and not a bigger number.
const messageMax = 200

// The longest message this endpoint accepts, in bytes of plain text. It is the
// bound the Team client's own composer keeps, and it is here so a caller
// driving the API by hand cannot write a document the client then cannot render.
const messageBytes = 16 << 10

// fileMax bounds how many files one message carries — a sanity bound on a
// write, the same kind bindMax is.
const fileMax = 20

// emojiBytes bounds one reaction. An emoji is a few code points; anything longer
// is a sentence being stored as a reaction.
const emojiBytes = 64

// teamMessages is one conversation's tail, oldest first — the order a
// conversation is read in, which is the opposite of the order it is paged in.
type teamMessages struct {
	// Messages are the conversation's, oldest first, at most `messageMax` of
	// them.
	Messages []teamMessage `json:"messages"`
}

// teamMessage is one thing somebody said.
type teamMessage struct {
	// ID is the message document's own id.
	ID string `json:"id"`
	// Channel is the channel it was said in — the same id the channel listing answers
	// with, so a caller holding a message can name its channel without a second read.
	// A reply names the channel its thread is in; a comment on a document has none.
	Channel string `json:"channel"`
	// Thread is the message this one replies to. Empty for a message that starts
	// a conversation rather than answering one.
	Thread string `json:"thread,omitempty"`
	// Doc is the document this message is a comment on. Empty for a message in a
	// channel.
	Doc string `json:"doc,omitempty"`
	// Author is the team account uuid that wrote it. It is an ACCOUNT and not a
	// display name: what to call somebody is the roster's answer
	// (GET /v1/team/members), and copying it onto every message is how the two
	// come to disagree. An agent's messages carry the account derived from its
	// id, so the same field answers for both.
	Author string `json:"author"`
	// Text is the message as PLAIN TEXT. The document stores markup; this is the
	// same `plainText` reduction the agent responder reads a prompt with, so a
	// caller never has to parse the client's markup to know what was said. A
	// mention reads as "@Name" here and is listed by id in Mentions.
	Text string `json:"text"`
	// Mentions are the account uuids the message @-mentions — exactly the
	// people notify.go told they were mentioned.
	Mentions []string `json:"mentions"`
	// CreatedOn is unix MILLIseconds, which is what the platform stamps.
	CreatedOn int64 `json:"createdOn"`
	// EditedOn is when the author last edited the message, unix milliseconds.
	// Absent for a message never edited.
	EditedOn int64 `json:"editedOn,omitempty"`
	// Replies is how many replies the message's thread holds.
	Replies int64 `json:"replies"`
	// LastReply is when the thread was last answered, unix milliseconds. Absent
	// for a message with no replies.
	LastReply int64 `json:"lastReply,omitempty"`
	// Reactions are the emoji people reacted with, one entry per emoji, in the
	// order each emoji was first used.
	Reactions []teamReaction `json:"reactions"`
	// Files are the files attached to the message.
	Files []teamFile `json:"files"`
}

// teamReaction is one emoji on a message and who reacted with it.
type teamReaction struct {
	// Emoji is the reaction itself, as the person picked it.
	Emoji string `json:"emoji"`
	// Count is how many people reacted with it.
	Count int `json:"count"`
	// Accounts are the account uuids that reacted with it, earliest first.
	Accounts []string `json:"accounts"`
}

// teamFile is one file attached to a message.
type teamFile struct {
	// ID is the attachment document's own id.
	ID string `json:"id"`
	// File is the blob id, readable at GET /v1/team/files/{space}/{name}?file={file}.
	File string `json:"file"`
	// Name is the file's name as the person uploaded it.
	Name string `json:"name"`
	// Type is the media type the uploader declared. The download route serves
	// what the BYTES are, not this; it is a hint for drawing an icon.
	Type string `json:"type,omitempty"`
	// Size is the file's length in bytes, as the uploader stated it.
	Size int64 `json:"size,omitempty"`
}

// teamFileIn attaches one already-uploaded blob to a message.
type teamFileIn struct {
	// File is the blob id — the uuid the file was uploaded under with
	// POST /v1/team/files/{space}. A blob of another space cannot be named: the
	// download is keyed by this message's space, so a foreign id resolves to
	// nothing.
	File string `json:"file"`
	// Name is the file's name as a person should see it.
	Name string `json:"name"`
	// Type is the file's media type, e.g. "image/png".
	Type string `json:"type,omitempty"`
	// Size is the file's length in bytes.
	Size int64 `json:"size,omitempty"`
}

// teamMessageRead names one channel to read.
type teamMessageRead struct {
	// ID is the channel, from the path. The URL is the authority.
	ID string `json:"id"`
	// Space names the space holding the channel, and is required for the reason the
	// bind op requires it: a channel id is unique within a space and not across the
	// org, so searching every space for a match would make the answer depend on
	// iteration order.
	Space string `json:"space"`
}

// teamMessageWrite is one thing to say.
type teamMessageWrite struct {
	// ID is the channel to say it in, from the path.
	ID string `json:"id"`
	// Space names the space holding the channel. Body-only: a query string may not
	// redirect a write.
	Space string `json:"space" url:"-"`
	// Text is what to say, as plain text. It is wrapped in the client's markup on
	// the way in, so a caller writes words rather than HTML. `<@account-uuid>`
	// mentions a member of the space: it is stored as the platform's mention, so
	// they are notified, and an agent mentioned this way answers.
	Text string `json:"text" url:"-"`
	// Files attaches blobs already uploaded to the space. A message may be
	// files alone, with no text.
	Files []teamFileIn `json:"files,omitempty" url:"-"`
}

// teamMessageAt names one message.
type teamMessageAt struct {
	// ID is the message, from the path.
	ID string `json:"id"`
	// Space names the space holding it. A message id is unique within a space,
	// not across the org.
	Space string `json:"space"`
}

// teamMessageEdit rewrites what a message says.
type teamMessageEdit struct {
	// ID is the message, from the path.
	ID string `json:"id"`
	// Space names the space holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Text is the message's new text, with the same `<@account-uuid>` mentions
	// a new message takes. Somebody newly mentioned is notified; somebody already
	// mentioned is not notified twice.
	Text string `json:"text" url:"-"`
}

// teamReplyWrite answers a message in its thread.
type teamReplyWrite struct {
	// ID is the message being answered, from the path.
	ID string `json:"id"`
	// Space names the space holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Text is the reply, with the same `<@account-uuid>` mentions a message
	// takes.
	Text string `json:"text" url:"-"`
	// Files attaches blobs already uploaded to the space.
	Files []teamFileIn `json:"files,omitempty" url:"-"`
}

// teamReactionWrite adds the caller's reaction to a message.
type teamReactionWrite struct {
	// ID is the message, from the path.
	ID string `json:"id"`
	// Emoji is the reaction, from the path (percent-encoded on the wire).
	Emoji string `json:"emoji"`
	// Space names the space holding the message. Body-only.
	Space string `json:"space" url:"-"`
}

// teamReactionAt names the caller's reaction to take back.
type teamReactionAt struct {
	// ID is the message, from the path.
	ID string `json:"id"`
	// Emoji is the reaction, from the path (percent-encoded on the wire).
	Emoji string `json:"emoji"`
	// Space names the space holding the message.
	Space string `json:"space"`
}

// registerMessages declares the message surface as TYPED ops.
//
// The group is built here, from the one prefix constant, because a typed op's
// path is its group's prefix composed with its leaf and cmd/zipdoc resolves that
// prefix from the assignment in the SAME file. Same shape channel.go and bots.go
// use; nothing is installed on it, so a second group at one address is an
// ADDRESS and not a second middleware chain.
func (b *channelBridge) registerMessages(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/channels/:id/messages", b.listMessages)
	g.Post("/channels/:id/messages", b.sendMessage, zip.WithStatus(http.StatusCreated))
	g.Patch("/messages/:id", b.editMessage)
	g.Delete("/messages/:id", b.deleteMessage, zip.WithStatus(http.StatusNoContent))
	g.Get("/messages/:id/replies", b.listReplies)
	g.Post("/messages/:id/replies", b.sendReply, zip.WithStatus(http.StatusCreated))
	g.Put("/messages/:id/reactions/:emoji", b.react)
	g.Delete("/messages/:id/reactions/:emoji", b.unreact)
}

// ListMessages returns the tail of one channel's conversation, oldest first, each
// message with its reactions, files and thread count.
//
// It reads the SAME Chunter documents the transactor serves, so a message typed
// in the Team client is here with no sync. A team member reads any channel they
// may see; an application of the org reads public channels only; anybody else
// without a team session is 401. A channel the caller may not read answers 404
// rather than 403, so a probe learns nothing about what exists.
//
// Example: {"messages": [{"id": "7a1c", "channel": "6543", "author": "9f2…", "text": "shipped", "mentions": [], "createdOn": 1756598400000, "replies": 0, "reactions": [], "files": []}]}
func (b *channelBridge) listMessages(ctx context.Context, in *teamMessageRead) (*teamMessages, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, ws, err := b.readable(ctx, in.ID, in.Space)
	if err != nil {
		return nil, err
	}
	docs, err := b.trans.store.byClasses(org, ws, []string{clChatMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: messages of space %s: %v", ws, err)
	}
	var mine []map[string]any
	for _, doc := range docs {
		if str(doc["attachedTo"]) == in.ID {
			mine = append(mine, doc)
		}
	}
	return &teamMessages{Messages: b.tail(org, ws, mine)}, nil
}

// readable resolves the (org, space) a channel read happens in, for either lane: a
// team member who may see the channel, or an application reading a public
// channel of its own org.
func (b *channelBridge) readable(ctx context.Context, id, space string) (string, string, error) {
	if _, ok := teamCaller(ctx, b.ident); ok {
		if strings.TrimSpace(space) == "" {
			return "", "", zip.ErrBadRequest("channel id and space are required")
		}
		s, err := enter(ctx, b.ident, space)
		if err != nil {
			return "", "", zip.ErrNotFound("channel not found")
		}
		if _, err := b.channel(s, id); err != nil {
			return "", "", err
		}
		return s.org, s.ws, nil
	}
	org, err := orgLane(ctx)
	if err != nil {
		return "", "", err
	}
	channel, ws, err := b.channelOwned(ctx, org, id, space)
	if err != nil {
		return "", "", err
	}
	if !publicChannel(channel) {
		return "", "", zip.ErrNotFound("channel not found")
	}
	return org, ws, nil
}

// SendMessage says one thing in a channel, as the caller.
//
// The write goes through the SAME applyTx path the Team client's own messages
// take and is broadcast to every connected client of the space, so a message
// sent here appears live in an open channel rather than on the next reload. It
// answers the message as the store now HOLDS it.
//
// `<@account-uuid>` in the text is stored as the platform's mention, so the
// person is notified in their inbox. Mentioning one of the org's agents — or
// writing in a direct message with one — wakes it: it runs as itself, on behalf
// of the caller, and posts its answer into the same channel. The caller must be
// able to see the channel, and an archived channel refuses new messages (409).
//
// Example: {"space": "0e3c…", "text": "deploying now, <@9f2c…> can you watch the dashboards?"}
func (b *channelBridge) sendMessage(ctx context.Context, in *teamMessageWrite) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	channel, err := b.channel(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := s.mayWrite(channel); err != nil {
		return nil, err
	}
	text, err := said(in.Text, len(in.Files))
	if err != nil {
		return nil, err
	}
	if err := checkFiles(in.Files); err != nil {
		return nil, err
	}
	markup, err := b.markup(ctx, s, text)
	if err != nil {
		return nil, err
	}
	id := newMsgID()
	channelID, channelClass := str(channel["_id"]), str(channel["_class"])
	txes := []map[string]any{attachedCreateTx(id, clChatMessage, channelID, channelID, channelClass,
		"messages", s.social(), map[string]any{"message": markup, "attachments": 0})}
	txes = append(txes, fileTxes(in.Files, id, clChatMessage, channelID, s.social())...)
	return b.post(s, id, txes)
}

// post writes a new message's txes, wakes any agent it addresses, and answers
// the message as the store now holds it — the tail every writer of a new
// message shares.
func (b *channelBridge) post(s seat, id string, txes []map[string]any) (*teamMessage, error) {
	applied, err := apply(b.session(s), b.trans, s.ws, txes...)
	if err != nil {
		return nil, err
	}
	if len(applied) == 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "team: the message was not applied")
	}
	// A person's message is the one kind of write that may address an agent, so
	// it is the one that asks. The socket path asks the same question from
	// session.tx; a projection never does.
	b.trans.maybeAgentReply(s.org, s.ws, applied)
	return b.messageNow(s.org, s.ws, id)
}

// EditMessage rewrites what a message says. Only its author may edit it.
//
// The edit is an ordinary update of the message document through the Team
// client's own write path, stamped with editedOn, so an open client shows the
// new text and the "edited" mark live. Somebody the new text mentions for the
// first time is notified.
//
// Example: {"space": "0e3c…", "text": "deploying at 3pm instead"}
func (b *channelBridge) editMessage(ctx context.Context, in *teamMessageEdit) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	msg, where, err := b.message(s, in.ID)
	if err != nil {
		return nil, err
	}
	if authorOf(msg) != s.account {
		return nil, zip.ErrForbidden("team: only the author can edit a message")
	}
	if err := b.writable(s, where); err != nil {
		return nil, err
	}
	text, err := said(in.Text, 0)
	if err != nil {
		return nil, err
	}
	markup, err := b.markup(ctx, s, text)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if _, err := apply(b.session(s), b.trans, s.ws, updateTx(in.ID, str(msg["_class"]), str(msg["space"]),
		s.social(), map[string]any{"message": markup, "editedOn": now})); err != nil {
		return nil, err
	}
	return b.messageNow(s.org, s.ws, in.ID)
}

// DeleteMessage removes a message, with its replies, reactions, files and the
// inbox notifications that point at it. Its author may delete it, and so may an
// owner or admin of the space.
//
// Each removal is a platform remove through the Team client's write path, so the
// message disappears from every open client live. Answers 204.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) deleteMessage(ctx context.Context, in *teamMessageAt) (*cloud.Unit, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	msg, _, err := b.message(s, in.ID)
	if err != nil {
		return nil, err
	}
	if authorOf(msg) != s.account && !s.admin() {
		return nil, zip.ErrForbidden("team: only the author or a space admin can delete a message")
	}
	doomed, err := b.subtree(s, msg)
	if err != nil {
		return nil, err
	}
	txes := make([]map[string]any, 0, len(doomed))
	for _, doc := range doomed {
		txes = append(txes, removeTx(doc, s.social()))
	}
	if _, err := apply(b.session(s), b.trans, s.ws, txes...); err != nil {
		return nil, err
	}
	return nil, nil
}

// subtree is a message and everything that exists only because of it — its
// replies, every reaction and file on it or on a reply, and the notifications
// pointing at any of them — children FIRST, so no reader ever holds a reaction
// whose message is already gone.
func (b *channelBridge) subtree(s seat, msg map[string]any) ([]map[string]any, error) {
	docs, err := b.trans.store.byClasses(s.org, s.ws,
		[]string{clThreadMessage, clReaction, clAttachment, clActivityInboxNotif})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: message children: %v", err)
	}
	under := map[string][]map[string]any{}
	for _, d := range docs {
		under[str(d["attachedTo"])] = append(under[str(d["attachedTo"])], d)
	}
	var out []map[string]any
	// visited, because attachedTo is stored data: a cycle in it (a document
	// attached to its own descendant) must end the walk, not the process.
	visited := map[string]bool{}
	var walk func(doc map[string]any)
	walk = func(doc map[string]any) {
		id := str(doc["_id"])
		if visited[id] {
			return
		}
		visited[id] = true
		for _, child := range under[id] {
			walk(child)
		}
		out = append(out, doc)
	}
	walk(msg)
	return out, nil
}

// ListReplies returns a message's thread, oldest first, each reply with its
// reactions and files.
//
// Example: {"messages": [{"id": "8b2d", "channel": "6543", "thread": "7a1c", "author": "9f2…", "text": "on it", "mentions": [], "createdOn": 1756598460000, "replies": 0, "reactions": [], "files": []}]}
func (b *channelBridge) listReplies(ctx context.Context, in *teamMessageAt) (*teamMessages, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	if _, _, err := b.message(s, in.ID); err != nil {
		return nil, err
	}
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clThreadMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: replies: %v", err)
	}
	var mine []map[string]any
	for _, doc := range docs {
		if str(doc["attachedTo"]) == in.ID {
			mine = append(mine, doc)
		}
	}
	return &teamMessages{Messages: b.tail(s.org, s.ws, mine)}, nil
}

// SendReply answers a message in its thread, as the caller.
//
// A reply is a thread message attached to the one it answers, written through
// the Team client's own path: the parent's reply count and last-reply time move
// with it, the people already in the thread and anyone mentioned are notified,
// and an agent mentioned in a reply answers in the same thread. A reply cannot
// itself be replied to — threads are one level deep, as in the Team client.
//
// Example: {"space": "0e3c…", "text": "on it"}
func (b *channelBridge) sendReply(ctx context.Context, in *teamReplyWrite) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	parent, where, err := b.message(s, in.ID)
	if err != nil {
		return nil, err
	}
	if str(parent["_class"]) == clThreadMessage {
		return nil, zip.ErrBadRequest("team: a reply cannot be replied to; answer the thread's first message")
	}
	if err := b.writable(s, where); err != nil {
		return nil, err
	}
	text, err := said(in.Text, len(in.Files))
	if err != nil {
		return nil, err
	}
	if err := checkFiles(in.Files); err != nil {
		return nil, err
	}
	markup, err := b.markup(ctx, s, text)
	if err != nil {
		return nil, err
	}
	id := newMsgID()
	home := str(parent["space"])
	txes := []map[string]any{attachedCreateTx(id, clThreadMessage, home, in.ID, str(parent["_class"]),
		"replies", s.social(), map[string]any{
			"message": markup, "attachments": 0,
			"objectId": str(where["_id"]), "objectClass": str(where["_class"]),
		})}
	txes = append(txes, fileTxes(in.Files, id, clThreadMessage, home, s.social())...)
	return b.post(s, id, txes)
}

// AddReaction adds the caller's reaction to a message and answers the message with
// its reactions as they now stand. Reacting twice with one emoji is one
// reaction: the second call changes nothing.
//
// The emoji is the last path segment, percent-encoded — PUT
// /v1/team/messages/7a1c/reactions/%F0%9F%91%8D for 👍.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) react(ctx context.Context, in *teamReactionWrite) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	emoji, err := emojiOf(in.Emoji)
	if err != nil {
		return nil, err
	}
	msg, where, err := b.message(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writable(s, where); err != nil {
		return nil, err
	}
	if len(b.reactionsBy(s, in.ID, emoji)) == 0 {
		// One id per (message, emoji, person): two reacts racing past the check
		// above write ONE document, and the second create is an upsert that does
		// not count again (txCreate), so the reaction is a set under concurrency
		// too, not only in sequence.
		tx := attachedCreateTx(reactionID(in.ID, emoji, s.account), clReaction, str(msg["space"]), in.ID, str(msg["_class"]),
			"reactions", s.social(), map[string]any{"emoji": emoji, "createBy": s.social()})
		if _, err := apply(b.session(s), b.trans, s.ws, tx); err != nil {
			return nil, err
		}
	}
	return b.messageNow(s.org, s.ws, in.ID)
}

// reactionID is the one id a person's reaction with one emoji on one message
// has.
func reactionID(msg, emoji, account string) string {
	sum := sha256.Sum256([]byte(msg + "\x00" + emoji + "\x00" + account))
	return "rx-" + hex.EncodeToString(sum[:16])
}

// RemoveReaction takes back the caller's reaction to a message and answers the message
// with its reactions as they now stand. Taking back a reaction the caller never
// made changes nothing and is not an error.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) unreact(ctx context.Context, in *teamReactionAt) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	emoji, err := emojiOf(in.Emoji)
	if err != nil {
		return nil, err
	}
	_, where, err := b.message(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writable(s, where); err != nil {
		return nil, err
	}
	for _, r := range b.reactionsBy(s, in.ID, emoji) {
		// One at a time, and a remove that finds the reaction already gone is the
		// outcome asked for: an unreact racing another unreact is not a conflict.
		if _, err := apply(b.session(s), b.trans, s.ws, removeTx(r, s.social())); err != nil {
			if still, _ := b.trans.store.get(s.org, s.ws, str(r["_id"])); still != nil {
				return nil, err
			}
		}
	}
	return b.messageNow(s.org, s.ws, in.ID)
}

// reactionsBy is the caller's own reactions to one message with one emoji.
func (b *channelBridge) reactionsBy(s seat, msgID, emoji string) []map[string]any {
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clReaction})
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, d := range docs {
		if str(d["attachedTo"]) == msgID && str(d["emoji"]) == emoji &&
			accountFromRef(str(firstNonNil(d["createBy"], d["createdBy"]))) == s.account {
			out = append(out, d)
		}
	}
	return out
}

// message resolves a message the caller may read — a channel message, a reply or
// a comment on a document — and the channel or document it lives in. Anything else
// is 404.
func (b *channelBridge) message(s seat, id string) (map[string]any, map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil, zip.ErrBadRequest("message id is required")
	}
	msg, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusBadGateway, "team: message: %v", err)
	}
	if msg == nil || !b.trans.hier.isDerived(str(msg["_class"]), clChatMessage) {
		return nil, nil, zip.ErrNotFound("message not found")
	}
	whereID := str(msg["attachedTo"])
	if str(msg["_class"]) == clThreadMessage {
		whereID = str(msg["objectId"])
	}
	where, err := b.trans.store.get(s.org, s.ws, whereID)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusBadGateway, "team: message: %v", err)
	}
	if where == nil || !b.visible(s, where) {
		return nil, nil, zip.ErrNotFound("message not found")
	}
	return msg, where, nil
}

// messageNow re-reads one message and projects it with its reactions and files.
// The answer is what the store now HOLDS, which is the only thing a caller can
// rely on.
func (b *channelBridge) messageNow(org, ws, id string) (*teamMessage, error) {
	doc, err := b.trans.store.get(org, ws, id)
	if err != nil || doc == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: message after write: %v", err)
	}
	v := messageOf(doc, b.extras(org, ws))
	return &v, nil
}

// tail orders messages oldest first and answers the last messageMax of them.
func (b *channelBridge) tail(org, ws string, docs []map[string]any) []teamMessage {
	x := b.extras(org, ws)
	out := make([]teamMessage, 0, len(docs))
	for _, doc := range docs {
		out = append(out, messageOf(doc, x))
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
	if len(out) > messageMax {
		out = out[len(out)-messageMax:]
	}
	return out
}

// extras is what hangs off messages in one space — reactions and files — read
// once per answer and grouped by the message they belong to.
type extras struct {
	reactions map[string][]map[string]any
	files     map[string][]map[string]any
}

func (b *channelBridge) extras(org, ws string) extras {
	x := extras{reactions: map[string][]map[string]any{}, files: map[string][]map[string]any{}}
	docs, err := b.trans.store.byClasses(org, ws, []string{clReaction, clAttachment})
	if err != nil {
		return x
	}
	for _, d := range docs {
		on := str(d["attachedTo"])
		switch str(d["_class"]) {
		case clReaction:
			x.reactions[on] = append(x.reactions[on], d)
		case clAttachment:
			x.files[on] = append(x.files[on], d)
		}
	}
	return x
}

// messageOf projects one stored message document — a channel message, a reply or
// a comment — with what hangs off it.
func messageOf(doc map[string]any, x extras) teamMessage {
	markup := str(doc["message"])
	m := teamMessage{
		ID:        str(doc["_id"]),
		Author:    authorOf(doc),
		Text:      plainText(markup),
		Mentions:  mentionsIn(markup),
		CreatedOn: asInt64(firstNonNil(doc["createdOn"], doc["modifiedOn"])),
		EditedOn:  asInt64(doc["editedOn"]),
		Replies:   asInt64(doc["replies"]),
		LastReply: asInt64(doc["lastReply"]),
		Reactions: reactionsOf(x.reactions[str(doc["_id"])]),
		Files:     filesOf(x.files[str(doc["_id"])]),
	}
	if m.CreatedOn == 0 {
		m.CreatedOn = time.Now().UnixMilli()
	}
	where, whereClass := str(doc["attachedTo"]), str(doc["attachedToClass"])
	if str(doc["_class"]) == clThreadMessage {
		m.Thread = where
		where, whereClass = str(doc["objectId"]), str(doc["objectClass"])
	}
	switch {
	case isChannel(whereClass):
		m.Channel = where
	case whereClass == clDocument:
		m.Doc = where
	}
	return m
}

// authorOf is the account a message (or any document) was written by.
func authorOf(doc map[string]any) string {
	return stripHanzo(str(firstNonNil(doc["createdBy"], doc["modifiedBy"])))
}

// reactionsOf folds one message's Reaction documents into one entry per emoji,
// in the order each emoji was first used.
func reactionsOf(docs []map[string]any) []teamReaction {
	sort.SliceStable(docs, func(i, j int) bool {
		return asInt64(docs[i]["createdOn"]) < asInt64(docs[j]["createdOn"])
	})
	out := []teamReaction{}
	at := map[string]int{}
	for _, d := range docs {
		emoji := str(d["emoji"])
		if emoji == "" {
			continue
		}
		who := accountFromRef(str(firstNonNil(d["createBy"], d["createdBy"])))
		i, ok := at[emoji]
		if !ok {
			i = len(out)
			at[emoji] = i
			out = append(out, teamReaction{Emoji: emoji, Accounts: []string{}})
		}
		out[i].Count++
		if who != "" {
			out[i].Accounts = append(out[i].Accounts, who)
		}
	}
	return out
}

// filesOf projects one message's Attachment documents.
func filesOf(docs []map[string]any) []teamFile {
	sort.SliceStable(docs, func(i, j int) bool {
		return asInt64(docs[i]["createdOn"]) < asInt64(docs[j]["createdOn"])
	})
	out := make([]teamFile, 0, len(docs))
	for _, d := range docs {
		out = append(out, teamFile{
			ID: str(d["_id"]), File: str(d["file"]), Name: str(d["name"]),
			Type: str(d["type"]), Size: asInt64(d["size"]),
		})
	}
	return out
}

// said validates what a person wrote: trimmed, bounded, and not empty unless the
// message carries files.
func said(text string, files int) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" && files == 0 {
		return "", zip.ErrBadRequest("a message needs something to say")
	}
	if len(text) > messageBytes {
		return "", zip.Errorf(http.StatusRequestEntityTooLarge,
			"team: a message is at most %d bytes of text", messageBytes)
	}
	return text, nil
}

// markup renders typed text as stored markup, resolving `<@id>` mentions against
// the space's roster. The roster is only read when the text names somebody.
func (b *channelBridge) markup(ctx context.Context, s seat, text string) (string, error) {
	if len(mentionTokens(text)) == 0 {
		return textMarkup(text, nil), nil
	}
	people, _, err := b.people(ctx, s)
	if err != nil {
		return "", err
	}
	return textMarkup(text, names(people)), nil
}

// checkFiles refuses an attachment list a reader would have to second-guess.
func checkFiles(files []teamFileIn) error {
	if len(files) > fileMax {
		return zip.ErrBadRequest("team: at most 20 files on one message")
	}
	for _, f := range files {
		if uuid.Validate(strings.TrimSpace(f.File)) != nil {
			return zip.ErrBadRequest("team: each file is the uuid it was uploaded under")
		}
		if n := strings.TrimSpace(f.Name); n == "" || len(n) > 255 {
			return zip.ErrBadRequest("team: each file needs a name of at most 255 bytes")
		}
		if f.Size < 0 || f.Size > maxBlobSize {
			return zip.ErrBadRequest("team: a file's size is between 0 and the upload ceiling")
		}
	}
	return nil
}

// fileTxes attaches uploaded blobs to a message.
func fileTxes(files []teamFileIn, msgID, msgClass, home, by string) []map[string]any {
	now := time.Now().UnixMilli()
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		out = append(out, attachedCreateTx(newMsgID(), clAttachment, home, msgID, msgClass,
			"attachments", by, map[string]any{
				"name": strings.TrimSpace(f.Name), "file": strings.TrimSpace(f.File),
				"type": strings.TrimSpace(f.Type), "size": f.Size, "lastModified": now,
			}))
	}
	return out
}

// emojiOf validates one reaction: present, short, printable.
func emojiOf(raw string) (string, error) {
	e := strings.TrimSpace(raw)
	if e == "" || len(e) > emojiBytes || !utf8.ValidString(e) {
		return "", zip.ErrBadRequest("team: a reaction is one emoji")
	}
	for _, r := range e {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", zip.ErrBadRequest("team: a reaction is one emoji")
		}
	}
	return e, nil
}

// channelOwned resolves (channel document, space) for a channel the caller's ORG owns,
// answering 404 for anything else — the org lane's gate, for a principal with no
// team account. A team member is gated by enter and channel instead.
func (b *channelBridge) channelOwned(ctx context.Context, org, id, space string) (map[string]any, string, error) {
	id = strings.TrimSpace(id)
	ws := strings.TrimSpace(space)
	if id == "" || ws == "" {
		return nil, "", zip.ErrBadRequest("channel id and space are required")
	}
	owned, err := b.accounts.SpacesForOrg(ctx, org)
	if err != nil {
		return nil, "", zip.Errorf(http.StatusInternalServerError, "team: spaces: %v", err)
	}
	if !ownsSpace(owned, ws) {
		// 404, not 403: a caller who may not touch this space must not learn
		// whether it exists.
		return nil, "", zip.ErrNotFound("channel not found")
	}
	doc, err := b.trans.store.get(org, ws, id)
	if err != nil {
		return nil, "", zip.Errorf(http.StatusBadGateway, "team: channel: %v", err)
	}
	if doc == nil || !isChannel(str(doc["_class"])) {
		return nil, "", zip.ErrNotFound("channel not found")
	}
	return doc, ws, nil
}
