package cloud

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveSock stands an app up on its well-known socket and returns nothing —
// Dial finds it by path, which is the whole point of the convention.
func serveSock(t *testing.T, app string, h http.Handler) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(runDirEnv, dir)
	ln, err := net.Listen("unix", filepath.Join(dir, app+".sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestDialPrefersTheSocket: a co-located app is reached over the socket and the
// call never touches the network. This is the case that replaces an import.
func TestDialPrefersTheSocket(t *testing.T) {
	var gotOrg, gotPath string
	serveSock(t, "treasury", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg, gotPath = r.Header.Get("X-Org-Id"), r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"cents": 4200})
	}))

	p := Dial("treasury")
	if !p.Local() {
		t.Fatal("a co-located app must resolve to its socket, not the internet")
	}
	var out struct {
		Cents int64 `json:"cents"`
	}
	if err := p.Get(context.Background(), "hanzo", "/v1/treasury/reserve", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Cents != 4200 {
		t.Fatalf("cents = %d, want 4200 — a real number, not the zero an import returned", out.Cents)
	}
	if gotPath != "/v1/treasury/reserve" {
		t.Fatalf("path = %q", gotPath)
	}
	// The caller's org rides along so the callee scopes the answer itself; a peer
	// call is never implicitly privileged.
	if gotOrg != "hanzo" {
		t.Fatalf("X-Org-Id = %q, want hanzo", gotOrg)
	}
}

// TestDialFallsBackToTheNetwork: no socket means the app is not co-located, so
// the same call goes out over the network with no change at the call site.
func TestDialFallsBackToTheNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"cents": 7})
	}))
	defer srv.Close()

	t.Setenv(runDirEnv, t.TempDir()) // empty: nothing co-located
	t.Setenv(remoteEnv, srv.URL)

	p := Dial("treasury")
	if p.Local() {
		t.Fatal("with no socket present the peer must not claim to be local")
	}
	var out struct {
		Cents int64 `json:"cents"`
	}
	if err := p.Get(context.Background(), "hanzo", "/v1/treasury/reserve", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Cents != 7 {
		t.Fatalf("cents = %d, want 7", out.Cents)
	}
}

// TestPeerFailureIsLoud is the whole reason this replaces the imports. A
// cross-app read that cannot be served must ERROR, not hand back a zero value —
// silently returning 0 is exactly how admin's money board went blank while
// still compiling.
func TestPeerFailureIsLoud(t *testing.T) {
	serveSock(t, "treasury", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	var out struct {
		Cents int64 `json:"cents"`
	}
	err := Dial("treasury").Get(context.Background(), "hanzo", "/v1/treasury/reserve", &out)
	if err == nil {
		t.Fatal("an unreachable peer must be an error, never a silent zero")
	}
	if out.Cents != 0 {
		t.Fatal("nothing should have been decoded")
	}
}

// TestUnreachablePeerNamesItsTransport: when a call fails, the message has to
// say whether we could not reach a SOCKET or could not reach the internet —
// those have completely different fixes.
func TestUnreachablePeerNamesItsTransport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(runDirEnv, dir)
	// A socket file that nothing is listening on: present, so Dial goes local,
	// and then the connect fails.
	if err := os.WriteFile(filepath.Join(dir, "ghost.sock"), nil, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := Dial("ghost").Get(context.Background(), "hanzo", "/v1/x", nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "uds") {
		t.Fatalf("error must name the transport it tried, got: %v", err)
	}
}
