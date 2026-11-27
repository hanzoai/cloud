package team

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

// doc creates a document as `who` and answers it.
func (c *cast) doc(t *testing.T, who map[string]string, body map[string]any) teamDoc {
	t.Helper()
	body["space"] = c.ws
	var d teamDoc
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/docs", who, body, &d)
	return d
}

// TestDocsAreATreeWithACollaborator: the first document opens a General
// teamspace for the space's people (not its guests), children rank after
// their siblings, and every document names the collaborator id its body
// opens with.
func TestDocsAreATreeWithACollaborator(t *testing.T) {
	c := newCast(t)
	root := c.doc(t, c.ada, map[string]any{"title": "Runbook"})
	if root.Teamspace != generalTeamspace || root.Parent != "" || root.Rank != rankMiddle || root.Author != c.adaID {
		t.Fatalf("root = %+v", root)
	}
	if want := c.ws + "|document:class:Document|" + root.ID + "|content"; root.Collaborator != want {
		t.Fatalf("collaborator = %q, want %q", root.Collaborator, want)
	}
	a := c.doc(t, c.bob, map[string]any{"title": "Step 1", "parent": root.ID})
	b := c.doc(t, c.bob, map[string]any{"title": "", "parent": root.ID})
	if a.Parent != root.ID || b.Title != "Untitled" || !(a.Rank < b.Rank) {
		t.Fatalf("children = %+v, %+v, want nested, ranked in order", a, b)
	}
	var tree teamDocs
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs"), c.bob, nil, &tree)
	if len(tree.Docs) != 3 || len(tree.Teamspaces) != 1 || tree.Teamspaces[0].Name != "General" {
		t.Fatalf("tree = %+v", tree)
	}
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs")+"&parent="+root.ID, c.bob, nil, &tree)
	if len(tree.Docs) != 2 || tree.Docs[0].ID != a.ID || tree.Docs[1].ID != b.ID {
		t.Fatalf("children of root = %+v, want a then b", tree.Docs)
	}
	var got teamDoc
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs/"+a.ID), c.ada, nil, &got)
	if got.Title != "Step 1" {
		t.Fatalf("get = %+v", got)
	}
	// The guest is in no teamspace, so the tree is not theirs.
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs"), c.gus, nil, &tree)
	if len(tree.Docs) != 0 {
		t.Fatalf("a guest reads %d documents, want none", len(tree.Docs))
	}
	if code := c.do(t, http.MethodGet, c.q("/v1/team/docs/"+a.ID), c.gus, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a guest reading a document = %d, want 404", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/docs"), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s listing = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodGet, c.q("/v1/team/docs/"+a.ID), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s reading = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodPost, "/v1/team/docs", s.who(c),
			map[string]any{"space": c.ws, "title": "x", "parent": root.ID}, nil); code != s.want {
			t.Errorf("%s creating = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestPrivateTeamspaceIsItsMembers: a document in a private teamspace reads
// and writes for its members and is a 404 for the rest of the space.
func TestPrivateTeamspaceIsItsMembers(t *testing.T) {
	c := newCast(t)
	sess := &session{server: mounted.State.trans, store: mounted.State.trans.store, hier: mounted.State.trans.hier,
		org: c.org, space: c.ws, account: c.adaID}
	if _, err := apply(sess, mounted.State.trans, c.ws, createTx("ts-board", clTeamspace, spaceChannels, "hanzo:"+c.adaID,
		map[string]any{"name": "Board", "private": true, "archived": false, "members": []string{c.adaID}, "owners": []string{c.adaID}})); err != nil {
		t.Fatal(err)
	}
	d := c.doc(t, c.ada, map[string]any{"title": "Comp", "teamspace": "ts-board"})
	if code := c.do(t, http.MethodGet, c.q("/v1/team/docs/"+d.ID), c.bob, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a non-member of the teamspace reading = %d, want 404", code)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/docs", c.bob,
		map[string]any{"space": c.ws, "teamspace": "ts-board"}, nil); code != http.StatusNotFound {
		t.Fatalf("a non-member creating in it = %d, want 404", code)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/docs/"+d.ID+"/comments", c.bob,
		map[string]any{"space": c.ws, "text": "hm"}, nil); code != http.StatusNotFound {
		t.Fatalf("a non-member commenting = %d, want 404", code)
	}
}

// TestEditDocRenamesAndMoves: rename, move to the top, and the two moves that
// would make the tree a cycle are refused.
func TestEditDocRenamesAndMoves(t *testing.T) {
	c := newCast(t)
	root := c.doc(t, c.ada, map[string]any{"title": "Root"})
	kid := c.doc(t, c.ada, map[string]any{"title": "Kid", "parent": root.ID})
	grand := c.doc(t, c.ada, map[string]any{"title": "Grand", "parent": kid.ID})
	var got teamDoc
	c.must(t, http.StatusOK, http.MethodPatch, "/v1/team/docs/"+kid.ID, c.bob,
		map[string]any{"space": c.ws, "title": "Kid (renamed)", "parent": ""}, &got)
	if got.Title != "Kid (renamed)" || got.Parent != "" || !(got.Rank > root.Rank) {
		t.Fatalf("after move = %+v, want renamed, at the top, after root", got)
	}
	if code := c.do(t, http.MethodPatch, "/v1/team/docs/"+kid.ID, c.ada,
		map[string]any{"space": c.ws, "parent": kid.ID}, nil); code != http.StatusBadRequest {
		t.Fatalf("a move under itself = %d, want 400", code)
	}
	if code := c.do(t, http.MethodPatch, "/v1/team/docs/"+kid.ID, c.ada,
		map[string]any{"space": c.ws, "parent": grand.ID}, nil); code != http.StatusBadRequest {
		t.Fatalf("a move under its own child = %d, want 400", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodPatch, "/v1/team/docs/"+kid.ID, s.who(c),
			map[string]any{"space": c.ws, "title": "x"}, nil); code != s.want {
			t.Errorf("%s editing = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestDeleteDocTakesItsSubtree: a plain member cannot delete somebody else's
// page; its author can, and the children and comments go with it.
func TestDeleteDocTakesItsSubtree(t *testing.T) {
	c := newCast(t)
	root := c.doc(t, c.ada, map[string]any{"title": "Root"})
	kid := c.doc(t, c.bob, map[string]any{"title": "Kid", "parent": root.ID})
	var note teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/docs/"+kid.ID+"/comments", c.bob,
		map[string]any{"space": c.ws, "text": "note"}, &note)
	if code := c.do(t, http.MethodDelete, c.q("/v1/team/docs/"+root.ID), c.bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a member deleting another's page = %d, want 403", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodDelete, c.q("/v1/team/docs/"+root.ID), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s deleting = %d, want %d", s.name, code, s.want)
		}
	}
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q("/v1/team/docs/"+root.ID), c.ada, nil, nil)
	for _, id := range []string{root.ID, kid.ID, note.ID} {
		if d, _ := mounted.State.trans.store.get(c.org, c.ws, id); d != nil {
			t.Errorf("%s survived its page's delete", id)
		}
	}
}

// TestCommentsAreMessagesOnADocument: a comment is the channel message shape
// with `doc` set, the document counts it, its author (subscribed at create) is
// notified, and it can be replied to and reacted to with the message ops.
func TestCommentsAreMessagesOnADocument(t *testing.T) {
	c := newCast(t)
	d := c.doc(t, c.ada, map[string]any{"title": "Spec"})
	var m teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/docs/"+d.ID+"/comments", c.bob,
		map[string]any{"space": c.ws, "text": "step 3?"}, &m)
	if m.Doc != d.ID || m.Channel != "" || m.Author != c.bobID {
		t.Fatalf("comment = %+v", m)
	}
	var got teamDoc
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs/"+d.ID), c.ada, nil, &got)
	if got.Comments != 1 {
		t.Fatalf("doc comments = %d, want 1", got.Comments)
	}
	if note, _ := mounted.State.trans.store.get(c.org, c.ws, "notif:"+m.ID+":"+c.adaID); note == nil {
		t.Fatal("the document's author was not notified of the comment")
	}
	var reply teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/messages/"+m.ID+"/replies", c.ada,
		map[string]any{"space": c.ws, "text": "fixed"}, &reply)
	if reply.Doc != d.ID || reply.Thread != m.ID {
		t.Fatalf("reply to a comment = %+v", reply)
	}
	var page teamMessages
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/docs/"+d.ID+"/comments"), c.ada, nil, &page)
	if len(page.Messages) != 1 || page.Messages[0].Replies != 1 {
		t.Fatalf("comments = %+v, want one, with one reply", page.Messages)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/docs/"+d.ID+"/comments"), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s reading comments = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodPost, "/v1/team/docs/"+d.ID+"/comments", s.who(c),
			map[string]any{"space": c.ws, "text": "x"}, nil); code != s.want {
			t.Errorf("%s commenting = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestDocChangesReachTheTap: a document write is broadcast like any other, so
// the event stream and open clients see it.
func TestDocChangesReachTheTap(t *testing.T) {
	c := newCast(t)
	tp, stop, err := mounted.State.trans.hub.listen(c.adaID, []string{c.ws})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	d := c.doc(t, c.ada, map[string]any{"title": "Live"})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case b := <-tp.ch:
			for _, raw := range b.txes {
				var tx map[string]any
				_ = json.Unmarshal(raw, &tx)
				if str(tx["objectId"]) == d.ID && slices.Contains([]string{clTxCreate}, str(tx["_class"])) {
					return
				}
			}
		case <-deadline:
			t.Fatal("the create never reached the hub")
		}
	}
}
