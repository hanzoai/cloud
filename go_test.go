package cloud

import (
	"sync"
	"testing"
	"time"
)

// The whole point: a panic on a spawned goroutine must NOT reach the runtime.
// middleware.Recover() covers only the request goroutine, so without containment
// here an unrecovered panic in background work takes the process down and with it
// every tenant in the shared binary. If this test ever regresses it does not fail —
// it crashes the test binary, which is exactly the production symptom.
func TestGo_ContainsPanic(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	Go(nil, "test.panic", nil, func() {
		defer wg.Done()
		panic("a page we fetched was malformed")
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("panicking work never completed — the recover defer did not run")
	}

	// The process survived, so subsequent work still runs.
	ran := make(chan struct{})
	Go(nil, "test.after", nil, func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime did not survive a contained panic")
	}
}

// A nil logger must not itself panic inside the recover path — that would turn a
// contained fault back into a process kill, at the worst possible moment.
func TestGo_NilLoggerIsSafe(t *testing.T) {
	done := make(chan struct{})
	Go(nil, "test.nil-logger", []any{"org", "acme"}, func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nil-logger panic path hung or re-panicked")
	}
}

// Base.Go is the same containment for a caller that already holds a Base, including
// the nil-Base case a partially-built subsystem can hand us.
func TestBaseGo_ContainsPanicAndToleratesNilBase(t *testing.T) {
	var b *Base
	done := make(chan struct{})
	b.Go("test.nil-base", nil, func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nil Base receiver did not contain the panic")
	}
}

// Containment must not swallow ordinary completion: work that does not panic runs
// to the end and its effects are visible.
func TestGo_RunsWorkToCompletion(t *testing.T) {
	var mu sync.Mutex
	got := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		Go(nil, "test.count", nil, func() {
			defer wg.Done()
			mu.Lock()
			got++
			mu.Unlock()
		})
	}
	wg.Wait()
	if got != 8 {
		t.Fatalf("ran %d of 8 units of work", got)
	}
}
