package dataset

// address_test.go pins WHERE this plane publishes, which is not a routing detail.
//
// THE ADDRESS IS THE PRODUCT. openapi.Fold takes an operation's product tag from
// the first /v1 segment of its path and nothing else (openapi.Product) — a per-op
// zip.WithTags names a different axis and cannot override it, because Fold assigns
// op.Tags = shape.Tags from the router projection. So an address is a published
// product membership: the fleet's tag list, the floor ratchet, the doc site's
// headings, every generated SDK's namespace and the CLI's command tree are all
// projections of that one segment.
//
// The first cut of this plane addressed /v1/ml/datasets — five paths, seven
// operations — beside the KServe model-SERVING plane's own four paths under
// /v1/ml, which is live and has customers on it. One prefix, two products. Nothing
// in the fleet said so: the floor ratchet read `ml: 7 -> 14` as growth, because it
// refuses a shrink and only a shrink. It is the same mistake apps/label and
// apps/reference each record having made and corrected once, and the third time it
// stops being a recollection and becomes this gate.
//
// wire_test.go freezes the exact seven ADDRESSES, which catches a move. This
// catches the thing a frozen list of strings cannot: the DERIVED product. Editing
// the freeze and the manifest row together is enough to re-file the whole plane
// under somebody else's product with every other gate green — so the invariant is
// asserted where it actually lives, on openapi.Product's reading of the published
// document.

import (
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// product is the one this plane belongs to: a dataset is a versioned snapshot the
// risk model is fitted on, so it is part of Risk. It is stated once here and read
// by every assertion below AND by wire_test.go's operation-id check, so the two
// files cannot half-agree with each other.
const product = "risk"

// published is the document this plane projects of itself — the same call
// describe.go makes to write plugin/dataset/openapi.json, which is the file the
// fleet spec is woven from. So these assertions are about the published artifact
// and not about a list of strings somebody kept in step with it.
func published(t *testing.T) *openapi.Document {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("datasettest"), DisableStartupMessage: true})
	if err := mount(newPlane(&fake{}), app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		t.Fatalf("FleetSpec: %v", err)
	}
	return doc
}

// TestEveryAddressFilesIntoOneProductAndItIsRisk walks the live projection.
func TestEveryAddressFilesIntoOneProductAndItIsRisk(t *testing.T) {
	doc := published(t)

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
		if !strings.HasPrefix(path, "/v1/"+product+"/datasets") {
			t.Errorf("ADDRESS: %s is not under /v1/%s/datasets, which is the ONE prefix manifest.Apps "+
				"grants this app; a path outside it is delivered to whichever sibling owns it.", path, product)
		}
	}

	// A gate that examined nothing passes. Every way this could see zero paths — a
	// mount that returned early, a projection that decoded to nothing — is a defect
	// that would otherwise arrive here as a green tick.
	if paths == 0 || ops == 0 {
		t.Fatal("the projection published no /v1 address — this gate proved nothing")
	}
	if len(products) != 1 {
		t.Errorf("this plane publishes into %d products (%v); one subsystem, one product", len(products), products)
	}
	t.Logf("%d paths, %d operations, all under /v1/%s/datasets", paths, ops, product)
}

// TestTheOperationIDsCarryTheProduct. An operation id is the SDK method name and
// the CLI command, so an id prefixed for the wrong product survives the address
// being right — `client.MlDatasets()` against /v1/risk/datasets is a generated
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
