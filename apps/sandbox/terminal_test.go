package sandbox

// The ticket is the ONLY thing standing between a WebSocket URL and a shell
// inside somebody's sandbox, so every property it claims is measured here rather
// than argued for in a comment. None of it needs a cluster: a ticket is decided
// before a pod is ever addressed, which is exactly why it can be tested at all.

import (
	"net/http"
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
	for range 256 {
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
	for range 100 {
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
	for range 8 {
		wg.Go(func() {
			if _, ok := ks.redeem(now, tok, "m_1"); ok {
				won <- struct{}{}
			}
		})
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

// The shell must not require anything of the image beyond /bin/sh. The sandbox
// classes are different images and the exec one is stock node today, so a
// command that assumed a tool would be a terminal that opens and immediately
// dies with a message nobody can read through a closed socket.
func TestShellRequiresOnlySh(t *testing.T) {
	argv := shell("")
	if len(argv) != 3 || argv[0] != "/bin/sh" || argv[1] != "-lc" {
		t.Fatalf("shell(\"\") = %q, want a plain /bin/sh -lc invocation", argv)
	}
	if !strings.Contains(argv[2], "exec sh -l") {
		t.Errorf("no fallback to sh: %q — every image has /bin/sh and not every "+
			"image has bash", argv[2])
	}
	if strings.Contains(argv[2], "hanzo") {
		t.Errorf("the shell names the hanzo CLI: %q — the CLI is a command the user "+
			"types, not a precondition for getting a prompt", argv[2])
	}
}

// THE CHAIN IS AN ORDER AND EVERY LINK IS OPTIONAL.
//
// zsh is what the admin image ships and what an operator expects to land in;
// bash is what the toolchain images have; sh is what everything has. Two
// properties, and the second is the one that is easy to lose: each step must be
// asked for and not required, so a class that carries no zsh gets bash and a
// stock node image still gets a prompt.
//
// It is checked by ORDER rather than by matching the whole string, because the
// string is a shell fragment and asserting it verbatim would make every future
// edit a test edit. What must not change is which shell wins when two are there.
func TestShellPrefersZshAndFallsAllTheWayDown(t *testing.T) {
	cmd := shell("")[2]
	z, b, s := strings.Index(cmd, "exec zsh"), strings.Index(cmd, "exec bash"), strings.Index(cmd, "exec sh")
	if z < 0 || b < 0 || s < 0 {
		t.Fatalf("%q does not name all three of zsh, bash and sh", cmd)
	}
	if !(z < b && b < s) {
		t.Errorf("%q does not prefer zsh, then bash, then sh — an operator's terminal "+
			"lands in whichever shell comes first", cmd)
	}
	// Each step SETTLES. `||` is what makes a missing shell cost the next one on
	// the list rather than the terminal; a chain joined by `&&` or `;` would open
	// a socket into an image that has no zsh and close it again.
	if strings.Count(cmd, "||") < 2 {
		t.Errorf("%q does not fall through: every shell here is a preference, and an "+
			"image that lacks one must still give a prompt", cmd)
	}
}

// A NAMED terminal is the same shell under tmux, and the name is what lets one
// sandbox hold many. Two properties have to hold together: the session is
// ATTACHED if it exists (`new -A`, or every reframe starts a fresh empty shell
// and the user's work is gone from view), and a missing tmux costs the caller its
// name rather than its terminal.
func TestNamedShellAttachesAndDegrades(t *testing.T) {
	argv := shell("pane-1")
	if len(argv) != 3 || argv[0] != "/bin/sh" {
		t.Fatalf("shell(\"pane-1\") = %q, want a /bin/sh invocation", argv)
	}
	cmd := argv[2]
	if !strings.Contains(cmd, "tmux new -A -s 'pane-1'") {
		t.Errorf("%q does not attach-or-create the named session; without -A every "+
			"reframe would open a fresh shell over the user's work", cmd)
	}
	if !strings.Contains(cmd, "command -v tmux") || !strings.Contains(cmd, plain) {
		t.Errorf("%q has no fallback — an image without tmux must still give a "+
			"prompt, because a missing multiplexer costs a session name and not a "+
			"terminal", cmd)
	}
}

// THE TERMINAL HAS TO SAY WHAT KIND OF TERMINAL IT IS.
//
// The exec subresource opens a pty and sets no environment on it, so TERM arrives
// unset and tmux refuses the screen it was given: "terminal does not support
// clear", then exit. Beside `exec`, that refusal is not a degradation — the shell
// is already gone, nothing is left to fall back to, and the socket closes. Every
// named terminal opened and immediately reported the connection closed, and no
// session was ever created to reattach to.
//
// So it is asserted on BOTH shapes and BEFORE the first command: a TERM exported
// after the shell has been replaced is a TERM nothing reads.
func TestShellNamesTheTerminal(t *testing.T) {
	for _, session := range []string{"", "pane-1"} {
		cmd := shell(session)[2]
		if !strings.Contains(cmd, "TERM=") {
			t.Errorf("shell(%q) = %q sets no TERM — Kubernetes sets none either, and a "+
				"pty of unknown type is one tmux will not draw on", session, cmd)
			continue
		}
		if !strings.Contains(cmd, "xterm") {
			t.Errorf("shell(%q) = %q does not name an xterm; every surface frames the "+
				"same xterm.js page, so anything else is a lie about the far end",
				session, cmd)
		}
		// Before the shell is replaced, or it is never read.
		if i, j := strings.Index(cmd, "TERM="), strings.Index(cmd, "exec"); i < 0 || j < 0 || i > j {
			t.Errorf("shell(%q) = %q exports TERM after the first exec", session, cmd)
		}
		// A caller that already said which terminal it is keeps its answer.
		if !strings.Contains(cmd, ":-") {
			t.Errorf("shell(%q) = %q overwrites TERM instead of defaulting it", session, cmd)
		}
	}
}

// The name reaches a COMMAND LINE, so what may be in it is an allowlist and not
// an escape. This is the injection gate and it is measured as one.
func TestSessionNameIsAnAllowlist(t *testing.T) {
	for _, ok := range []string{"a", "pane-1", "PANE_1", "0", strings.Repeat("x", 64)} {
		if !sessionOK(ok) {
			t.Errorf("sessionOK(%q) refused a legitimate name", ok)
		}
	}
	refused := []string{
		"", strings.Repeat("x", 65),
		"-rf",               // tmux would read it as a flag
		"a b", "a;rm -rf /", // a second command
		"a'b", "a\"b", "a$(id)", "a`id`", "a|b", "a&b", "a\nb",
		"../etc", "a/b", "ünïcode",
	}
	for _, bad := range refused {
		if sessionOK(bad) {
			t.Errorf("sessionOK(%q) accepted a name that reaches a command line", bad)
		}
	}
	// And nothing that gets through can leave its own quotes: the name is
	// allowlisted AND quoted, because one of the two being wrong later should not
	// be enough on its own.
	if got := shell("pane-1")[2]; !strings.Contains(got, "'pane-1'") {
		t.Errorf("the session name is not quoted in %q", got)
	}
}

// A terminal may not outlive the sandbox it is attached to. The reaper is the
// floor; this is the ceiling, and it is read off the row rather than from a
// second knob that could disagree with it.
func TestTerminalEndsWithTheLease(t *testing.T) {
	at := time.Now().Add(37 * time.Minute).Truncate(time.Second)
	if got := leaseEnd(Sandbox{ExpiresAt: at.Unix()}, absoluteDefault); !got.Equal(at) {
		t.Errorf("leaseEnd = %v, want the row's own expiry %v", got, at)
	}
	// A row with no expiry is a row written before the lease was, not permission
	// to hold a socket open forever.
	got := leaseEnd(Sandbox{}, absoluteDefault)
	if bound := time.Now().Add(absoluteDefault); got.After(bound.Add(time.Minute)) {
		t.Errorf("leaseEnd = %v for an expiry-less row, want no later than %v", got, bound)
	}
}

// A handler nothing can reach is the failure this package has already had once:
// Create, List, Get and Delete existed as exported functions with no routes
// registered, and nothing said so. The terminal adds three more doors, so the
// mounting is a fact with a test rather than a line somebody remembered to write.
//
// None of it needs a cluster. Every refusal here happens before a pod or a store
// is addressed, which is the same reason they are safe refusals in production.
func TestTerminalRoutesAreMountedAndGated(t *testing.T) {
	app := mountHTTP(t)

	// The ticket door is the ordinary org gate, and it is the SAME 403 the
	// siblings answer. A 404 here would mean the route is not registered at all,
	// which is the bug this test exists for. What happens WITH a principal needs
	// the org's store and so belongs to the live suite.
	if code, b := req(t, app, http.MethodPost, "/v1/sandbox/m_nope/terminal/ticket", "", ""); code != http.StatusForbidden {
		t.Fatalf("unauthenticated ticket: want 403, got %d %s", code, b)
	}

	// The socket door answers the TICKET and nothing else. It refuses BEFORE
	// upgrading — a socket that opens and then closes tells a browser nothing —
	// and it refuses identically whether the caller brings a principal or not,
	// because a principal is not what opens a terminal.
	for _, org := range []string{"", "hanzo"} {
		for _, q := range []string{"", "?ticket=", "?ticket=forged"} {
			code, b := req(t, app, http.MethodGet, "/v1/sandbox/m_nope/terminal/ws"+q, org, "")
			if code != http.StatusUnauthorized {
				t.Errorf("socket with org=%q %s: want 401, got %d %s", org, q, code, b)
			}
		}
	}

	// A session name that could reach a command line is refused at the door, ahead
	// of the ticket — so a caller cannot learn anything about a ticket by varying
	// the name, and a malformed name never gets as far as a shell.
	if code, b := req(t, app, http.MethodGet, "/v1/sandbox/m_nope/terminal/ws?arg=a;id&ticket=x", "", ""); code != http.StatusBadRequest {
		t.Errorf("socket with an illegal session name: want 400, got %d %s", code, b)
	}

	// The PAGE is served to anyone. It is inert markup — its only power is the
	// ticket in its own URL, and it does not redeem it — so gating it would only
	// mean a framing host could not load the thing that asks for the credential.
	code, b := req(t, app, http.MethodGet, "/v1/sandbox/m_nope/terminal", "", "")
	if code != http.StatusOK {
		t.Fatalf("terminal page: want 200, got %d %s", code, b)
	}
	// WITH the trailing slash too, which is the form a framing host builds when it
	// appends a query to a directory-shaped URL. The page derives its socket from
	// its own path, so both spellings have to reach it or one of them silently
	// dials the wrong address.
	for _, p := range []string{
		"/v1/sandbox/m_nope/terminal/",
		"/v1/sandbox/m_nope/terminal/?ticket=t&arg=pane-1",
	} {
		if c, _ := req(t, app, http.MethodGet, p, "", ""); c != http.StatusOK {
			t.Errorf("GET %s: want 200, got %d", p, c)
		}
	}
	page := string(b)
	for _, want := range []string{"<!doctype html>", "new WebSocket", "hanzo-term", "FitAddon"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not contain %q — it is not a working terminal", want)
		}
	}
	// SELF-CONTAINED. A terminal that fetches its emulator from somewhere else is
	// a terminal that stops working when the somewhere else does.
	if strings.Contains(page, "src=\"http") || strings.Contains(page, "href=\"http") {
		t.Error("the page loads something from another origin")
	}
	// And the emulator really is IN it, rather than the markers being left unfilled.
	for _, marker := range []string{"__CSS__", "__XTERM__", "__FIT__"} {
		if strings.Contains(page, marker) {
			t.Errorf("%s was never substituted — the page has no emulator", marker)
		}
	}
	if len(page) < 400_000 {
		t.Errorf("the page is %d bytes, far too small to carry xterm", len(page))
	}
	// Nothing inside a <script> may close it early, which is the one way inlining
	// a vendored file could become an injection.
	if n := strings.Count(page, "</script"); n != 3 {
		t.Errorf("found %d </script, want exactly the 3 the template opens — a "+
			"vendored file that closes a script tag would be markup, not code", n)
	}
}

// THE PAGE REPORTS BOTH OUTCOMES, NOT ONLY THE GOOD ONE.
//
// It announced `ready` on open and said NOTHING on any failure, so a framing
// host had one signal and one absence — and an absence has no cause written on
// it. Every way of failing (a refused socket, a gate, a CSP refusal, a page that
// never loaded) arrived as the same silence, the host waited out its deadline,
// and the only explanation that fits an unexplained silence is a stale
// credential. So it minted a fresh ticket for a socket that was failing for its
// own reasons, and did it again, and again.
//
// That silence is what made the sandbox terminal's 502 read as an expired
// ticket for as long as it did. The page knows what happened; this is it saying so.
func TestThePageReportsAFailureAndNotOnlyReadiness(t *testing.T) {
	page := document()

	// The success half, unchanged.
	if !strings.Contains(page, "ready: true") {
		t.Error("the page no longer announces readiness — every framed pane reads as dead")
	}
	// The half that did not exist.
	if !strings.Contains(page, "ready: false") {
		t.Error("the page never tells its host that it FAILED, so a failure is " +
			"indistinguishable from a slow start and gets answered with a new ticket")
	}
	// Carrying the reason, which is the whole difference between "it did not come
	// up" and something a person can act on.
	if !strings.Contains(page, "why: why") {
		t.Error("the failure carries no reason; the host can only guess at one")
	}
	// Both outcomes leave by the same door, so a host has one message to parse.
	if n := strings.Count(page, "source: 'hanzo-term'"); n != 3 {
		t.Errorf("found %d messages to the host, want 3 (ready, failed, retry) — "+
			"one channel, or a host has to learn a second", n)
	}
	// A spent ticket cannot be re-presented, so the page must not answer its own
	// Reconnect by reloading itself when there is a parent holding the identity
	// that can mint another.
	if !strings.Contains(page, "retry: true") {
		t.Error("the page's Reconnect reloads with a ticket the socket already " +
			"spent, which is refused for a reason unrelated to the first failure")
	}
}

// Who may FRAME the terminal is derived from the brand registry, so adding a
// brand admits its hosts and nothing else has to be edited. It is defence in
// depth against a clickjack — the ticket is the gate — but a policy that admits
// the whole web is not defence at all.
func TestOnlyOurOwnHostsMayFrameTheTerminal(t *testing.T) {
	p := framers()
	if !strings.HasPrefix(p, "frame-ancestors ") {
		t.Fatalf("policy = %q, want a frame-ancestors directive", p)
	}
	for _, want := range []string{"'self'", "https://*.hanzo.ai", "https://hanzo.ai"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy %q does not admit %s — tabs and the console are both "+
				"subdomains", p, want)
		}
	}
	// Every brand in the registry, not just ours: one binary serves them all.
	for _, want := range []string{"https://*.lux.network", "https://*.zoo.ngo", "https://*.pars.network"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy %q does not admit %s — the white-label estates frame "+
				"the same page", p, want)
		}
	}
	if strings.Contains(p, "*;") || strings.Contains(p, " * ") || strings.HasSuffix(p, " *") {
		t.Errorf("policy %q admits any origin", p)
	}
}
