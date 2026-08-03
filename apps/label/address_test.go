package label

// address_test.go pins WHERE this plane publishes, which is not a routing detail.
//
// THE ADDRESS IS THE PRODUCT. openapi.Fold takes an operation's product tag from
// the first /v1 segment of its path and nothing else (openapi.Product) — a per-op
// zip.WithTags names a different axis and cannot override it. So an address is a
// published product membership: the fleet's tag list, the floor ratchet, the doc
// site's headings, every generated SDK's namespace and the CLI's command tree are
// all projections of that one segment.
//
// The first cut of this plane addressed /v1/ml/labels. /v1/ml is the KServe model
// SERVING product — four paths, live, with customers on them — so seven compliance
// operations with their own writers, their own retention clock and their own
// five-year floor would have been published as part of it. Nothing in the fleet
// would have said so: the floor ratchet reads `ml: 7 -> 14` as growth, because it
// refuses a shrink and only a shrink. It is the same mistake apps/risk's own
// manifest row already records having made and corrected once, one layer up.
//
// So the gate is here, at the plane, derived from the same projection the committed
// artifact is generated from.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// product is the one this plane belongs to: ground truth about what turned out to
// be fraud is part of Risk. It is stated once here and read by every assertion
// below, so the test cannot half-agree with itself.
const product = "risk"

// TestEveryAddressFilesIntoOneProductAndItIsRisk walks the live projection.
//
// It reads openapi.FleetSpec — the same function describe.go calls to write
// plugin/label/openapi.json, which is the file the fleet spec is woven from — so it
// asserts about the published document and not about a list of strings somebody
// kept in step with it.
func TestEveryAddressFilesIntoOneProductAndItIsRisk(t *testing.T) {
	app, _ := wireApp(t, "")
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}

	products := map[string]int{}
	paths, ops := 0, 0
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/") {
			// Every address this plane owns is under /v1. Anything else is a
			// liveness route the host owns, and it carries no product at all.
			continue
		}
		paths++
		ops += len(item)
		p := openapi.Product(path)
		products[p] += len(item)
		if p != product {
			t.Errorf("ADDRESS: %s files into product %q, not %q. openapi.Product reads the first /v1 "+
				"segment, so this path publishes these operations as part of somebody else's product — "+
				"its tag list, its SDK namespace and its floor. Address it under /v1/%s.", path, p, product, product)
		}
		if !strings.HasPrefix(path, "/v1/"+product+"/labels") {
			t.Errorf("ADDRESS: %s is not under /v1/%s/labels, which is the ONE prefix manifest.Apps "+
				"grants this app; a path outside it is delivered to whichever sibling owns it.", path, product)
		}
	}

	// A gate that examined nothing passes. Every way this could see zero paths — a
	// routes() that returned early on a missing registry, a projection that decoded
	// to nothing — is a defect that would otherwise arrive here as a green tick.
	if paths == 0 || ops == 0 {
		t.Fatal("the projection published no /v1 address — this gate proved nothing")
	}
	if len(products) != 1 {
		t.Errorf("this plane publishes into %d products (%v); one subsystem, one product", len(products), products)
	}
	t.Logf("%d paths, %d operations, all under /v1/%s/labels", paths, ops, product)
}

// TestTheOperationIDsCarryTheProduct. An operation id is the SDK method name and
// the CLI command, so an id prefixed for the wrong product survives the address
// being right — `client.MlLabel()` against /v1/risk/labels is a generated method
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
