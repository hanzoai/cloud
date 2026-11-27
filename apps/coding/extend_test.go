package coding

// extend_test.go — a run that is working is given more time, and is ended at the
// time it has been given.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not an HTTP error", err)
	}
	return he.Status
}

// More time moves WHEN the run is ended: it is not ended at the time it first
// had, and it is at the time it was given, with the same cause a deadline gives.
func TestAnExtendedRunIsEndedAtTheTimeItWasGiven(t *testing.T) {
	ctx, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	k := newClock(time.Now(), 80*time.Millisecond, time.Hour, stop)
	defer k.end()
	if _, err := k.extend(time.Now(), 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("the run was ended at the time it first had, though it was given more")
	case <-time.After(160 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the run was never ended at the time it was given")
	}
	if !errors.Is(context.Cause(ctx), errTimeUp) {
		t.Fatalf("cause = %v, want its time ran out", context.Cause(ctx))
	}
}

// An extension stops at the ceiling, and says so: asking for more than a run may
// have answers what it got, and a run already at the ceiling is refused.
func TestAnExtensionStopsAtTheCeiling(t *testing.T) {
	_, stop := context.WithCancelCause(context.Background())
	now := time.Now()
	k := newClock(now, time.Minute, 90*time.Minute, stop)
	defer k.end()
	until, err := k.extend(now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(90 * time.Minute); !until.Equal(want) {
		t.Fatalf("until = %v, want the ceiling %v", until, want)
	}
	if _, err := k.extend(now, time.Minute); codeOf(t, err) != http.StatusConflict {
		t.Fatalf("a run at its ceiling must be refused, got %v", err)
	}
}

// A run whose time is up cannot be given more: it has been ended, and an answer
// that quietly changed nothing would read as success.
func TestARunOutOfTimeIsNotExtended(t *testing.T) {
	_, stop := context.WithCancelCause(context.Background())
	k := newClock(time.Now(), 10*time.Millisecond, time.Hour, stop)
	defer k.end()
	time.Sleep(60 * time.Millisecond)
	if _, err := k.extend(time.Now(), time.Minute); !errors.Is(err, errSpent) {
		t.Fatalf("err = %v, want the run's time is up", err)
	}
}

// Extend finds the caller's own running run and no other: another person's is
// refused, a stranger's is not found, a paused one is a conflict, and seconds
// must be above zero.
func TestExtendIsForTheRunsOwnPersonWhileItWorks(t *testing.T) {
	ctx, stop := context.WithCancelCause(context.Background())
	k := newClock(time.Now(), time.Minute, time.Hour, stop)
	f := &flight{org: "acme", subject: "u-bob", session: "sess_ext", cancel: stop, done: make(chan struct{}), ctx: ctx, clock: k}
	fly(f)
	t.Cleanup(func() { f.forget(); k.end() })

	if _, err := Extend(context.Background(), "acme", "u-bob", &client.CodingExtendIn{Session: "sess_ext", Seconds: 0}); codeOf(t, err) != http.StatusBadRequest {
		t.Fatalf("zero seconds must be a bad request, got %v", err)
	}
	if _, err := Extend(context.Background(), "acme", "u-bob", &client.CodingExtendIn{Session: "sess_nobody", Seconds: 60}); codeOf(t, err) != http.StatusNotFound {
		t.Fatalf("a run nobody holds must be not found, got %v", err)
	}
	if _, err := Extend(context.Background(), "other", "u-bob", &client.CodingExtendIn{Session: "sess_ext", Seconds: 60}); codeOf(t, err) != http.StatusNotFound {
		t.Fatalf("another org's run must be not found, got %v", err)
	}
	if _, err := Extend(context.Background(), "acme", "u-eve", &client.CodingExtendIn{Session: "sess_ext", Seconds: 60}); codeOf(t, err) != http.StatusForbidden {
		t.Fatalf("another person's run must be refused, got %v", err)
	}
	got, err := Extend(context.Background(), "acme", "u-bob", &client.CodingExtendIn{Session: "sess_ext", Seconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if got.Seconds < 350 || got.Seconds > 360 {
		t.Fatalf("seconds = %d, want about the minute it had and the five it was given", got.Seconds)
	}
	f.mu.Lock()
	f.paused = true
	f.mu.Unlock()
	if _, err := Extend(context.Background(), "acme", "u-bob", &client.CodingExtendIn{Session: "sess_ext", Seconds: 60}); codeOf(t, err) != http.StatusConflict {
		t.Fatalf("a paused run must be a conflict, got %v", err)
	}
}
