// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The payment ops exist to be CALLED BY AN AGENT, and an agent can only call what
// the registry describes. These tests pin the two facts that make that true and
// that nothing else in the build would notice breaking: that both ops are in the
// registry at all, and that their inputs are real SCHEMAS rather than open blobs.
//
// The second is the one worth a test. A route registered raw still answers curl,
// so a human would not notice it had stopped being a tool; and a typed op whose In
// degenerated to map[string]any still compiles, still serves, and still appears in
// tools/list — as a tool with no parameters, which an agent cannot use and cannot
// be told why. Both failures are silent everywhere except here.

// toolsFor registers the payment surface on a throwaway app and returns its MCP
// projection — the SAME projection api.hanzo.ai/v1/mcp serves, read in-process.
func toolsFor(t *testing.T) map[string]map[string]any {
	t.Helper()
	app := zip.New(zip.Config{})
	// The REAL screen, because there is no other kind now and because that is the
	// point: the screen is inside the op's handler, so this projection is taken off
	// exactly the registration an agent calls. The description, the schema and the name
	// are properties of the op's declared types and its doc comment, which a wrapped
	// handler does not touch — proven here by these tests still reading them.
	exposePayments(app, riskGate(luxlog.New("tooltest")))
	exposeInvoices(app)
	out := map[string]map[string]any{}
	for _, tool := range app.MCPTools() {
		name, _ := tool["name"].(string)
		out[name] = tool
	}
	return out
}

// TestPaymentOpsAreTools is the acceptance criterion in test form: after this
// registration, `tools/list` contains a tool that takes a payment and a tool that
// reads one back. Before this file the fleet published 570 tools and none of them
// could take money.
func TestPaymentOpsAreTools(t *testing.T) {
	tools := toolsFor(t)
	for _, want := range []string{"takePayment", "getPayment"} {
		if _, ok := tools[want]; !ok {
			got := make([]string, 0, len(tools))
			for n := range tools {
				got = append(got, n)
			}
			t.Fatalf("tool %q missing from the MCP projection; got %v", want, got)
		}
	}
}

// TestTakePaymentIsTyped pins the PARAMETERS. An agent handed a tool with no
// declared inputs has to guess the body, which is the difference between a tool
// it can call and a tool it can only fail at — and it is exactly what an untyped
// JSON-blob write publishes.
func TestTakePaymentIsTyped(t *testing.T) {
	tool, ok := toolsFor(t)["takePayment"]
	if !ok {
		t.Fatal("takePayment not registered")
	}
	schema, err := json.Marshal(tool["inputSchema"])
	if err != nil {
		t.Fatalf("marshal inputSchema: %v", err)
	}
	var s struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("inputSchema is not an object schema: %v (%s)", err, schema)
	}
	if s.Type != "object" {
		t.Fatalf("inputSchema.type = %q, want object — got %s", s.Type, schema)
	}
	// The four fields that make the op callable. sourceId and amountCents are what
	// a charge IS; idempotencyKey is what keeps an agent's retry from charging
	// twice, so an agent that cannot see it cannot be safe.
	for _, field := range []string{"sourceId", "amountCents", "currency", "idempotencyKey"} {
		if _, ok := s.Properties[field]; !ok {
			t.Errorf("takePayment inputSchema has no %q property; schema=%s", field, schema)
		}
	}
	// An org field would be a cross-tenant write the caller asserted for itself.
	// The paying org comes off the validated principal and must never be nameable.
	for _, forbidden := range []string{"org", "organization", "subject", "userId", "test"} {
		if _, ok := s.Properties[forbidden]; ok {
			t.Errorf("takePayment publishes %q as an input — identity and mode are never caller-supplied; schema=%s", forbidden, schema)
		}
	}
}

// TestGetPaymentIsTyped pins the read op's one parameter, for the same reason.
func TestGetPaymentIsTyped(t *testing.T) {
	tool, ok := toolsFor(t)["getPayment"]
	if !ok {
		t.Fatal("getPayment not registered")
	}
	schema, _ := json.Marshal(tool["inputSchema"])
	if !strings.Contains(string(schema), `"id"`) {
		t.Fatalf("getPayment inputSchema does not declare id: %s", schema)
	}
}

// TestPaymentToolsDescribeThemselves pins that each tool carries prose. A tool
// with an empty description is one a model has to guess the purpose of from its
// name, and the whole point of the zipdoc pipeline is that the doc comment on the
// handler becomes that description.
func TestPaymentToolsDescribeThemselves(t *testing.T) {
	for name, tool := range toolsFor(t) {
		if desc, _ := tool["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("tool %q has no description", name)
		}
	}
}

// TestPaymentOpsRefuseAnonymousCallers is the security half. Off the HTTP path
// there is no validated principal, so there is no org — and the honest answer to
// "whose card is this and whose balance goes up" is nobody, not the platform's.
// A default here would be a money write attributed to a tenant nobody proved.
//
// BOTH LAYERS ARE ASKED, so neither is the only one refusing: the REGISTERED handler
// (the screened one every projection dispatches to) and the money core underneath it.
// A context with no request behind it is exactly the CLI's LocalInvoke, and it is also
// the state a screen cannot resolve a payer in — screened as that state, then refused
// by the core's own gate, never waved past because the screen had nothing to judge.
func TestPaymentOpsRefuseAnonymousCallers(t *testing.T) {
	o := paymentOps{}
	in := &PaymentIn{SourceID: "cnon:card-nonce-ok", AmountCents: 500}
	if _, err := o.take(riskGate(luxlog.New("tooltest")))(context.Background(), in); err == nil {
		t.Fatal("the registered payment op accepted a call with no validated org — a payment must " +
			"never be attributed to a guess")
	}
	if _, err := o.charge(context.Background(), in); err == nil {
		t.Fatal("the money core accepted a call with no validated org")
	}
	if _, err := o.get(context.Background(), &PaymentRef{ID: "whatever"}); err == nil {
		t.Fatal("get accepted a call with no validated org")
	}
}

// TestInvoiceLifecycleIsTools pins that the WHOLE lifecycle is agent-callable, not
// just the half that reads. Before this, cloud mounted the invoice list and the
// PDF and nothing that could raise one — an org could read invoices it had no way
// to create, and no invoice tool existed at any address.
func TestInvoiceLifecycleIsTools(t *testing.T) {
	tools := toolsFor(t)
	for _, want := range []string{"raiseInvoice", "issueInvoice", "collectInvoice", "voidInvoice", "getInvoice"} {
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
	tool, ok := toolsFor(t)["raiseInvoice"]
	if !ok {
		t.Fatal("raiseInvoice not registered")
	}
	schema, _ := json.Marshal(tool["inputSchema"])
	for _, want := range []string{`"userId"`, `"lines"`, `"$defs"`, `"description"`, `"amount"`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("raiseInvoice schema is missing %s — an agent cannot construct a line item from it; schema=%s", want, schema)
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
	if _, err := o.raise(ctx, &RaiseInvoiceIn{UserID: "someone"}); err == nil {
		t.Error("raise accepted a call with no validated org")
	}
	if _, err := o.issue(ctx, &InvoiceRefIn{ID: "x"}); err == nil {
		t.Error("issue accepted a call with no validated org")
	}
	if _, err := o.collect(ctx, &InvoiceRefIn{ID: "x"}); err == nil {
		t.Error("collect accepted a call with no validated org")
	}
	if _, err := o.void(ctx, &InvoiceRefIn{ID: "x"}); err == nil {
		t.Error("void accepted a call with no validated org")
	}
	if _, err := o.read(ctx, &InvoiceRefIn{ID: "x"}); err == nil {
		t.Error("read accepted a call with no validated org")
	}
}
