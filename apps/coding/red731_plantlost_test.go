package coding

// RED 731 L1: a renewal whose exec reached the box but whose answer did not come back (the 20s
// clientTimeout, a dropped stream) is read as "the box did not take it": the token is revoked and the
// old one kept, while the box's file now holds the REVOKED token. dev reads the file first and after
// every 401, so every model call is a 401 until the next successful renewal (>= 30s later, up to
// 15 min), although the environment's token and the run's records say all is well.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
)

func TestRed731ARenewalTheBoxTookButNeverAnsweredIsNotLeftRevokedInTheFile(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	var mu sync.Mutex
	file := ""
	b, err := key(t.Context(), loan{org: "acme", person: "alice", run: "sess_1", ttl: 24 * 3600,
		plant: func(_ context.Context, token string) error {
			mu.Lock()
			defer mu.Unlock()
			file = token                                   // the exec ran ...
			return errors.New("context deadline exceeded") // ... and its answer was lost
		}, halt: r.halt})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	r.clock.tick(credLife - credMargin)
	eventually(t, "the renewal was tried", func() bool { return len(r.iam.asks()) >= 2 })
	// The box never said whether it took the token, so it is not revoked: a retry is set instead.
	eventually(t, "a retry was set", func() bool { return len(r.clock.pending()) == 1 })
	mu.Lock()
	got := file
	mu.Unlock()
	if slices.Contains(r.iam.revokedTokens(), got) {
		t.Fatalf("the box's credential file holds %q, which was revoked unused: dev answers 401 until the next renewal", got)
	}
}
