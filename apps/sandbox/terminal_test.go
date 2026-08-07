package sandbox

// The ticket is the ONLY thing standing between a WebSocket URL and a shell
// inside somebody's sandbox, so every property it claims is measured here rather
// than argued for in a comment. None of it needs a cluster: a ticket is decided
// before a pod is ever addressed, which is exactly why it can be tested at all.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

func TestTicketIsSpentExactlyOnce(t *testing.T) {
	ks := newTickets()
	now := time.Now()

	tok, err := ks.mint(now, "acme", "m_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	org, ok := ks.redeem(now, tok, "m_1")
	if !ok {
		t.Fatal("a fresh ticket was refused")
	}
	if org != "acme" {
		t.Errorf("redeem gave org %q, want the org it was minted for", org)
	}
	if _, ok := ks.redeem(now, tok, "m_1"); ok {
		t.Fatal("a ticket was accepted TWICE — single-use is the whole point: a " +
			"credential in a URL ends up in logs and history, and one that still works " +
			"when it is read there is a shell somebody else can open")
	}
}

func TestTicketExpires(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	tok, err := ks.mint(now, "acme", "m_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// One instant before the window closes it still works…
	if _, ok := ks.redeem(now.Add(ticketTTL-time.Millisecond), tok, "m_1"); !ok {
		t.Fatal("a ticket inside its window was refused")
	}
	tok, _ = ks.mint(now, "acme", "m_1")
	// …and at the boundary it does not. The edge is checked, not a comfortable
	// distance past it, because an off-by-one in a credential's lifetime is the
	// kind of bug that only shows up as a flake.
	if _, ok := ks.redeem(now.Add(ticketTTL), tok, "m_1"); ok {
		t.Fatal("an expired ticket was accepted")
	}
}

func TestTicketOpensOnlyTheSandboxItNames(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	tok, err := ks.mint(now, "acme", "m_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, ok := ks.redeem(now, tok, "m_2"); ok {
		t.Fatal("a ticket minted for one sandbox opened another — the binding is what " +
			"stops a caller with a legitimate ticket from walking the id space")
	}
	// And the failed attempt SPENT it. A ticket that survives being presented
	// against the wrong sandbox can simply be retried against the right one,
	// which is single-use in name only.
	if _, ok := ks.redeem(now, tok, "m_1"); ok {
		t.Fatal("a ticket survived being presented — it must be spent on presentation, " +
			"not on success")
	}
}

func TestUnknownTicketIsRefused(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	for _, tok := range []string{"", "not-a-ticket", strings.Repeat("A", 43)} {
		if _, ok := ks.redeem(now, tok, "m_1"); ok {
			t.Errorf("redeem accepted %q, which was never minted", tok)
		}
	}
}

func TestTicketsAreUnguessableAndDistinct(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		tok, err := ks.mint(now, "acme", "m_1")
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if seen[tok] {
			t.Fatal("two mints produced the same ticket")
		}
		seen[tok] = true
		// 32 random bytes, unpadded base64url: anything shorter is a token
		// somebody could get through by trying.
		if len(tok) != 43 {
			t.Fatalf("ticket is %d characters, want the 43 that 32 random bytes make", len(tok))
		}
	}
}

func TestExpiredTicketsDoNotAccumulate(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if _, err := ks.mint(now, "acme", "m_1"); err != nil {
			t.Fatalf("mint: %v", err)
		}
	}
	// One mint past the window, and the hundred that expired are gone. Nothing
	// else sweeps: a ticket nobody redeems is a ticket only a later mint can
	// clear, so if this leaks it leaks for the life of the process.
	if _, err := ks.mint(now.Add(2*ticketTTL), "acme", "m_1"); err != nil {
		t.Fatalf("mint: %v", err)
	}
	ks.mu.Lock()
	n := len(ks.live)
	ks.mu.Unlock()
	if n != 1 {
		t.Errorf("%d tickets held after the window passed, want 1", n)
	}
}

func TestConcurrentRedeemHasExactlyOneWinner(t *testing.T) {
	ks := newTickets()
	now := time.Now()
	tok, err := ks.mint(now, "acme", "m_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	var wg sync.WaitGroup
	won := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := ks.redeem(now, tok, "m_1"); ok {
				won <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(won)
	if n := len(won); n != 1 {
		t.Fatalf("%d of 8 racing redemptions succeeded, want exactly 1", n)
	}
}

// The control frame is the other half of the wire, and getting it wrong is not a
// cosmetic failure: a resize misread as input types garbage at the prompt, and
// input misread as a resize silently drops what somebody typed.
func TestResizeIsToldFromInput(t *testing.T) {
	control := []struct {
		frame      string
		cols, rows uint16
	}{
		{`{"resize":{"cols":80,"rows":24}}`, 80, 24},
		{` {"resize":{"cols":200,"rows":50}}`, 0, 0}, // leading space: not the frame
		{`{"resize":{"rows":24,"cols":80}}`, 80, 24},
	}
	for _, tc := range control {
		cols, rows, ok := resize(0x1 /* text */, []byte(tc.frame))
		if tc.cols == 0 {
			if ok {
				t.Errorf("%q was read as a resize", tc.frame)
			}
			continue
		}
		if !ok || cols != tc.cols || rows != tc.rows {
			t.Errorf("resize(%q) = (%d,%d,%v), want (%d,%d,true)",
				tc.frame, cols, rows, ok, tc.cols, tc.rows)
		}
	}

	typed := []string{
		"ls -la\n", "", "{", "{}", `{"resize":null}`,
		`{"resize":{"cols":0,"rows":24}}`, // a zero column count is not a window
		`{"other":{"cols":80,"rows":24}}`,
		"echo '{\"resize\":\"whatever\"}'\n",
	}
	for _, in := range typed {
		if _, _, ok := resize(0x1, []byte(in)); ok {
			t.Errorf("%q was swallowed as a resize instead of reaching the shell", in)
		}
	}

	// A BINARY frame is stdin whatever it contains. Nothing a program pipes in
	// can be mistaken for a control frame, which is the reason the two frame
	// types carry two meanings rather than one type carrying both.
	if _, _, ok := resize(0x2 /* binary */, []byte(`{"resize":{"cols":80,"rows":24}}`)); ok {
		t.Error("a binary frame was read as a resize")
	}
}

// The window is what the exec stream reads sizes from, and the two things it must
// do are report the LATEST size and stop reporting when the session ends. A Next
// that blocks forever leaks the goroutine Kubernetes runs it in.
func TestWindowReportsTheLatestSizeAndThenEnds(t *testing.T) {
	w := newWindow()
	w.to(80, 24)
	w.to(120, 40) // a drag: the size in between is not one anyone needs to see
	if got := w.Next(); got == nil || got.Width != 120 || got.Height != 40 {
		t.Fatalf("Next() = %+v, want the latest size (120x40)", got)
	}

	ended := make(chan *remotecommand.TerminalSize, 1)
	go func() { ended <- w.Next() }()
	w.close()
	select {
	case got := <-ended:
		if got != nil {
			t.Fatalf("Next() answered %+v after close, want nil", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next() never returned after close — the goroutine the exec stream " +
			"runs it in would be leaked for the life of the process")
	}
	// Closing twice is what happens when the read pump and the session end at
	// once, and a second close of a channel is a panic.
	w.close()
}

// The login shell must not require anything of the image beyond a shell. The
// three sandbox classes are three different images and the exec one is stock
// node today, so a command that assumed a tool would be a terminal that opens
// and immediately dies with a message nobody can read through a closed socket.
func TestLoginShellRequiresOnlySh(t *testing.T) {
	if len(login) != 3 || login[0] != "/bin/sh" || login[1] != "-lc" {
		t.Fatalf("login = %q, want a plain /bin/sh -lc invocation", login)
	}
	if !strings.Contains(login[2], "exec sh -l") {
		t.Errorf("login has no fallback to sh: %q — every image has /bin/sh and not "+
			"every image has bash", login[2])
	}
	if strings.Contains(login[2], "hanzo") {
		t.Errorf("login names the hanzo CLI: %q — the CLI is a command the user types, "+
			"not a precondition for getting a prompt", login[2])
	}
}

// A terminal may not outlive the sandbox it is attached to. The reaper is the
// floor; this is the ceiling, and it is read off the row rather than from a
// second knob that could disagree with it.
func TestTerminalEndsWithTheLease(t *testing.T) {
	at := time.Now().Add(37 * time.Minute).Truncate(time.Second)
	if got := leaseEnd(Sandbox{ExpiresAt: at.Unix()}); !got.Equal(at) {
		t.Errorf("leaseEnd = %v, want the row's own expiry %v", got, at)
	}
	// A row with no expiry is a row written before the lease was, not permission
	// to hold a socket open forever.
	got := leaseEnd(Sandbox{})
	if bound := time.Now().Add(maxTTL * time.Second); got.After(bound.Add(time.Minute)) {
		t.Errorf("leaseEnd = %v for an expiry-less row, want no later than %v", got, bound)
	}
}
