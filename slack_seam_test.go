package cloud

import (
	"context"
	"errors"
	"testing"
)

// TestSlackSendSeam proves the cross-subsystem Slack egress: a nil seam errors
// cleanly (never panics), and a registered sender is reached with its args
// intact. This is the seam that replaced o11y reaching into integrations'
// isolated `mounted` package global — the "integrations: not mounted" failure.
func TestSlackSendSeam(t *testing.T) {
	slackSender = nil // isolate from any prior registration
	if err := SlackSend(context.Background(), "acme", "#ops", "", "hi"); err == nil {
		t.Fatal("nil seam must error, not panic or silently succeed")
	}

	var got struct{ org, channel, thread, text string }
	SetSlackSender(func(_ context.Context, org, channel, threadTS, text string) error {
		got.org, got.channel, got.thread, got.text = org, channel, threadTS, text
		return nil
	})
	t.Cleanup(func() { slackSender = nil })
	if err := SlackSend(context.Background(), "acme", "#ops", "t1", "hello"); err != nil {
		t.Fatalf("registered sender: %v", err)
	}
	if got.org != "acme" || got.channel != "#ops" || got.thread != "t1" || got.text != "hello" {
		t.Fatalf("args not passed through: %+v", got)
	}

	SetSlackSender(func(context.Context, string, string, string, string) error {
		return errors.New("boom")
	})
	if err := SlackSend(context.Background(), "a", "c", "", "x"); err == nil || err.Error() != "boom" {
		t.Fatalf("sender error must propagate verbatim, got %v", err)
	}
}
