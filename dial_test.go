package cloud

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// dial_test.go proves the internal plane end to end: real frames over a real
// unix socket, no HTTP and no JSON anywhere in the path.

// hdr is the test stand-in for the sliver of a request As needs.
type hdr map[string]string

func (h hdr) Header(k string) string { return h[k] }

// serve exposes the given methods and listens on app's socket in a temp run
// dir. The registry is process-global, so each test uses its own method names.
func serve(t *testing.T, app string, names map[string]Method) {
	t.Helper()
	t.Setenv(runDirEnv, t.TempDir())
	for n, m := range names {
		Expose(n, m)
	}
	c, err := Listen(app, nil)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
}

// TestCallMovesBytesUntouched is the property this plane exists for: the
// payload is opaque to the transport, so the bytes the callee returns are the
// bytes the caller reads — no re-encode, no translation layer, nothing between
// the two memories but the socket.
func TestCallMovesBytesUntouched(t *testing.T) {
	serve(t, "echo", map[string]Method{
		"echo.raw": func(_ context.Context, _ Ident, req []byte) ([]byte, error) {
			return req, nil
		},
	})
	// Arbitrary bytes, deliberately not text and not valid JSON or ZAP: the
	// transport must not care.
	in := []byte{0x00, 0xff, 0x7f, 0x80, 'z', 0x00, 0x01, 0xfe, 0x00}
	out, err := Dial("echo").Call(context.Background(), "echo.raw", in)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("payload changed in transit:\n in  %x\n out %x", in, out)
	}
}

// TestIdentityRidesTheCapability: the delegated principal travels in the
// envelope's capability slot and arrives as a value — the callee never parses
// headers, and a caller with no principal arrives anonymous rather than as an
// empty claim.
func TestIdentityRidesTheCapability(t *testing.T) {
	var got Ident
	serve(t, "who", map[string]Method{
		"who.ami": func(_ context.Context, who Ident, _ []byte) ([]byte, error) {
			got = who
			return nil, nil
		},
	})

	caller := hdr{
		"X-Org-Id": "hanzo", "X-User-Id": "u_z", "X-User-IsAdmin": "true",
	}
	if _, err := Dial("who").As(caller).Call(context.Background(), "who.ami", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Org != "hanzo" || got.User != "u_z" || !got.Admin {
		t.Fatalf("principal arrived as %+v", got)
	}

	if _, err := Dial("who").Call(context.Background(), "who.ami", nil); err != nil {
		t.Fatalf("anonymous call: %v", err)
	}
	if got != (Ident{}) {
		t.Fatalf("an undelegated call must arrive anonymous, got %+v", got)
	}
}

// TestFaultCarriesItsStatus: a refusal arrives with the exact status the method
// chose and the method's own words — 402 is "unfunded", not a generic failure,
// and never a zero value.
func TestFaultCarriesItsStatus(t *testing.T) {
	serve(t, "till", map[string]Method{
		"till.take": func(_ context.Context, _ Ident, _ []byte) ([]byte, error) {
			return nil, Fault(402, "unfunded")
		},
	})
	_, err := Dial("till").Call(context.Background(), "till.take", nil)
	if err == nil {
		t.Fatal("a refused call must be an error")
	}
	if !strings.Contains(err.Error(), "402") || !strings.Contains(err.Error(), "unfunded") {
		t.Fatalf("refusal lost its status or its words: %v", err)
	}
}

// TestUnknownMethodIsNotSilence: a live app answering a method it does not
// serve is version skew — a 404 with the method named by the caller, distinct
// from "app not running".
func TestUnknownMethodIsNotSilence(t *testing.T) {
	serve(t, "sparse", map[string]Method{
		"sparse.only": func(_ context.Context, _ Ident, _ []byte) ([]byte, error) { return nil, nil },
	})
	_, err := Dial("sparse").Call(context.Background(), "sparse.other", nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("unknown method must answer 404, got: %v", err)
	}
}

// TestNoSocketNamesThePath: "that app is not running here" is an error naming
// the socket it looked for — the diagnosis is in the message, and there is no
// second transport to fall back to.
func TestNoSocketNamesThePath(t *testing.T) {
	t.Setenv(runDirEnv, t.TempDir())
	_, err := Dial("ghost").Call(context.Background(), "ghost.read", nil)
	if err == nil {
		t.Fatal("a missing socket must be an error, never a zero value")
	}
	if !strings.Contains(err.Error(), "ghost.sock") {
		t.Fatalf("error must name the socket it tried: %v", err)
	}
}

// TestScalarRoundTrip: the interim scalar payload helpers carry an int64 as a
// ZAP message — negative values included — with no JSON anywhere.
func TestScalarRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 4200, -913 << 40} {
		got, err := I64(PutI64(v))
		if err != nil {
			t.Fatalf("I64(%d): %v", v, err)
		}
		if got != v {
			t.Fatalf("round trip %d -> %d", v, got)
		}
	}
}

// The loop closed: an app serving its canonical peer socket is found by Dial and
// answered over ZAP. Before listenOn bound these, nothing created the path Dial
// looks for, so every "local" call silently fell out to the public edge — the
// inner plane existed in dial.go and nowhere on disk.
func TestPeerSocketIsWhatDialLooksFor(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(runDirEnv, dir)

	// What listenOn hands zip for an app named "tasks"...
	addrs, _ := listenOn(&Config{ListenAddr: ":0", ZAPListenAddr: ":0", HealthListenAddr: ":0"},
		[]MountSpec{{Name: "tasks"}})
	if len(addrs) == 0 {
		t.Fatal("listenOn bound nothing")
	}
	served := addrs[0]

	// ...must be exactly the path Dial resolves for that name.
	if want := PeerSocket("tasks"); served != want {
		t.Fatalf("listenOn serves %q but Dial looks for %q — an app would be up and unreachable", served, want)
	}

	// And a peer standing on it is reached over ZAP, not the network.
	srv := &zaphttp.Server{Network: "unix", Addr: served, Handler: fasthttpadaptor.NewFastHTTPHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"queued": 7})
		}))}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", served); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("peer socket never accepted")
		}
		time.Sleep(5 * time.Millisecond)
	}

	p := Dial("tasks")
	if !p.Local() {
		t.Fatal("an app on its canonical socket must resolve local")
	}
	var out struct {
		Queued int `json:"queued"`
	}
	if err := p.Get(context.Background(), "hanzo", "/v1/tasks", &out); err != nil {
		t.Fatalf("commerce->tasks over zap/uds: %v", err)
	}
	if out.Queued != 7 {
		t.Fatalf("queued = %d, want 7", out.Queued)
	}
}
