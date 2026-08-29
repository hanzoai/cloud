// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// The invoice ops exist to be CALLED BY AN AGENT, and an agent can only call what
// the registry describes. These tests pin the two facts that make that true and
// that nothing else in the build would notice breaking: that the ops are in the
// registry at all, and that their inputs are real SCHEMAS rather than open blobs.
//
// The second is the one worth a test. A route registered raw still answers curl,
// so a human would not notice it had stopped being a tool; and a typed op whose In
// degenerated to map[string]any still compiles, still serves, and still appears in
// tools/list — as a tool with no parameters, which an agent cannot use and cannot
// be told why. Both failures are silent everywhere except here.
//
// It covered the typed PAYMENT ops too, at POST /v1/commerce/payments. That was a
// second public address onto the one card money move the browser top-up already
// reached, so it was retired; the surviving door is POST /v1/billing/topup/token
// and its screen is proven in risk_payments_test.go.

// toolsFor returns the invoice surface's MCP projection — the SAME projection
// api.hanzo.ai/v1/mcp serves, read in-process.
//
// The invoice lifecycle publishes on the INTERNAL PLANE — it is reached by name
// from the money endpoint rather than mounted on an app's edge — so its projection
// is read off the plane's registry.
func toolsFor(t *testing.T) map[string]map[string]any {
	t.Helper()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeInvoices()

	out := map[string]map[string]any{}
	for _, tool := range cloud.Plane().MCPTools() {
		name, _ := tool["name"].(string)
		out[name] = tool
	}
	return out
}

// reads like an outage.
func TestInvoiceLifecycleIsTools(t *testing.T) {
	tools := toolsFor(t)
	for _, want := range []string{
		plane.BillingInvoiceRaise, plane.BillingInvoiceIssue, plane.BillingInvoiceCollect,
		plane.BillingInvoiceVoid, plane.BillingInvoiceRead, plane.BillingInvoices,
		plane.BillingInvoicePDF,
	} {
		if _, ok := tools[want]; !ok {
			t.Errorf("tool %q missing — the invoice lifecycle is not fully callable", want)
		}
	}
}

// TestRaiseInvoiceIsTyped pins the one input that is structurally hard to publish
// and therefore the one most likely to silently degrade to a blob: the LINE ITEMS.
// A lines array that lost its element schema still compiles and still serves, and
// an agent handed it can only guess what a line looks like.
func TestRaiseInvoiceIsTyped(t *testing.T) {
	tool, ok := toolsFor(t)[plane.BillingInvoiceRaise]
	if !ok {
		t.Fatal(plane.BillingInvoiceRaise + " not registered")
	}
	schema, _ := json.Marshal(tool["inputSchema"])
	for _, want := range []string{`"userId"`, `"lines"`, `"$defs"`, `"description"`, `"amount"`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("the raise-invoice schema is missing %s — an agent cannot construct a line item from it; schema=%s", want, schema)
		}
	}
	// A caller-named org would be a cross-tenant invoice.
	if strings.Contains(string(schema), `"org"`) {
		t.Errorf("raiseInvoice publishes org as an input; schema=%s", schema)
	}
}

// TestInvoiceOpsRefuseAnonymousCallers is the tenancy half: every lifecycle op
// must refuse a caller with no validated org rather than defaulting to one.
func TestInvoiceOpsRefuseAnonymousCallers(t *testing.T) {
	o := invoiceOps{}
	ctx := context.Background()
	if _, err := o.raise(ctx, &plane.RaiseIn{UserID: "someone"}); err == nil {
		t.Error("raise accepted a call with no validated org")
	}
	if _, err := o.issue(ctx, &plane.InvoiceRef{ID: "x"}); err == nil {
		t.Error("issue accepted a call with no validated org")
	}
	if _, err := o.collect(ctx, &plane.InvoiceRef{ID: "x"}); err == nil {
		t.Error("collect accepted a call with no validated org")
	}
	if _, err := o.void(ctx, &plane.InvoiceRef{ID: "x"}); err == nil {
		t.Error("void accepted a call with no validated org")
	}
	if _, err := o.read(ctx, &plane.InvoiceRef{ID: "x"}); err == nil {
		t.Error("read accepted a call with no validated org")
	}
}
