package cloud

import (
	"bytes"
	"context"
	"os"
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

// The loop closed: what rpc.Listen SERVES is exactly what Call RESOLVES. They are
// two functions deriving a path from a name, and if they ever disagreed an app
// would be up and unreachable — so the socket a listener creates is asserted to be
// the one PeerSocket names, rather than each side being trusted separately.
func TestPeerSocketIsWhatDialLooksFor(t *testing.T) {
	t.Setenv(runDirEnv, t.TempDir())
	Expose("loop.ping", func(_ context.Context, _ Ident, _ []byte) ([]byte, error) {
		return []byte("pong"), nil
	})
	c, err := Listen("loop", nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := os.Stat(PeerSocket("loop")); err != nil {
		t.Fatalf("rpc.Listen served something other than %s: %v", PeerSocket("loop"), err)
	}
	// Reachability, not just presence: a real call over that same file, and a
	// real reply. A socket that exists and answers nothing is the failure this
	// closes — the app was up, one socket away, and every peer call missed it.
	out, err := Dial("loop").Call(context.Background(), "loop.ping", nil)
	if err != nil {
		t.Fatalf("an app that is up must be reachable at the path callers resolve: %v", err)
	}
	if string(out) != "pong" {
		t.Fatalf("reply = %q, want pong", out)
	}
}
