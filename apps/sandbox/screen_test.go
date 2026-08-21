package sandbox

// What a screen has to get right, none of which needs a cluster.
//
// The transport is the whole risk here: RFB is arbitrary bytes over the one
// channel that also carries terminals, and the terminal's shape of that channel
// would corrupt it silently — a pty translates, and the corruption would look
// like a desktop that mostly works and tears occasionally. So the first two
// cases are about which shape is used and what it says when it fails, and they
// are stated where they cannot drift back.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"k8s.io/client-go/tools/remotecommand"
)

// spy answers a stream and remembers what it was asked to run. It refuses a
// terminal outright, because a screen asking for one is the bug this file
// exists to prevent rather than a variation to tolerate.
type spy struct {
	argv []string
	say  string // what the command writes to stderr
	err  error  // what it ends with
}

func (s *spy) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	s.argv = argv
	if s.say != "" {
		_, _ = io.WriteString(stderr, s.say)
	}
	return s.err
}

func (s *spy) tty(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout io.Writer, size remotecommand.TerminalSizeQueue) error {
	return errors.New("a screen must not run on a pseudo-terminal: a pty translates bytes and RFB is bytes")
}

func TestScreenIsRawBytesToTheLoopbackVNCPort(t *testing.T) {
	str := &spy{}
	s := service(t, str)
	ready(t, s)
	m := seed(t, s, "acme", "m-screen")

	if err := s.State.rt.screen(context.Background(), m, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("screen: %v", err)
	}
	// 127.0.0.1 and not the pod's address: the desktop image binds its VNC
	// server to loopback on purpose, and a screen that dialled the pod network
	// would be reaching for something nothing serves.
	want := []string{"socat", "-", "TCP:127.0.0.1:5900"}
	if len(str.argv) != len(want) {
		t.Fatalf("argv = %v, want %v", str.argv, want)
	}
	for i := range want {
		if str.argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", str.argv, want)
		}
	}
}

func TestScreenSaysWhyItFailed(t *testing.T) {
	// The one failure anybody meets: a pod whose screen is not running. socat
	// says so in one line and exits non-zero, and a close frame reading "command
	// terminated with exit code 1" tells the person nothing they can act on.
	str := &spy{say: "socat: Connection refused\n", err: errors.New("command terminated with exit code 1")}
	s := service(t, str)
	ready(t, s)
	m := seed(t, s, "acme", "m-refused")

	err := s.State.rt.screen(context.Background(), m, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("a screen that could not be reached reported success")
	}
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("error = %q, which does not carry what socat said", err)
	}
}

func TestScreenPageNeedsNoOriginButItsOwn(t *testing.T) {
	page := desktop()
	// Every marker substituted. An unreplaced one is a page that loads nothing
	// and reports nothing, which is the failure that looks like a black pane.
	if strings.Contains(page, "__RFB__") {
		t.Error("the page still carries its marker, so noVNC was never substituted in")
	}
	// The client is IN the page. A desktop that fetches its client from
	// somewhere else is a desktop that stops working when the somewhere else
	// does — and tabs frames this under a policy that forbids the fetch anyway.
	if !strings.Contains(page, "window.RFB") {
		t.Error("the page does not carry the RFB client")
	}
	for _, outside := range []string{"src=\"http", "src='http", "href=\"http", "@import", "cdn."} {
		if strings.Contains(page, outside) {
			t.Errorf("the page reaches for %q, so it is not self-contained", outside)
		}
	}
}

func TestTicketNamesTheDoorItWasMintedAt(t *testing.T) {
	// The terminal's URL used to be spelled into the mint, so a second door
	// would have handed callers the first door's address — a ticket that opens
	// the right sandbox and shows the wrong thing.
	app := mountHTTP(t)
	s := mounted.Load()
	if s == nil {
		t.Fatal("Mount did not publish the service")
	}
	ready(t, s)
	m := seed(t, s, "acme", "m-doors")

	for _, door := range []string{"terminal", "screen"} {
		code, body := req(t, app, "POST", "/v1/sandbox/"+m.ID+"/"+door+"/ticket", "acme", "")
		if code != 201 {
			t.Fatalf("%s ticket: %d %s", door, code, body)
		}
		want := "/v1/sandbox/" + m.ID + "/" + door + "?ticket="
		if !strings.Contains(string(body), want) {
			t.Errorf("%s ticket url does not name its own door: %s", door, body)
		}
	}
}
