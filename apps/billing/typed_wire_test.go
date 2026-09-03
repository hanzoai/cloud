package billing

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of this package's OWN operations that are
// not typed ops, each with the wire fact that keeps it raw.
//
// NINE, in two families, and both come down to one property: THE ANSWER IS
// COMMERCE'S OWN BYTES. This package does not own the ledger; it is the customer
// face in front of it. A byte relay typed here would mean inventing a Go shape
// for somebody else's money, published as if it were ours — the second
// definition the plane exists to prevent, and the one that goes stale silently
// because nothing compares the two.
//
// The saved-method family splits ON THE ANSWER rather than on the resource: the
// two reads and the save are rendered documents and stay raw, while the DETACH
// answers a value and is typed — twice, at both addresses, registered explicitly
// because cmd/zipdoc refuses a computed path and a route it cannot place is prose
// silently dropped from the document and the tool.
var untypedByDesign = map[string]string{
	"GET /v1/billing/usage": "forwards commerce's own status and rows and then enriches them; the shape " +
		"is commerce's either leg, never one declared here.",
	"GET /v1/billing/balance": "forwards commerce's own body and status; the co-resident leg writes the " +
		"same envelope from the ledger, so the shape is the ledger's.",
	"GET /v1/billing/plans": "answers the catalog document commerce rendered, verbatim bytes — a public " +
		"read that states no tenant and re-marshals nothing.",
	"GET /v1/billing/invoices/{id}/pdf": "answers PDF BYTES. A typed op's only response path is c.JSON.",
	"GET /v1/billing/methods":           "answers the document commerce rendered, verbatim bytes.",
	"POST /v1/billing/methods": "answers the document commerce rendered, verbatim bytes, under commerce's " +
		"own status.",
	"GET /v1/billing/portal/methods":  "the same rendered document at the address a hosted checkout uses.",
	"POST /v1/billing/portal/methods": "the same rendered document at the hosted-checkout address.",
	"POST /v1/billing/subscribe/card": "answers TWO statuses over two different bodies, and one is " +
		"VERBATIM: a replayed sale returns the sealed bytes the first attempt sent (200), a fresh one " +
		"the sale just made (201). A typed op declares one Out, and re-marshalling a sealed replay is " +
		"no longer the same bytes.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to what this
// package registers, so a route added untyped goes red without anyone
// remembering this file and a name that stops being served goes red too.
//
// It quantifies over THIS PACKAGE'S registrations rather than over the whole
// /v1/billing prefix, because the co-resident commerce app serves six operations
// under the same prefix and holding this package to another's surface would make
// the gate fail for work it cannot do.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountApp(t, "")
	doc, err := openapi.Spec(app, openapi.Info{Title: "billing", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/billing") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/billing") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if typed[key] {
			continue
		}
		if _, named := untypedByDesign[key]; !named {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"An untyped route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it, or name it in untypedByDesign with the WIRE FACT that keeps it raw "+
			"— re-read against the pinned zip, never inherited from an older pass.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this surface no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
}

// TestEveryTypedOpIsDescribed: prose is the product surface. A typed op with no
// description reaches the document, every generated SDK and the MCP tool list as
// a name and a shape with nothing saying what it does.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t, ""), openapi.Info{Title: "billing", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/billing") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if _, named := untypedByDesign[key]; named {
				continue
			}
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/billing/...", key)
			}
		}
	}
}
