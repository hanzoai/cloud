package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// slackStub answers one conversations.* method and records the first primary
// conversation read it was asked.
func slackStub(t *testing.T, body string) *url.Values {
	t.Helper()
	var mu sync.Mutex
	got := &url.Values{}
	recorded := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("the read must carry the org's own bot token, got %q", r.Header.Get("Authorization"))
		}
		_ = r.ParseForm()
		mu.Lock()
		if !recorded && (r.URL.Path == "/conversations.replies" || r.URL.Path == "/conversations.history") {
			cp := url.Values{}
			for k, v := range r.Form {
				cp[k] = append([]string(nil), v...)
			}
			cp.Set("path", r.URL.Path)
			*got = cp
			recorded = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users.info" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	orig := slackWebAPIBase
	slackWebAPIBase = srv.URL
	t.Cleanup(func() { slackWebAPIBase = orig; srv.Close() })
	return got
}

// slackRouterStub routes Slack Web API requests by path for multi-endpoint tests.
func slackRouterStub(t *testing.T, handler func(path string, form url.Values) string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("the read must carry the org's own bot token, got %q", r.Header.Get("Authorization"))
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(handler(r.URL.Path, r.Form)))
	}))
	orig := slackWebAPIBase
	slackWebAPIBase = srv.URL
	t.Cleanup(func() { slackWebAPIBase = orig; srv.Close() })
}

// A THREAD reads as a thread. The bug this fixes is one level up from "no
// history": the bridge asked for the history of the ROOM, so every thread in a
// busy channel read as one tangled conversation.
func TestAThreadIsReadAsAThread(t *testing.T) {
	asked := slackStub(t, `{"ok":true,"messages":[
		{"user":"U1","text":"<@UBOT> weather in Benicia","ts":"1.0"},
		{"user":"UBOT","bot_id":"B1","text":"It is 24 degrees and clear.","ts":"2.0"},
		{"user":"U1","text":"try again","ts":"3.0"}
	]}`)

	in := Inbound{Provider: "slack", Channel: "C1", ThreadID: "1.0", User: "U1", Text: "try again"}
	turns, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if p := asked.Get("path"); p != "/conversations.replies" {
		t.Errorf("a threaded turn must read the thread, called %q", p)
	}
	if asked.Get("ts") != "1.0" || asked.Get("channel") != "C1" {
		t.Errorf("the thread must be named, got %v", *asked)
	}
	// The newest turn IS this message — it reached Slack before the event reached
	// us — so sending it on would have the agent answer the same question twice.
	if len(turns) != 2 {
		t.Fatalf("want the two turns before this one, got %d: %+v", len(turns), turns)
	}
	// The mention is stripped, so the transcript reads as the person's own words.
	if turns[0].Text != "weather in Benicia" || turns[0].Self {
		t.Errorf("the question is the user's, got %+v", turns[0])
	}
	if !turns[1].Self {
		t.Errorf("the assistant's own reply must come back as its own, got %+v", turns[1])
	}
}

// A DM does not thread — Slack's assistant surface answers inline — so the room's
// recent messages ARE the conversation, and Slack hands those back newest first.
func TestAnUnthreadedRoomReadsNewestLast(t *testing.T) {
	asked := slackStub(t, `{"ok":true,"messages":[
		{"user":"U1","text":"and after that?","ts":"3.0"},
		{"user":"UBOT","bot_id":"B1","text":"Second.","ts":"2.0"},
		{"user":"U1","text":"First.","ts":"1.0"},
		{"user":"U1","subtype":"channel_join","text":"<@U1> has joined","ts":"0.5"}
	]}`)

	in := Inbound{Provider: "slack", Channel: "D1", User: "U1", Text: "and after that?"}
	turns, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if p := asked.Get("path"); p != "/conversations.history" {
		t.Errorf("an unthreaded turn must read the room, called %q", p)
	}
	if n, _ := strconv.Atoi(asked.Get("limit")); n != slackTurnPage {
		t.Errorf("the page must be bounded, asked for %q", asked.Get("limit"))
	}
	if len(turns) != 2 {
		t.Fatalf("want the exchange without the join or this message, got %+v", turns)
	}
	if turns[0].Text != "First." || turns[1].Text != "Second." {
		t.Fatalf("a transcript in the wrong order invents an exchange nobody had, got %+v", turns)
	}
}

// A read that fails is not a turn that fails. An install predating the history
// scopes answers missing_scope, and less context makes a worse answer while
// refusing to answer makes none — so the error is reported and the caller falls
// back rather than the person seeing nothing.
func TestAWorkspaceThatCannotBeReadStillGetsAnAnswer(t *testing.T) {
	slackStub(t, `{"ok":false,"error":"missing_scope","needed":"channels:history"}`)
	in := Inbound{Provider: "slack", Channel: "C1", User: "U1", Text: "hi"}
	_, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err == nil {
		t.Fatal("a refused read must be reported, not silently taken as an empty conversation")
	}
	if !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "channels:history") {
		t.Fatalf("expected error to name missing_scope and needed scope, got %q", err.Error())
	}
}

func TestSlackThreadContextWithAuthorsAttachmentsBlocksFilesAndSurroundingChannel(t *testing.T) {
	slackRouterStub(t, func(path string, form url.Values) string {
		switch path {
		case "/conversations.replies":
			if form.Get("ts") != "1700001000.000100" {
				t.Errorf("unexpected thread ts %q", form.Get("ts"))
			}
			return `{"ok":true,"messages":[
				{
					"bot_id":"B_ALERT","username":"Sentry","subtype":"bot_message","ts":"1700001000.000100",
					"text":"",
					"attachments":[{"title":"500 on /v1/auth/callback","title_link":"https://sentry.io/issues/99","text":"nil pointer dereference in callback.go:88","fields":[{"title":"Env","value":"prod"}]}]
				},
				{
					"user":"U_ALICE","subtype":"file_share","ts":"1700001010.000200",
					"text":"Captured the failing trace here",
					"files":[{"id":"F123","name":"trace.log","mimetype":"text/plain","preview":"panic: runtime error"}]
				},
				{
					"user":"U_BOB","ts":"1700001020.000300",
					"text":"",
					"blocks":[{"type":"section","text":{"text":"Looks like redirect_uri was empty"}}]
				},
				{
					"user":"U_CAROL","ts":"1700001030.000400",
					"text":"<@UBOT> fix this"
				}
			]}`
		case "/conversations.history":
			if form.Get("latest") != "1700001000.000100" {
				t.Errorf("expected surrounding history before thread root, got latest=%q", form.Get("latest"))
			}
			return `{"ok":true,"messages":[
				{"user":"U_ALICE","ts":"1700000900.000000","text":"Just deployed auth v2 to prod"},
				{"user":"U_OLD","ts":"1699000000.000000","text":"Week old unrelated message"}
			]}`
		case "/users.info":
			switch form.Get("user") {
			case "U_ALICE":
				return `{"ok":true,"user":{"id":"U_ALICE","name":"alice","profile":{"display_name":"Alice"}}}`
			case "U_BOB":
				return `{"ok":true,"user":{"id":"U_BOB","name":"bob","real_name":"Bob Chen"}}`
			}
		}
		return `{"ok":false,"error":"unknown"}`
	})

	in := Inbound{
		Provider: "slack", Channel: "C0DEV", ThreadID: "1700001000.000100",
		MessageID: "1700001030.000400", User: "U_CAROL", Text: "fix this",
	}
	turns, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err != nil {
		t.Fatalf("slackReadTurns: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("want 1 recent pre-thread channel msg + 3 thread turns (excluding triggering msg and week-old msg), got %d: %+v", len(turns), turns)
	}
	// 0: surrounding channel message right before thread
	if turns[0].Sender != "Alice" || !strings.Contains(turns[0].Text, "[channel context before thread]") || !strings.Contains(turns[0].Text, "Just deployed auth v2") {
		t.Errorf("unexpected surrounding channel turn: %+v", turns[0])
	}
	// 1: Sentry alert bot (must NOT be Self=true!)
	if turns[1].Self || turns[1].Sender != "Sentry" || !strings.Contains(turns[1].Text, "500 on /v1/auth/callback") || !strings.Contains(turns[1].Text, "nil pointer dereference") {
		t.Errorf("unexpected alert attachment turn: %+v", turns[1])
	}
	// 2: file_share message with file metadata & preview
	if turns[2].Sender != "Alice" || !strings.Contains(turns[2].Text, "trace.log") || !strings.Contains(turns[2].Text, "F123") || !strings.Contains(turns[2].Text, "panic: runtime error") {
		t.Errorf("unexpected file_share turn: %+v", turns[2])
	}
	// 3: Block Kit only message with Bob's real name
	if turns[3].Sender != "Bob Chen" || !strings.Contains(turns[3].Text, "redirect_uri was empty") {
		t.Errorf("unexpected block-kit turn: %+v", turns[3])
	}
}

func TestSlackLongThreadPreservesRootMessage(t *testing.T) {
	msgs := make([]map[string]any, 0, 50)
	msgs = append(msgs, map[string]any{"user": "U1", "ts": "1000.0", "text": "ROOT BUG REPORT: OOM in worker"})
	for i := 1; i <= 48; i++ {
		msgs = append(msgs, map[string]any{"user": "U2", "ts": fmt.Sprintf("%d.0", 1000+i), "text": fmt.Sprintf("reply %d", i)})
	}
	msgs = append(msgs, map[string]any{"user": "U1", "ts": "1049.0", "text": "<@UBOT> summarize and fix"})
	payload, _ := json.Marshal(map[string]any{"ok": true, "messages": msgs})

	slackRouterStub(t, func(path string, _ url.Values) string {
		if path == "/conversations.replies" {
			return string(payload)
		}
		return `{"ok":false,"error":"missing_scope"}`
	})

	in := Inbound{Provider: "slack", Channel: "C1", ThreadID: "1000.0", MessageID: "1049.0", User: "U1", Text: "summarize and fix"}
	turns, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err != nil {
		t.Fatalf("slackReadTurns: %v", err)
	}
	if len(turns) != slackThreadKeep {
		t.Fatalf("want %d turns, got %d", slackThreadKeep, len(turns))
	}
	if turns[0].Text != "ROOT BUG REPORT: OOM in worker" {
		t.Fatalf("long thread must preserve root message, got %q", turns[0].Text)
	}
	if turns[len(turns)-1].Text != "reply 48" {
		t.Fatalf("expected newest reply 48 last, got %q", turns[len(turns)-1].Text)
	}
}

func TestSlackChannelMentionReadsRecentHistoryExcludingCurrentAndNewer(t *testing.T) {
	slackRouterStub(t, func(path string, form url.Values) string {
		switch path {
		case "/conversations.history":
			// Newest-first as Slack returns it.
			return `{"ok":true,"messages":[
				{"user":"U3","ts":"201.0","text":"concurrent message posted after mention"},
				{"user":"U2","ts":"200.0","text":"<@UBOT> fix this"},
				{"user":"U1","ts":"199.0","text":"Seeing 502 on https://api.hanzo.ai/v1/dev"},
				{"bot_id":"B_O11Y","username":"Hanzo Alerts","subtype":"bot_message","ts":"198.0","attachments":[{"text":"pod cloud-xyz CrashLoopBackOff"}]}
			]}`
		case "/users.info":
			if form.Get("user") == "U1" {
				return `{"ok":true,"user":{"id":"U1","profile":{"display_name":"Zach"}}}`
			}
		}
		return `{"ok":false,"error":"missing_scope"}`
	})

	in := Inbound{Provider: "slack", Channel: "C0OPS", MessageID: "200.0", User: "U2", Text: "fix this"}
	turns, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in)
	if err != nil {
		t.Fatalf("slackReadTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 prior channel turns (excluding current 200.0 and newer 201.0), got %d: %+v", len(turns), turns)
	}
	if turns[0].Sender != "Hanzo Alerts" || !strings.Contains(turns[0].Text, "CrashLoopBackOff") {
		t.Errorf("expected alert first, got %+v", turns[0])
	}
	if turns[1].Sender != "Zach" || !strings.Contains(turns[1].Text, "502 on https://api.hanzo.ai/v1/dev") {
		t.Errorf("expected Zach's error message second, got %+v", turns[1])
	}
}

func TestRouteSlackEventIncludesAttachmentsAndFiles(t *testing.T) {
	raw := []byte(`{
		"type":"event_callback",
		"team_id":"T1",
		"event":{
			"type":"app_mention",
			"channel":"C1",
			"user":"U1",
			"ts":"1700000001.000100",
			"text":"<@UBOT> fix this",
			"attachments":[{"pretext":"Forwarded message","text":"TypeError: cannot read property 'id' of undefined"}],
			"files":[{"id":"F9","name":"error.png","mimetype":"image/png"}]
		}
	}`)
	d := routeSlackEvent(raw)
	if d.Kind != slackRouteAgent {
		t.Fatalf("expected slackRouteAgent, got %v", d.Kind)
	}
	if !strings.Contains(d.Text, "fix this") || !strings.Contains(d.Text, "TypeError") || !strings.Contains(d.Text, "error.png") {
		t.Fatalf("expected inbound text to include prompt, attachment, and file metadata, got %q", d.Text)
	}

	// File-only upload in a followed thread with empty text must not be dropped.
	fileOnly := []byte(`{
		"type":"event_callback",
		"team_id":"T1",
		"event":{
			"type":"message",
			"subtype":"file_share",
			"channel":"C1",
			"channel_type":"channel",
			"user":"U1",
			"ts":"1700000002.000100",
			"thread_ts":"1700000001.000100",
			"text":"",
			"files":[{"id":"F10","name":"stacktrace.txt","mimetype":"text/plain"}]
		}
	}`)
	d2 := routeSlackEvent(fileOnly)
	if d2.Kind != slackRouteAgent || !strings.Contains(d2.Text, "stacktrace.txt") {
		t.Fatalf("expected file-only thread upload to route to agent with file metadata, got %+v", d2)
	}
}
