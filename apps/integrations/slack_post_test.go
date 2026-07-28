package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPostSlackBlocksIncludesBlocks proves the shared exported poster sends the
// Block Kit `blocks` array (and the text fallback) with the bot bearer, through the
// ONE chat.postMessage path. This is the seam the git-lifecycle notifier and the
// automations connector both post through.
func TestPostSlackBlocksIncludesBlocks(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.2"}`))
	}))
	defer ts.Close()
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })

	blocks := []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "hi"}}}
	if err := PostSlackBlocks(context.Background(), "xoxb-secret", "#deploys", "fallback text", blocks); err != nil {
		t.Fatalf("PostSlackBlocks: %v", err)
	}
	if gotAuth != "Bearer xoxb-secret" {
		t.Fatalf("bearer not set correctly: %q", gotAuth)
	}
	if gotBody["channel"] != "#deploys" || gotBody["text"] != "fallback text" {
		t.Fatalf("channel/text wrong: %+v", gotBody)
	}
	if _, ok := gotBody["blocks"]; !ok {
		t.Fatalf("blocks array not sent: %+v", gotBody)
	}
}

// TestPostSlackBlocksErrorEnvelope proves an ok:false response surfaces as an error
// (fail-closed) and never leaks the token.
func TestPostSlackBlocksErrorEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer ts.Close()
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })

	err := PostSlackBlocks(context.Background(), "xoxb-secret", "#nope", "t", nil)
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("want channel_not_found error, got %v", err)
	}
	if strings.Contains(err.Error(), "xoxb-secret") {
		t.Fatalf("token leaked into error: %v", err)
	}
}

// TestNotifySlackFailsClosedUnmounted proves the high-level door fails closed when
// the integrations subsystem is not mounted (no org can be resolved to a token).
func TestNotifySlackFailsClosedUnmounted(t *testing.T) {
	if mounted != nil {
		t.Skip("integrations mounted in this run; unmounted-path test not applicable")
	}
	if err := NotifySlack(context.Background(), "acme", "#c", "t", nil); err == nil {
		t.Fatal("NotifySlack must fail closed when integrations is not mounted")
	}
}
