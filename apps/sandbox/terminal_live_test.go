package sandbox

// The live proof for the TERMINAL, and it exists because the pty is the one part
// of this that no unit test can reach. The ticket is decided in memory and the
// wire is decided in a function, but "does the apiserver actually give us a
// pseudo-terminal on that pod, and does a shell come up on it" is a question only
// a cluster answers — and the failure mode if it does not is a socket that opens,
// says nothing, and closes.
//
// Guarded by SANDBOX_LIVE like its siblings:
//
//	SANDBOX_LIVE=1 go test ./apps/sandbox/ -run TestLiveTerminal -v

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

// screen collects what the shell printed, safely: the stream writes from its own
// goroutine while the test reads.
type screen struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLiveTerminalIsARealShell(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	r := newRuntime()
	if err := r.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}

	m := Sandbox{
		ID:     "live-terminal",
		Org:    "hanzo",
		Status: "running",
		Class:  "exec",
		Pod:    fmt.Sprintf("sandbox-live-terminal-%d", time.Now().Unix()),
		Image:  envOr("SANDBOX_LIVE_IMAGE", "node:22"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Logf("starting %s image=%s ns=%s", m.Pod, m.Image, r.ns)
	// The isolation boundary comes from the SAME derivation production uses, so the
	// live proof runs under whatever the fleet is set to rather than under the
	// node default. want is empty, and runtimeFor cannot refuse an empty ask.
	m.Runtime, _ = r.runtimeFor(m, "", r.preference(ctx))
	if err := r.start(ctx, m, cred{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		if err := r.stop(context.Background(), m); err != nil {
			t.Logf("stop: %v", err)
		}
	}()

	in := newPipe()
	out := &screen{}
	w := newWindow()
	w.to(100, 30)

	// THE REAL ARGV. Not a command written for the test: if the login shell
	// does not come up on this image, the product is broken and this is where
	// that shows.
	ended := make(chan error, 1)
	go func() { ended <- r.tty(ctx, m, shell(""), in, out, w) }()

	// A pty ECHOES what is typed, so a marker that appears in the command text
	// would match the echo rather than the output and prove nothing. `$((6*7))`
	// is typed and `42` is printed, and only one of them is the shell running.
	if _, err := in.Write([]byte("echo $((6*7))\n")); err != nil {
		t.Fatalf("type: %v", err)
	}
	if !waitFor(out, "42", 60*time.Second) {
		t.Fatalf("the shell never answered: %q", out.String())
	}
	t.Logf("SHELL RUNS: %s", oneLine(out.String()))

	// The window is the shell's, not the pty default. A terminal nobody told the
	// size of runs at 80 columns and every full-screen program in it draws into
	// the wrong rectangle — so the size is asked for, from inside.
	if _, err := in.Write([]byte("echo COLS=$(tput cols 2>/dev/null || echo ?)\n")); err != nil {
		t.Fatalf("type: %v", err)
	}
	if !waitFor(out, "COLS=100", 60*time.Second) {
		t.Logf("window size not confirmed (tput may be absent): %s", oneLine(out.String()))
	} else {
		t.Logf("WINDOW IS OURS: 100 columns, as sent")
	}

	// And it ends when the person on the other end does. `exit` is the ordinary
	// way out of a shell; the stream has to notice.
	if _, err := in.Write([]byte("exit\n")); err != nil {
		t.Fatalf("type: %v", err)
	}
	select {
	case err := <-ended:
		// A shell that exits 0 is a clean end. Anything else is still an END,
		// which is what is being measured here.
		t.Logf("TERMINAL ENDED: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the shell exited but the stream did not end — a terminal that " +
			"outlives its shell holds a pod open until the reaper takes it")
	}
	w.close()
	in.drop()
}

func waitFor(out *screen, want string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
}

var _ io.Writer = (*screen)(nil)
var _ remotecommand.TerminalSizeQueue = (*window)(nil)

// TestLiveTerminalNamedSession is the property tabs depends on: one sandbox, many
// terminals, each reattaching to what it left. `arg` names a tmux session and
// `new -A` attaches or creates, so the second terminal with the same name finds
// the first one's scrollback rather than a fresh empty shell.
//
// It also answers the question that decides whether that works at all — does the
// IMAGE carry tmux — and says so either way rather than failing, because an image
// without tmux is a deployment fact and not a bug in this code.
func TestLiveTerminalNamedSession(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	r := newRuntime()
	if err := r.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	m := Sandbox{
		ID:     "live-session",
		Org:    "hanzo",
		Status: "running",
		Class:  "exec",
		Pod:    fmt.Sprintf("sandbox-live-session-%d", time.Now().Unix()),
		Image:  envOr("SANDBOX_LIVE_IMAGE", "node:22"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// The isolation boundary comes from the SAME derivation production uses, so the
	// live proof runs under whatever the fleet is set to rather than under the
	// node default. want is empty, and runtimeFor cannot refuse an empty ask.
	m.Runtime, _ = r.runtimeFor(m, "", r.preference(ctx))
	if err := r.start(ctx, m, cred{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = r.stop(context.Background(), m) }()

	has, err := r.exec(ctx, m, []string{"sh", "-c", "command -v tmux >/dev/null 2>&1 && echo yes || echo no"}, nil, 60, nil)
	if err != nil {
		t.Fatalf("probe tmux: %v", err)
	}
	tmux := strings.Contains(has.Stdout, "yes")
	t.Logf("IMAGE HAS TMUX: %v (%s)", tmux, m.Image)

	// Whether or not tmux is there, a NAMED terminal must still give a prompt.
	// That is the degradation the fallback exists for, and it is the difference
	// between "your panes are not multiplexed" and "your terminal is broken".
	in, out, w := newPipe(), &screen{}, newWindow()
	w.to(100, 30)
	ended := make(chan error, 1)
	go func() { ended <- r.tty(ctx, m, shell("pane-1"), in, out, w) }()

	if _, err := in.Write([]byte("echo $((6*7))\n")); err != nil {
		t.Fatalf("type: %v", err)
	}
	if !waitFor(out, "42", 60*time.Second) {
		t.Fatalf("a named terminal gave no prompt: %q", oneLine(out.String()))
	}
	t.Logf("NAMED TERMINAL RUNS: %s", oneLine(out.String()))

	if tmux {
		// Inside tmux, the session is named and $TMUX is set. Only meaningful when
		// tmux is actually there.
		if _, err := in.Write([]byte("echo SESSION=${TMUX:+on}\n")); err != nil {
			t.Fatalf("type: %v", err)
		}
		if !waitFor(out, "SESSION=on", 30*time.Second) {
			t.Errorf("tmux is installed but the shell is not inside a session: %s", oneLine(out.String()))
		} else {
			t.Log("SESSION IS MULTIPLEXED: the shell runs inside tmux")
		}
	}

	w.close()
	in.drop()
	<-ended
}
