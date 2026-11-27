package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

func asMemberHeaders(t *testing.T, app *zip.App, method, path, user string, headers map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", user)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestSessionStreamCursorReplayAndDeduplication(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "ok"})
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}

		alice := Session{
			ID: "s_alice_replay", Org: "acme", RootID: "s_alice_replay",
			Actor: "acme/alice", Agent: "dev", Status: StatusRunning,
			StartedAt: 1, CreatedAt: 1, UpdatedAt: 1,
		}
		bob := Session{
			ID: "s_bob_replay", Org: "acme", RootID: "s_bob_replay",
			Actor: "acme/bob", Agent: "dev", Status: StatusRunning,
			StartedAt: 1, CreatedAt: 1, UpdatedAt: 1,
		}
		for _, x := range []Session{alice, bob} {
			if err := sto.CreateSession(context.Background(), x); err != nil {
				t.Fatal(err)
			}
		}

		var storedAlice []Event
		for i := 1; i <= 4; i++ {
			ev, err := sto.AppendEvent(context.Background(), Event{
				ID: fmt.Sprintf("ev_a_%d", i), SessionID: alice.ID, Org: "acme",
				Kind: KindLog, Actor: alice.Actor,
				Payload: fmt.Sprintf(`{"step":%d}`, i), CreatedAt: int64(i),
			})
			if err != nil {
				t.Fatal(err)
			}
			storedAlice = append(storedAlice, ev)
		}
		if _, err := sto.AppendEvent(context.Background(), Event{
			ID: "ev_b_1", SessionID: bob.ID, Org: "acme",
			Kind: KindLog, Actor: bob.Actor,
			Payload: `{"secret":"bob-only"}`, CreatedAt: 5,
		}); err != nil {
			t.Fatal(err)
		}

		// Invalid cursors fail closed with 400.
		for _, bad := range []string{"/v1/agent/sessions/stream?after=-1", "/v1/agent/sessions/stream?after=nope"} {
			if code, _ := asMemberHeaders(t, app, http.MethodGet, bad, "alice", nil); code != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400", bad, code)
			}
		}

		// 1) Replay via ?after=2&root=s_alice_replay + live deduplication of seq 4 + live delivery of seq 5.
		type reply struct {
			code int
			body []byte
		}
		got := make(chan reply, 1)
		go func() {
			code, b := asMemberHeaders(t, app, http.MethodGet, "/v1/agent/sessions/stream?root=s_alice_replay&after=2", "alice", nil)
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
				t.Fatal("stream never subscribed")
			}
		}
		time.Sleep(40 * time.Millisecond)
		// Duplicate live broadcast of already-replayed seq 4 MUST be dropped.
		publishEvent(mounted, alice, storedAlice[3])
		// Fresh seq 5 MUST arrive after replay.
		ev5, err := sto.AppendEvent(context.Background(), Event{
			ID: "ev_a_5", SessionID: alice.ID, Org: "acme",
			Kind: KindLog, Actor: alice.Actor,
			Payload: `{"step":5}`, CreatedAt: 6,
		})
		if err != nil {
			t.Fatal(err)
		}
		publishEvent(mounted, alice, ev5)
		time.Sleep(40 * time.Millisecond)
		bus.close()

		r := <-got
		if r.code != http.StatusOK {
			t.Fatalf("stream = %d %s", r.code, r.body)
		}
		text := string(r.body)
		for _, drop := range []string{`"step":1`, `"step":2`, "bob-only"} {
			if strings.Contains(text, drop) {
				t.Fatalf("stream replayed event that should have been skipped (%s):\n%s", drop, text)
			}
		}
		for _, want := range []string{"id: 3\nevent: event\n", "id: 4\nevent: event\n", "id: 5\nevent: event\n", `"step":3`, `"step":4`, `"step":5`} {
			if !strings.Contains(text, want) {
				t.Fatalf("stream missing %q:\n%s", want, text)
			}
		}
		if n := strings.Count(text, `"step":4`); n != 1 {
			t.Fatalf("seq 4 appeared %d times (expected deduplicated count 1):\n%s", n, text)
		}

		// 2) Replay via Last-Event-ID header when ?after is omitted.
		mounted.State.bus = newBus()
		gotHeader := make(chan reply, 1)
		go func() {
			code, b := asMemberHeaders(t, app, http.MethodGet, "/v1/agent/sessions/stream", "alice", map[string]string{
				"Last-Event-ID": "3",
			})
			gotHeader <- reply{code, b}
		}()
		bus = mounted.State.bus
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			bus.mu.Lock()
			n := len(bus.subs)
			bus.mu.Unlock()
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("header stream never subscribed")
			}
		}
		time.Sleep(40 * time.Millisecond)
		bus.close()

		rh := <-gotHeader
		if rh.code != http.StatusOK {
			t.Fatalf("Last-Event-ID stream = %d %s", rh.code, rh.body)
		}
		htext := string(rh.body)
		if strings.Contains(htext, `"step":3`) || strings.Contains(htext, "bob-only") {
			t.Fatalf("Last-Event-ID=3 leaked <=3 or non-visible event:\n%s", htext)
		}
		if !strings.Contains(htext, `"step":4`) || !strings.Contains(htext, `"step":5`) {
			t.Fatalf("Last-Event-ID=3 missed seq 4/5:\n%s", htext)
		}
	})
}

func TestGetSessionSupportsLimitAndAfterPagination(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "ok"})
		code, b := asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{
			"agent": "dev", "title": "long coding run",
		})
		if code != http.StatusCreated {
			t.Fatalf("register = %d %s", code, b)
		}
		var created sessionView
		mustJSON(t, b, &created)

		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 120; i++ {
			if _, err := sto.AppendEvent(context.Background(), Event{
				ID: fmt.Sprintf("ev_%d", i), SessionID: created.ID, Org: "acme",
				Kind: KindLog, Actor: "acme/alice",
				Payload: fmt.Sprintf(`{"n":%d}`, i), CreatedAt: int64(i),
			}); err != nil {
				t.Fatal(err)
			}
		}

		// Default GET keeps 50-event tail compatibility (seq 71..120).
		code, b = asMember(t, app, http.MethodGet, "/v1/agent/sessions/"+created.ID, "alice", false, nil)
		if code != http.StatusOK {
			t.Fatalf("default get = %d %s", code, b)
		}
		var def sessionDetail
		mustJSON(t, b, &def)
		if len(def.RecentEvents) != 50 || def.RecentEvents[0].Seq != 71 || def.RecentEvents[49].Seq != 120 || def.Next != 0 {
			t.Fatalf("default tail got len=%d first=%d last=%d next=%d",
				len(def.RecentEvents), def.RecentEvents[0].Seq, def.RecentEvents[len(def.RecentEvents)-1].Seq, def.Next)
		}

		// ?limit=500 returns all 120 events without dropping the middle of the run.
		code, b = asMember(t, app, http.MethodGet, "/v1/agent/sessions/"+created.ID+"?limit=500", "alice", false, nil)
		if code != http.StatusOK {
			t.Fatalf("limit=500 get = %d %s", code, b)
		}
		var full sessionDetail
		mustJSON(t, b, &full)
		if len(full.RecentEvents) != 120 || full.RecentEvents[0].Seq != 1 || full.RecentEvents[119].Seq != 120 {
			t.Fatalf("limit=500 got len=%d", len(full.RecentEvents))
		}

		// Forward cursor pagination via ?after=0&limit=50 -> ?after=50&limit=50 -> ?after=100&limit=50.
		var collected []int64
		var cursor int64
		for page := 0; page < 5; page++ {
			url := fmt.Sprintf("/v1/agent/sessions/%s?after=%d&limit=50", created.ID, cursor)
			code, b = asMember(t, app, http.MethodGet, url, "alice", false, nil)
			if code != http.StatusOK {
				t.Fatalf("page %d (%s) = %d %s", page, url, code, b)
			}
			var d sessionDetail
			mustJSON(t, b, &d)
			for _, ev := range d.RecentEvents {
				collected = append(collected, ev.Seq)
			}
			if d.Next == 0 {
				break
			}
			cursor = d.Next
		}
		if len(collected) != 120 || collected[0] != 1 || collected[119] != 120 {
			t.Fatalf("forward pagination collected %d events (want 1..120)", len(collected))
		}
	})
}

func TestTalkAcceptsDeveloperInstructionsSeparateFromInput(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &fakeAI{content: "All set."}
		app := mountApp(t, ai)

		body, _ := json.Marshal(map[string]any{
			"input":                 "check auth callback",
			"developerInstructions": "# Active Skills\nUse ponytail.\n# Memories\n- Prefer .ts",
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/turns", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "alice")
		req.Header.Set("X-User-Sub", "alice")
		resp, err := app.Test(req, deadline)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/agent/turns = %d %s", resp.StatusCode, rb)
		}
		var out talkOut
		mustJSON(t, rb, &out)

		// Wait briefly for the async turn to invoke fakeAI and record its messages.
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if len(ai.gotMsgs) > 0 {
				break
			}
		}
		if len(ai.gotMsgs) < 2 {
			t.Fatalf("expected prefix instructions + clean user message, got %d msgs: %+v", len(ai.gotMsgs), ai.gotMsgs)
		}
		prefix := ai.gotMsgs[0].Content
		userText := ai.gotMsgs[len(ai.gotMsgs)-1].Content
		if !strings.Contains(prefix, "Active Skills") || !strings.Contains(prefix, "Prefer .ts") {
			t.Fatalf("prefix instructions missing developerInstructions: %q", prefix)
		}
		if strings.Contains(userText, "Active Skills") || userText != "check auth callback" {
			t.Fatalf("user prompt polluted: %q", userText)
		}
		// Also verify the persisted user message event on the session transcript stays clean.
		code, b := asMember(t, app, http.MethodGet, "/v1/agent/sessions/"+out.SessionID, "alice", false, nil)
		if code != http.StatusOK {
			t.Fatalf("get session = %d %s", code, b)
		}
		var det sessionDetail
		mustJSON(t, b, &det)
		if len(det.RecentEvents) == 0 || strings.Contains(string(det.RecentEvents[0].Payload), "Active Skills") {
			t.Fatalf("session transcript user message was polluted: %+v", det.RecentEvents)
		}
	})
}
