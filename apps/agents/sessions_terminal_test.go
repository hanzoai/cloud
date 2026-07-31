package agents

import (
	"context"
	"testing"
)

// A published terminal URL survives the round trip. It is execution context like
// host and cwd, so it stores and reads back the same way — the point of the field
// is that a console can find the terminal again later, not just at register time.
func TestTerminalRoundTripsThroughTheStore(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()

	x := mkSession("hanzo", "sess_t", "", "sess_t")
	x.Terminal = "https://abc.share.hanzo.ai"
	if err := s.CreateSession(ctx, x); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.GetSession(ctx, "hanzo", "sess_t")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Terminal != x.Terminal {
		t.Fatalf("terminal = %q, want %q", got.Terminal, x.Terminal)
	}
}

// The console FRAMES this value, so a scheme it would not open must be refused at
// the edge rather than rendered on a signed-in page.
func TestSessionTerminalRefusesNonHTTPS(t *testing.T) {
	for _, bad := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"http://plain.example.com",
		"//protocol-relative.example.com",
	} {
		if _, err := sessionTerminal(bad); err == nil {
			t.Errorf("sessionTerminal(%q) was accepted; want refused", bad)
		}
	}
}

// Empty is not an error: a session that publishes no terminal is still a session,
// it simply cannot be watched.
func TestSessionTerminalAllowsEmpty(t *testing.T) {
	got, err := sessionTerminal("  ")
	if err != nil {
		t.Fatalf("sessionTerminal(empty): %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSessionTerminalAcceptsHTTPS(t *testing.T) {
	got, err := sessionTerminal(" https://abc.share.hanzo.ai ")
	if err != nil {
		t.Fatalf("sessionTerminal(https): %v", err)
	}
	if got != "https://abc.share.hanzo.ai" {
		t.Fatalf("got %q, want the trimmed url", got)
	}
}

// A URL long enough to be a payload rather than an address is refused.
func TestSessionTerminalIsBounded(t *testing.T) {
	long := "https://" + string(make([]byte, maxTerminal))
	if _, err := sessionTerminal(long); err == nil {
		t.Fatal("an over-long terminal url was accepted")
	}
}
