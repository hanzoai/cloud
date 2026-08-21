package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newIdleServer is a listening sshServer with a real host key and no service
// behind it. The peers in these tests never speak, so nothing past the handshake
// is reached and no key registry, repo or store is needed.
func newIdleServer(t *testing.T) *sshServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return nil, errUnknownKey
	}}
	cfg.AddHostKey(signer)
	srv := &sshServer{listen: "127.0.0.1:0", cfg: cfg}
	if err := srv.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return srv
}

var errUnknownKey = errors.New("ssh: unknown key")

// stop() closes the LISTENER, which does not close connections already accepted.
// handleConn then sits inside the SSH handshake (or, once handshaked, on
// `range chans`) until the client hangs up — so waiting on the WaitGroup alone
// waits on the CLIENT. A peer that connects and then says nothing held shutdown
// open forever, which is how the SSH wire test finished every assertion and then
// hung until the suite timeout, taking `go test ./...` with it.
//
// The peer here never speaks, so it needs no key, no handshake and no git: the
// property is about a connection this process has ACCEPTED and cannot end, and an
// unauthenticated one is the sharpest case — nothing about it is trusted, and it
// could still decide when we exit.
func TestSilentPeerCannotHoldShutdownOpen(t *testing.T) {
	srv := newIdleServer(t)

	conn, err := net.Dial("tcp", srv.addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Give the accept loop a moment to hand the connection to a handler, so the
	// test exercises a TRACKED connection rather than racing the listener close.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.conns)
		srv.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			if n == 0 {
				t.Fatal("the connection was never handed to a handler, so this test proves nothing")
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() { srv.stop(); close(done) }()

	// The bound is the grace plus room for the close to propagate. Without the
	// grace this blocks until the suite's own timeout kills the process.
	select {
	case <-done:
	case <-time.After(shutdownGrace + 5*time.Second):
		t.Fatal("stop() did not return: a peer that says nothing decides when this process exits")
	}
}

// The paired control: with no connection at all, stop() returns AT ONCE rather
// than sitting out the grace. A shutdown that always waits five seconds is a
// different defect wearing the same fix.
func TestStopReturnsImmediatelyWhenNothingIsConnected(t *testing.T) {
	srv := newIdleServer(t)
	start := time.Now()
	srv.stop()
	if elapsed := time.Since(start); elapsed >= shutdownGrace {
		t.Fatalf("stop() waited out the full grace with nobody connected (%s)", elapsed)
	}
}
