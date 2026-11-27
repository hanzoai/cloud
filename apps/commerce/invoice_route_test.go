// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The invoice rail as the routes serve it: commerce's ops stood on their own
// socket (a separate process in production, so every answer crosses the plane's
// encoding), the billing routes in front of them, and the error renderer the host
// installs. And the shape commerce answers in, decoded into cloud's client types,
// field by field.

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercesecrets "github.com/hanzoai/commerce/secrets"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/billing"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/sdk"
)

// stood stands commerce's invoice ops on commerce's socket, as its own process
// serves them, so a route reaches them over the plane and not by a function call.
func stood(t *testing.T) {
	t.Helper()
	peer := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	peer.Post("/billing/subscribe/invoice", planeOrderInvoice, zip.WithOperationID(client.BillingSubscribeInvoice))
	peer.Post("/billing/order/hosted", planeOrderHosted, zip.WithOperationID(client.BillingOrderHosted))
	peer.Post("/billing/order/hosted/pdf", planeOrderHostedPDF, zip.WithOperationID(client.BillingOrderHostedPDF))
	sock := zip.SocketPath("commerce")
	listened := make(chan error, 1)
	go func() { listened <- peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for range 300 {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		select {
		case err := <-listened:
			t.Fatalf("commerce did not listen on %s: %v", sock, err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("commerce never listened on %s", sock)
}

// edge is the billing routes behind the host's error renderer: behind the
// identity boundary itself, with the IAM that signs its tokens (identify), or
// behind the bridge alone, with the caller stated in the headers the boundary
// mints.
func edge(t *testing.T, identify bool) (*zip.App, *iamtest.Issuer0) {
	t.Helper()
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), ErrorHandler: cloud.ErrorHandler})
	var iss *iamtest.Issuer0
	if identify {
		iss = iamtest.New(t)
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	} else {
		app.Use(cloud.Bridge())
	}
	if err := billing.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("billing: %v", err)
	}
	return app, iss
}

// do sends req to app and answers the response and its body.
func do(t *testing.T, app *zip.App, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// unkept reports whether resp is marked to be kept nowhere.
func unkept(resp *http.Response) bool {
	return resp.Header.Get("Cache-Control") == "no-store" && resp.Header.Get("X-Robots-Tag") == "noindex" &&
		resp.Header.Get("Referrer-Policy") == "no-referrer"
}

// Anyone holding the link reads the invoice through the identity boundary with no
// sign-in, on the brand's API host: the order nested, how to pay it inside it, no
// address and no receipt, and nothing kept by a cache, an index or a referrer. The
// PDF the same. On another brand's host, or by a link that names nothing, it is a
// miss, kept nowhere either.
func TestTheHostedInvoiceThroughTheBoundary(t *testing.T) {
	invoicing(t)
	stood(t)
	org := fresh("invco")
	got, err := planeOrderInvoice(buyer(org), orderFor(org, fresh("k")))
	if err != nil {
		t.Fatal(err)
	}
	link := strings.TrimPrefix(got.Order.HostedURL, "https://hanzo.ai/pay/invoice/")
	app, _ := edge(t, true)
	read := func(host, path string) (*http.Response, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		return do(t, app, req)
	}
	resp, body := read("api.hanzo.ai", "/v1/billing/invoices/hosted/"+link)
	if resp.StatusCode != http.StatusOK || !unkept(resp) {
		t.Fatalf("hosted: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"order", "issuer", "billTo"} {
		if len(doc[k]) == 0 {
			t.Errorf("the hosted answer lacks %s: %s", k, body)
		}
	}
	var order map[string]json.RawMessage
	if err := json.Unmarshal(doc["order"], &order); err != nil {
		t.Fatalf("order: %v in %s", err, body)
	}
	for _, k := range []string{"invoiceId", "number", "reference", "status", "amountCents", "balanceCents", "currency", "issuedAt",
		"dueAt", "voidAt", "lines", "wire", "howToPay", "hostedUrl", "pdfUrl", "notice", "unpaid"} {
		if len(order[k]) == 0 || string(order[k]) == "null" {
			t.Errorf("the hosted order lacks %s: %s", k, doc["order"])
		}
	}
	var lines []map[string]any
	if json.Unmarshal(order["lines"], &lines) != nil || len(lines) != 2 || lines[0]["amountCents"] != float64(480000) || lines[0]["unitCents"] != float64(24000) {
		t.Errorf("lines read %s", order["lines"])
	}
	if strings.Contains(string(body), "@") || order["payment"] != nil || order["email"] != nil {
		t.Errorf("the hosted answer carries an address or a receipt: %s", body)
	}
	resp, body = read("api.hanzo.ai", "/v1/billing/invoices/hosted/"+link+"/pdf")
	if resp.StatusCode != http.StatusOK || !unkept(resp) || resp.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(body), "%PDF") {
		t.Fatalf("pdf: %d %v", resp.StatusCode, resp.Header)
	}
	for _, miss := range []struct{ host, path string }{
		{"api.lux.network", "/v1/billing/invoices/hosted/" + link},
		{"api.lux.network", "/v1/billing/invoices/hosted/" + link + "/pdf"},
		{"api.hanzo.ai", "/v1/billing/invoices/hosted/" + strings.Repeat("x", 43)},
	} {
		if resp, body := read(miss.host, miss.path); resp.StatusCode != http.StatusNotFound || !unkept(resp) {
			t.Errorf("%s%s: %d %v %s", miss.host, miss.path, resp.StatusCode, resp.Header, body)
		}
	}
}

// Every refusal of an order by invoice a page acts on reaches it with its code,
// through the route and across the plane, and the open invoice it names travels
// with the refusal: the page reads the code, never the sentence.
func TestAnInvoiceOrderAnswersItsCodeOverHTTP(t *testing.T) {
	invoicing(t)
	stood(t)
	app, _ := edge(t, false)
	org := fresh("invco")
	order := func(body, key string, chosen bool) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/invoice", strings.NewReader(body))
		req.Host = "api.hanzo.ai"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Id", "ada")
		req.Header.Set("X-User-Email", "ap@"+org+".test")
		req.Header.Set("X-User-IsOrgAdmin", "true")
		req.Header.Set("X-Idempotency-Key", key)
		if chosen {
			req.Header.Set("X-Org-Id", org)
		}
		resp, raw := do(t, app, req)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		return resp.StatusCode, doc
	}
	const twenty = `{"planId":"enterprise","quantity":20,"interval":"year","balanceCents":4000000}`
	status, first := order(twenty, "k1", true)
	if status != http.StatusCreated || first["invoiceId"] == nil {
		t.Fatalf("the order: %d %v", status, first)
	}
	status, open := order(twenty, "k2", true)
	if status != http.StatusConflict || open["code"] != "invoice_open" || open["number"] != first["number"] || open["invoiceId"] != first["invoiceId"] ||
		!strings.HasPrefix(open["hostedUrl"].(string), "https://hanzo.ai/pay/invoice/") || open["hostedUrl"] == first["hostedUrl"] {
		t.Fatalf("an open invoice: %d %v", status, open)
	}
	for code, c := range map[string]struct {
		body   string
		key    string
		status int
	}{
		"key_reused":           {`{"planId":"enterprise","quantity":25,"interval":"year","balanceCents":4000000}`, "k1", http.StatusConflict},
		"seats_out_of_range":   {`{"planId":"enterprise","quantity":19,"interval":"year"}`, "k3", http.StatusBadRequest},
		"balance_out_of_range": {`{"planId":"enterprise","quantity":20,"interval":"year","balanceCents":10000100}`, "k4", http.StatusBadRequest},
	} {
		if status, doc := order(c.body, c.key, true); status != c.status || doc["code"] != code {
			t.Errorf("%s: %d %v", code, status, doc)
		}
	}
	// A signed-in buyer who chose no org acts in their own: the order is refused,
	// never invoiced to that org.
	signed, iss := edge(t, true)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe/invoice", strings.NewReader(twenty))
	req.Host = "api.hanzo.ai"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+iss.Sign(t, iamtest.Claims{Sub: "7a1e0000-0000-4000-8000-0000000000c1", PreferredUsername: "ada", Owner: org, Orgs: []map[string]any{{"org": org, "role": "owner"}}}))
	if resp, raw := do(t, signed, req); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"code":"org_required"`) {
		t.Errorf("no org chosen: %d %s", resp.StatusCode, raw)
	}
	staff := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "z", Admin: true})
	if _, err := riskGate(luxlog.New("invoicetest")).planeOrderSettle(staff, &client.InvoiceSettleIn{
		InvoiceID: first["invoiceId"].(string), Receipt: fresh("FED"), AmountCents: int64(first["amountCents"].(float64))}); err != nil {
		t.Fatal(err)
	}
	if status, doc := order(twenty, "k6", true); status != http.StatusConflict || doc["code"] != "plan_held" {
		t.Errorf("a plan held: %d %v", status, doc)
	}
	commercesecrets.Set(bank{})
	if status, doc := order(twenty, "k7", true); status != http.StatusServiceUnavailable || doc["code"] != "wire_not_configured" {
		t.Errorf("no bank: %d %v", status, doc)
	}
}

// jsonKeys are the members a struct type is written with.
func jsonKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// written are the members v is written with.
func written(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// decoded is commerce's v, written as commerce writes it and read as cloud's
// client type reads it.
func decoded[T any](t *testing.T, v any) *T {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out := new(T)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%T does not read %T: %v", out, v, err)
	}
	return out
}

// The invoice rail is ONE SHAPE on both sides: what commerce answers — an order,
// a hosted read, a settlement, a void, a refund, a staff read — is read by
// cloud's client types without losing a member, and every member of the client
// types is one commerce fills, but where the order is read (cloud's own). A
// member added on one side and not the other fails here, by name.
func TestTheInvoiceRailIsOneShape(t *testing.T) {
	invoicing(t)
	super := superAdmin(context.Background())
	yes, no := true, false
	if _, err := planeCreate(super, &client.PlanSet{Slug: "enterprise-b", Name: "Enterprise B", Category: "enterprise",
		Price: 30000, AnnualTotal: 30000, Interval: "year", Status: "active", PerSeat: &yes, ContactSales: &no,
		Limits: &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(2000), BalanceMonths: ptr(1),
			BalanceCeiling: ptr(100000), InvoiceDays: ptr(14), GraceDays: ptr(7)}}); err != nil {
		t.Fatal(err)
	}
	name := fresh("invco")
	ctx := buyer(name)
	org, err := orgOf(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	in := func(plan string) commercebilling.OrderIn {
		return commercebilling.OrderIn{Subject: name, Email: "ap@" + name + ".test", PlanID: plan, Quantity: 20,
			Interval: "year", BalanceCents: 4000000, Host: "hanzo.ai"}
	}
	ordered, _, err := commercebilling.OrderInvoice(ctx, org, in("enterprise"))
	if err != nil {
		t.Fatal(err)
	}
	hosted, err := commercebilling.HostedInvoice(ctx, ordered.Link, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	paid, err := commercebilling.SettleInvoice(ctx, org, commercebilling.SettleIn{InvoiceID: ordered.InvoiceID, Receipt: fresh("FED"), AmountCents: ordered.AmountCents})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := commercebilling.OrderInvoice(ctx, org, in("enterprise-b"))
	if err != nil {
		t.Fatal(err)
	}
	replacing, err := commercebilling.SettleInvoice(ctx, org, commercebilling.SettleIn{InvoiceID: second.InvoiceID, Receipt: fresh("FED"),
		AmountCents: second.AmountCents + 100, Difference: "over"})
	if err != nil {
		t.Fatal(err)
	}
	refunded, err := commercebilling.RefundOrder(ctx, org, ordered.InvoiceID, 100000, fresh("RF"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := commercebilling.Order(ctx, org, second.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	other := fresh("invco")
	octx := buyer(other)
	oorg, err := orgOf(octx, "test")
	if err != nil {
		t.Fatal(err)
	}
	oin := in("enterprise")
	oin.Subject = other
	gone, _, err := commercebilling.OrderInvoice(octx, oorg, oin)
	if err != nil {
		t.Fatal(err)
	}
	voided, err := commercebilling.VoidOrder(octx, oorg, gone.InvoiceID, "duplicate")
	if err != nil {
		t.Fatal(err)
	}

	// Every member commerce writes is one cloud reads, but what cloud keeps to itself.
	kept := map[string]bool{"email": true, "brand": true, "link": true, "net": true}
	orderKeys := jsonKeys(reflect.TypeFor[client.InvoiceOrder]())
	orders := []*commercebilling.InvoiceOrder{ordered, hosted.Order, paid.Order, replacing.Order, refunded, read, voided}
	for _, o := range orders {
		for k := range written(t, o) {
			if !orderKeys[k] && !kept[k] {
				t.Errorf("commerce writes order member %q, which client.InvoiceOrder does not read", k)
			}
		}
	}
	// Every member cloud reads is one commerce fills, in one answer or another,
	// but where the order is read, which cloud adds.
	filled := map[string]bool{}
	lineFilled := map[string]bool{}
	for _, o := range orders {
		v := reflect.ValueOf(decoded[client.InvoiceOrder](t, o)).Elem()
		for i := range v.NumField() {
			if !v.Field(i).IsZero() {
				filled[v.Type().Field(i).Name] = true
			}
		}
		for _, l := range decoded[client.InvoiceOrder](t, o).Lines {
			lv := reflect.ValueOf(l)
			for i := range lv.NumField() {
				if !lv.Field(i).IsZero() {
					lineFilled[lv.Type().Field(i).Name] = true
				}
			}
		}
	}
	for f := range reflect.TypeFor[client.InvoiceOrder]().Fields() {
		if !filled[f.Name] && f.Name != "HostedURL" && f.Name != "PDFURL" {
			t.Errorf("client.InvoiceOrder.%s is never filled from commerce's answers", f.Name)
		}
	}
	for f := range reflect.TypeFor[client.OrderLine]().Fields() {
		if !lineFilled[f.Name] {
			t.Errorf("client.OrderLine.%s is never filled from commerce's lines", f.Name)
		}
	}
	for k := range written(t, ordered.Wire) {
		if !jsonKeys(reflect.TypeFor[client.WireInstructions]())[k] {
			t.Errorf("commerce writes wire member %q, which client.WireInstructions does not read", k)
		}
	}
	// The hosted read nests the order, and reads whole.
	if w, c := written(t, hosted), jsonKeys(reflect.TypeFor[client.HostedInvoice]()); !reflect.DeepEqual(w, c) {
		t.Errorf("commerce writes the hosted read as %v; cloud reads %v", w, c)
	}
	h := decoded[client.HostedInvoice](t, hosted)
	if h.Order == nil || h.Order.InvoiceID != ordered.InvoiceID || len(h.Order.HowToPay) == 0 || h.Order.Wire == nil || h.BillTo == "" || h.Issuer == "" {
		t.Errorf("the hosted read decodes as %+v", h)
	}
	// A settlement reads whole, and says what it did.
	for _, s := range []*commercebilling.Settled{paid, replacing} {
		for k := range written(t, s) {
			if !jsonKeys(reflect.TypeFor[client.InvoiceSettled]())[k] {
				t.Errorf("commerce writes settlement member %q, which client.InvoiceSettled does not read", k)
			}
		}
	}
	got := decoded[client.InvoiceSettled](t, replacing)
	if got.OverCents != 100 || got.ReceivedCents != second.AmountCents+100 || got.Payment == "" || got.SubscriptionID == "" ||
		got.Order == nil || got.Order.Replaced != paid.SubscriptionID {
		t.Errorf("a settlement decodes as %+v", got)
	}
}

// One wire is one key on both rails: commerce keys a receipt (WireKey) as the
// admin console keys a wire it records (core.KeyOf), whatever the spelling.
func TestOneWireKeyOnBothRails(t *testing.T) {
	for _, receipt := range []string{"FED-2026-0001", "fed 2026 0001", "wire:FED20260001", " Fed/2026.0001 ", "WIRE: wire:fed-2026-0001", "UETR 3f2a-77"} {
		if got, want := commercebilling.WireKey(receipt), testWireKeyOf("wire:"+receipt); got != want {
			t.Errorf("%q: commerce keys %q, the console %q", receipt, got, want)
		}
	}
}

func testWireKeyOf(payment string) string {
	p := strings.TrimSpace(payment)
	for len(p) >= 5 && strings.EqualFold(p[:5], "wire:") {
		p = strings.TrimSpace(p[5:])
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(p) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "wire:" + b.String()
}
