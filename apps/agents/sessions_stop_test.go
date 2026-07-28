package agents

import (
	"context"
	"testing"
)

// mkLive inserts a running session under a given actor/host/provider/account so the
// stop-scope tests can craft the exact overlap a hostile revoke would try to exploit.
func mkLive(t *testing.T, s *Store, id, org, actor, host, provider, account string) {
	t.Helper()
	if err := s.CreateSession(context.Background(), Session{
		ID: id, Org: org, Agent: "hanzo", Actor: actor, Status: StatusRunning,
		RootID: id, Title: "t", StartedAt: 1, CreatedAt: 1, UpdatedAt: 1,
		Host: host, Provider: provider, Account: account,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func statusOf(t *testing.T, s *Store, org, id string) string {
	t.Helper()
	x, err := s.GetSession(context.Background(), org, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return string(x.Status)
}

// TestStopSessions_ActorScoped is the HIGH-1 regression. A login-out stop is bounded
// to the REVOKING user's own actor, so no org member can terminate another member's
// live sessions — even though the match's Host/Provider/Account come from a link row
// the caller fully controls (attacker-set at upsert). It also proves the fail-closed
// direction: a match with no actor stops NOTHING (an under-stop, never a cross-user
// over-stop).
func TestStopSessions_ActorScoped(t *testing.T) {
	ctx := context.Background()

	t.Run("provider wildcard stops only the caller's own sessions", func(t *testing.T) {
		mountInproc(t)
		st := mounted.State.store
		// One org, three users; Alice + Bob overlap on host AND provider so an
		// org-only match (the pre-fix behavior) would sweep every claude session.
		mkLive(t, st, "alice", "acme", "acme/alice", "box1", "claude", "alice@x")
		mkLive(t, st, "bob", "acme", "acme/bob", "box1", "claude", "bob@x")
		mkLive(t, st, "carol", "acme", "acme/carol", "box2", "openai", "carol@x")

		n, err := StopSessions(ctx, "acme", SessionMatch{Actor: "acme/alice", Provider: "claude"})
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		if n != 1 {
			t.Fatalf("wildcard-claude stop must hit only Alice's 1 session, got %d", n)
		}
		if s := statusOf(t, st, "acme", "alice"); s != string(StatusError) {
			t.Fatalf("Alice's own session must be stopped, got %q", s)
		}
		if s := statusOf(t, st, "acme", "bob"); s != string(StatusRunning) {
			t.Fatalf("co-tenant Bob must survive, got %q", s)
		}
		if s := statusOf(t, st, "acme", "carol"); s != string(StatusRunning) {
			t.Fatalf("co-tenant Carol must survive, got %q", s)
		}
	})

	t.Run("host forge cannot reach a co-tenant on the same device", func(t *testing.T) {
		mountInproc(t)
		st := mounted.State.store
		// Alice and Bob both have a live session on the SAME host. Alice forges her
		// link Host to that shared box and revokes it.
		mkLive(t, st, "alice", "acme", "acme/alice", "shared", "claude", "alice@x")
		mkLive(t, st, "bob", "acme", "acme/bob", "shared", "claude", "bob@x")

		n, err := StopSessions(ctx, "acme", SessionMatch{Actor: "acme/alice", Host: "shared"})
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		if n != 1 {
			t.Fatalf("host-forge stop must hit only Alice's session on the host, got %d", n)
		}
		if s := statusOf(t, st, "acme", "bob"); s != string(StatusRunning) {
			t.Fatalf("co-tenant Bob on the same host must survive, got %q", s)
		}
	})

	t.Run("a match with no actor fails closed (stops nothing)", func(t *testing.T) {
		mountInproc(t)
		st := mounted.State.store
		mkLive(t, st, "alice", "acme", "acme/alice", "box1", "claude", "alice@x")
		mkLive(t, st, "bob", "acme", "acme/bob", "box1", "claude", "bob@x")

		// An org+provider match that lost its caller identity must NOT sweep the org.
		n, err := StopSessions(ctx, "acme", SessionMatch{Provider: "claude", Host: "box1"})
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		if n != 0 {
			t.Fatalf("no-actor match must stop nothing (fail-closed), got %d", n)
		}
		if s := statusOf(t, st, "acme", "alice"); s != string(StatusRunning) {
			t.Fatalf("no session may be stopped without an actor, Alice got %q", s)
		}
		if s := statusOf(t, st, "acme", "bob"); s != string(StatusRunning) {
			t.Fatalf("no session may be stopped without an actor, Bob got %q", s)
		}
	})

	t.Run("count is actor scoped too", func(t *testing.T) {
		mountInproc(t)
		st := mounted.State.store
		mkLive(t, st, "alice", "acme", "acme/alice", "box1", "claude", "alice@x")
		mkLive(t, st, "bob", "acme", "acme/bob", "box1", "claude", "bob@x")

		// Alice counting her device's active sessions sees only HER own, not Bob's.
		n, err := CountActiveSessions(ctx, "acme", SessionMatch{Actor: "acme/alice", Host: "box1"})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("count must be scoped to Alice's own sessions on the host, got %d", n)
		}
		// A count with no actor is 0, never the whole host.
		if n, _ := CountActiveSessions(ctx, "acme", SessionMatch{Host: "box1"}); n != 0 {
			t.Fatalf("no-actor count must be 0 (fail-closed), got %d", n)
		}
	})

	t.Run("caller still stops their OWN matching session", func(t *testing.T) {
		mountInproc(t)
		st := mounted.State.store
		// The fix must not over-restrict: Alice logging out her claude account DOES
		// tear down her matching session (the intended teardown).
		mkLive(t, st, "alice", "acme", "acme/alice", "box1", "claude", "alice@x")

		n, err := StopSessions(ctx, "acme", SessionMatch{Actor: "acme/alice", Provider: "claude", Account: "alice@x"})
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
		if n != 1 {
			t.Fatalf("Alice's own-account logout must stop her session, got %d", n)
		}
		if s := statusOf(t, st, "acme", "alice"); s != string(StatusError) {
			t.Fatalf("Alice's session must be terminal after her own logout, got %q", s)
		}
	})
}
