package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

// ingress_test.go proves the two client pieces channels rides: the SendDiscord
// transport (the one new HTTP verb — httptest via the package's own repoint
// pattern, zero live network) and emitIngress (registration, goroutine hop,
// bounded context, panic containment).

// discordMsgCapture records what SendDiscord put on the wire. Mutex-guarded:
// the httptest handler runs on the server goroutine.
type discordMsgCapture struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	auth   string
	body   []byte
	status int
	reply  string
}

func (c *discordMsgCapture) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.calls++
	c.method = r.Method
	c.path = r.URL.Path
	c.auth = r.Header.Get("Authorization")
	c.body = body
	status, reply := c.status, c.reply
	c.mu.Unlock()
	w.WriteHeader(status)
	_, _ = w.Write([]byte(reply))
}

func (c *discordMsgCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *discordMsgCapture) last() (method, path, auth string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.path, c.auth, c.body
}

// newDiscordMsgServer repoints discordAPIBase at an httptest server — the
// package's established repoint idiom (slackWebAPIBase, telegramAPIBase).
func newDiscordMsgServer(t *testing.T, status int, reply string) *discordMsgCapture {
	t.Helper()
	rec := &discordMsgCapture{status: status, reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	saved := discordAPIBase
	discordAPIBase = srv.URL
	t.Cleanup(func() {
		discordAPIBase = saved
		srv.Close()
	})
	return rec
}

func TestSendDiscordPostsMessage(t *testing.T) {
	t.Setenv(discordBotTokenEnv, "test-token")
	rec := newDiscordMsgServer(t, http.StatusOK, `{"id":"456"}`)

	id, err := SendDiscord(context.Background(), "123", "789", "hello")
	if err != nil {
		t.Fatalf("SendDiscord: %v", err)
	}
	if id != "456" {
		t.Fatalf("message id = %q, want 456", id)
	}
	method, path, auth, body := rec.last()
	if method != http.MethodPost {
		t.Fatalf("method = %q, want POST", method)
	}
	if path != "/channels/123/messages" {
		t.Fatalf("path = %q, want /channels/123/messages", path)
	}
	if auth != "Bot test-token" {
		t.Fatalf("authorization = %q, want the Bot token header", auth)
	}
	var payload struct {
		Content   string `json:"content"`
		Reference *struct {
			MessageID string `json:"message_id"`
		} `json:"message_reference"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	if payload.Content != "hello" {
		t.Fatalf("content = %q", payload.Content)
	}
	if payload.Reference == nil || payload.Reference.MessageID != "789" {
		t.Fatalf("message_reference = %+v, want message_id 789", payload.Reference)
	}

	// replyTo "" ⇒ a top-level message: no message_reference key at all.
	if _, err := SendDiscord(context.Background(), "123", "", "top"); err != nil {
		t.Fatalf("SendDiscord top-level: %v", err)
	}
	_, _, _, body = rec.last()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	if _, ok := raw["message_reference"]; ok {
		t.Fatal("message_reference must be absent when replyTo is empty")
	}
}

func TestSendDiscordTokenUnset(t *testing.T) {
	t.Setenv(discordBotTokenEnv, "")
	rec := newDiscordMsgServer(t, http.StatusOK, `{"id":"1"}`)

	_, err := SendDiscord(context.Background(), "123", "", "x")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v, want a not-configured refusal", err)
	}
	if rec.count() != 0 {
		t.Fatal("no HTTP call may fire without a token")
	}
}

func TestSendDiscordTruncatesContent(t *testing.T) {
	t.Setenv(discordBotTokenEnv, "test-token")
	rec := newDiscordMsgServer(t, http.StatusOK, `{"id":"1"}`)

	if _, err := SendDiscord(context.Background(), "123", "", strings.Repeat("a", discordMaxContent+500)); err != nil {
		t.Fatalf("SendDiscord: %v", err)
	}
	_, _, _, body := rec.last()
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(payload.Content) != discordMaxContent {
		t.Fatalf("content length = %d, want exactly %d", len(payload.Content), discordMaxContent)
	}
}

func TestSendDiscordHTTPErrorRedacted(t *testing.T) {
	t.Setenv(discordBotTokenEnv, "test-token")
	newDiscordMsgServer(t, http.StatusForbidden, `{"message":"Missing Access"}`)

	_, err := SendDiscord(context.Background(), "123", "", "x")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the HTTP status", err)
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatal("transport errors carry status/shape only — never the token")
	}
}

// The three tests that stood here registered an in-process consumer through
// RegisterIngress and asserted it received the event. They passed on every run —
// while production dropped every event, because the consumer runs in ANOTHER
// PROCESS and the function pointer they installed was one this process happened
// to hold. A test that only ever builds the co-resident case says nothing about
// the deployed one, which is the same trap that let a chat channel call
// agents.RunOnBehalf directly for as long as it did.
//
// What can be checked from here is the mapping, and that emitting never delays
// the webhook. Whether channels TOOK the event is channels' own test, and
// whether the hop works at all is the compose check.

func TestIngestEventCarriesTheVerifiedFields(t *testing.T) {
	in := Inbound{Provider: "slack", ExternalID: "T1", User: "u1", Channel: "C1", ThreadID: "th", Text: "hi", DedupeKey: "e1"}
	got := ingestIn("org1", in, "root")

	if got.Org != "org1" {
		t.Errorf("org = %q — the isolation root, resolved from the signed payload", got.Org)
	}
	if got.Provider != in.Provider || got.ExternalID != in.ExternalID || got.User != in.User {
		t.Errorf("identity fields = %+v, want them from %+v", got, in)
	}
	if got.Channel != in.Channel || got.ThreadID != in.ThreadID {
		t.Errorf("reply target = %q/%q, want %q/%q", got.Channel, got.ThreadID, in.Channel, in.ThreadID)
	}
	if got.Text != in.Text || got.DedupeKey != in.DedupeKey {
		t.Errorf("text/dedupe = %q/%q, want %q/%q", got.Text, got.DedupeKey, in.Text, in.DedupeKey)
	}
	if got.ReplyRoot != "root" {
		t.Errorf("reply root = %q, want the transport-verified one", got.ReplyRoot)
	}
}

func TestEmitIngressNeverDelaysTheWebhook(t *testing.T) {
	newApp(t, newKMS(t))
	// No plane peer here, so the dispatch fails — which is the point: the webhook
	// path must return regardless of whether the inbox is reachable.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !emitIngress(context.Background(), mounted, "org1", Inbound{Provider: "slack", DedupeKey: "e-nodelay"}, "") {
			t.Error("an unreachable inbox must not stop the event being taken")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emitIngress blocked the caller")
	}
	// Give the detached goroutine a beat to fail and unwind; the process staying
	// alive IS the assertion.
	time.Sleep(50 * time.Millisecond)
}

// A taken event RETURNS its pool slot, and it is the dispatching path that used
// to keep it: the slot was acquired in the handler and released by the goroutine
// the turn ran in, and the turn moved to channels, so nothing released it. The
// pool is global with a small per-org share, so a few tenants of ordinary
// traffic wedged every tenant on every transport until the process restarted.
//
// A cap-1 pool makes that deterministic: the second event can only be taken if
// the first gave its slot back.
func TestEmitIngressReleasesTheSlotItTook(t *testing.T) {
	newApp(t, newKMS(t))
	channelReady()
	saved := channelLim
	channelLim = newOrgLimiter(1, 1)
	t.Cleanup(func() { channelLim = saved })

	const org = "leakorg"
	for i, key := range []string{"e-1", "e-2", "e-3"} {
		if !emitIngress(context.Background(), mounted, org, Inbound{Provider: "slack", DedupeKey: key}, "") {
			t.Fatalf("event %d was not taken; the pool is holding slots nothing released", i+1)
		}
		waitForSlot(t, org)
	}
	// Nothing is in flight, so the pool is empty for every tenant.
	if !channelLim.acquire(org) {
		t.Fatal("the pool never came back; a dispatched event leaked its slot")
	}
	channelLim.release(org)
}

// waitForSlot blocks until the org's in-flight dispatch has unwound, so the next
// acquire measures the release rather than racing it.
func waitForSlot(t *testing.T, org string) {
	t.Helper()
	for range 400 {
		if channelLim.acquire(org) {
			channelLim.release(org)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the dispatch never released its slot")
}

// A send names the org it sends AS, and the boundary refuses one that does not.
// The org is what buys the credential — TokenFor refuses an org that names no
// store and telegram's chat bind can never match it — so a caller that dropped it
// reached a per-transport error on two transports and spent a shared app
// credential unchecked on the other two.
func TestChatSendNeedsTheOrgItSendsAs(t *testing.T) {
	newApp(t, newKMS(t))
	t.Setenv(discordBotTokenEnv, "test-token")
	rec := newDiscordMsgServer(t, http.StatusOK, `{"id":"m1"}`)

	if _, err := planeChatSend(context.Background(),
		&client.ChatSendIn{Provider: "discord", Room: "123", Text: "hi"}); err == nil ||
		!strings.Contains(err.Error(), "org") {
		t.Fatalf("err = %v, want a refusal naming the org", err)
	}
	if rec.count() != 0 {
		t.Fatal("an org-less send must be refused before any transport is spent")
	}

	// The same call, named, goes through — so the refusal is the missing org and
	// nothing else.
	if _, err := planeChatSend(context.Background(),
		&client.ChatSendIn{Org: "acme", Provider: "discord", Room: "123", Text: "hi"}); err != nil {
		t.Fatalf("named send: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("transport calls = %d, want the named send to reach it", rec.count())
	}
}
