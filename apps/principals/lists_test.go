package principals

import (
	"context"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// The first screening is answered from the lists it started loading, not from the moment
// before they were in: a process that screens its first org must not say "not loaded"
// to a publisher that is a few milliseconds from answering, and must not store a verdict
// made on none of them.
func TestTheFirstScreeningWaitsForTheLists(t *testing.T) {
	l := newLists(func(ctx context.Context) []ScreenResult {
		time.Sleep(200 * time.Millisecond)
		return designations(ctx)
	}, luxlog.New("test"))
	t.Cleanup(l.close)
	if err := l.fit(); err != nil {
		t.Fatalf("the first screening ran before the lists loaded: %v", err)
	}
}

// Closing a process that is still loading its first lists does not wait for a publisher.
func TestClosingCancelsALoadInFlight(t *testing.T) {
	started := make(chan struct{})
	l := newLists(func(ctx context.Context) []ScreenResult {
		close(started)
		<-ctx.Done()
		return nil
	}, luxlog.New("test"))
	go func() { _ = l.fit() }()
	<-started
	done := make(chan struct{})
	go func() { l.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("close waited on a load nobody can use")
	}
}
