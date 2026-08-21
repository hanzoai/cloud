package datasets

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
//
// The hazard is unchanged, and the new rule guards it from the other side. The
// first cut of this plane addressed /v1/ml/datasets — five paths, seven
// operations — beside the KServe model-SERVING plane's own four paths under
// /v1/ml, which is live and has customers on it. One prefix, two products. Nothing
// in the fleet said so: the floor ratchet read `ml: 7 -> 14` as growth, because it
// refuses a shrink and only a shrink.
//
// wire_test.go freezes the exact seven ADDRESSES, which catches a move. This
// catches the thing a frozen list of strings cannot: editing the freeze and the
// manifest row together is enough to move the whole plane under somebody else's
// name with every other gate green, so the invariant is asserted against the
// published document rather than against the list.

import (
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// app is this capability's one name — the package, the plugin, the tag, the
// address prefix. Stated once here and read by every assertion below, so the test
// cannot half-agree with itself.
const app = "datasets"

// product is the OPERATION-ID prefix these seven ids still carry, read by
// wire_test.go's id check. It is not the capability and no longer decides anything
// about the address; the ids and the schema names below them (riskDatasetSpec,
// riskDatasetRow) are the risk vocabulary this plane was cut out of, and renaming
// them is a separate change to that vocabulary rather than a consequence of the
// address moving.
const product = "risk"

// published is the document this plane projects of itself — the same call
// describe.go makes to write plugin/datasets/openapi.json, which is the file the
// fleet spec is woven from. So these assertions are about the published artifact
// and not about a list of strings somebody kept in step with it.
func published(t *testing.T) *openapi.Document {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("datasettest"), DisableStartupMessage: true})
	compose(app)
	if err := mount(newPlane(&fake{}), app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}
	return doc
}

// TestEveryAddressIsUnderThisCapabilitysName walks the live projection.
func TestEveryAddressIsUnderThisCapabilitysName(t *testing.T) {
	doc := published(t)

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
	// mount that returned early, a projection that decoded to nothing — is a defect
	// that would otherwise arrive here as a green tick.
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
// being right — `client.MlDatasets()` against /v1/datasets is a generated
// method that tells its caller the wrong thing about which product it is buying.
//
// wire_test.go asserts the same prefix off zip's TYPED registry. This asserts it
// off the published DOCUMENT, which is what the SDK generators actually read, and
// the two cannot disagree about which product because both read `product` above.
func TestTheOperationIDsCarryTheProduct(t *testing.T) {
	doc := published(t)
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

// TestEverySchemaNameCarriesTheProduct. The fleet's schema namespace is FLAT —
// openapi.Weave refuses one name with two shapes across apps — so a schema left
// prefixed for the product this plane no longer belongs to is a generated type
// named `MlDataset` returned by a `Risk` method, and the next app that reaches for
// the obvious name collides with it and cannot weave at all.
func TestEverySchemaNameCarriesTheProduct(t *testing.T) {
	doc := published(t)
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("the projection published no schema — this gate proved nothing")
	}
	for name := range doc.Components.Schemas {
		if !strings.HasPrefix(name, product) {
			t.Errorf("SCHEMA: %q does not begin with %q — the flat fleet namespace files it under a "+
				"product this plane is not part of", name, product)
		}
	}
	t.Logf("%d schemas, all prefixed %q", len(doc.Components.Schemas), product)
}
