package sandbox

// The ticket is the ONLY thing standing between a WebSocket URL and a shell
// inside somebody's sandbox, so every property it claims is measured here rather
// than argued for in a comment. None of it needs a cluster: a ticket is decided
// before a pod is ever addressed, which is exactly why it can be tested at all.

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"k8s.io/client-go/tools/remotecommand"
)

func TestTicketIsSpentExactlyOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

func TestTicketExpires(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

func TestTicketOpensOnlyTheSandboxItNames(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

func TestUnknownTicketIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ks := newTickets()
		now := time.Now()
		for _, tok := range []string{"", "not-a-ticket", strings.Repeat("A", 43)} {
			if _, ok := ks.redeem(now, tok, "m_1"); ok {
				t.Errorf("redeem accepted %q, which was never minted", tok)
			}
		}
	})
}

func TestTicketsAreUnguessableAndDistinct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

func TestExpiredTicketsDoNotAccumulate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

func TestConcurrentRedeemHasExactlyOneWinner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The control frame is the other half of the wire, and getting it wrong is not a
// cosmetic failure: a resize misread as input types garbage at the prompt, and
// input misread as a resize silently drops what somebody typed.
func TestResizeIsToldFromInput(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The window is what the exec stream reads sizes from, and the two things it must
// do are report the LATEST size and stop reporting when the session ends. A Next
// that blocks forever leaks the goroutine Kubernetes runs it in.
func TestWindowReportsTheLatestSizeAndThenEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The shell must not require anything of the image beyond /bin/sh. The sandbox
// classes are different images and the exec one is stock node today, so a
// command that assumed a tool would be a terminal that opens and immediately
// dies with a message nobody can read through a closed socket.
func TestShellRequiresOnlySh(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		cmd := shell("")[2]
		z, b, s := strings.Index(cmd, "exec zsh"), strings.Index(cmd, "exec bash"), strings.Index(cmd, "exec sh")
		if z < 0 || b < 0 || s < 0 {
			t.Fatalf("%q does not name all three of zsh, bash and sh", cmd)
		}
		if !(z < b && b < s) {
			t.Errorf("%q does not prefer zsh, then bash, then sh — an operator's terminal "+
				"lands in whichever shell comes first", cmd)
		}
		// Each step is ASKED FOR before it is exec'd. A failed exec ends a non-interactive
		// shell, so `exec zsh || exec bash` costs the terminal and never reaches bash on an
		// image without zsh; TestShellSettlesForWhatTheImageCarries runs the chain.
		for _, sh := range []string{"zsh", "bash"} {
			if !strings.Contains(cmd, "command -v "+sh+" ") {
				t.Errorf("%q does not ask for %s before it execs it: every shell here is a "+
					"preference, and an image that lacks one must still give a prompt", cmd, sh)
			}
		}
	})
}

// A NAMED terminal is the same shell under tmux, and the name is what lets one
// sandbox hold many. Two properties have to hold together: the session is
// ATTACHED if it exists (`new -A`, or every reframe starts a fresh empty shell
// and the user's work is gone from view), and a missing tmux costs the caller its
// name rather than its terminal.
func TestNamedShellAttachesAndDegrades(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		// Config errors put the first pane in view mode, which takes the keyboard.
		// The same command list closes it, and only when the pane is in it.
		if !strings.Contains(cmd, `-s 'pane-1' \; if -F '#{==:#{pane_mode},view-mode}' 'send-keys -X cancel'`) {
			t.Errorf("%q leaves a pane in view mode, where typing never reaches the shell", cmd)
		}
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The name reaches a COMMAND LINE, so what may be in it is an allowlist and not
// an escape. This is the injection gate and it is measured as one.
func TestSessionNameIsAnAllowlist(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// A terminal may not outlive the sandbox it is attached to. The reaper is the
// floor; this is the ceiling, and it is read off the row rather than from a
// second knob that could disagree with it.
func TestTerminalEndsWithTheLease(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// A handler nothing can reach is the failure this package has already had once:
// Create, List, Get and Delete existed as exported functions with no routes
// registered, and nothing said so. The terminal adds three more routes, so the
// mounting is a fact with a test rather than a line somebody remembered to write.
//
// None of it needs a cluster. Every refusal here happens before a pod or a store
// is addressed, which is the same reason they are safe refusals in production.
func TestTerminalRoutesAreMountedAndGated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountHTTP(t)

		// The ticket route is the ordinary org gate, and it is the SAME 403 the
		// siblings answer. A 404 here would mean the route is not registered at all,
		// which is the bug this test exists for. What happens WITH a principal needs
		// the org's store and so belongs to the live suite.
		if code, b := req(t, app, http.MethodPost, "/v1/sandbox/m_nope/terminal/ticket", "", ""); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated ticket: want 401, got %d %s", code, b)
		}

		// The socket route answers the TICKET and nothing else. It refuses BEFORE
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

		// A session name that could reach a command line is refused on arrival, ahead
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
	})
}

// THE TERMINAL IS SET IN ITS OWN FACE, AND THE EMULATOR STILL HONOURS IT.
//
// xterm writes the rows' font-family from a template, `font-family:
// ${…rawOptions.fontFamily}`, inside its minified source. A font-stack sweep
// (95d6c7e92) rewrote that template into `font-family: Zen, … sans-serif{this…`
// — CSS no browser accepts — so every terminal fell back to the browser default
// and drew its shell in Times. The template is held here, as the page's face is:
// Zen Mono, @hanzo/font's mono, named first in the one stack the page uses.
func TestTheTerminalSetsItsRowsInZenMono(t *testing.T) {
	if !strings.Contains(xtermJS, "font-family: ${this._optionsService.rawOptions.fontFamily}") {
		t.Error("the vendored xterm no longer writes the rows' font-family from its option — a sweep has rewritten it")
	}
	if strings.Contains(xtermJS, "sans-serif{this.") {
		t.Error("the vendored xterm carries a font stack spliced into a template")
	}
	if !strings.Contains(pageHTML, "font-family: 'Zen Mono';") ||
		!strings.Contains(pageHTML, "--mono: 'Zen Mono',") {
		t.Error("the terminal page does not embed and name Zen Mono first")
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		// Both outcomes leave by the same channel, so a host has one message to parse.
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
	})
}

// Who may FRAME the terminal and the screen is the surfaces the brand registry
// names, origin by origin. It is defence in depth against a clickjack — the
// ticket is the gate — and a policy with a wildcard over an apex whose
// subdomains are customer sites admits every customer.
func TestOnlyTheSurfacesWeNameMayFrameTheTerminal(t *testing.T) {
	p := framers()
	if !strings.HasPrefix(p, "frame-ancestors 'self' ") {
		t.Fatalf("policy = %q, want a frame-ancestors directive", p)
	}
	// The surfaces that embed a run's Terminal and Desktop: the builder, tabs, the
	// console under each of its names, each white-label estate's console — one
	// binary serves them all — and every host the Bots app's computer opens on.
	for _, want := range []string{"https://hanzo.ai", "https://hanzo.app", "https://hanzo.cloud",
		"https://hanzo.bot", "https://bot.hanzo.ai", "https://hanzo.team",
		"https://hanzo.build", "https://www.hanzo.build", "https://build.hanzo.ai", "https://tabs.hanzo.ai",
		"https://console.hanzo.ai", "https://platform.hanzo.ai",
		"https://console.lux.cloud", "https://console.zoo.cloud",
		"tauri://localhost", "http://tauri.localhost"} {
		if !strings.Contains(p+" ", " "+want+" ") {
			t.Errorf("policy %q does not admit %s, which frames a sandbox's pages", p, want)
		}
	}
	// No wildcard, anywhere: *.hanzo.app is every customer's site — and no
	// customer's site by name either.
	if strings.Contains(p, "*") || strings.Contains(p, ".hanzo.app") {
		t.Errorf("policy %q admits a wildcard — every host under that apex, customers' included", p)
	}
	for _, src := range strings.Fields(strings.TrimPrefix(p, "frame-ancestors ")) {
		if src == "'self'" || src == "tauri://localhost" || src == "http://tauri.localhost" {
			continue
		}
		u, err := url.Parse(src)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			t.Errorf("framer %q is not an https origin", src)
		}
	}
}

// TestShellInitSmoke boots a real sandbox shell via shell("") against a home
// directory populated with the broken dot-zsh layer seen in cloud#536:
//   - .zshrc line 31 sourcing "$HOME/.local/bin/env" when absent
//   - .zshenv / .zshrc exporting TMPDIR=/home/z/.cache/{tmp,go-tmp} and running mkdir -p "$TMPDIR"
//   - status/prompt bullet glyph `•` entered at the shell prompt
//
// It verifies both writable $HOME and read-only $HOME (readOnlyRootFilesystem).
func TestShellInitSmoke(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if _, err := exec.LookPath("zsh"); err != nil {
			t.Fatalf("this boots the zsh the admin sandbox image carries, and this host has none: %v", err)
		}
		const brokenZshenv = "setopt noglobalrcs\nexport TMPDIR=/home/z/.cache/tmp\n"
		const brokenZshrc = "" +
			"# lines 1-30\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n" +
			". \"$HOME/.local/bin/env\"\n" +
			"export TMPDIR=/home/z/.cache/go-tmp\n" +
			"mkdir -p \"$TMPDIR\"\n"

		for _, tc := range []struct {
			name     string
			readOnly bool
		}{
			{name: "writable_home", readOnly: false},
			{name: "readonly_home", readOnly: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				homeDir := t.TempDir()
				workDir := t.TempDir()
				if err := os.WriteFile(filepath.Join(homeDir, ".zshenv"), []byte(brokenZshenv), 0o644); err != nil {
					t.Fatalf("write .zshenv: %v", err)
				}
				if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte(brokenZshrc), 0o644); err != nil {
					t.Fatalf("write .zshrc: %v", err)
				}
				if tc.readOnly {
					if err := os.Chmod(homeDir, 0o555); err != nil {
						t.Fatalf("chmod homeDir: %v", err)
					}
					t.Cleanup(func() { _ = os.Chmod(homeDir, 0o755) })
				}

				argv := shell("")
				// Force interactive mode without ZLE (+Z -il) and without 2>/dev/null on
				// the final exec so zsh reads .zshenv and .zshrc over a pipe and any
				// startup stderr is captured by the test.
				script := strings.ReplaceAll(argv[2], "exec zsh -l 2>/dev/null", "exec zsh +Z -il")
				cmd := exec.Command(argv[0], argv[1], script)
				cmd.Dir = workDir
				cmd.Env = []string{
					"HOME=" + homeDir,
					"TMPDIR=" + workDir,
					"PATH=" + os.Getenv("PATH"),
					"TERM=xterm-256color",
				}
				cmd.Stdin = strings.NewReader("•\n• status glyph\necho SMOKE_OK:$?:$TMPDIR\nexit\n")
				var out bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &out
				if err := cmd.Run(); err != nil {
					t.Fatalf("shell smoke failed: %v\noutput:\n%s", err, out.String())
				}

				got := out.String()
				for _, forbidden := range []string{
					"no such file or directory",
					"Permission denied",
					"command not found: •",
				} {
					if strings.Contains(got, forbidden) {
						t.Errorf("shell startup produced forbidden error %q in output:\n%s", forbidden, got)
					}
				}
				if !strings.Contains(got, "SMOKE_OK:0:") {
					t.Errorf("expected SMOKE_OK:0:<tmpdir>, got output:\n%s", got)
				}
				if strings.Contains(got, "SMOKE_OK:0:/home/z") {
					t.Errorf("TMPDIR still points to /home/z in output:\n%s", got)
				}
				if !tc.readOnly {
					if _, err := os.Stat(filepath.Join(homeDir, ".local", "bin", "env")); err != nil {
						t.Errorf("expected %s/.local/bin/env to exist: %v", homeDir, err)
					}
				}
			})
		}
	})
}

// A SANDBOX IMAGE SETTLES FOR THE SHELL IT CARRIES: zsh, else bash, else sh. A
// failed exec ends a non-interactive shell, so the chain once written
// `exec zsh || exec bash` never reached bash, and an image without zsh opened a
// terminal that closed at once with status 127.
//
// MUTATION: write plain as `exec zsh -l || exec bash -l || exec sh -l` and both fail.
func TestShellSettlesForWhatTheImageCarries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name  string
			carry []string
			want  string
		}{
			{name: "bash", carry: []string{"bash"}, want: "SHELL:bash\n"},
			{name: "sh", want: "SHELL:\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// An image is the tools the shell chain and rc reach, and the shells it carries.
				bin := t.TempDir()
				for _, tool := range append([]string{"mkdir", "sh"}, tc.carry...) {
					path, err := exec.LookPath(tool)
					if err != nil {
						t.Fatalf("this host has no %s to put in the image: %v", tool, err)
					}
					if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
						t.Fatal(err)
					}
				}
				argv := shell("")
				cmd := exec.Command(argv[0], argv[1], argv[2])
				cmd.Dir = t.TempDir()
				cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + bin, "TERM=xterm-256color"}
				cmd.Stdin = strings.NewReader("echo SHELL:${BASH_VERSION:+bash}\nexit\n")
				// A login shell on a bare image complains of the profile scripts' tools on
				// stderr; the terminal's word is stdout.
				var out, errs bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &errs
				if err := cmd.Run(); err != nil {
					t.Fatalf("the terminal did not open: %v\nstderr:\n%s", err, errs.String())
				}
				if out.String() != tc.want {
					t.Fatalf("the terminal said %q, want %q\nstderr:\n%s", out.String(), tc.want, errs.String())
				}
			})
		}
	})
}
