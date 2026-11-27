package agents

// sessions_visible_test.go — one rule decides who may see a session and who may
// act on one: a member the sessions they opened, an admin of the org or a
// SuperAdmin the org's. It holds on every op on one session (rename, publish,
// retarget, pause, stop, resume, message, budget, events, drain, tree, progress)
// and with no request behind the call, where it used to be skipped.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// as sends one request as a member of acme; admin marks them an org admin.
func asMember(t *testing.T, app *zip.App, method, path, user string, admin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", user)
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Another member of the org reaches none of a member's session: every op on it
// answers as an absent session does, and nothing about it changes. The owner and
// an org admin reach all of it.
func TestAMembersSessionIsNotAnotherMembersToActOn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		code, b := asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "dev", "title": "alice's"})
		if code != http.StatusCreated {
			t.Fatalf("register = %d %s", code, b)
		}
		var x sessionView
		mustJSON(t, b, &x)
		path := "/v1/agent/sessions/" + x.ID
		for _, c := range []struct {
			method, leaf string
			body         any
		}{
			{http.MethodGet, "", nil},
			{http.MethodPatch, "", map[string]any{"title": "bob's now", "project": "p", "published": true}},
			{http.MethodPatch, "", map[string]any{"target": "t_bob"}},
			{http.MethodGet, "/tree", nil},
			{http.MethodGet, "/progress", nil},
			{http.MethodPost, "/events", map[string]any{"kind": "log", "payload": map[string]any{"text": "planted"}}},
			{http.MethodGet, "/control", nil},
			{http.MethodPost, "/pause", nil},
			{http.MethodPost, "/message", map[string]any{"message": "do something else"}},
			{http.MethodPost, "/budget", map[string]any{"budgetMicroUsd": 1}},
			{http.MethodPost, "/stop", nil},
			{http.MethodPost, "/resume", nil},
			{http.MethodDelete, "", nil},
		} {
			if code, b := asMember(t, app, c.method, path+c.leaf, "bob", false, c.body); code != http.StatusNotFound {
				t.Errorf("%s %s as another member = %d %s, want 404", c.method, c.leaf, code, b)
			}
		}
		// Nor may another member hang a session of theirs in its flow.
		if code, b := asMember(t, app, http.MethodPost, "/v1/agent/sessions", "bob", false,
			map[string]any{"agent": "dev", "parentSessionId": x.ID}); code != http.StatusBadRequest {
			t.Errorf("registering under another member's session = %d %s, want 400", code, b)
		}
		code, b = asMember(t, app, http.MethodGet, path, "alice", false, nil)
		var det sessionDetail
		mustJSON(t, b, &det)
		if code != http.StatusOK || det.Title != "alice's" || det.Status != StatusRunning || det.Published || det.Events != 0 ||
			len(det.Children) != 0 {
			t.Fatalf("after another member's attempts the session is %d %+v", code, det.sessionView)
		}

		// The owner renames it, and an org admin pauses it and stops it.
		if code, b := asMember(t, app, http.MethodPatch, path, "alice", false, map[string]any{"title": "renamed"}); code != http.StatusOK {
			t.Fatalf("the owner's rename = %d %s", code, b)
		}
		if code, b := asMember(t, app, http.MethodPost, path+"/pause", "carol", true, nil); code != http.StatusOK {
			t.Fatalf("an org admin's pause = %d %s", code, b)
		}
		if code, b := asMember(t, app, http.MethodPost, path+"/stop", "carol", true, nil); code != http.StatusOK {
			t.Fatalf("an org admin's stop = %d %s", code, b)
		}

	})
}

// With no request behind the call the rule still holds, read off the caller the
// plane states: a member sees their own session and not another's, an admin
// stated as one sees the org's, and a call that states no person sees neither
// person's.
func TestVisibleHoldsWithNoRequestBehindTheCall(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = mountApp(t, &fakeAI{content: "x"})
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, id := range []string{"s_alice", "s_bob"} {
			if err := sto.CreateSession(ctx, Session{ID: id, Org: "acme", Actor: "acme/" + id[2:], Agent: "dev",
				Status: StatusRunning, StartedAt: 1, CreatedAt: 1, UpdatedAt: 1}); err != nil {
				t.Fatal(err)
			}
		}
		as := func(c zip.Caller) context.Context { return zip.WithCaller(ctx, c) }
		for _, c := range []struct {
			name   string
			ctx    context.Context
			id     string
			admits bool
		}{
			{"alice, her own", as(zip.Caller{Org: "acme", User: "alice"}), "s_alice", true},
			{"alice, bob's", as(zip.Caller{Org: "acme", User: "alice"}), "s_bob", false},
			{"an org admin, bob's", as(zip.Caller{Org: "acme", User: "carol", OrgAdmin: true}), "s_bob", true},
			{"a SuperAdmin, bob's", as(zip.Caller{Org: "acme", User: "dave", Admin: true}), "s_bob", true},
			{"nobody, bob's", as(zip.Caller{Org: "acme"}), "s_bob", false},
		} {
			_, err := visible(c.ctx, sto, "acme", c.id)
			if (err == nil) != c.admits {
				t.Errorf("%s: visible = %v, want admitted %v", c.name, err, c.admits)
			}
		}
		// The listing is narrowed the same way, with no request behind it either.
		o := sessionOps{s: mounted}
		list, err := o.list(as(zip.Caller{Org: "acme", User: "alice"}), &sessionQuery{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list.Sessions) != 1 || list.Sessions[0].ID != "s_alice" {
			t.Fatalf("alice's listing = %v, want only her own", list.Sessions)
		}

	})
}

// An agent-run's tool calls carry the run's ORG and the linked SUBJECT (bare,
// e.g. u-1) on the plane caller. Every reader forms the session's actor as
// billingActor(org, sub) — org/sub — so a coding run's session must be recorded
// under that composite value, or its own creator is answered "session not found"
// when it reads the run's events (sought refuses another actor's session, and a
// run in a conversation tree is private even to an org admin).
func TestACodingRunsCreatorReadsItsOwnSession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = mountApp(t, &fakeAI{content: "x"})
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		// What the session-open boundary (agents/session/open) records after the
		// propagation fix: the subject keyed the same way every reader keys it.
		const sub = "u-1"
		session := Session{ID: "sess_c", Org: "acme", Agent: "hanzo", Actor: billingActor("acme", sub),
			Status: StatusRunning, Channel: talkChannel, RootID: "sess_c", StartedAt: 1, CreatedAt: 1, UpdatedAt: 1}
		if err := sto.CreateSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
		// The caller an agent-run tool call carries: org + bare linked subject.
		creator := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: sub})
		if _, err := visible(creator, sto, "acme", "sess_c"); err != nil {
			t.Fatalf("the actor that opened the run cannot read its events: %v", err)
		}
		// A session recorded under the bare subject is a DIFFERENT actor's to that
		// reader, exactly the failure the old boundary produced.
		wrong := session
		wrong.ID, wrong.RootID = "sess_w", "sess_w"
		wrong.Actor = sub
		if err := sto.CreateSession(context.Background(), wrong); err != nil {
			t.Fatal(err)
		}
		if _, err := visible(creator, sto, "acme", "sess_w"); err == nil {
			t.Fatal("a session keyed by the bare subject was visible to the org/sub reader")
		}
	})
}

// THE STREAM SENDS A WATCHER WHAT THEY MAY SEE. The org-wide feed is filtered by
// the rule a read of one session is (reader.sees): a member receives the frames
// of the sessions they opened and not another member's, and an org admin
// receives the org's.
func TestTheStreamSendsAMemberOnlyTheirOwnSessions(t *testing.T) {
	for _, c := range []struct {
		name   string
		user   string
		admin  bool
		sees   []string
		misses []string
	}{
		{"a member", "bob", false, []string{"s_bob"}, []string{"s_alice"}},
		{"an org admin", "carol", true, []string{"s_bob", "s_alice"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				app := mountApp(t, &fakeAI{content: "x"})
				sto, err := mounted.State.storeFor("acme")
				if err != nil {
					t.Fatal(err)
				}
				xs := map[string]Session{}
				for _, id := range []string{"s_alice", "s_bob"} {
					x := Session{ID: id, Org: "acme", RootID: id, Actor: "acme/" + id[2:], Agent: "dev",
						Status: StatusRunning, StartedAt: 1, CreatedAt: 1, UpdatedAt: 1}
					if err := sto.CreateSession(context.Background(), x); err != nil {
						t.Fatal(err)
					}
					xs[id] = x
				}
				type reply struct {
					code int
					body []byte
				}
				got := make(chan reply, 1)
				go func() {
					code, b := asMember(t, app, http.MethodGet, "/v1/agent/sessions/stream", c.user, c.admin, nil)
					got <- reply{code, b}
				}()
				bus := mounted.State.bus
				for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
					bus.mu.Lock()
					n := len(bus.subs)
					bus.mu.Unlock()
					if n > 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("the stream never subscribed")
					}
				}
				for _, id := range []string{"s_alice", "s_bob"} {
					publishSession(mounted, xs[id], 0, 0)
					publishEvent(mounted, xs[id], Event{ID: "evt_" + id, SessionID: id, Org: "acme", Kind: KindLog,
						Payload: `{"text":"secret of ` + id + `"}`, CreatedAt: 1})
				}
				time.Sleep(50 * time.Millisecond)
				bus.close() // ends the stream, so the response is whole
				r := <-got
				if r.code != http.StatusOK {
					t.Fatalf("stream = %d %s", r.code, r.body)
				}
				for _, id := range c.sees {
					if !bytes.Contains(r.body, []byte(`"id":"`+id+`"`)) || !bytes.Contains(r.body, []byte("secret of "+id)) {
						t.Errorf("the stream did not carry %s's session and event:\n%s", id, r.body)
					}
				}
				for _, id := range c.misses {
					if bytes.Contains(r.body, []byte(id)) {
						t.Errorf("the stream carried %s, another member's:\n%s", id, r.body)
					}
				}

			})
		})
	}
}

// A stream's presence beat states its viewer to the sandbox — their subject and
// their admin bits in the stream's org, and nothing else of the request — since
// a sandbox is stamped watched only by one who holds it.
func TestTheStreamsBeatStatesItsViewer(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", User: "alice", Email: "a@acme.test", OrgAdmin: true, Project: "p"})
	if got, keyed := watcherOf(ctx, "acme"); !keyed || got != (zip.Caller{Org: "acme", User: "alice", OrgAdmin: true}) {
		t.Fatalf("the beat states %+v (keyed %v)", got, keyed)
	}
}

// A viewer, and a run's person, whose credential names no subject a sandbox is
// held by — a token with no `sub`, whose X-User-Id is a display name — is stated
// to the sandbox as nobody: the beat keeps no sandbox alive, and the run's box
// leases none, where each used to name whoever else goes by that name.
func TestAViewerWithNoSubjectIsStatedAsNobody(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Raw(http.MethodGet, "/probe", func(zc *zip.Ctx) error {
		principal.Mint(zc, principal.Principal{Org: "acme", User: "Zach Kelling"})
		ctx := principal.WithSubject(zc.Forward(), zc)
		if w, keyed := watcherOf(ctx, "acme"); keyed || w != (zip.Caller{}) {
			t.Errorf("an unkeyed viewer's beat states %+v (keyed %v)", w, keyed)
		}
		run, stop := context.WithCancel(ctx)
		defer stop()
		b := boxFrom(withBox(run, "acme", "Zach Kelling", nil, nil))
		b.warm()
		if _, err := b.take(ctx); !errors.Is(err, errBoxUnkeyed) {
			t.Errorf("an unkeyed run's box leased: %v", err)
		}
		if b.held() != "" {
			t.Errorf("an unkeyed run's box holds %q", b.held())
		}
		return zc.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-User-Id", "Zach Kelling")
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatal(err)
	}
}

// A SESSION'S CHILDREN AND ITS TREE are read by the rule the session is: a member
// is shown the subagent sessions they opened under it, and one another member
// opened is absent with everything beneath it; an admin of the org is shown all.
func TestAChildAnotherMemberOpenedIsNotShown(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = mountApp(t, &fakeAI{content: "x"})
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, x := range []Session{
			{ID: "s_root", Actor: "acme/alice"},
			{ID: "s_hers", ParentID: "s_root", Actor: "acme/alice"},
			{ID: "s_his", ParentID: "s_root", Actor: "acme/bob"},
			{ID: "s_under", ParentID: "s_his", Actor: "acme/alice"},
		} {
			x.Org, x.RootID, x.Agent, x.Status, x.StartedAt, x.CreatedAt, x.UpdatedAt = "acme", "s_root", "dev", StatusRunning, 1, 1, 1
			if err := sto.CreateSession(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		o := sessionOps{s: mounted}
		ids := func(n treeNode) []string {
			var out []string
			var walk func(treeNode)
			walk = func(n treeNode) {
				out = append(out, n.Session.ID)
				for _, k := range n.Children {
					walk(k)
				}
			}
			walk(n)
			return out
		}
		for _, c := range []struct {
			name     string
			who      zip.Caller
			children []string
			tree     []string
		}{
			{"alice", zip.Caller{Org: "acme", User: "alice"}, []string{"s_hers"}, []string{"s_root", "s_hers"}},
			{"an org admin", zip.Caller{Org: "acme", User: "carol", OrgAdmin: true}, []string{"s_hers", "s_his"}, []string{"s_root", "s_hers", "s_his", "s_under"}},
		} {
			as := zip.WithCaller(ctx, c.who)
			det, err := o.get(as, &sessionDetailIn{ID: "s_root"})
			if err != nil {
				t.Fatalf("%s get: %v", c.name, err)
			}
			var kids []string
			for _, k := range det.Children {
				kids = append(kids, k.ID)
			}
			slices.Sort(kids)
			if !slices.Equal(kids, c.children) {
				t.Fatalf("%s is shown children %v, want %v", c.name, kids, c.children)
			}
			if det.sessionView.Children != len(c.children) {
				t.Fatalf("%s is told s_root has %d children, want %d", c.name, det.sessionView.Children, len(c.children))
			}
			// The listing's count is the same number: the children the caller may see.
			page, err := o.list(as, &sessionQuery{})
			if err != nil {
				t.Fatalf("%s list: %v", c.name, err)
			}
			for _, v := range page.Sessions {
				if v.ID == "s_root" && v.Children != len(c.children) {
					t.Fatalf("%s's listing counts %d children of s_root, want %d", c.name, v.Children, len(c.children))
				}
			}
			// And so is each child's own count, one level down: s_his holds s_under,
			// alice's, and only an admin sees s_his at all.
			for _, k := range det.Children {
				if k.ID == "s_hers" && k.Children != 0 {
					t.Fatalf("%s: s_hers counts %d children, want 0", c.name, k.Children)
				}
			}
			tree, err := o.tree(as, &sessionRef{ID: "s_root"})
			if err != nil {
				t.Fatalf("%s tree: %v", c.name, err)
			}
			if got := ids(*tree); !slices.Equal(got, c.tree) {
				t.Fatalf("%s is shown the tree %v, want %v", c.name, got, c.tree)
			}
		}

	})
}
