package marketing

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// sentMessage records one delivery that reached the notify rail (past the gate).
type sentMessage struct {
	org, channel, to, subject, body string
}

// captureSends swaps the delivery hand-off for a recorder and returns the slice
// it fills plus a restore func. Every test that asserts "who got sent" uses this
// so no real provider is ever touched.
func captureSends(t *testing.T) (*[]sentMessage, func()) {
	t.Helper()
	var got []sentMessage
	prev := sendFn
	sendFn = func(_ context.Context, _ cloud.KMSClient, org, channel, _ string, to []string, subject, body string) (string, error) {
		for _, addr := range to {
			got = append(got, sentMessage{org, channel, addr, subject, body})
		}
		return "fake", nil
	}
	return &got, func() { sendFn = prev }
}

// testService builds a mounted-shaped service over a fresh store with a no-op
// logger and no KMS (unsubscribe links resolve to "" — the send path never needs
// a provider because captureSends replaces it).
func testService(t *testing.T) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{store: testStore(t)},
	}
}

// TestSendGateSuppression is the load-bearing gate test: a suppressed recipient
// is skipped (never reaches the rail) and a non-suppressed one is delivered.
func TestSendGateSuppression(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	// Opt "blocked@x.com" out of email for org hanzo.
	if err := s.State.store.Suppress(ctx, Suppression{Org: "hanzo", Channel: "email", Address: "blocked@x.com", CreatedAt: 1}); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	// A suppressed recipient: skipped, no error, never sent.
	sent, err := s.State.deliver(ctx, nil, "hanzo", "email", "blocked@x.com", "hi", "body")
	if err != nil || sent {
		t.Fatalf("suppressed deliver want (false,nil), got (%v,%v)", sent, err)
	}
	if len(*got) != 0 {
		t.Fatalf("suppressed recipient must NOT reach the rail, got %+v", *got)
	}

	// A clean recipient: delivered exactly once.
	sent, err = s.State.deliver(ctx, nil, "hanzo", "email", "ok@x.com", "hi", "body")
	if err != nil || !sent {
		t.Fatalf("clean deliver want (true,nil), got (%v,%v)", sent, err)
	}
	if len(*got) != 1 || (*got)[0].to != "ok@x.com" {
		t.Fatalf("clean recipient must reach the rail once, got %+v", *got)
	}
}

// TestSuppressionCaseInsensitive: opt-out matching ignores case + surrounding
// whitespace, so a re-cased address can't slip past the gate.
func TestSuppressionCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	if err := s.State.store.Suppress(ctx, Suppression{Org: "hanzo", Channel: "email", Address: "Case@X.com", CreatedAt: 1}); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	sent, err := s.State.deliver(ctx, nil, "hanzo", "email", "  case@x.COM ", "s", "b")
	if err != nil || sent {
		t.Fatalf("re-cased suppressed address must be skipped, got (%v,%v)", sent, err)
	}
	if len(*got) != 0 {
		t.Fatalf("re-cased address reached the rail: %+v", *got)
	}
}

// TestSuppressionScopedByOrgAndChannel: an opt-out in one (org,channel) never
// leaks to another org or another channel.
func TestSuppressionScopedByOrgAndChannel(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	got, restore := captureSends(t)
	defer restore()

	if err := s.State.store.Suppress(ctx, Suppression{Org: "acme", Channel: "email", Address: "u@x.com", CreatedAt: 1}); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	// Same address, DIFFERENT org → not suppressed.
	if sent, _ := s.State.deliver(ctx, nil, "hanzo", "email", "u@x.com", "s", "b"); !sent {
		t.Fatalf("suppression must not cross orgs")
	}
	// Same address + org, DIFFERENT channel → not suppressed.
	if sent, _ := s.State.deliver(ctx, nil, "acme", "sms", "u@x.com", "s", "b"); !sent {
		t.Fatalf("suppression must not cross channels")
	}
	// Same org + channel → suppressed.
	if sent, _ := s.State.deliver(ctx, nil, "acme", "email", "u@x.com", "s", "b"); sent {
		t.Fatalf("acme/email/u@x.com must be suppressed")
	}
	if len(*got) != 2 {
		t.Fatalf("exactly the two cross-scope sends should reach the rail, got %+v", *got)
	}
}

// TestUnsubscribeTokenRoundTrip: a minted one-click token verifies for its own
// tuple and is rejected when any field or the token is tampered.
func TestUnsubscribeTokenRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tok := unsubToken(key, "hanzo", "email", "u@x.com")
	if !unsubValid(key, "hanzo", "email", "u@x.com", tok) {
		t.Fatalf("valid token must verify")
	}
	if unsubValid(key, "acme", "email", "u@x.com", tok) {
		t.Fatalf("token must not verify for another org")
	}
	if unsubValid(key, "hanzo", "email", "other@x.com", tok) {
		t.Fatalf("token must not verify for another address")
	}
	if unsubValid(key, "hanzo", "email", "u@x.com", tok+"00") {
		t.Fatalf("tampered token must not verify")
	}
	if unsubValid([]byte("different-key-different-key-xxxx"), "hanzo", "email", "u@x.com", tok) {
		t.Fatalf("token must not verify under a different key")
	}
}
