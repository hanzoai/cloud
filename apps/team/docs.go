package team

// docs.go is the DOCUMENT tree: the pages of a space, nested by parent, grouped
// in teamspaces — the same document:class:Document rows the Team client's
// Documents app reads and writes.
//
// A document's BODY is not here. It is a collaborative Y.js document edited
// over the /v1/team/collaborator socket, and every document this surface answers
// carries the `collaborator` id a client opens that socket with. What this
// surface owns is everything around the body: the title, where the page sits in
// the tree, and the conversation beside it (comments, which are messages).
//
// Who may touch a document is its TEAMSPACE's question: a public teamspace is
// open to every non-guest member of the space, a private one to its members.
// That is the Team client's rule too — a document carries no ACL of its own.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// The document model's vocabulary.
const (
	clDocument  = "document:class:Document"
	clTeamspace = "document:class:Teamspace"
	// docNoParent is the parent a top-level document names — the platform's
	// document.ids.NoParent.
	docNoParent = "document:ids:NoParent"
	// teamspaceType is the space type every teamspace the Team client creates
	// by default carries.
	teamspaceType = "document:spaceType:DefaultTeamspaceType"
	// docBody is the collaborative attribute a document's body lives in.
	docBody = "content"
	// generalTeamspace is the id of the teamspace a space's first document
	// opens when it has none, fixed so two first documents written at once land
	// in one teamspace rather than two.
	generalTeamspace = "teamspace-general"
)

// titleBytes bounds a document title.
const titleBytes = 1024

// teamDocsRead filters the document tree.
type teamDocsRead struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty"`
	// Teamspace narrows the answer to one teamspace.
	Teamspace string `json:"teamspace,omitempty"`
	// Parent narrows the answer to one document's children.
	Parent string `json:"parent,omitempty"`
}

// teamDocs is a space's document tree, as the caller may see it.
type teamDocs struct {
	// Docs are the documents, grouped by teamspace, then parent, then the
	// order the Team client shows siblings in.
	Docs []teamDoc `json:"docs"`
	// Teamspaces are the teamspaces the caller may see, by name — the groups a
	// client draws the tree under.
	Teamspaces []teamTeamspace `json:"teamspaces"`
}

// teamTeamspace is one group of documents.
type teamTeamspace struct {
	// ID is the teamspace document's id, the value teamDoc.teamspace names.
	ID string `json:"id"`
	// Name is what a person sees in a sidebar.
	Name string `json:"name"`
	// Private reports that the teamspace is open to its members only.
	Private bool `json:"private"`
	// Archived reports that the teamspace is closed: its documents still read,
	// and none can be created, moved into it, renamed or commented on.
	Archived bool `json:"archived"`
}

// teamDoc is one document.
type teamDoc struct {
	// ID is the document's own id.
	ID string `json:"id"`
	// Space is the space uuid holding it.
	Space string `json:"space"`
	// Teamspace is the teamspace it belongs to.
	Teamspace string `json:"teamspace"`
	// Title is the document's title.
	Title string `json:"title"`
	// Parent is the document this one is nested under. Empty for a document at
	// the top of its teamspace.
	Parent string `json:"parent,omitempty"`
	// Rank orders siblings: compare as plain strings, ascending.
	Rank string `json:"rank"`
	// Author is the account uuid that created it.
	Author string `json:"author"`
	// CreatedOn is when it was created, unix milliseconds.
	CreatedOn int64 `json:"createdOn"`
	// ModifiedOn is when its title or place last changed, unix milliseconds.
	// Edits to the body happen on the collaborator socket and do not move it.
	ModifiedOn int64 `json:"modifiedOn"`
	// Comments is how many comments it has.
	Comments int64 `json:"comments"`
	// Collaborator is the documentId to open the body with on the
	// /v1/team/collaborator Y.js socket:
	// "<space>|document:class:Document|<id>|content".
	Collaborator string `json:"collaborator"`
}

// teamDocNew creates a document.
type teamDocNew struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty" url:"-"`
	// Teamspace is where the document goes. Optional: a document nested under a
	// parent goes in the parent's teamspace, and a space with exactly one
	// teamspace the caller can write needs no name — with none, the first
	// document opens a public "General" teamspace for the whole space.
	Teamspace string `json:"teamspace,omitempty" url:"-"`
	// Title is the document's title. Empty reads "Untitled", as in the Team
	// client.
	Title string `json:"title,omitempty" url:"-"`
	// Parent nests the new document under another one.
	Parent string `json:"parent,omitempty" url:"-"`
}

// teamDocAt names one document.
type teamDocAt struct {
	// ID is the document, from the path.
	ID string `json:"id"`
	// Space is the space uuid holding it.
	Space string `json:"space"`
}

// teamDocEdit renames or moves a document. Every field is optional and an
// absent one is left alone.
type teamDocEdit struct {
	// ID is the document, from the path.
	ID string `json:"id"`
	// Space is the space uuid holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Title renames the document. Empty reads "Untitled".
	Title *string `json:"title,omitempty" url:"-"`
	// Parent moves the document under another one; "" moves it to the top of
	// its teamspace. It goes after its new siblings.
	Parent *string `json:"parent,omitempty" url:"-"`
	// Teamspace moves the document — with everything nested under it — to
	// another teamspace, at the top unless Parent names a document there.
	Teamspace *string `json:"teamspace,omitempty" url:"-"`
}

// teamCommentWrite is one comment on a document.
type teamCommentWrite struct {
	// ID is the document, from the path.
	ID string `json:"id"`
	// Space is the space uuid holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Text is the comment, with the same `<@account-uuid>` mentions a message
	// takes.
	Text string `json:"text" url:"-"`
	// Files attaches blobs already uploaded to the space.
	Files []teamFileIn `json:"files,omitempty" url:"-"`
}

// registerDocs declares the document surface as TYPED ops. The group is built
// here so cmd/zipdoc can resolve the prefix from this file.
func (b *channelBridge) registerDocs(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/docs", b.listDocs)
	g.Post("/docs", b.createDoc, zip.WithStatus(http.StatusCreated))
	g.Get("/docs/:id", b.getDoc)
	g.Patch("/docs/:id", b.editDoc)
	g.Delete("/docs/:id", b.deleteDoc, zip.WithStatus(http.StatusNoContent))
	g.Get("/docs/:id/comments", b.listComments)
	g.Post("/docs/:id/comments", b.sendComment, zip.WithStatus(http.StatusCreated))
}

// ListDocs returns the documents of a space the caller may see, with the
// teamspaces they are grouped in.
//
// These are the Team client's own document rows, so a page created there is
// here with no sync. Each carries the `collaborator` id its body opens with on
// the /v1/team/collaborator socket. A private teamspace's documents are listed
// only for its members.
//
// Example: {"docs": [{"id": "d41c", "space": "0e3c…", "teamspace": "teamspace-general", "title": "Runbook", "rank": "0|hzzzzz:", "author": "9f2…", "createdOn": 1756598400000, "modifiedOn": 1756598400000, "comments": 0, "collaborator": "0e3c…|document:class:Document|d41c|content"}], "teamspaces": [{"id": "teamspace-general", "name": "General", "private": false, "archived": false}]}
func (b *channelBridge) listDocs(ctx context.Context, in *teamDocsRead) (*teamDocs, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	spaces, err := b.teamspaces(s)
	if err != nil {
		return nil, err
	}
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clDocument})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: documents: %v", err)
	}
	out := &teamDocs{Docs: []teamDoc{}, Teamspaces: []teamTeamspace{}}
	for _, ts := range spaces {
		out.Teamspaces = append(out.Teamspaces, teamTeamspace{
			ID: str(ts["_id"]), Name: str(ts["name"]),
			Private: truthy(ts["private"]), Archived: truthy(ts["archived"]),
		})
	}
	sort.Slice(out.Teamspaces, func(i, j int) bool { return out.Teamspaces[i].Name < out.Teamspaces[j].Name })
	for _, doc := range docs {
		v := docOf(s.ws, doc)
		if spaces[v.Teamspace] == nil {
			continue
		}
		if in.Teamspace != "" && v.Teamspace != in.Teamspace {
			continue
		}
		if in.Parent != "" && v.Parent != in.Parent {
			continue
		}
		out.Docs = append(out.Docs, v)
	}
	sort.Slice(out.Docs, func(i, j int) bool {
		a, c := out.Docs[i], out.Docs[j]
		if a.Teamspace != c.Teamspace {
			return a.Teamspace < c.Teamspace
		}
		if a.Parent != c.Parent {
			return a.Parent < c.Parent
		}
		if a.Rank != c.Rank {
			return a.Rank < c.Rank
		}
		return a.ID < c.ID
	})
	return out, nil
}

// CreateDoc creates a document, as the caller, after its siblings.
//
// It is created through the Team client's own write path, so it appears in an
// open Documents sidebar live, and its author is subscribed to it: a comment on
// it lands in their inbox. Its body starts empty — open the returned
// `collaborator` id on the /v1/team/collaborator socket to write it.
//
// A space whose caller can write no teamspace at all gets one on its first
// document: a public "General" teamspace every current member of the space is
// in, which is what the Team client would otherwise make somebody create by hand
// before the first page. Guests and agents are not made members of it.
//
// Example: {"title": "Runbook", "parent": "d41c"}
func (b *channelBridge) createDoc(ctx context.Context, in *teamDocNew) (*teamDoc, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	title, err := titleOf(in.Title)
	if err != nil {
		return nil, err
	}
	sess := b.session(s)
	parent := docNoParent
	var home string
	switch {
	case strings.TrimSpace(in.Parent) != "":
		p, err := b.doc(s, in.Parent)
		if err != nil {
			return nil, err
		}
		home, parent = str(p["space"]), str(p["_id"])
		if in.Teamspace != "" && in.Teamspace != home {
			return nil, zip.ErrBadRequest("team: a nested document lives in its parent's teamspace")
		}
	case strings.TrimSpace(in.Teamspace) != "":
		home = strings.TrimSpace(in.Teamspace)
	default:
		if home, err = b.homeFor(ctx, s, sess); err != nil {
			return nil, err
		}
	}
	ts, err := b.teamspace(s, home)
	if err != nil {
		return nil, err
	}
	if truthy(ts["archived"]) {
		return nil, zip.Errorf(http.StatusConflict, "team: the teamspace is archived")
	}
	rank, err := b.lastRank(s, home, parent)
	if err != nil {
		return nil, err
	}
	id := newMsgID()
	now := time.Now().UnixMilli()
	// Subscribe the author before the create lands, so the context the Inbox
	// groups their notifications under exists from the first comment on.
	sess.ensureNotifyContext(s.account, id, clDocument, home, now)
	create := createTx(id, clDocument, home, s.social(), map[string]any{
		"title": title, "content": nil, "parent": parent, "rank": rankAfter(rank),
		"attachments": 0, "embeddings": 0, "labels": 0, "comments": 0, "references": 0,
	})
	if _, err := apply(sess, b.trans, s.ws, create); err != nil {
		return nil, err
	}
	return b.docNow(s, id)
}

// homeFor picks the teamspace a document with no parent and no named teamspace
// goes in: the caller's only writable one, or — when they can write none — the
// space's General teamspace, opened now.
func (b *channelBridge) homeFor(ctx context.Context, s seat, sess *session) (string, error) {
	spaces, err := b.teamspaces(s)
	if err != nil {
		return "", err
	}
	var open []string
	for id, ts := range spaces {
		if !truthy(ts["archived"]) {
			open = append(open, id)
		}
	}
	switch len(open) {
	case 1:
		return open[0], nil
	case 0:
	default:
		return "", zip.ErrBadRequest("team: this space has more than one teamspace: name the one the document goes in")
	}
	if s.guest() {
		return "", zip.ErrNotFound("team: no teamspace to write in")
	}
	if existing, _ := b.trans.store.get(s.org, s.ws, generalTeamspace); existing != nil {
		// It exists and the caller cannot see it — somebody made it private. Not
		// theirs to reopen.
		return "", zip.ErrNotFound("team: no teamspace to write in")
	}
	people, _, err := b.people(ctx, s)
	if err != nil {
		return "", err
	}
	// Its people are the space's, less its guests — a guest reaches only what
	// names them — and less its agents, which join a teamspace on purpose.
	members := []string{s.account}
	for id, m := range people {
		if !m.Bot && m.Role != roleGuest && id != s.account {
			members = append(members, id)
		}
	}
	sort.Strings(members[1:])
	create := createTx(generalTeamspace, clTeamspace, spaceChannels, s.social(), map[string]any{
		"name": "General", "description": "", "private": false, "archived": false,
		"members": members, "owners": []string{s.account}, "autoJoin": true,
		"type": teamspaceType, "icon": "document:icon:Teamspace",
	})
	if _, err := apply(sess, b.trans, s.ws, create); err != nil {
		return "", err
	}
	return generalTeamspace, nil
}

// GetDoc returns one document the caller may see.
//
// Example: {"id": "d41c", "space": "0e3c…", "teamspace": "teamspace-general", "title": "Runbook", "rank": "0|hzzzzz:", "author": "9f2…", "createdOn": 1756598400000, "modifiedOn": 1756598400000, "comments": 2, "collaborator": "0e3c…|document:class:Document|d41c|content"}
func (b *channelBridge) getDoc(ctx context.Context, in *teamDocAt) (*teamDoc, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	doc, err := b.doc(s, in.ID)
	if err != nil {
		return nil, err
	}
	v := docOf(s.ws, doc)
	return &v, nil
}

// EditDoc renames a document or moves it — under another document, to the top
// of its teamspace, or to another teamspace with everything nested under it.
//
// Anyone who can write the teamspace may rename or move a page, as in the Team
// client. A move goes after its new siblings, and a document cannot be moved
// under itself or under one of its own descendants.
//
// Example: {"space": "0e3c…", "title": "Runbook (2026)", "parent": ""}
func (b *channelBridge) editDoc(ctx context.Context, in *teamDocEdit) (*teamDoc, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	doc, err := b.doc(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writable(s, doc); err != nil {
		return nil, err
	}
	ops := map[string]any{}
	if in.Title != nil {
		title, err := titleOf(*in.Title)
		if err != nil {
			return nil, err
		}
		ops["title"] = title
	}
	from := str(doc["space"])
	home := from
	if in.Teamspace != nil && strings.TrimSpace(*in.Teamspace) != from {
		home = strings.TrimSpace(*in.Teamspace)
		ts, err := b.teamspace(s, home)
		if err != nil {
			return nil, err
		}
		if truthy(ts["archived"]) {
			return nil, zip.Errorf(http.StatusConflict, "team: the teamspace is archived")
		}
	}
	parent := str(doc["parent"])
	if home != from {
		parent = docNoParent
	}
	if in.Parent != nil {
		parent = docNoParent
		if p := strings.TrimSpace(*in.Parent); p != "" {
			target, err := b.doc(s, p)
			if err != nil {
				return nil, err
			}
			if str(target["space"]) != home {
				return nil, zip.ErrBadRequest("team: a document moves under a parent in its own teamspace")
			}
			parent = p
		}
	}
	tree, err := b.descendants(s, str(doc["_id"]))
	if err != nil {
		return nil, err
	}
	if parent == str(doc["_id"]) || tree[parent] != nil {
		return nil, zip.ErrBadRequest("team: a document cannot move under itself")
	}
	txes := []map[string]any{}
	if parent != str(doc["parent"]) || home != from {
		rank, err := b.lastRank(s, home, parent)
		if err != nil {
			return nil, err
		}
		ops["parent"] = parent
		ops["rank"] = rankAfter(rank)
	}
	if home != from {
		ops["space"] = home
		for id := range tree {
			txes = append(txes, updateTx(id, clDocument, from, s.social(), map[string]any{"space": home}))
		}
	}
	if len(ops) > 0 {
		txes = append([]map[string]any{updateTx(in.ID, clDocument, from, s.social(), ops)}, txes...)
	}
	if len(txes) > 0 {
		if _, err := apply(b.session(s), b.trans, s.ws, txes...); err != nil {
			return nil, err
		}
	}
	return b.docNow(s, in.ID)
}

// DeleteDoc removes a document with everything nested under it and every
// comment on any of them — the Team client's own delete, which takes the
// subtree with it. Its author, an owner of its teamspace or an admin of the
// space may delete it. Answers 204.
//
// The document model has no archived state for a page (only a teamspace can be
// archived), so there is nothing softer to offer here than removal.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) deleteDoc(ctx context.Context, in *teamDocAt) (*cloud.Unit, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	doc, err := b.doc(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writable(s, doc); err != nil {
		return nil, err
	}
	ts, err := b.teamspace(s, str(doc["space"]))
	if err != nil {
		return nil, err
	}
	owns := false
	for _, o := range toStringSlice(ts["owners"]) {
		owns = owns || o == s.account
	}
	if authorOf(doc) != s.account && !owns && !s.admin() {
		return nil, zip.ErrForbidden("team: only the author, a teamspace owner or a space admin can delete a document")
	}
	tree, err := b.descendants(s, in.ID)
	if err != nil {
		return nil, err
	}
	tree[in.ID] = doc
	comments, err := b.trans.store.byClasses(s.org, s.ws, []string{clChatMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: comments: %v", err)
	}
	var txes []map[string]any
	for _, c := range comments {
		if tree[str(c["attachedTo"])] == nil {
			continue
		}
		sub, err := b.subtree(s, c)
		if err != nil {
			return nil, err
		}
		for _, d := range sub {
			txes = append(txes, removeTx(d, s.social()))
		}
	}
	// Deepest first, so no reader ever holds a page whose parent is gone.
	for _, d := range b.deepestFirst(tree, in.ID) {
		txes = append(txes, removeTx(d, s.social()))
	}
	if _, err := apply(b.session(s), b.trans, s.ws, txes...); err != nil {
		return nil, err
	}
	return nil, nil
}

// ListComments returns the comments on a document, oldest first — the same
// message shape a channel's conversation answers.
//
// Example: {"messages": [{"id": "c01", "channel": "", "doc": "d41c", "author": "9f2…", "text": "step 3 is out of date", "mentions": [], "createdOn": 1756598400000, "replies": 0, "reactions": [], "files": []}]}
func (b *channelBridge) listComments(ctx context.Context, in *teamDocAt) (*teamMessages, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	if _, err := b.doc(s, in.ID); err != nil {
		return nil, err
	}
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clChatMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: comments: %v", err)
	}
	var mine []map[string]any
	for _, d := range docs {
		if str(d["attachedTo"]) == in.ID {
			mine = append(mine, d)
		}
	}
	return &teamMessages{Messages: b.tail(s.org, s.ws, mine)}, nil
}

// SendComment comments on a document, as the caller.
//
// It is a message attached to the document, written through the Team client's
// own path: the people subscribed to the document and anyone mentioned are
// notified, and an agent mentioned answers with a comment of its own. It can be
// replied to, reacted to, edited and deleted with the message ops.
//
// Example: {"space": "0e3c…", "text": "step 3 is out of date, <@9f2c…>"}
func (b *channelBridge) sendComment(ctx context.Context, in *teamCommentWrite) (*teamMessage, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	doc, err := b.doc(s, in.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writable(s, doc); err != nil {
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
	home := str(doc["space"])
	txes := []map[string]any{attachedCreateTx(id, clChatMessage, home, in.ID, clDocument,
		"comments", s.social(), map[string]any{"message": markup, "attachments": 0})}
	txes = append(txes, fileTxes(in.Files, id, clChatMessage, home, s.social())...)
	return b.post(s, id, txes)
}

// teamspace resolves a teamspace the caller may see, answering 404 otherwise.
func (b *channelBridge) teamspace(s seat, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, zip.ErrNotFound("teamspace not found")
	}
	ts, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: teamspace: %v", err)
	}
	if ts == nil || str(ts["_class"]) != clTeamspace || !s.inTeamspace(ts) {
		return nil, zip.ErrNotFound("teamspace not found")
	}
	return ts, nil
}

// inTeamspace is canSee for a teamspace: public ones are open to every
// non-guest member of the space, private ones to their members.
func (s seat) inTeamspace(ts map[string]any) bool { return openTo(s.role, s.account, ts) }

// teamspaces is every teamspace the caller may see, by id.
func (b *channelBridge) teamspaces(s seat) (map[string]map[string]any, error) {
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clTeamspace})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: teamspaces: %v", err)
	}
	out := map[string]map[string]any{}
	for _, ts := range docs {
		if s.inTeamspace(ts) {
			out[str(ts["_id"])] = ts
		}
	}
	return out, nil
}

// doc resolves a document the caller may see, answering 404 otherwise.
func (b *channelBridge) doc(s seat, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, zip.ErrBadRequest("document id is required")
	}
	doc, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: document: %v", err)
	}
	if doc == nil || str(doc["_class"]) != clDocument {
		return nil, zip.ErrNotFound("document not found")
	}
	if _, err := b.teamspace(s, str(doc["space"])); err != nil {
		return nil, zip.ErrNotFound("document not found")
	}
	return doc, nil
}

// docNow re-reads one document: the answer is what the store now holds.
func (b *channelBridge) docNow(s seat, id string) (*teamDoc, error) {
	doc, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil || doc == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: document after write: %v", err)
	}
	v := docOf(s.ws, doc)
	return &v, nil
}

// lastRank is the greatest rank among a parent's children in a teamspace —
// what the next child is ranked after. Empty for a parent with none.
func (b *channelBridge) lastRank(s seat, home, parent string) (string, error) {
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clDocument})
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "team: documents: %v", err)
	}
	last := ""
	for _, d := range docs {
		if str(d["space"]) == home && pick(str(d["parent"]), docNoParent) == parent && str(d["rank"]) > last {
			last = str(d["rank"])
		}
	}
	return last, nil
}

// descendants is every document nested under one, at any depth, by id.
func (b *channelBridge) descendants(s seat, root string) (map[string]map[string]any, error) {
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clDocument})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: documents: %v", err)
	}
	kids := map[string][]map[string]any{}
	for _, d := range docs {
		kids[str(d["parent"])] = append(kids[str(d["parent"])], d)
	}
	out := map[string]map[string]any{}
	var walk func(id string)
	walk = func(id string) {
		for _, k := range kids[id] {
			kid := str(k["_id"])
			if out[kid] != nil || kid == root {
				continue // a cycle in stored data ends the walk rather than looping
			}
			out[kid] = k
			walk(kid)
		}
	}
	walk(root)
	return out, nil
}

// deepestFirst orders a subtree so every document comes before its parent.
func (b *channelBridge) deepestFirst(tree map[string]map[string]any, root string) []map[string]any {
	depth := func(id string) int {
		n := 0
		for cur := id; cur != root && n <= len(tree); n++ {
			d := tree[cur]
			if d == nil {
				break
			}
			cur = str(d["parent"])
		}
		return n
	}
	ids := make([]string, 0, len(tree))
	for id := range tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := depth(ids[i]), depth(ids[j])
		if di != dj {
			return di > dj
		}
		return ids[i] < ids[j]
	})
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, tree[id])
	}
	return out
}

// docOf projects one stored document.
func docOf(ws string, doc map[string]any) teamDoc {
	id := str(doc["_id"])
	parent := str(doc["parent"])
	if parent == docNoParent {
		parent = ""
	}
	return teamDoc{
		ID: id, Space: ws, Teamspace: str(doc["space"]),
		Title: str(doc["title"]), Parent: parent, Rank: str(doc["rank"]),
		Author:       authorOf(doc),
		CreatedOn:    asInt64(doc["createdOn"]),
		ModifiedOn:   asInt64(doc["modifiedOn"]),
		Comments:     asInt64(doc["comments"]),
		Collaborator: ws + "|" + clDocument + "|" + id + "|" + docBody,
	}
}

// titleOf validates a document title, defaulting an empty one as the Team client
// does.
func titleOf(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "Untitled", nil
	}
	if len(t) > titleBytes {
		return "", zip.ErrBadRequest("team: a title is at most 1024 bytes")
	}
	return t, nil
}
