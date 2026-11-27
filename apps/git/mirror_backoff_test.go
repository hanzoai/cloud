package git

import (
	"testing"
	"time"
)

// A refused target waits, longer with each refusal and never past a day; a success
// clears it, and another target is never held for it.
func TestARefusingTargetBacksOff(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	const id = "mir_test"
	t.Cleanup(func() { accepted(id) })
	if paused(id, now) {
		t.Fatal("a target that never refused is held")
	}
	waits := []time.Duration{}
	for i := 0; i < 10; i++ {
		waits = append(waits, refused(id, now))
	}
	if waits[0] != retryFirst || waits[1] != 2*retryFirst || waits[9] != retryMost {
		t.Fatalf("waits = %v, want %v doubling to %v", waits, retryFirst, retryMost)
	}
	if !paused(id, now.Add(retryMost-time.Second)) || paused(id, now.Add(retryMost)) {
		t.Fatal("the wait does not end where it says it does")
	}
	if paused("mir_other", now) {
		t.Fatal("another target was held for this one's refusals")
	}
	accepted(id)
	if paused(id, now) {
		t.Fatal("a success left the target held")
	}
}
