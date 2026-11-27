package coding

// RED 731 H1: a turn that begins after a renewal is not blinded against the token it is fed.
// sandboxRunner.Run builds client.RunIn once (Blind: in.secrets() at the first turn) and reuses it for
// every later turn (the "asked again", resume, 402-fallback and loop turns); in.turn only rewrites
// Argv/Stdin/TimeoutSec. So from the first renewal on, `env` or `cat /tmp/hanzo-run/model.*` in any
// later turn streams the live credential into the session, SSE and Slack thread unredacted.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestRed731ATurnAfterARenewalIsBlindedAgainstTheTokenItIsFed(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
	iam.serve(t)
	app := newSessionsApp(t)
	clock := holdClock(t)
	l := newLongRun(t)
	// The first turn is refused for balance, so the run asks once more on the free model; the second answers.
	var mu sync.Mutex
	turns := 0
	inner := l.p.answer
	l.p.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			mu.Lock()
			turns++
			n := turns
			mu.Unlock()
			if n == 1 {
				return client.Ran{ExitCode: 1, Stderr: "unexpected status 402 Payment Required: balance"}
			}
		}
		return inner(argv)
	}
	run := aRun()
	app.run("acme", "alice", run.SessionID)

	done, res, err := l.fly(t, context.Background(), run)
	clock.tick(credLife - credMargin)
	eventually(t, "the renewal is in the box", func() bool { fed, _ := plantedIn(l.p); return len(fed) == 1 })
	l.free()
	awaited(t, "the run", done)
	if *err != nil {
		t.Fatalf("run: %+v / %v", res, *err)
	}
	var fedAll []string
	var blindAll [][]string
	l.p.mu.Lock()
	for i, a := range l.p.ran {
		if slices.Contains(a, "--skip-git-repo-check") {
			fedAll = append(fedAll, l.p.fed[i])
			blindAll = append(blindAll, l.p.blind[i])
		}
	}
	l.p.mu.Unlock()
	if len(fedAll) < 2 {
		t.Fatalf("the harness ran %d times, want a second turn after the renewal", len(fedAll))
	}
	second := jwtWith("ai:inference", "run-2")
	cred, _, _ := strings.Cut(fedAll[1], "\n")
	if cred != second {
		t.Fatalf("the second turn was fed %q, want the renewed token %q", cred, second)
	}
	if !slices.Contains(blindAll[1], second) {
		t.Fatalf("the second turn is fed the renewed token and told to blind only %d strings; the token is not among them", len(blindAll[1]))
	}
}
