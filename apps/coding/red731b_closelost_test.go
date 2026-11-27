package coding

// RED 731b M1: a renewal is in flight when the run ends (close), and its plant's answer is lost. fresh() appends the
// token the box MAY hold to b.unsure AFTER revokeAll has already run and set b.cut. Nothing revokes it again: no later
// renewal (b.done), no later close (revokeAll returns at b.cut). A token the box may have written into its file is left
// valid at IAM for up to its whole life (credLife) although the run is over.

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRed731bARenewalWhoseAnswerIsLostAsTheRunEndsIsRevoked(t *testing.T) {
	r := newRenewRig(t)
	gate := make(chan struct{})
	r.mu.Lock()
	r.gate = gate
	r.plantErr = errors.New("context canceled") // the exec may have run; its answer was lost with the run's end
	r.mu.Unlock()
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	first := b.current()

	r.clock.advance(credLife - credMargin)
	eventually(t, "IAM has given a second token and the box has not answered", func() bool { return len(r.iam.asks()) == 2 })
	b.close() // the run ends: revokeAll sees only the first token
	close(gate)
	r.clock.firing.Wait()

	// every token IAM gave must be revoked once the run is over
	second := jwtWith("ai:inference", "tok-2")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !slices.Contains(r.iam.revokedTokens(), second) {
		time.Sleep(10 * time.Millisecond)
	}
	got := r.iam.revokedTokens()
	if !slices.Contains(got, first) {
		t.Fatalf("the first token was not revoked: %v", got)
	}
	if !slices.Contains(got, second) {
		t.Fatalf("the run is over and the token minted by the renewal in flight (%q, possibly in the box's file) was never revoked: %v", second, got)
	}
}
