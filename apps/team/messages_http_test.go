package team

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// TestMentionIsThePlatformsAndNotifies: `<@id>` of a space member is stored as
// the reference node notify.go reads, so the person gets an inbox row; a token
// naming somebody outside the space stays literal and notifies nobody.
func TestMentionIsThePlatformsAndNotifies(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	m := c.say(t, c.ada, general.ID, "ping <@"+c.bobID+"> and <@"+c.malID+">")
	if !slices.Equal(m.Mentions, []string{c.bobID}) {
		t.Fatalf("mentions = %v, want bob alone", m.Mentions)
	}
	if !strings.Contains(m.Text, "@Bob Babbage") || !strings.Contains(m.Text, "<@"+c.malID+">") {
		t.Fatalf("text = %q, want bob named and the stranger's token literal", m.Text)
	}
	stored, err := mounted.State.trans.store.get(c.org, c.ws, m.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if markup := str(stored["message"]); !strings.Contains(markup,
		`{"type":"reference","attrs":{"id":"`+PersonRef(c.bobID)+`","label":"Bob Babbage","objectclass":"contact:class:Person"}}`) ||
		!strings.HasPrefix(markup, `{"type":"doc"`) {
		t.Fatalf("stored markup = %s, want the platform's JSON document with a reference node", markup)
	}
	note, _ := mounted.State.trans.store.get(c.org, c.ws, "notif:"+m.ID+":"+c.bobID)
	if note == nil || str(note["user"]) != c.bobID {
		t.Fatalf("bob has no inbox row for the mention: %v", note)
	}
	if n, _ := mounted.State.trans.store.get(c.org, c.ws, "notif:"+m.ID+":"+c.malID); n != nil {
		t.Fatal("a stranger to the space was notified")
	}
}

// TestEditMessageIsTheAuthors: the author rewrites and the edit is stamped;
// anybody else in the channel is refused 403, and outsiders 404.
func TestEditMessageIsTheAuthors(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	m := c.say(t, c.ada, general.ID, "draft")
	var got teamMessage
	c.must(t, http.StatusOK, http.MethodPatch, "/v1/team/messages/"+m.ID, c.ada,
		map[string]any{"space": c.ws, "text": "final"}, &got)
	if got.Text != "final" || got.EditedOn == 0 || got.ID != m.ID {
		t.Fatalf("after edit = %+v, want the new text, stamped", got)
	}
	if code := c.do(t, http.MethodPatch, "/v1/team/messages/"+m.ID, c.bob,
		map[string]any{"space": c.ws, "text": "mine now"}, nil); code != http.StatusForbidden {
		t.Fatalf("editing somebody else's message = %d, want 403", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodPatch, "/v1/team/messages/"+m.ID, s.who(c),
			map[string]any{"space": c.ws, "text": "x"}, nil); code != s.want {
			t.Errorf("%s editing = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestDeleteMessageTakesItsThread: the author or a space admin deletes, a
// plain member cannot delete another's message, and the delete takes the
// replies and reactions with it.
func TestDeleteMessageTakesItsThread(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	bobs := c.say(t, c.bob, general.ID, "hot take")
	var reply teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/messages/"+bobs.ID+"/replies", c.ada,
		map[string]any{"space": c.ws, "text": "disagree"}, &reply)
	c.must(t, http.StatusOK, http.MethodPut, "/v1/team/messages/"+bobs.ID+"/reactions/fire", c.ada,
		map[string]any{"space": c.ws}, nil)

	adas := c.say(t, c.ada, general.ID, "mine")
	if code := c.do(t, http.MethodDelete, c.q("/v1/team/messages/"+adas.ID), c.bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a member deleting another's message = %d, want 403", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodDelete, c.q("/v1/team/messages/"+adas.ID), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s deleting = %d, want %d", s.name, code, s.want)
		}
	}
	// Ada owns the space: she may delete Bob's, and it takes the thread with it.
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q("/v1/team/messages/"+bobs.ID), c.ada, nil, nil)
	for _, id := range []string{bobs.ID, reply.ID} {
		if d, _ := mounted.State.trans.store.get(c.org, c.ws, id); d != nil {
			t.Errorf("%s survived its message's delete", id)
		}
	}
	reactions, _ := mounted.State.trans.store.byClasses(c.org, c.ws, []string{clReaction})
	if len(reactions) != 0 {
		t.Errorf("reactions survived: %v", reactions)
	}
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q("/v1/team/messages/"+adas.ID), c.ada, nil, nil)
}

// TestThreadsAreOneLevel: a reply lands in the thread, moves the parent's
// counters, and cannot itself be answered.
func TestThreadsAreOneLevel(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	m := c.say(t, c.ada, general.ID, "question")
	var reply teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/messages/"+m.ID+"/replies", c.bob,
		map[string]any{"space": c.ws, "text": "answer"}, &reply)
	if reply.Thread != m.ID || reply.Channel != general.ID || reply.Author != c.bobID {
		t.Fatalf("reply = %+v, want it in %s's thread in %s by bob", reply, m.ID, general.ID)
	}
	var thread teamMessages
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/messages/"+m.ID+"/replies"), c.ada, nil, &thread)
	if len(thread.Messages) != 1 || thread.Messages[0].ID != reply.ID {
		t.Fatalf("thread = %+v, want the one reply", thread.Messages)
	}
	var page teamMessages
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/channels/"+general.ID+"/messages"), c.ada, nil, &page)
	if len(page.Messages) != 1 || page.Messages[0].Replies != 1 || page.Messages[0].LastReply == 0 {
		t.Fatalf("channel = %+v, want the parent alone, counting one reply", page.Messages)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/messages/"+reply.ID+"/replies", c.ada,
		map[string]any{"space": c.ws, "text": "deeper"}, nil); code != http.StatusBadRequest {
		t.Fatalf("a reply to a reply = %d, want 400", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/messages/"+m.ID+"/replies"), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s reading the thread = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodPost, "/v1/team/messages/"+m.ID+"/replies", s.who(c),
			map[string]any{"space": c.ws, "text": "x"}, nil); code != s.want {
			t.Errorf("%s replying = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestReactionsAreASetPerPerson: reacting twice is one reaction, two people are
// a count of two, and taking one back leaves the other.
func TestReactionsAreASetPerPerson(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	m := c.say(t, c.ada, general.ID, "shipped")
	thumbs := "/v1/team/messages/" + m.ID + "/reactions/" + url.PathEscape("👍")
	var got teamMessage
	c.must(t, http.StatusOK, http.MethodPut, thumbs, c.ada, map[string]any{"space": c.ws}, nil)
	c.must(t, http.StatusOK, http.MethodPut, thumbs, c.ada, map[string]any{"space": c.ws}, nil)
	c.must(t, http.StatusOK, http.MethodPut, thumbs, c.bob, map[string]any{"space": c.ws}, &got)
	if len(got.Reactions) != 1 || got.Reactions[0].Emoji != "👍" || got.Reactions[0].Count != 2 ||
		!slices.Equal(got.Reactions[0].Accounts, []string{c.adaID, c.bobID}) {
		t.Fatalf("reactions = %+v, want one 👍 from ada and bob", got.Reactions)
	}
	c.must(t, http.StatusOK, http.MethodDelete, c.q(thumbs), c.ada, nil, &got)
	if len(got.Reactions) != 1 || got.Reactions[0].Count != 1 || got.Reactions[0].Accounts[0] != c.bobID {
		t.Fatalf("after ada takes hers back = %+v", got.Reactions)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodPut, thumbs, s.who(c), map[string]any{"space": c.ws}, nil); code != s.want {
			t.Errorf("%s reacting = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodDelete, c.q(thumbs), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s unreacting = %d, want %d", s.name, code, s.want)
		}
	}
	if code := c.do(t, http.MethodPut, "/v1/team/messages/"+m.ID+"/reactions/"+url.PathEscape("a b"), c.ada,
		map[string]any{"space": c.ws}, nil); code != http.StatusBadRequest {
		t.Fatalf("a sentence as a reaction = %d, want 400", code)
	}
}

// TestMessageCarriesFiles: a message may be files alone, each naming a blob by
// the uuid it was uploaded under; anything else is refused.
func TestMessageCarriesFiles(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	blob := uuid.NewString()
	var m teamMessage
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/channels/"+general.ID+"/messages", c.ada,
		map[string]any{"space": c.ws, "files": []map[string]any{{"file": blob, "name": "plan.pdf", "type": "application/pdf", "size": 1234}}}, &m)
	if len(m.Files) != 1 || m.Files[0].File != blob || m.Files[0].Name != "plan.pdf" || m.Files[0].Size != 1234 {
		t.Fatalf("files = %+v, want the one attachment", m.Files)
	}
	stored, _ := mounted.State.trans.store.get(c.org, c.ws, m.ID)
	if n, _ := asFloat(stored["attachments"]); n != 1 {
		t.Fatalf("message attachments counter = %v, want 1", n)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/channels/"+general.ID+"/messages", c.ada,
		map[string]any{"space": c.ws, "files": []map[string]any{{"file": "../../etc/passwd", "name": "x"}}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a file that is not an upload id = %d, want 400", code)
	}
}

// TestConcurrentReactsAreOneReaction: twenty identical reacts at once are one
// reaction and a count of one.
func TestConcurrentReactsAreOneReaction(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	m := c.say(t, c.ada, general.ID, "race")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = call(t, c.app, http.MethodPut, "/v1/team/messages/"+m.ID+"/reactions/tada", c.ada, map[string]any{"space": c.ws})
		}()
	}
	wg.Wait()
	var got teamMessage
	c.must(t, http.StatusOK, http.MethodPut, "/v1/team/messages/"+m.ID+"/reactions/tada", c.ada, map[string]any{"space": c.ws}, &got)
	if len(got.Reactions) != 1 || got.Reactions[0].Count != 1 {
		t.Fatalf("reactions after 21 identical reacts = %+v, want one", got.Reactions)
	}
	stored, _ := mounted.State.trans.store.get(c.org, c.ws, m.ID)
	if n, _ := asFloat(stored["reactions"]); n != 1 {
		t.Fatalf("message reactions counter = %v, want 1", n)
	}
}

// TestDeleteSurvivesACycleInStoredData: two replies attached to each other —
// stored data no client should write — end the delete walk instead of
// recursing forever.
func TestDeleteSurvivesACycleInStoredData(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	for _, pair := range [][2]string{{"t1", "t2"}, {"t2", "t1"}} {
		if err := mounted.State.trans.store.put(c.org, c.ws, map[string]any{
			"_id": pair[0], "_class": clThreadMessage, "space": general.ID,
			"attachedTo": pair[1], "attachedToClass": clThreadMessage, "collection": "replies",
			"objectId": general.ID, "objectClass": clChannel, "message": "loop",
			"createdBy": "hanzo:" + c.adaID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q("/v1/team/messages/t1"), c.ada, nil, nil)
	for _, id := range []string{"t1", "t2"} {
		if d, _ := mounted.State.trans.store.get(c.org, c.ws, id); d != nil {
			t.Errorf("%s survived", id)
		}
	}
}
