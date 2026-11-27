package commerce

// An organization's order by invoice, paid by wire: the plane ops over commerce's
// invoice rail (commerce api/billing invoice_sale.go). Ordering raises the open
// invoice, mails it to the buyer and opens nothing; the link the invoice is shared
// by is read by anyone holding it; the platform's SuperAdmin marks it paid against
// the bank's receipt, voids it, or refunds it. invoice_created, invoice_paid and
// invoice_voided are stated on the event plane, and order_completed only at paid.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/hanzoai/commerce/types"

	"github.com/hanzoai/cloud/client"
	flagplane "github.com/hanzoai/cloud/client/flag"
	notifypeer "github.com/hanzoai/cloud/client/notify"
	cloudds "github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
)

// The names an order by invoice is stated under on the event plane, one per
// moment: raised, paid, voided. The sale itself is order_completed, at paid.
const (
	orderInvoiced = "invoice_created"
	orderPaid     = "invoice_paid"
	orderVoided   = "invoice_voided"
)

// exposeOrders registers the invoice rail's plane ops. The screen states the
// sale a paid invoice completes.
func exposeOrders(s screen) {
	cloud.Plane().Post("/billing/subscribe/invoice", planeOrderInvoice,
		zip.WithOperationID(client.BillingSubscribeInvoice),
		zip.WithSummary("Order a plan by invoice, paid by wire: nothing starts until it is paid"))
	cloud.Plane().Post("/billing/order/hosted", planeOrderHosted,
		zip.WithOperationID(client.BillingOrderHosted),
		zip.WithSummary("Read the order invoice a link shows"))
	cloud.Plane().Post("/billing/order/hosted/pdf", planeOrderHostedPDF,
		zip.WithOperationID(client.BillingOrderHostedPDF),
		zip.WithSummary("Render the order invoice a link shows as a PDF"))
	cloud.Plane().Post("/billing/order/settle", s.planeOrderSettle,
		zip.WithOperationID(client.BillingOrderSettle),
		zip.WithSummary("Mark an order invoice paid by a wire"))
	cloud.Plane().Post("/billing/order/void", planeOrderVoid,
		zip.WithOperationID(client.BillingOrderVoid),
		zip.WithSummary("Void an unpaid order invoice"))
	cloud.Plane().Post("/billing/order/refund", planeOrderRefund,
		zip.WithOperationID(client.BillingOrderRefund),
		zip.WithSummary("Refund a paid order's wire"))
	cloud.Plane().Post("/billing/order/read", planeOrderRead,
		zip.WithOperationID(client.BillingOrderRead),
		zip.WithSummary("Read one order invoice whole, as staff read it"))
}

// payOrigin is where a payer reads an invoice the host's brand issues, or "" for
// a brand that takes no order by invoice.
func payOrigin(host string) string { return brand.For(brand.ForHost(host)).Pay }

// planeOrderInvoice orders a plan by invoice. It is sold where the catalog is, on
// a brand that shows invoices (payOrigin), to an admin of the organization chosen
// with X-Org-Id (admits): an order by invoice is an organization's whatever its
// row says, so a request that chose none is refused rather than invoicing the
// buyer's default org. The invoice is mailed to the buyer; the answer is the
// order with where it is read and how to pay it. A retry under the same key is
// answered the same order as it stands, its link and bank read again, and mails
// nothing.
func planeOrderInvoice(ctx context.Context, in *client.InvoiceSaleIn) (*client.InvoiceOrdered, error) {
	if !commercebilling.Sells(in.Host) {
		return nil, zip.ErrNotFound("plan not found")
	}
	pay := payOrigin(in.Host)
	if pay == "" {
		return nil, &zip.HTTPError{Status: http.StatusServiceUnavailable, Code: "wire_not_configured", Msg: "ordering by invoice is not offered here: pay by card, or contact sales"}
	}
	org, err := orgOf(ctx, "order by invoice")
	if err != nil {
		return nil, err
	}
	if err := admits(ctx, in.Chosen); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Email) == "" {
		return nil, zip.ErrBadRequest("an invoice is sent to the buyer's email: sign in with an address; nothing was ordered")
	}
	out, replayed, err := commercebilling.OrderInvoice(ctx, org, commercebilling.OrderIn{
		Subject: in.Subject, Email: in.Email, PlanID: in.PlanID, Quantity: in.Quantity,
		Lines: saleLines(in.Lines), Level: in.Level, Interval: types.Interval(in.Interval),
		BalanceCents: in.BalanceCents, StoreID: in.StoreID, IdempotencyKey: in.IdempotencyKey,
		Host: in.Host, Net: netTerms(ctx, org.Name), Promo: promoNow(ctx),
	})
	if r, ok := errors.AsType[*commercebilling.Refusal](err); ok && r.Open != nil {
		open, verr := orderView(r.Open, pay)
		if verr != nil {
			return nil, verr
		}
		return &client.InvoiceOrdered{Open: open, Refusal: r.Error()}, nil
	}
	if err != nil {
		return nil, saleFault(err)
	}
	view, err := orderView(out, pay)
	if err != nil {
		return nil, err
	}
	if replayed {
		body, err := json.Marshal(view)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "order by invoice: the order could not be read")
		}
		return &client.InvoiceOrdered{Replayed: body}, nil
	}
	mailOrder(ctx, org.Name, out, view, "ordered")
	stateInvoice(org.Name, org.TestMode(), orderInvoiced, view)
	return &client.InvoiceOrdered{Order: view}, nil
}

// netTerms reports whether org is approved to start before it pays: the platform
// switch names the approved orgs. Silence is not approved.
func netTerms(ctx context.Context, org string) bool {
	ask, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	v, err := flagplane.FlagsValue(cloud.For(ask, principal.Reserved), &client.FlagValueIn{Key: client.NetTerms, Kind: "string"})
	if err != nil || v == nil {
		luxlog.Default().Warn("the net terms switch did not answer; the order starts when paid", "org", org, "err", err)
		return false
	}
	return names(v.Value)[strings.ToLower(strings.TrimSpace(org))]
}

// orderView is an order as its buyer reads it: commerce's order in commerce's
// own shape — client.InvoiceOrder is that shape, so it is read through it rather
// than copied field by field (TestTheInvoiceRailIsOneShape holds the two equal) —
// and, when the answer carries the order's link, where it is read: the brand's
// pay page (pay) and the PDF. The link itself is not part of the view.
func orderView(o *commercebilling.InvoiceOrder, pay string) (*client.InvoiceOrder, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the order could not be read")
	}
	v := new(client.InvoiceOrder)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the order could not be read")
	}
	if o.Link != "" {
		if pay != "" {
			v.HostedURL = pay + "/invoice/" + o.Link
		}
		v.PDFURL = "https://" + brand.APIHost(o.Brand) + "/v1/billing/invoices/hosted/" + o.Link + "/pdf"
	}
	return v, nil
}

// planeOrderHosted reads the order invoice a link shows, on the brand the caller
// reads it on (cloud.For(brand), the host's): the link is the only credential, and
// an unknown one, or one read on another brand, is a miss, never a hint. It reads
// the brand's bank acting as the brand, as the anonymous reader cannot.
func planeOrderHosted(ctx context.Context, in *client.InvoiceLinkIn) (*client.HostedInvoice, error) {
	h, err := commercebilling.HostedInvoice(ctx, in.Link, cloud.Who(ctx).Org)
	if err != nil {
		return nil, saleFault(err)
	}
	h.Order.Link = in.Link
	view, err := orderView(h.Order, brand.For(h.Order.Brand).Pay)
	if err != nil {
		return nil, err
	}
	return &client.HostedInvoice{Order: view, Issuer: h.Issuer, IssuerAddress: h.IssuerAddress, BillTo: h.BillTo}, nil
}

// planeOrderHostedPDF renders the order invoice a link shows, with how to pay it,
// on the brand the caller reads it on.
func planeOrderHostedPDF(ctx context.Context, in *client.InvoiceLinkIn) (*client.Document, error) {
	d, err := commercebilling.HostedInvoicePDF(ctx, in.Link, cloud.Who(ctx).Org)
	if err != nil {
		return nil, saleFault(err)
	}
	return &client.Document{Filename: d.Filename, Body: d.Body}, nil
}

// planeOrderSettle marks an order invoice in the caller's org paid by a wire: the
// platform's SuperAdmin alone, acting in the customer's org (cloud.As). Paid, the
// plan opens and the starting balance is credited, once; the buyer is mailed the
// receipt, and invoice_paid and the sale (order_completed) are stated, at what the
// invoice was worth and was paid of it: an overpayment is not revenue. A
// settlement already made in full (Replayed) moved nothing, and tells nobody again.
func (s screen) planeOrderSettle(ctx context.Context, in *client.InvoiceSettleIn) (*client.InvoiceSettled, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "settle an order invoice")
	if err != nil {
		return nil, err
	}
	got, err := commercebilling.SettleInvoice(ctx, org, commercebilling.SettleIn{
		InvoiceID: in.InvoiceID, Receipt: in.Receipt, AmountCents: in.AmountCents,
		ReceivedAt: in.ReceivedAt, Difference: in.Difference,
	})
	if err != nil {
		return nil, saleFault(err)
	}
	view, err := orderView(got.Order, "")
	if err != nil {
		return nil, err
	}
	if !got.Replayed {
		mailOrder(ctx, org.Name, got.Order, view, "paid")
		stateInvoice(org.Name, org.TestMode(), orderPaid, view)
		s.emit(payment{
			org: org.Name, ledger: org.Name, subject: org.Name, email: strings.ToLower(strings.TrimSpace(got.Order.Email)), test: org.TestMode(), moded: true,
			path: "/billing/order/settle", via: "/billing/order/settle",
			cents: got.ReceivedCents - got.OverCents, currency: view.Currency, plan: view.PlanID, interval: intervalOf(got.Order),
			goods: goods{tab: view.Category, method: "wire", setup: "new", seats: view.Seats, balance: got.BalanceCents},
		}, got.Payment)
	}
	return &client.InvoiceSettled{
		Order: view, Payment: got.Payment, SubscriptionID: got.SubscriptionID, ReceivedCents: got.ReceivedCents,
		ShortCents: got.ShortCents, OverCents: got.OverCents, BalanceCents: got.BalanceCents, BalanceID: got.BalanceID,
		Reopened: got.Reopened, Replayed: got.Replayed,
	}, nil
}

// intervalOf is the term an order bought, as the sale is stated: year or month.
func intervalOf(o *commercebilling.InvoiceOrder) string {
	for _, l := range o.Lines {
		if strings.Contains(l.Description, "/seat/year") {
			return "year"
		}
	}
	return "month"
}

// planeOrderVoid voids an unpaid order invoice in the caller's org: the platform's
// SuperAdmin alone. The buyer is told, and invoice_voided is stated.
func planeOrderVoid(ctx context.Context, in *client.InvoiceVoidIn) (*client.InvoiceOrder, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "void an order invoice")
	if err != nil {
		return nil, err
	}
	o, err := commercebilling.VoidOrder(ctx, org, in.InvoiceID, in.Reason)
	if err != nil {
		return nil, saleFault(err)
	}
	return voided(ctx, org.Name, org.TestMode(), o)
}

// voided tells a voided order's buyer and states it.
func voided(ctx context.Context, org string, test bool, o *commercebilling.InvoiceOrder) (*client.InvoiceOrder, error) {
	view, err := orderView(o, "")
	if err != nil {
		return nil, err
	}
	mailOrder(ctx, org, o, view, "voided")
	stateInvoice(org, test, orderVoided, view)
	return view, nil
}

// planeOrderRead reads one order invoice in the caller's org whole, as staff read
// it: how to pay it while it is open (the bank read now), the plan it opened and
// the wire that paid it. The platform's SuperAdmin alone, acting in the org; an
// invoice the org's books do not hold is 404.
func planeOrderRead(ctx context.Context, in *client.InvoiceRef) (*client.InvoiceOrder, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "read an order invoice")
	if err != nil {
		return nil, err
	}
	o, err := commercebilling.Order(ctx, org, in.ID)
	if err != nil {
		return nil, saleFault(err)
	}
	return orderView(o, "")
}

// planeOrderRefund refunds a paid order's wire in the caller's org: the
// platform's SuperAdmin alone.
func planeOrderRefund(ctx context.Context, in *client.InvoiceRefundIn) (*client.InvoiceOrder, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org, err := orgOf(ctx, "refund an order invoice")
	if err != nil {
		return nil, err
	}
	o, err := commercebilling.RefundOrder(ctx, org, in.InvoiceID, in.AmountCents, in.Reference)
	if err != nil {
		return nil, saleFault(err)
	}
	if !org.TestMode() {
		mirrorCommerceLedgerFact(cloudds.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID("refund", org.Name, in.InvoiceID, in.Reference),
			Org:         org.Name,
			Product:     "billing",
			Kind:        "refund",
			Sign:        -1,
			AmountMinor: in.AmountCents,
			CashMinor:   in.AmountCents,
		})
	}
	mirrorOrgInvoices(org)
	mirrorOrgBook(org.Name)
	return orderView(o, "")
}

// expired voids every order invoice unpaid past its due date and grace, tells
// each buyer and states each. The renewal sweep runs it; a failure is logged and
// the next sweep asks again.
func expired(ctx context.Context) {
	gone, err := commercebilling.ExpireInvoices(ctx, time.Now())
	if err != nil {
		luxlog.Default().Error("invoice expiry: the open invoices could not be read", "err", err)
		return
	}
	for _, g := range gone {
		row, err := commerceorg.Resolve(ctx, g.Org)
		if _, err := voided(ctx, g.Org, err == nil && row.TestMode(), g.Order); err != nil {
			luxlog.Default().Error("invoice expiry: a voided invoice was not told", "org", g.Org, "invoice", g.Order.InvoiceID, "err", err)
		}
	}
}

// stateInvoice states an order invoice's moment on its org's event plane: what it
// is for and what it is worth, never an address or a bank detail. A sandbox
// order is stated as test traffic.
func stateInvoice(org string, test bool, name string, v *client.InvoiceOrder) {
	attrs := []client.Signal{
		{Name: "event_id", Value: name + ":" + v.InvoiceID},
		{Name: "order_id", Value: v.InvoiceID},
		{Name: "item_id", Value: v.PlanID},
		{Name: "tab", Value: v.Category},
		{Name: "seats", Value: strconv.FormatInt(v.Seats, 10)},
		{Name: "amount_cents", Value: strconv.FormatInt(v.AmountCents, 10)},
		{Name: "balance_cents", Value: strconv.FormatInt(v.BalanceCents, 10)},
		{Name: "payment_method", Value: "wire"},
		{Name: "currency", Value: v.Currency},
	}
	if test {
		attrs = append(attrs, client.Signal{Name: "test", Value: "true"})
	}
	in := &client.EventIn{Name: name, Product: "commerce", Subject: org, Attributes: attrs}
	lg, call := luxlog.Default(), send
	select {
	case emitting <- struct{}{}:
	default:
		lg.Debug("an invoice was not stated on the event plane: statements in flight are at the ceiling", "ceiling", maxEmits)
		return
	}
	go func() {
		defer func() { <-emitting }()
		defer func() {
			if r := recover(); r != nil {
				lg.Error("stating an invoice on the event plane panicked", "err", r)
			}
		}()
		state(context.Background(), lg, call, org, in)
	}()
}

// mailOrder mails an order's buyer (o.Email) what just happened to it, as a
// transactional message sent as the brand that issued the invoice (o.Brand): the
// invoice they asked for ("ordered"), the receipt ("paid"), the notice
// ("voided"). Detached and best effort: the order stands whatever the mail does,
// and the hosted link reads it any time.
func mailOrder(ctx context.Context, org string, o *commercebilling.InvoiceOrder, v *client.InvoiceOrder, what string) {
	to := strings.TrimSpace(o.Email)
	if to == "" {
		return
	}
	subject, htmlBody, text := orderMail(v, what, o.Net)
	from := brand.For(o.Brand).ID
	detached := context.WithoutCancel(ctx)
	go func() {
		ask, cancel := context.WithTimeout(detached, 30*time.Second)
		defer cancel()
		if _, err := notifypeer.NotifySend(ask, &client.Send{
			Org: from, Channel: "email", To: to, Subject: subject, Body: htmlBody, Text: text,
		}); err != nil {
			luxlog.Default().Warn("an order invoice was not mailed", "org", org, "invoice", v.InvoiceID, "what", what, "err", err)
		}
	}()
}

// orderMail is an order's mail: its subject, its HTML and its plain text, from
// the same lines so the two never say different things. An invoice states the
// amount due, the due date, the reference to send exactly, the bank one field to
// a line, that the plan starts when the payment is confirmed, and what happens
// if it is not paid, with the links it is read by. A void under net terms (net)
// ended a plan that had started.
func orderMail(v *client.InvoiceOrder, what string, net bool) (subject, htmlBody, text string) {
	amount := grouped(v.AmountCents, v.Currency)
	var head []string
	var lines []string
	switch {
	case what == "paid":
		subject = fmt.Sprintf("Payment received: invoice %s", v.Number)
		head = []string{fmt.Sprintf("We received your payment for invoice %s. Your plan has started.", v.Number)}
	case what == "voided" && net:
		subject = fmt.Sprintf("Invoice %s was voided", v.Number)
		head = []string{fmt.Sprintf("Invoice %s was voided, unpaid, and the plan it started has ended.", v.Number),
			"To start the plan again, place the order again."}
	case what == "voided":
		subject = fmt.Sprintf("Invoice %s was voided", v.Number)
		head = []string{fmt.Sprintf("Invoice %s was voided, unpaid. Nothing was started and nothing is owed.", v.Number),
			"To start the plan, place the order again."}
	default:
		subject = fmt.Sprintf("Invoice %s: %s due %s", v.Number, amount, v.DueAt.UTC().Format("Jan 2, 2006"))
		head = []string{fmt.Sprintf("Here is your invoice %s.", v.Number), v.Notice}
		lines = v.HowToPay
	}
	var t strings.Builder
	var h strings.Builder
	h.WriteString(`<div style="font-family:system-ui,sans-serif;font-size:15px;line-height:1.5">`)
	for _, l := range head {
		t.WriteString(l + "\n")
		h.WriteString("<p>" + html.EscapeString(l) + "</p>")
	}
	if len(lines) > 0 {
		t.WriteString("\nHow to pay:\n")
		h.WriteString(`<p><strong>How to pay</strong></p><table style="border-collapse:collapse">`)
		for _, l := range lines {
			t.WriteString(l + "\n")
			style := ""
			if strings.HasPrefix(l, "Reference: ") {
				style = ` style="font-size:18px;font-weight:600"`
			}
			h.WriteString("<tr><td" + style + ">" + html.EscapeString(l) + "</td></tr>")
		}
		h.WriteString("</table>")
	}
	if v.HostedURL != "" {
		t.WriteString("\nView the invoice: " + v.HostedURL + "\nDownload the PDF: " + v.PDFURL + "\n")
		h.WriteString(`<p><a href="` + html.EscapeString(v.HostedURL) + `">View the invoice</a> · <a href="` +
			html.EscapeString(v.PDFURL) + `">Download the PDF</a></p>`)
	}
	h.WriteString("</div>")
	return subject, h.String(), t.String()
}

// grouped renders cents with thousands grouped, as a payer reads them.
func grouped(cents int64, cur string) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	sym := "$"
	if c := strings.ToLower(cur); c != "" && c != "usd" {
		sym = strings.ToUpper(c) + " "
	}
	return fmt.Sprintf("%s%s%s.%02d", sign, sym, b.String(), cents%100)
}
