package reference

import (
	"testing"

	"github.com/hanzoai/cloud/sdk/botauth"
)

// TestDownloadsAreSigned pins the transport every publisher download leaves
// through to the deployment's Web Bot Auth signer (hanzo-inc/cloud#423).
func TestDownloadsAreSigned(t *testing.T) {
	if !botauth.Signs(signed) {
		t.Fatal("reference downloads do not sign: they leave without Web Bot Auth")
	}
}
