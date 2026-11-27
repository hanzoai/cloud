package team

import (
	"net/http"
	"testing"
	"time"
)

// TestInboxIsTheCallersOwn walks one notification's life: a mention files it
// for bob (and nobody else sees it), it lists newest first with the channel and the
// message that caused it, reading it clears the badge, archiving takes it out
// of the live inbox, and read-all clears the rest.
func TestInboxIsTheCallersOwn(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	first := c.say(t, c.ada, general.ID, "<@"+c.bobID+"> first")
	// Newest-first is by the platform's millisecond stamp; two messages in one
	// millisecond order by id, which says nothing about which was said first.
	time.Sleep(2 * time.Millisecond)
	second := c.say(t, c.ada, general.ID, "<@"+c.bobID+"> second")

	var box teamInbox
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.bob, nil, &box)
	if box.Unread != 2 || len(box.Items) != 2 {
		t.Fatalf("bob's inbox = %+v, want two unread", box)
	}
	top := box.Items[0]
	if top.Reason != reasonMention || top.Channel != general.ID || top.Message == nil ||
		top.Message.ID != second.ID || top.Read {
		t.Fatalf("newest = %+v, want the second mention, unread, with its message", top)
	}
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.ada, nil, &box)
	if len(box.Items) != 0 {
		t.Fatalf("ada sees %d of bob's notifications", len(box.Items))
	}

	// Ada cannot read or archive Bob's; outsiders reach nothing.
	for _, who := range []map[string]string{c.ada, c.mal, c.eve} {
		if code := c.do(t, http.MethodPost, "/v1/team/inbox/"+top.ID+"/read", who,
			map[string]any{"space": c.ws}, nil); code != http.StatusNotFound {
			t.Errorf("reading somebody else's notification = %d, want 404", code)
		}
	}
	for _, s := range strangers {
		if code := c.do(t, http.MethodGet, c.q("/v1/team/inbox"), s.who(c), nil, nil); code != s.want {
			t.Errorf("%s reading an inbox in the space = %d, want %d", s.name, code, s.want)
		}
	}

	var item teamInboxItem
	c.must(t, http.StatusOK, http.MethodPost, "/v1/team/inbox/"+top.ID+"/read", c.bob,
		map[string]any{"space": c.ws}, &item)
	if !item.Read {
		t.Fatalf("after read = %+v", item)
	}
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.bob, nil, &box)
	if box.Unread != 1 {
		t.Fatalf("unread after reading one = %d, want 1", box.Unread)
	}

	c.must(t, http.StatusOK, http.MethodPost, "/v1/team/inbox/"+top.ID+"/archive", c.bob,
		map[string]any{"space": c.ws}, &item)
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.bob, nil, &box)
	if len(box.Items) != 1 || box.Items[0].Message.ID != first.ID {
		t.Fatalf("live inbox after archive = %+v, want the first mention alone", box.Items)
	}
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox")+"&archived=true", c.bob, nil, &box)
	if len(box.Items) != 1 || box.Items[0].ID != top.ID || !box.Items[0].Archived {
		t.Fatalf("archived inbox = %+v, want the archived one", box.Items)
	}

	var cleared teamInboxCleared
	c.must(t, http.StatusOK, http.MethodPost, "/v1/team/inbox/read", c.bob, map[string]any{"space": c.ws}, &cleared)
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.bob, nil, &box)
	if cleared.Read != 1 || box.Unread != 0 {
		t.Fatalf("read-all = %+v, unread after = %d, want 1 then 0", cleared, box.Unread)
	}
}

// TestInboxNamesWhyItWasFiled: a DM is "dm", a comment on a followed document
// is "comment", a reply in a thread is "reply".
func TestInboxNamesWhyItWasFiled(t *testing.T) {
	c := newCast(t)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)
	c.say(t, c.ada, direct.Channel.ID, "psst")
	d := c.doc(t, c.bob, map[string]any{"title": "Spec"})
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/docs/"+d.ID+"/comments", c.ada,
		map[string]any{"space": c.ws, "text": "nit"}, nil)

	var box teamInbox
	c.must(t, http.StatusOK, http.MethodGet, c.q("/v1/team/inbox"), c.bob, nil, &box)
	reasons := map[string]bool{}
	for _, it := range box.Items {
		reasons[it.Reason] = true
	}
	if !reasons[reasonDirect] || !reasons[reasonComment] {
		t.Fatalf("reasons = %v (%+v), want dm and comment", reasons, box.Items)
	}
}
