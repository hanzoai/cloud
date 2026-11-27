package team

import (
	"net/http"
	"slices"
	"testing"
)

// TestChannelsListIsWhatTheCallerMaySee is the channel visibility rule end to end: a
// public channel is every member's, a private channel and a DM are their
// members' alone, a guest reads only what names them, and somebody outside the
// space reads nothing of it.
func TestChannelsListIsWhatTheCallerMaySee(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	secret := c.channel(t, "secret", true)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)
	dm := direct.Channel

	names := func(who map[string]string) []string {
		t.Helper()
		var got teamChannels
		c.must(t, http.StatusOK, http.MethodGet, "/v1/team/channels", who, nil, &got)
		var out []string
		for _, r := range got.Channels {
			out = append(out, r.ID)
		}
		return out
	}
	for _, tc := range []struct {
		name      string
		who       map[string]string
		see, miss []string
	}{
		{"owner", c.ada, []string{general.ID, secret.ID, dm.ID}, nil},
		{"member", c.bob, []string{general.ID, dm.ID}, []string{secret.ID}},
		{"guest", c.gus, nil, []string{general.ID, secret.ID, dm.ID}},
		{"non-member", c.mal, nil, []string{general.ID, secret.ID, dm.ID}},
		{"cross-org", c.eve, nil, []string{general.ID, secret.ID, dm.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(tc.who)
			for _, id := range tc.see {
				if !slices.Contains(got, id) {
					t.Errorf("%s does not see %s: %v", tc.name, id, got)
				}
			}
			for _, id := range tc.miss {
				if slices.Contains(got, id) {
					t.Errorf("%s sees %s, which is not theirs: %v", tc.name, id, got)
				}
			}
		})
	}
}

// TestOpenChannelAsAMember: the opener is the channel's first member and owner, the
// kind is published, a member named from outside the space is refused, and a
// guest cannot open channels.
func TestOpenChannelAsAMember(t *testing.T) {
	c := newCast(t)
	r := c.channel(t, "eng", true, c.bobID)
	if r.Kind != kindPrivate || !slices.Contains(r.Members, c.adaID) || !slices.Contains(r.Members, c.bobID) {
		t.Fatalf("channel = %+v, want private with Ada and Bob", r)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/channels", c.ada,
		map[string]any{"name": "x", "space": c.ws, "members": []string{c.malID}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a member from outside the space = %d, want 400", code)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/channels", c.gus,
		map[string]any{"name": "g", "space": c.ws}, nil); code != http.StatusForbidden {
		t.Fatalf("a guest opening a channel = %d, want 403", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodPost, "/v1/team/channels", s.who(c),
			map[string]any{"name": "intrude", "space": c.ws}, nil); code != s.want {
			t.Errorf("%s opening a channel in the space = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestPrivateChannelMessagesAreItsMembers: a member of the space who is not in a
// private channel can neither read nor write it, and the answer is the same 404 a
// channel that does not exist gets.
func TestPrivateChannelMessagesAreItsMembers(t *testing.T) {
	c := newCast(t)
	secret := c.channel(t, "secret", true)
	c.say(t, c.ada, secret.ID, "the plan")
	for _, who := range []map[string]string{c.bob, c.gus, c.mal, c.eve} {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/channels/"+secret.ID+"/messages"), who, nil, nil); code != http.StatusNotFound {
			t.Errorf("read of a private channel by an outsider = %d, want 404", code)
		}
		if code := c.do(t, http.MethodPost, "/v1/team/channels/"+secret.ID+"/messages", who,
			map[string]any{"space": c.ws, "text": "hi"}, nil); code != http.StatusNotFound {
			t.Errorf("write to a private channel by an outsider = %d, want 404", code)
		}
	}
}

// TestRosterNamesPeopleAndAgents: the roster is IAM's people with their names
// and roles plus the org's agents, and presence follows a live connection.
func TestRosterNamesPeopleAndAgents(t *testing.T) {
	c := newCast(t)
	mounted.State.trans.bots = func(_ ctxT, _ string) ([]Bot, error) {
		return []Bot{{ID: "agent_enso", Name: "enso", Active: true}}, nil
	}
	var got teamMembers
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/members"), c.bob, nil, &got)
	by := map[string]teamMember{}
	for _, m := range got.Members {
		by[m.ID] = m
	}
	if a := by[c.adaID]; a.Name != "Ada Lovelace" || a.Role != roleOwner || a.Bot {
		t.Errorf("ada = %+v, want her IAM name, owner, not a bot", a)
	}
	if g := by[c.gusID]; g.Role != roleGuest {
		t.Errorf("gus = %+v, want guest", g)
	}
	bot := by[botUserID("agent_enso")]
	if !bot.Bot || bot.Name != "enso" || !bot.Active || bot.PersonRef != PersonRef(botUserID("agent_enso")) {
		t.Errorf("agent = %+v, want the org's agent as a bot member", bot)
	}
	if _, in := by[c.malID]; in {
		t.Error("somebody outside the space is on its roster")
	}
	if len(got.Members) == 0 || got.Members[len(got.Members)-1].Bot != true || got.Members[0].Bot {
		t.Errorf("order = %+v, want people first, agents after", got.Members)
	}
	// Presence: a live tap on the space is somebody online.
	_, stop, err := mounted.State.trans.hub.listen(c.bobID, []string{c.ws})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/members"), c.ada, nil, &got)
	for _, m := range got.Members {
		want := presenceOffline
		if m.ID == c.bobID {
			want = presenceOnline
		}
		if m.Presence != want {
			t.Errorf("%s presence = %q, want %q", m.ID, m.Presence, want)
		}
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/members"), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s reading the roster = %d, want %d", s.name, code, s.want)
		}
	}
}

// TestRosterSaysWhenAgentsAreUnreachable: an agent registry that fails is
// named, and the people still load.
func TestRosterSaysWhenAgentsAreUnreachable(t *testing.T) {
	c := newCast(t)
	mounted.State.trans.bots = func(_ ctxT, _ string) ([]Bot, error) { return nil, errBoom }
	var got teamMembers
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/members"), c.ada, nil, &got)
	if len(got.Degraded) != 1 || got.Degraded[0] != "agents" || len(got.Members) != 3 {
		t.Fatalf("roster = %+v, want the three people and degraded [agents]", got)
	}
}

// TestDirectMessageIsOnePerSet: asking twice for the same people answers the
// same conversation (201 then 200), in either order of naming, and the channel
// lists as kind "dm"; a stranger to the space cannot be named, and nobody
// outside the space can open one in it.
func TestDirectMessageIsOnePerSet(t *testing.T) {
	c := newCast(t)
	var first, again teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &first)
	c.must(t, http.StatusOK, http.MethodPost, "/v1/team/dms", c.bob,
		map[string]any{"space": c.ws, "members": []string{c.adaID, c.bobID}}, &again)
	if !first.Created || again.Created || first.Channel.ID != again.Channel.ID || first.Channel.Kind != kindDM {
		t.Fatalf("first = %+v, again = %+v, want one dm created once", first, again)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.malID}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a dm with somebody outside the space = %d, want 400", code)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodPost, "/v1/team/dms", s.who(c),
			map[string]any{"space": c.ws, "members": []string{c.adaID}}, nil); code != s.want {
			t.Errorf("%s opening a dm in the space = %d, want %d", s.name, code, s.want)
		}
	}
	// A DM's people are what it is.
	if code := c.do(t, http.MethodPost, "/v1/team/channels/"+first.Channel.ID+"/members", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.gusID}}, nil); code != http.StatusConflict {
		t.Fatalf("adding to a dm = %d, want 409", code)
	}
}

// TestJoinLeaveAndArchive walks a channel's lifecycle: a member joins a public
// channel by naming themselves, cannot add others until they are in it, an
// owner removes somebody, a member leaves, and an archived channel refuses messages
// and new members but still reads.
func TestJoinLeaveAndArchive(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	path := "/v1/team/channels/" + general.ID + "/members"

	if code := c.do(t, http.MethodPost, path, c.bob,
		map[string]any{"space": c.ws, "members": []string{c.gusID}}, nil); code != http.StatusForbidden {
		t.Fatalf("adding somebody to a channel you are not in = %d, want 403", code)
	}
	var r teamChannelOne
	c.must(t, http.StatusOK, http.MethodPost, path, c.bob,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &r)
	if !slices.Contains(r.Channel.Members, c.bobID) {
		t.Fatalf("bob did not join: %+v", r.Channel.Members)
	}
	var got teamChannelMembers
	c.must(t, http.StatusOK, http.MethodGet, c.q(path), c.bob, nil, &got)
	if len(got.Members) != 2 || got.Members[1].Name != "Bob Babbage" {
		t.Fatalf("channel members = %+v, want Ada and Bob by name", got.Members)
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q(path), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s listing channel members = %d, want %d", s.name, code, s.want)
		}
		if code := c.do(t, http.MethodPost, path, s.who(c),
			map[string]any{"space": c.ws, "members": []string{c.malID}}, nil); code != s.want {
			t.Errorf("%s joining = %d, want %d", s.name, code, s.want)
		}
	}
	// Bob cannot remove Ada (not an owner); Ada, the owner, can remove Bob.
	if code := c.do(t, http.MethodDelete, c.q(path+"/"+c.adaID), c.bob, nil, nil); code != http.StatusForbidden {
		t.Fatalf("a member removing the owner = %d, want 403", code)
	}
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q(path+"/"+c.bobID), c.ada, nil, nil)
	// Rejoin, then leave on his own.
	c.must(t, http.StatusOK, http.MethodPost, path, c.bob, map[string]any{"space": c.ws, "members": []string{c.bobID}}, nil)
	c.must(t, http.StatusNoContent, http.MethodDelete, c.q(path+"/"+c.bobID), c.bob, nil, nil)

	// Archive: only an owner or admin; then the channel reads and refuses writes.
	arch := true
	if code := c.do(t, http.MethodPatch, "/v1/team/channels/"+general.ID, c.bob,
		map[string]any{"space": c.ws, "archived": arch}, nil); code != http.StatusForbidden {
		t.Fatalf("a non-owner archiving = %d, want 403", code)
	}
	c.must(t, http.StatusOK, http.MethodPatch, "/v1/team/channels/"+general.ID, c.ada,
		map[string]any{"space": c.ws, "archived": arch, "topic": "retired"}, &r)
	if !r.Channel.Archived || r.Channel.Topic != "retired" {
		t.Fatalf("channel after archive = %+v", r)
	}
	if code := c.do(t, http.MethodPost, "/v1/team/channels/"+general.ID+"/messages", c.ada,
		map[string]any{"space": c.ws, "text": "hello?"}, nil); code != http.StatusConflict {
		t.Fatalf("a message into an archived channel = %d, want 409", code)
	}
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/channels/"+general.ID+"/messages"), c.ada, nil, nil)
	for _, s := range strangers {
		if code := c.do(t, http.MethodPatch, "/v1/team/channels/"+general.ID, s.who(c),
			map[string]any{"space": c.ws, "archived": false}, nil); code != s.want {
			t.Errorf("%s editing = %d, want %d", s.name, code, s.want)
		}
	}
}
