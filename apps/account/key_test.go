package account

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// A valid shared key, as an operator would provision it: 32 bytes, hex.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// The verifier's boot check is GONE, and with it the test that pinned it. It
// refused a whole app — every route, every caller — for a key that gates one
// branch of one control, and it was asked by eleven apps that are all lazy, so
// the "loud at boot" it promised never happened once: the child exited on its
// first request instead. cloud.Intended is fail-closed without it (see
// TestAKeylessProcessRefusesTheChangeAndServesTheRead), and cloud.keyed is the
// composition-time rule, scoped to what a process actually SERVES.

// TestTheMinterKeepsItsOwnKeyOnALaptopAndRefusesItOnADeployment: the issuer accepts
// only the tokens it wrote, so one key over one lifetime is self-consistent and a
// machine with no KMS still works. A deployment is neither — it holds a master key,
// so it has a secret store, and a key per replica refuses every session that moves.
func TestTheMinterKeepsItsOwnKeyOnALaptopAndRefusesItOnADeployment(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if err := own(false); err != nil {
		t.Fatalf("a machine with no secret store was refused its own key: %v", err)
	}
	err := own(true)
	if err == nil {
		t.Fatal("a deployment came up on a key of its own; it would hold one value per replica")
	}
	if !strings.Contains(err.Error(), KeyEnv) {
		t.Errorf("the refusal does not name the value to set: %v", err)
	}

	t.Setenv(KeyEnv, testKey)
	if err := own(true); err != nil {
		t.Fatalf("a provisioned key was refused on a deployment: %v", err)
	}
}

// TestDeployedDecidesWhichKeyTheMinterMayHold pins the fact `own` reads. cloud.Deployed
// is false in this binary — nothing handed it a master key — which is exactly the
// laptop answer, so the minter's fallback stays available to the suite.
func TestDeployedDecidesWhichKeyTheMinterMayHold(t *testing.T) {
	if cloud.Deployed() {
		t.Fatal("a test binary reports itself a deployment; the minter's fallback is then untestable")
	}
}
