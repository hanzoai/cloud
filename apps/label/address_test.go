package label

// address_test.go pins WHERE this plane publishes, which is not a routing detail.
//
// EVERY ROUTE A CAPABILITY SERVES IS UNDER /v1/<its own name> (HIP-0139 §3.1). A
// second top-level address is a second capability or a misfiled route, never an
// alias, and the fleet ratchet that measures it (openapi/misfiled.txt) reads the
// woven document — so it can only speak after every app has been described. This
// gate asks the same question one plane earlier, of this plane's own projection,
// where the answer is cheap and names the route that moved.
//
// It used to assert the opposite rule, and the rule is what changed: openapi.Fold
// took an operation's product from the FIRST /v1 SEGMENT, so membership of the
// risk product had to be spelled into the address, and these seven operations
// answered under /v1/risk to be counted as risk. HIP-0139 §4.1 retires that — the
// tag is the app that serves the operation — so grouping no longer rides the
// address, and the address is free to name the capability that owns the store.
// The hazard the old rule guarded against is unchanged and now guarded by the
// same rule from the other side: an address under somebody else's name publishes
// these operations into somebody else's product, and the floor ratchet reads the
// arrival as growth because it refuses a shrink and only a shrink.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// app is this capability's one name — the package, the plugin, the tag, the
// address prefix. Stated once here and read by every assertion below, so the test
// cannot half-agree with itself.
const app = "label"

// product is the OPERATION-ID prefix these seven ids still carry. It is not the
// capability and no longer decides anything about the address; the ids and the
// schema names below them (riskLabelFact, riskLabelRecord) are the risk
// vocabulary this plane was cut out of, and renaming them is a separate change to
// that vocabulary rather than a consequence of the address moving.
const product = "risk"

// TestEveryAddressIsUnderThisCapabilitysName walks the live projection.
//
// It reads openapi.FleetSpec — the same function describe.go calls to write
// plugin/label/openapi.json, which is the file the fleet spec is woven from — so it
// asserts about the published document and not about a list of strings somebody
// kept in step with it.
func TestEveryAddressIsUnderThisCapabilitysName(t *testing.T) {
	zapp, _ := wireApp(t, "")
	doc, err := openapi.FleetSpec(zapp)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}

	roots := map[string]int{}
	paths, ops := 0, 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			// Every address this plane owns is under /v1. Anything else is a
			// liveness route the host owns, and it belongs to no capability.
			continue
		}
		paths++
		ops += len(item)
		roots[openapi.Product(path)] += len(item)
		if path != "/v1/"+app && !strings.HasPrefix(path, "/v1/"+app+"/") {
			t.Errorf("ADDRESS: %s is not under /v1/%s. A capability answers at its own name and "+
				"nowhere else (HIP-0139 §3.1): an address under another name publishes these "+
				"operations as part of that capability — its tag list, its SDK namespace and its "+
				"floor — and is routed to whichever sibling owns the root.", path, app)
		}
	}

	// A gate that examined nothing passes. Every way this could see zero paths — a
	// routes() that returned early on a missing registry, a projection that decoded
	// to nothing — is a defect that would otherwise arrive here as a green tick.
	if paths == 0 || ops == 0 {
		t.Fatal("the projection published no /v1 address — this gate proved nothing")
	}
	if len(roots) != 1 {
		t.Errorf("this plane answers at %d top-level addresses (%v); one capability, one address", len(roots), roots)
	}
	t.Logf("%d paths, %d operations, all under /v1/%s", paths, ops, app)
}

// TestTheOperationIDsCarryTheProduct. An operation id is the SDK method name and
// the CLI command, so an id prefixed for the wrong product survives the address
// being right — `client.MlLabel()` against /v1/label is a generated method
// that tells its caller the wrong thing about which product it is buying.
func TestTheOperationIDsCarryTheProduct(t *testing.T) {
	app, _ := wireApp(t, "")
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}
	seen := 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		for method, op := range item {
			if op.OperationID == "" {
				t.Errorf("%s %s publishes no operation id, so it has no SDK method and no CLI command", method, path)
				continue
			}
			seen++
			if !strings.HasPrefix(op.OperationID, product) {
				t.Errorf("OPERATION ID: %s %s is %q, which does not begin with %q — the generated "+
					"method would name a product this operation is not part of",
					method, path, op.OperationID, product)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no operation id was examined — this gate proved nothing")
	}
}
