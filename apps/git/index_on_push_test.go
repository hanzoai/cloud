// Copyright © 2026 Hanzo AI. MIT License.

package git

import (
	"testing"
	"time"
)

// THE INDEX CALL IS BOUNDED, and that bound is load-bearing rather than tidy.
//
// This rides a push reactor: the reconcile embeds every text file in the tree,
// which is paid inference on the far side and slow by nature. Unbounded, a code
// plane that has stopped answering holds the reactor open for as long as it stays
// unwell — and the reactor is what a developer's `git push` waits behind.
//
// Generous at the top, because a first index of a large repo really does take
// minutes; floored at the bottom, because giving up in seconds would make the
// feature silently useless on exactly the repos it matters most for.
func TestTheIndexCallIsBounded(t *testing.T) {
	if indexCallTimeout <= 0 {
		t.Fatal("the index call is unbounded; a code plane that stops answering would " +
			"hold a push reactor open for as long as it stayed unwell")
	}
	if indexCallTimeout > 10*time.Minute {
		t.Errorf("the index call waits up to %s behind a push", indexCallTimeout)
	}
	if indexCallTimeout < 30*time.Second {
		t.Errorf("the index call gives up after %s — a first index of a large repo "+
			"embeds every file and legitimately takes minutes", indexCallTimeout)
	}
}
