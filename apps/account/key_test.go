package account

import (
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

// The minter's key verdict is GONE with the key. Nothing mints a token another
// process verifies, so there is no value two processes can disagree about and no
// boot question to ask about one. cloud.Intended reads Sec-Fetch-Site instead —
// a fact the browser states on the request, which needs no agreement at all.

// TestDeployedDecidesWhichKeyTheMinterMayHold pins the fact `own` reads. cloud.Deployed
// is false in this binary — nothing handed it a master key — which is exactly the
// laptop answer, so the minter's fallback stays available to the suite.
func TestDeployedDecidesWhichKeyTheMinterMayHold(t *testing.T) {
	if cloud.Deployed() {
		t.Fatal("a test binary reports itself a deployment; the minter's fallback is then untestable")
	}
}
