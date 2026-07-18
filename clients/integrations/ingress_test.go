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
)

// ingress_test.go proves the two seam pieces channels rides: the SendDiscord
// door (the one new HTTP verb — httptest via the package's own repoint
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
		t.Fatal("door errors carry status/shape only — never the token")
	}
}

func TestEmitIngressDelivers(t *testing.T) {
	t.Cleanup(func() { ingressFn = nil })
	got := make(chan IngressEvent, 1)
	deadline := make(chan bool, 1)
	RegisterIngress(func(ctx context.Context, ev IngressEvent) {
		_, ok := ctx.Deadline()
		deadline <- ok
		got <- ev
	})

	in := Inbound{Provider: "slack", ExternalID: "T1", User: "u1", Channel: "C1", ThreadID: "th", Text: "hi", DedupeKey: "e1"}
	emitIngress("org1", in, "root")

	select {
	case ev := <-got:
		if ev.Org != "org1" || ev.ReplyRoot != "root" || ev.In != in {
			t.Fatalf("event = %+v, want the emitted fields intact", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ingress event not delivered")
	}
	if ok := <-deadline; !ok {
		t.Fatal("consumer context must carry the bounded deadline")
	}
}

func TestEmitIngressRecoversPanic(t *testing.T) {
	t.Cleanup(func() { ingressFn = nil })
	entered := make(chan struct{})
	RegisterIngress(func(context.Context, IngressEvent) {
		close(entered)
		panic("boom")
	})

	emitIngress("org1", Inbound{Provider: "slack"}, "")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer never ran")
	}
	// The deferred recover in emitIngress owns the panic; give the goroutine a
	// beat to unwind — the process staying alive IS the assertion.
	time.Sleep(50 * time.Millisecond)
}

func TestEmitIngressUnregistered(t *testing.T) {
	ingressFn = nil
	// No consumer registered (fresh process state): emit must be a silent no-op.
	emitIngress("org1", Inbound{Provider: "slack"}, "")
}
