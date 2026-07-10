package cloud

import (
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// TestWriterLease_SerializesHandoff proves the surge-roll safety property: while
// one writer holds the lease, a second cannot acquire it; the instant the first
// releases (as it would AFTER closing its stores at shutdown), the second
// acquires. This is exactly the ZapDB/audit store handoff — never a double-open.
func TestWriterLease_SerializesHandoff(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()

	release1, err := acquireWriterLease(dir, 2*time.Second, log)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A second writer must NOT acquire while the first holds it.
	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := acquireWriterLease(dir, 5*time.Second, log)
		if err != nil {
			t.Errorf("second acquire (after handoff): %v", err)
			return
		}
		acquired.Store(true)
		_ = release2()
	}()

	time.Sleep(300 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("second writer acquired the lease while the first still held it — double-open possible")
	}

	// Release the first (post-store-close). The second must now acquire promptly.
	if err := release1(); err != nil {
		t.Fatalf("release1: %v", err)
	}
	select {
	case <-done:
		if !acquired.Load() {
			t.Fatal("second writer did not acquire after handoff")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second writer never acquired after the first released (handoff stuck)")
	}
}

// TestWriterLease_FailsClosedOnTimeout proves that a writer which cannot get the
// lease within the timeout REFUSES to proceed (fail-closed) rather than opening
// the exclusive stores beside a live holder.
func TestWriterLease_FailsClosedOnTimeout(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()

	release, err := acquireWriterLease(dir, time.Second, log)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer release()

	start := time.Now()
	if _, err := acquireWriterLease(dir, 400*time.Millisecond, log); err == nil {
		t.Fatal("expected fail-closed timeout while the lease is held")
	}
	if elapsed := time.Since(start); elapsed < 350*time.Millisecond {
		t.Fatalf("gave up in %s — should have waited the ~400ms timeout", elapsed)
	}
}

// TestWriterLease_ReacquireAfterRelease proves a clean single-writer restart
// (Recreate) reacquires with no wait — the default topology stays byte-identical
// in behavior (acquire returns immediately when uncontended).
func TestWriterLease_ReacquireAfterRelease(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	for i := 0; i < 3; i++ {
		release, err := acquireWriterLease(dir, time.Second, log)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if err := release(); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
}
