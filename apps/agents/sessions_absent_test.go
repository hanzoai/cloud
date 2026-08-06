package agents

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
)

// The defect: agents ships as its own binary, so the link process that revokes a
// credential has never had the session store in it. StopSessions answered
// (0, nil) for that, and the revoke handler reads a count — so every revoke on
// the fleet returned 200 {"sessionsStopped":0} having torn down nothing while
// the sessions kept running under the revoked account.
//
// A zero is only readable as an answer if failure cannot produce one. These pin
// that: absence is ErrNoPeer, which is also what lets the caller take the plane
// leg instead of believing the zero.
func TestSessionsAbsentIsAnErrorNotAZero(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })

	m := SessionMatch{Actor: "acme/alice", Provider: "claude"}

	n, err := StopSessions(context.Background(), "acme", m)
	if err == nil {
		t.Fatal("unmounted StopSessions returned no error — a revoke that stopped " +
			"nothing must not report success")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Errorf("StopSessions err = %v, want ErrNoPeer so the caller can take the plane leg", err)
	}
	if n != 0 {
		t.Errorf("StopSessions n = %d, want 0 alongside the error", n)
	}

	n, err = CountActiveSessions(context.Background(), "acme", m)
	if err == nil {
		t.Fatal("unmounted CountActiveSessions returned no error")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Errorf("CountActiveSessions err = %v, want ErrNoPeer", err)
	}
	if n != 0 {
		t.Errorf("CountActiveSessions n = %d, want 0 alongside the error", n)
	}
}

// An empty match is a different fact from an absent store: it is fail-closed
// ("stop nothing"), it is a real answer, and it must NOT become an error — or a
// revoke that legitimately matches nothing starts reporting a fault.
func TestEmptyMatchStaysAnAnswer(t *testing.T) {
	mountInproc(t)
	n, err := StopSessions(context.Background(), "acme", SessionMatch{})
	if err != nil {
		t.Fatalf("empty match must be an answer, not an error: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty match stopped %d sessions — it must stop nothing", n)
	}
}
