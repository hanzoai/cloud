package integrations

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
)

// A plugin is a process: an error crossing that wire is rebuilt from its STRING,
// so the sentinel is lost and every caller that distinguishes "absent" from
// "broken" takes the broken branch. This pins the repair.
func TestFlattenedNotFoundIsRecognised(t *testing.T) {
	// exactly what production logged
	wire := errors.New("kms kms_get: kms.get: store: secret not found")
	if errors.Is(wire, kms.ErrSecretNotFound) {
		t.Fatal("precondition: the flattened wire error must NOT already match the sentinel")
	}
	if !isNotFoundText(wire) {
		t.Fatal("the store's own not-found phrasing must be recognised")
	}
	restored := fmt.Errorf("%s: %w", wire.Error(), kms.ErrSecretNotFound)
	if !errors.Is(restored, kms.ErrSecretNotFound) {
		t.Fatal("after repair errors.Is must see the sentinel")
	}
}

// Narrow on purpose: a genuine failure that merely mentions something missing
// must NOT be laundered into "unlinked", which would hide a broken store.
func TestUnrelatedErrorsAreNotLaundered(t *testing.T) {
	for _, msg := range []string{
		"kms: master key missing",
		"dial tcp: connection refused",
		"user not found",
		"kms.get: permission denied",
	} {
		if isNotFoundText(errors.New(msg)) {
			t.Errorf("%q must not be treated as a missing secret", msg)
		}
	}
}
