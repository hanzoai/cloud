// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/organization"
	commercesecrets "github.com/hanzoai/commerce/secrets"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// bank is the brand's secret plane in a test: orgs/<org>/… answers only a call
// acting for <org>, as the host's does.
type bank map[string]string

func (b bank) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	rest, ok := strings.CutPrefix(ref, "orgs/")
	if !ok {
		return nil, errors.New("not a tenant ref")
	}
	if org, _, _ := strings.Cut(rest, "/"); zip.CallerOf(ctx).Org != org {
		return nil, errors.New("refused: the call acts for another org")
	}
	if v, ok := b[ref]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("no such secret")
}

// invoicing is commerce co-resident with Hanzo's bank in its secrets, an
// Enterprise row sold by invoice (14 days to pay, voided 7 days after), and the
// invoice rail's ops exposed; IAM says the buyer administers their org.
func invoicing(t *testing.T) {
	t.Helper()
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(ctx)
	})
	funded(t)
	prev := commercesecrets.Get()
	commercesecrets.Set(bank{
		"orgs/hanzo/wire/WIRE_BANK_NAME":      "Hanzo Bank",
		"orgs/hanzo/wire/WIRE_ACCOUNT_HOLDER": "Hanzo AI Inc",
		"orgs/hanzo/wire/WIRE_ACCOUNT_NUMBER": "000488108457",
		"orgs/hanzo/wire/WIRE_ROUTING_NUMBER": "026009593",
	})
	t.Cleanup(func() { commercesecrets.Set(prev) })
	priorAdmin := administers
	administers = func(context.Context) (bool, error) { return true, nil }
	t.Cleanup(func() { administers = priorAdmin })
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeOrders(riskGate(luxlog.New("invoicetest")))
	quiet(t)

	super := superAdmin(context.Background())
	yes, no := true, false
	if _, err := planeSet(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", PerSeat: &yes, ContactSales: &no,
		Limits: &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(2000), BalanceMonths: ptr(1),
			BalanceCeiling: ptr(100000), InvoiceDays: ptr(14), GraceDays: ptr(7)}}); err != nil {
		t.Fatalf("add the row: %v", err)
	}
	// The brand's own org, whose bank the wire is sent to, in the books the wire
	// read asks.
	brandOrg := organization.New(datastore.New(context.Background()))
	brandOrg.Name = "hanzo"
	if err := brandOrg.GetOrCreate("Name=", "hanzo"); err != nil {
		t.Fatalf("the brand's org: %v", err)
	}
}

// buyer is an admin of org ordering for it.
func buyer(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "ada", Email: "ap@" + org + ".test", OrgAdmin: true})
}

// orderFor is 20 seats of Enterprise for a year and a $40,000 starting balance.
func orderFor(org, key string) *client.InvoiceSaleIn {
	return &client.InvoiceSaleIn{Host: "hanzo.ai", PlanID: "enterprise", Subject: org, Quantity: 20, Interval: "year",
		Email: "ap@" + org + ".test", IdempotencyKey: key, BalanceCents: 4000000, Chosen: true}
}

// An order by invoice raises the open invoice and opens nothing: it answers the
// amount, the due date, the reference, the bank and the same as steps one field
// to a line, where to read it (the brand's pay page and the PDF), and the notice;
// invoice_created is stated, never an address or a bank detail. Anyone holding
// the link reads it. Marked paid by the SuperAdmin, the plan opens and the
// balance is credited; invoice_paid and the sale (order_completed, by wire) are
// stated. Voided, invoice_voided is stated.
func TestAnOrderByInvoiceOverThePlane(t *testing.T) {
	invoicing(t)
	seen := watchSales(t)
	org := fresh("invco")
	got, err := planeOrderInvoice(buyer(org), orderFor(org, fresh("k")))
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	o := got.Order
	if o == nil || o.Status != "open" || o.Active || o.AmountCents != 20*24000+4000000 || o.Wire == nil ||
		o.Wire.AccountNumber != "000488108457" || !strings.HasPrefix(o.Reference, "INV-") ||
		!strings.HasPrefix(o.HostedURL, "https://hanzo.ai/pay/invoice/") ||
		!strings.HasPrefix(o.PDFURL, "https://api.hanzo.ai/v1/billing/invoices/hosted/") ||
		o.Notice != "Your plan starts when we confirm your payment." {
		t.Fatalf("order: %+v", o)
	}
	steps := strings.Join(o.HowToPay, "\n")
	for _, want := range []string{"Amount due: $44", "Reference: " + o.Reference, "Include this reference exactly", "Account number: 000488108457"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("how to pay lacks %q:\n%s", want, steps)
		}
	}
	// stated waits for name, keeping whatever else arrives first: statements are
	// detached, so their order is not theirs to keep.
	var held []*client.EventIn
	stated := func(name string) *client.EventIn {
		t.Helper()
		for {
			for i, in := range held {
				if in.Name == name {
					held = append(held[:i], held[i+1:]...)
					return in
				}
			}
			select {
			case s := <-seen:
				held = append(held, s.in)
			case <-time.After(3 * time.Second):
				t.Fatalf("%s was never stated", name)
				return nil
			}
		}
	}
	created := stated(orderInvoiced)
	for k, v := range map[string]string{"order_id": o.InvoiceID, "item_id": "enterprise", "seats": "20",
		"amount_cents": "4480000", "balance_cents": "4000000", "payment_method": "wire"} {
		if said(created, k) != v {
			t.Errorf("invoice_created %s = %q, want %q", k, said(created, k), v)
		}
	}
	for _, a := range created.Attributes {
		if strings.Contains(a.Value, "000488108457") || strings.Contains(a.Value, "@") {
			t.Errorf("invoice_created carries %s = %q", a.Name, a.Value)
		}
	}

	link := strings.TrimPrefix(o.HostedURL, "https://hanzo.ai/pay/invoice/")
	h, err := planeOrderHosted(cloud.For(context.Background(), "hanzo"), &client.InvoiceLinkIn{Link: link})
	if err != nil || h.Order.InvoiceID != o.InvoiceID || len(h.Order.HowToPay) == 0 || h.BillTo == "" {
		t.Fatalf("hosted: %+v %v", h, err)
	}
	if _, err := planeOrderHosted(cloud.For(context.Background(), "hanzo"), &client.InvoiceLinkIn{Link: strings.Repeat("x", 43)}); statusOf(err) != http.StatusNotFound {
		t.Fatalf("an unknown link: %v, want 404", err)
	}
	if _, err := planeOrderHosted(cloud.For(context.Background(), "lux"), &client.InvoiceLinkIn{Link: link}); statusOf(err) != http.StatusNotFound {
		t.Fatalf("a Hanzo invoice read on Lux: %v, want 404", err)
	}

	s := riskGate(luxlog.New("invoicetest"))
	staff := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "z", Admin: true})
	if _, err := s.planeOrderSettle(buyer(org), &client.InvoiceSettleIn{InvoiceID: o.InvoiceID, Receipt: "FED-1", AmountCents: o.AmountCents}); statusOf(err) != http.StatusForbidden {
		t.Fatalf("a buyer marking their own invoice paid: %v, want 403", err)
	}
	paid, err := s.planeOrderSettle(staff, &client.InvoiceSettleIn{InvoiceID: o.InvoiceID, Receipt: "FED-1", AmountCents: o.AmountCents})
	if err != nil || paid.SubscriptionID == "" || paid.Order.Status != "paid" || paid.BalanceCents != 4000000 {
		t.Fatalf("paid: %+v %v", paid, err)
	}
	stated(orderPaid)
	sale := stated(orderCompleted)
	if said(sale, "payment_method") != "wire" || said(sale, "seats") != "20" {
		t.Errorf("the sale stated %+v", sale.Attributes)
	}
	// Marked paid again, it answers the same and tells nobody again.
	again, err := s.planeOrderSettle(staff, &client.InvoiceSettleIn{InvoiceID: o.InvoiceID, Receipt: "fed 1", AmountCents: o.AmountCents})
	if err != nil || !again.Replayed || again.SubscriptionID != paid.SubscriptionID {
		t.Fatalf("paid again: %+v %v", again, err)
	}
	select {
	case s := <-seen:
		t.Fatalf("a settlement answered again stated %s", s.in.Name)
	case <-time.After(300 * time.Millisecond):
	}

	other := fresh("invco")
	got2, err := planeOrderInvoice(buyer(other), orderFor(other, fresh("k")))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := planeOrderVoid(zip.WithCaller(context.Background(), zip.Caller{Org: other, User: "z", Admin: true}),
		&client.InvoiceVoidIn{InvoiceID: got2.Order.InvoiceID, Reason: "duplicate"})
	if err != nil || gone.Status != "void" {
		t.Fatalf("void: %+v %v", gone, err)
	}
	stated(orderVoided)
}

// An order by invoice is refused where the brand takes none (no pay page), and
// where the host sells no plans.
func TestAnOrderByInvoiceIsRefusedWhereNoneIsTaken(t *testing.T) {
	invoicing(t)
	org := fresh("invco")
	in := orderFor(org, "")
	in.Host = "lux.network"
	if _, err := planeOrderInvoice(buyer(org), in); statusOf(err) != http.StatusServiceUnavailable && statusOf(err) != http.StatusNotFound {
		t.Fatalf("a brand with no pay page: %v, want 503 (or 404 where it sells no plans)", err)
	}
	in = orderFor(org, "")
	in.Chosen = false
	var he *zip.HTTPError
	if _, err := planeOrderInvoice(buyer(org), in); !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "org_required" {
		t.Fatalf("an org not chosen: %v, want 400 org_required", err)
	}
	priorAdmin := administers
	administers = func(context.Context) (bool, error) { return false, nil }
	defer func() { administers = priorAdmin }()
	if _, err := planeOrderInvoice(buyer(org), orderFor(org, "")); statusOf(err) != http.StatusForbidden {
		t.Fatalf("a buyer IAM says is no admin of the org: %v, want 403", err)
	}
}

// The invoice mail says how to pay, one field to a line, the reference set apart,
// with the links it is read by; the receipt and the void notice say what happened.
func TestTheInvoiceMailSaysHowToPay(t *testing.T) {
	v := &client.InvoiceOrder{Number: "INV-0007", Reference: "INV-ACME-0007", AmountCents: 4480000, Currency: "usd",
		DueAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), Notice: "Your plan starts when we confirm your payment.",
		HowToPay:  []string{"Amount due: $44,800.00", "Reference: INV-ACME-0007", "Include this reference exactly, so we can match your payment."},
		HostedURL: "https://hanzo.ai/pay/invoice/abc", PDFURL: "https://api.hanzo.ai/v1/billing/invoices/hosted/abc/pdf"}
	subject, body, text := orderMail(v, "ordered", false)
	if subject != "Invoice INV-0007: $44,800.00 due Nov 16, 2026" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{"Reference: INV-ACME-0007", "Your plan starts when we confirm your payment.", "https://hanzo.ai/pay/invoice/abc", "/abc/pdf"} {
		if !strings.Contains(body, want) || !strings.Contains(text, want) {
			t.Fatalf("the mail lacks %q:\n%s\n%s", want, body, text)
		}
	}
	if !strings.Contains(body, `font-size:18px;font-weight:600">Reference: INV-ACME-0007`) {
		t.Fatalf("the reference is not set apart:\n%s", body)
	}
	if s, _, _ := orderMail(v, "paid", false); s != "Payment received: invoice INV-0007" {
		t.Fatalf("receipt subject %q", s)
	}
	if s, _, text := orderMail(v, "voided", false); s != "Invoice INV-0007 was voided" || !strings.Contains(text, "Nothing was started") {
		t.Fatalf("void %q %q", s, text)
	}
	// Under net terms the plan had started: the void says it ended, never that
	// nothing started.
	if _, body, text := orderMail(v, "voided", true); strings.Contains(text, "Nothing was started") || strings.Contains(body, "Nothing was started") ||
		!strings.Contains(text, "the plan it started has ended") {
		t.Fatalf("a net-terms void says %q", text)
	}
	if grouped(4480000, "usd") != "$44,800.00" || grouped(-500, "usd") != "-$5.00" || grouped(123456789, "usd") != "$1,234,567.89" {
		t.Fatalf("grouped: %q %q %q", grouped(4480000, "usd"), grouped(-500, "usd"), grouped(123456789, "usd"))
	}
}
