package platform

// planewire_probe_test.go — what is actually on the wire.
//
// Every other test here asserts a VALUE that came back. That is necessary and it
// is not sufficient: a client that quietly fell back to HTTP, or to an in-process
// shortcut, would satisfy all of them. This one reads the bytes.
//
// It puts a recording relay at the socket the caller dials and forwards to the
// one the peer bound, so the frames it captures are the real call's — not a
// replay, not an encoder invoked on the side.
//
// Run with -v to print the capture.

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// relay copies both directions between the caller's socket and the peer's,
// recording every byte.
//
// The transcripts are guarded because the recording happens on the pump's
// goroutines while the test reads them — an unsynchronized capture would be a
// race in the very harness that exists to prove something about the wire.
type relay struct {
	mu      sync.Mutex
	out, in bytes.Buffer
}

// write appends to one transcript under the lock.
func (r *relay) write(b *bytes.Buffer, p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return b.Write(p)
}

// transcripts returns both captures, taken together so they are one consistent
// view of the exchange.
func (r *relay) transcripts() (out, in string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String(), r.in.String()
}

// await is transcripts once BOTH directions have been recorded.
//
// The capture is not finished when the caller's call returns. Each direction is
// copied by a relay goroutine into io.MultiWriter(conn, sink), and MultiWriter
// writes to the CONNECTION FIRST — so the reply can reach the caller, satisfy it
// and let List return while the last chunk is still on its way to the sink. Read
// once and the transcript is empty on a call that plainly succeeded, which is how
// this failed under a whole-repo run while its own log showed the request served.
//
// Waiting does not weaken the claim: a call that really crossed nothing still
// fails here, two seconds later, with the same words.
func (r *relay) await(t *testing.T) (out, in string) {
	t.Helper()
	for range 200 {
		if out, in = r.transcripts(); out != "" && in != "" {
			return out, in
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the relay captured nothing; no bytes crossed the socket")
	return "", ""
}

// sink adapts one transcript to io.Writer for io.MultiWriter.
type sink struct {
	r *relay
	b *bytes.Buffer
}

func (s sink) Write(p []byte) (int, error) { return s.r.write(s.b, p) }

// linkTarget matches an RFC 8288 Link target — `<...>` — which addresses the
// reply rather than being carried by it.
var linkTarget = regexp.MustCompile(`<[^>]*>`)

// TestPlaneWireIsZAPNotJSON captures a real IAMProjects call and states three
// things about the bytes:
//
//  1. the frames carry ZAP's magic and the peer declares application/zap
//  2. the VALUES are on the wire and the FIELD NAMES are not — under JSON every
//     one of owner/name/displayName/description/createdTime would appear
//  3. nothing is listening on TCP for this peer; the only address is the socket file
//
// (2) is the load-bearing one. zapenc lays a struct out positionally — "a field
// IS its offset" — so the absence of names is not a coincidence of this payload,
// it is the encoding. A capture containing them would mean something re-encoded
// the call as JSON on the way.
func TestPlaneWireIsZAPNotJSON(t *testing.T) {
	front, back := t.TempDir(), t.TempDir()

	// 9653 is a FIXED port and a developer's machine is shared with whatever else
	// is running on it — a cloud listening there is the ordinary case and says
	// nothing about the peer this test starts. So ask BEFORE the peer exists:
	// only an answer that appears afterwards can be its listener. Without this the
	// check reads "port 9653 is free on this host", which is true in CI and false
	// on any machine already running the thing under test.
	heldBefore := false
	if c, derr := net.DialTimeout("tcp", "127.0.0.1:9653", 200*time.Millisecond); derr == nil {
		_ = c.Close()
		heldBefore = true
	}

	// The peer binds in `back`.
	t.Setenv("ZIP_RUNTIME_DIR", back)
	plane.Unbind()
	cloud.ResetPlane()
	zip.Post[struct{}, plane.Projects](cloud.Plane(), "/iam/projects",
		func(ctx context.Context, _ *struct{}) (*plane.Projects, error) {
			return &plane.Projects{Projects: []plane.Project{{
				Owner: "acme", Name: "web", DisplayName: "Web",
				Description: "the site", CreatedTime: "2026-08-01T00:00:00Z",
			}}}, nil
		},
		zip.WithOperationID(plane.IAMProjects))
	stop, err := cloud.ServePlane("iam", nil)
	if err != nil {
		t.Fatalf("ServePlane(iam): %v", err)
	}
	defer func() { _ = stop() }()
	// ServePlane freezes the plane app; a test binary mounts many times, so it
	// must not leave a frozen one for the next test's Mount to panic on.
	t.Cleanup(cloud.ResetPlane)
	peerSock := filepath.Join(back, "iam.sock")
	waitSock(t, peerSock)

	// The relay binds in `front`, which is where the caller will dial.
	r := &relay{}
	frontSock := filepath.Join(front, "iam.sock")
	ln, err := net.Listen("unix", frontSock)
	if err != nil {
		t.Fatalf("listen %s: %v", frontSock, err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go r.pump(c, peerSock)
		}
	}()

	// The caller dials `front` — and knows only the NAME "iam".
	t.Setenv("ZIP_RUNTIME_DIR", front)
	plane.Unbind()
	rows, err := (canonicalProjects{}).List(context.Background(), "acme")
	if err != nil {
		t.Fatalf("List through the relay: %v", err)
	}
	if len(rows) != 1 || rows[0].DisplayName != "Web" {
		t.Fatalf("the relayed call returned %+v", rows)
	}

	req, resp := r.await(t)
	t.Logf("request  %d bytes:\n%s", len(req), dump(req))
	t.Logf("response %d bytes:\n%s", len(resp), dump(resp))

	// 1. ZAP framing, declared and present.
	if !strings.Contains(resp, "application/zap") {
		t.Errorf("the peer did not declare application/zap; the reply's content type is not ZAP")
	}
	if !strings.Contains(resp, "ZAP") {
		t.Errorf("no ZAP magic in the reply; the frame is not a ZAP frame")
	}
	// The op is addressed by NAME on the call plane, not by a REST path.
	if !strings.Contains(req, "/.well-known/zip/op/"+plane.IAMProjects) {
		t.Errorf("the request did not address the op by name; got:\n%s", dump(req))
	}

	// 2. Values yes, field names no.
	for _, v := range []string{"acme", "web", "Web", "the site", "2026-08-01T00:00:00Z"} {
		if !strings.Contains(resp, v) {
			t.Errorf("value %q is not on the wire — the reply did not carry it", v)
		}
	}
	// Field names are looked for in what the reply CARRIES, not in what addresses
	// it. A ZAP frame carries Link headers (RFC 8288) whose targets name the op —
	// `</.well-known/zip/op/iam_projects>; rel="self"` — so an op whose name shares
	// a word with a field would fail a scan of the whole frame while the payload is
	// perfectly positional. Link targets are addresses; strip them and read the rest.
	carried := linkTarget.ReplaceAllString(resp, "")
	for _, name := range []string{"owner", "displayName", "description", "createdTime", "projects"} {
		if strings.Contains(carried, name) {
			t.Errorf("FIELD NAME %q is on the wire: this is not ZAP's positional layout, "+
				"something re-encoded the reply as JSON", name)
		}
	}

	// 3. No TCP listener. The peer's only address is a file.
	if fi, serr := os.Stat(peerSock); serr != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("the peer's address is not a unix socket file: %v", serr)
	}
	if heldBefore {
		t.Log("tcp/9653 already answered before this peer started, so it belongs to " +
			"something else on this machine and the no-TCP-listener claim is not testable here")
	} else if c, derr := net.DialTimeout("tcp", "127.0.0.1:9653", 200*time.Millisecond); derr == nil {
		_ = c.Close()
		t.Errorf("something answered on tcp/9653; this peer must have no TCP listener")
	}
}

// pump forwards one accepted connection to the peer, recording both directions.
func (r *relay) pump(c net.Conn, peer string) {
	defer func() { _ = c.Close() }()
	up, err := net.Dial("unix", peer)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.MultiWriter(up, sink{r, &r.out}), c)
		_ = up.(*net.UnixConn).CloseWrite()
		close(done)
	}()
	_, _ = io.Copy(io.MultiWriter(c, sink{r, &r.in}), up)
	<-done
}

// waitSock blocks until path accepts.
func waitSock(t *testing.T, path string) {
	t.Helper()
	for range 200 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never accepted", path)
}

// dump renders a transcript readably: printable runs kept, everything else as a
// dot, so the header and the payload are both legible in one view.
func dump(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\r':
			b.WriteString("\\r")
		case c == '\n':
			b.WriteString("\\n\n")
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			b.WriteByte('.')
		}
	}
	return b.String()
}
