package team

import (
	"net/http"
	"testing"
	"time"
)

// TestMentioningAnAgentMemberRunsIt is the agent lane over the typed surface: an
// agent added to a channel answers an @-mention there, as itself, on behalf of the
// person who asked — in the channel for a message, in the thread for a reply, and
// on the document for a comment. An agent that was never added stays quiet.
func TestMentioningAnAgentMemberRunsIt(t *testing.T) {
	c := newCast(t)
	enso := Bot{ID: "agent_enso", Name: "enso", Active: true}
	botID := botUserID(enso.ID)
	srv := mounted.State.trans
	srv.bots = func(_ ctxT, _ string) ([]Bot, error) { return []Bot{enso}, nil }
	fr := &fakeRunner{out: "on it"}
	srv.runAgent = fr.run

	quiet := c.channel(t, "quiet", true)
	c.say(t, c.ada, quiet.ID, "<@"+botID+"> are you there?")
	time.Sleep(150 * time.Millisecond)
	if n := len(botMessages(t, srv, c.org, c.ws, botID)); n != 0 {
		t.Fatalf("an agent that is not in the private channel answered %d times", n)
	}

	ops := c.channel(t, "ops", true, botID, c.bobID)
	asked := c.say(t, c.ada, ops.ID, "<@"+botID+"> status?")
	waitFor(t, 2*time.Second, func() bool { return len(botMessages(t, srv, c.org, c.ws, botID)) == 1 },
		"the agent never answered in the channel")
	fr.mu.Lock()
	if fr.userSub != c.adaID || fr.agent != enso.ID || fr.text != "@enso status?" {
		t.Errorf("run = (%q, %q, %q), want ada asking enso", fr.userSub, fr.agent, fr.text)
	}
	fr.mu.Unlock()
	var page teamMessages
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/channels/"+ops.ID+"/messages"), c.ada, nil, &page)
	var answer *teamMessage
	for i := range page.Messages {
		if page.Messages[i].Author == botID {
			answer = &page.Messages[i]
		}
	}
	if answer == nil || answer.Text != "on it" || answer.Channel != ops.ID {
		t.Fatalf("channel = %+v, want the agent's answer in it", page.Messages)
	}

	// In a thread: the answer is a reply in the same thread.
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/messages/"+asked.ID+"/replies", c.bob,
		map[string]any{"space": c.ws, "text": "<@" + botID + "> and the logs?"}, nil)
	waitFor(t, 2*time.Second, func() bool {
		var th teamMessages
		c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/messages/"+asked.ID+"/replies"), c.ada, nil, &th)
		for _, r := range th.Messages {
			if r.Author == botID && r.Thread == asked.ID {
				return true
			}
		}
		return false
	}, "the agent never answered in the thread")
}

// TestAPublicMentionIsEnoughAndAGuestCannotSpend: in a public channel an
// agent answers an @-mention without being added; a guest can neither open a DM
// with an agent nor spend a turn by mentioning one in a channel they are in.
func TestAPublicMentionIsEnoughAndAGuestCannotSpend(t *testing.T) {
	c := newCast(t)
	enso := Bot{ID: "agent_enso", Name: "enso", Active: true}
	botID := botUserID(enso.ID)
	srv := mounted.State.trans
	srv.bots = func(_ ctxT, _ string) ([]Bot, error) { return []Bot{enso}, nil }
	fr := &fakeRunner{out: "here"}
	srv.runAgent = fr.run

	lobby := c.channel(t, "lobby", false)
	c.say(t, c.ada, lobby.ID, "<@"+botID+"> hi")
	waitFor(t, 2*time.Second, func() bool { return len(botMessages(t, srv, c.org, c.ws, botID)) == 1 },
		"the agent never answered a mention in a public channel")

	if code := c.do(t, http.MethodPost, "/v1/team/dms", c.gus,
		map[string]any{"space": c.ws, "members": []string{botID}}, nil); code != http.StatusForbidden {
		t.Fatalf("a guest opening a DM with an agent = %d, want 403", code)
	}
	guests := c.channel(t, "guests", true, c.gusID, botID)
	c.say(t, c.gus, guests.ID, "<@"+botID+"> spend for me")
	time.Sleep(200 * time.Millisecond)
	if n := len(botMessages(t, srv, c.org, c.ws, botID)); n != 1 {
		t.Fatalf("a guest's mention was answered (%d answers), want the one from before", n)
	}
}
