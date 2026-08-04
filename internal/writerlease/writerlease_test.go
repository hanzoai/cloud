// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package writerlease

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAssess covers the decision itself, which is the whole of the fix: every
// process in a pod runs the same code, so what it does can only come from where
// it sits.
func TestAssess(t *testing.T) {
	const parent = 4242

	cases := []struct {
		name string
		env  map[string]string
		want Duty
	}{{
		name: "unset is off, and that is production today",
		env:  map[string]string{},
		want: Off,
	}, {
		name: "a reader never locks the volume against the writer",
		env:  map[string]string{Enable: "1", "CLOUD_ROLE": "reader"},
		want: Off,
	}, {
		name: "the pod root takes it: nothing spawned it, nobody holds it",
		env:  map[string]string{Enable: "1"},
		want: Take,
	}, {
		name: "a child whose parent stamped it inherits",
		env:  map[string]string{Enable: "1", Held: "4242"},
		want: Inherit,
	}, {
		name: "a child under a router inherits even with no stamp",
		env:  map[string]string{Enable: "1", zipAddr: "/run/cloud/kms.sock"},
		want: Inherit,
	}, {
		name: "a stamp naming someone else's pid is not evidence about this pod",
		env:  map[string]string{Enable: "1", Held: "9999"},
		want: Take,
	}, {
		name: "a junk stamp is ignored rather than believed",
		env:  map[string]string{Enable: "1", Held: "yes"},
		want: Take,
	}, {
		name: "off wins over everything: no lease means no lease",
		env:  map[string]string{Held: "4242", zipAddr: "/run/x.sock"},
		want: Off,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := assess(func(k string) string { return c.env[k] }, func() int { return parent })
			if got != c.want {
				t.Fatalf("assess = %v, want %v (%s)", got, c.want, why)
			}
			if strings.TrimSpace(why) == "" {
				t.Fatal("every decision must carry the sentence explaining it — an unexplained lease is how both failures happened")
			}
		})
	}
}

// TestStampCannotBeForgedByConfig is the one way this design could fail open: a
// CLOUD_WRITER_LEASE_HELD left in a manifest, or inherited from a pod generation
// that is gone, talking the ROOT of a fresh pod out of taking the lock. The stamp
// counts only when it names this process's actual parent, so it cannot.
func TestStampCannotBeForgedByConfig(t *testing.T) {
	env := map[string]string{Enable: "1", Held: "1"} // "1" — plausible, and wrong
	got, why := assess(func(k string) string { return env[k] }, func() int { return 31337 })
	if got != Take {
		t.Fatalf("a hand-placed stamp disarmed the pod root: assess = %v (%s)", got, why)
	}
}

// TestHoldIsInertWhenUnset pins the property the rollout depends on: with the
// variable unset, Hold touches nothing at all — no lock file, no environment
// change — so shipping this is a no-op in production.
func TestHoldIsInertWhenUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(Enable, "")
	t.Setenv(Held, "")

	release, err := Hold(dir, time.Second, nil)
	if err != nil {
		t.Fatalf("Hold with no lease configured must succeed and do nothing: %v", err)
	}
	if release == nil {
		t.Fatal("release must never be nil — callers defer it unconditionally")
	}
	if got := lockExists(dir); got {
		t.Fatal("Hold created a lock file with the lease switched off")
	}
	if got := getenvOrEmpty(Held); got != "" {
		t.Fatalf("Hold stamped %q with the lease switched off", got)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// TestSerializesHandoff is the surge-roll property, in one process: while a
// holder has the lease a second acquire cannot succeed, and the moment the
// holder lets go — which in the real shutdown is AFTER every store is closed —
// the successor gets it. That is the handoff, and it is the only reason the
// mechanism exists.
func TestSerializesHandoff(t *testing.T) {
	dir := t.TempDir()

	release1, err := Acquire(dir, 2*time.Second, nil)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := Acquire(dir, 5*time.Second, nil)
		if err != nil {
			t.Errorf("second acquire (after handoff): %v", err)
			return
		}
		acquired.Store(true)
		_ = release2()
	}()

	time.Sleep(300 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("a second writer took the lease while the first held it — the stores could be double-opened")
	}

	if err := release1(); err != nil {
		t.Fatalf("release1: %v", err)
	}
	select {
	case <-done:
		if !acquired.Load() {
			t.Fatal("the successor never acquired after the handoff")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the successor did not acquire within 6s of the handoff — a roll would stall here")
	}
}

// TestFailsClosedOnTimeout: a writer that cannot prove it is the only opener
// refuses to open the stores. Correct against another POD — and, before the fix,
// the exact reason three siblings of ONE pod all refused to start.
func TestFailsClosedOnTimeout(t *testing.T) {
	dir := t.TempDir()

	release, err := Acquire(dir, time.Second, nil)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = release() }()

	start := time.Now()
	if _, err := Acquire(dir, 400*time.Millisecond, nil); err == nil {
		t.Fatal("a second acquire succeeded while the lease was held — fail-open")
	} else if !strings.Contains(err.Error(), "still held by another writer") {
		t.Fatalf("the error must name the cause, got: %v", err)
	}
	if spent := time.Since(start); spent < 400*time.Millisecond {
		t.Fatalf("gave up after %s, before the budget was spent — it did not wait for a handoff", spent)
	}
}

// TestReleaseIsIdempotent: callers defer release AND may call it explicitly on
// the shutdown path. Doing both must not return a spurious error.
func TestReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(Enable, "1")
	t.Setenv(Held, "")

	release, err := Hold(dir, time.Second, nil)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release must be a no-op, got: %v", err)
	}
	if got := getenvOrEmpty(Held); got != "" {
		t.Fatalf("release left the stamp behind (%q) — anything spawned afterwards would believe a lease is held", got)
	}
}
