// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"bytes"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
)

// The at-rest posture decision, pinned from BOTH sides of the predicate it gates on
// so the same assertions hold under `make test` (CGO_ENABLED=0, pure-Go) and
// `make test-codec` (CGO_ENABLED=1 + libsqlcipher, the engine the image ships).
// Writing it against CodecLinked rather than against one build's answer is the whole
// point: the bug this replaced was cloud asserting a posture the build could not
// deliver, and a test that only ran one way would not have caught it either.
func TestCommerceMasterKey(t *testing.T) {
	lg := luxlog.New("test")
	key := bytes.Repeat([]byte{7}, 32)

	got := commerceMasterKey(key, lg)

	if sqlitedrv.CodecLinked() {
		// The production posture: the key reaches commerce, which encrypts with it.
		if !bytes.Equal(got, key) {
			t.Fatalf("codec-linked build must inject the master key so commerce encrypts; got %d bytes, want the 32 supplied", len(got))
		}
		return
	}

	// The dev posture. commerce's dual pool cannot open encrypted without the live
	// codec, so injecting here is not a stricter posture — it is a hard refusal that
	// takes the entire money plane to 503. nil hands the decision back to commerce's
	// own env, whose documented answer is the zero-config unencrypted dev store.
	if got != nil {
		t.Fatalf("pure-Go build must NOT inject a key commerce cannot use (it refuses to boot and the money plane 503s); got %d bytes, want nil", len(got))
	}
}

// A build that resolved no key of its own must still hand commerce nothing, and must
// not manufacture one. Which store commerce then opens is commerce's decision to
// make from its own env — this function only ever narrows, never invents.
func TestCommerceMasterKeyWithoutAKeyStaysEmpty(t *testing.T) {
	if got := commerceMasterKey(nil, luxlog.New("test")); got != nil {
		t.Fatalf("no key in, no key out; got %d bytes", len(got))
	}
}
