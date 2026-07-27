package bot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func session(org, node, conn string, sent *[][]byte) *Session {
	return &Session{
		Key:    NodeKey{Org: org, NodeID: node},
		ConnID: conn,
		send: func(_ context.Context, b []byte) error {
			if sent != nil {
				*sent = append(*sent, b)
			}
			return nil
		},
	}
}

func frame(string) ([]byte, error) { return []byte("{}"), nil }

// The reason this port exists. Two orgs using the SAME node id must not reach
// each other — and the answer for a foreign node must be identical to the answer
// for a node that does not exist, or the error itself reveals another tenant.
func TestOrgsCannotReachEachOthersNodes(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(session("acme", "laptop-1", "c1", nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(session("globex", "laptop-1", "c2", nil)); err != nil {
		t.Fatal(err)
	}

	// Same node id, two orgs, both registered: they are different nodes.
	if got := len(r.List("acme")); got != 1 {
		t.Fatalf("acme sees %d nodes, want 1", got)
	}
	if got := len(r.List("globex")); got != 1 {
		t.Fatalf("globex sees %d nodes, want 1", got)
	}

	// A third org sees nothing and gets the not-found answer, not a hint.
	if got := len(r.List("initech")); got != 0 {
		t.Fatalf("initech sees %d nodes, want 0", got)
	}
	_, err := r.Invoke(context.Background(), NodeKey{Org: "initech", NodeID: "laptop-1"}, frame, time.Second)
	if !errors.Is(err, ErrNoSuchNode) {
		t.Fatalf("cross-org invoke returned %v, want ErrNoSuchNode", err)
	}
}

// A node whose socket dies mid-call must fail immediately. Leaving the caller to
// time out holds it for the full timeout on a question already unanswerable.
func TestDisconnectFailsPendingInvokesImmediately(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(session("acme", "n1", "conn-1", nil)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.Invoke(context.Background(), NodeKey{Org: "acme", NodeID: "n1"}, frame, 30*time.Second)
		done <- err
	}()

	// Let the invoke register its waiter, then drop the connection.
	time.Sleep(50 * time.Millisecond)
	r.Unregister("conn-1")

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, ErrNodeGone) {
			t.Fatalf("invoke returned %v; want a node-gone result", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not fail on disconnect; it waited for the timeout instead")
	}
}

// A node that reconnects after a blip must be reachable again at once, not
// stuck behind a dead entry until something expires.
func TestReconnectReplacesTheStaleSession(t *testing.T) {
	r := NewRegistry()
	var first, second [][]byte
	if err := r.Register(session("acme", "n1", "old", &first)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(session("acme", "n1", "new", &second)); err != nil {
		t.Fatal(err)
	}

	if got := len(r.List("acme")); got != 1 {
		t.Fatalf("reconnect left %d sessions, want 1", got)
	}
	go r.Invoke(context.Background(), NodeKey{Org: "acme", NodeID: "n1"}, frame, time.Second)
	time.Sleep(50 * time.Millisecond)
	if len(second) == 0 {
		t.Fatal("invoke went to the stale socket, not the reconnected one")
	}
}

// An answer nobody is waiting for is dropped. Queueing it would surface later as
// a reply to an unrelated call.
func TestOrphanAnswerIsDropped(t *testing.T) {
	r := NewRegistry()
	r.Answer("nobody-waiting", InvokeResult{OK: true}) // must not panic or block
}

func TestRegisterRejectsIncompleteSessions(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Session{Key: NodeKey{NodeID: "n1"}, ConnID: "c"}); err == nil {
		t.Fatal("a session with no org was accepted")
	}
	if err := r.Register(&Session{Key: NodeKey{Org: "acme"}, ConnID: "c"}); err == nil {
		t.Fatal("a session with no node id was accepted")
	}
	if err := r.Register(session("acme", "n1", "c1", nil)); err != nil {
		t.Fatalf("a complete session was rejected: %v", err)
	}
}
