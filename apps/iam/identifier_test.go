package iam

import (
	"fmt"
	"testing"
	"time"
)

// A client is held to its limit, and every client together to the ceiling.
func TestAdmitIdentifierHoldsBothBounds(t *testing.T) {
	const limit, ceiling = 2, 6
	for i := range limit {
		if !admitIdentifier("198.51.100.1", limit, ceiling, time.Minute) {
			t.Fatalf("question %d of %d from one client was refused", i+1, limit)
		}
	}
	if admitIdentifier("198.51.100.1", limit, ceiling, time.Minute) {
		t.Fatal("a client past its limit was answered")
	}
	for i := 2; i <= 3; i++ {
		for range limit {
			if !admitIdentifier(fmt.Sprintf("198.51.100.%d", i), limit, ceiling, time.Minute) {
				t.Fatalf("client %d was refused under the ceiling", i)
			}
		}
	}
	if admitIdentifier("198.51.100.9", limit, ceiling, time.Minute) {
		t.Fatal("a fresh client past the ceiling was answered")
	}
}
