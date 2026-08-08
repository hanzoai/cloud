package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// slackStub answers one conversations.* method and records what it was asked.
func slackStub(t *testing.T, body string) *url.Values {
	t.Helper()
	got := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("the read must carry the org's own bot token, got %q", r.Header.Get("Authorization"))
		}
		_ = r.ParseForm()
		*got = r.Form
		got.Set("path", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	orig := slackWebAPIBase
	slackWebAPIBase = srv.URL
	t.Cleanup(func() { slackWebAPIBase = orig; srv.Close() })
	return got
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
	slackStub(t, `{"ok":false,"error":"missing_scope"}`)
	in := Inbound{Provider: "slack", Channel: "C1", User: "U1", Text: "hi"}
	if _, err := slackReadTurns(context.Background(), "xoxb-test", "acme", in); err == nil {
		t.Fatal("a refused read must be reported, not silently taken as an empty conversation")
	}
}
