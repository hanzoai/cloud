package billing

// An organization's order by invoice, paid by wire, and the invoice's link
// (commerce invoice_sale.go, apps/commerce invoice_rpc.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/internal/consent"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/spec"
)

// mountOrders registers the order by invoice and the invoice's link.
func mountOrders(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	// UNTYPED BY DESIGN, as subscribe/card is: a fresh order answers 201 and a
	// retry under the same key the same order, as it stands, at 200.
	g.Raw(http.MethodPost, "/subscribe/invoice", cloud.Handle(o.s, orderByInvoice))
	// The link is the credential: anyone holding it reads the invoice, and nothing
	// else is asked. Untyped: one answers JSON and the other a PDF.
	g.Raw(http.MethodGet, "/invoices/hosted/:link", hostedInvoice)
	g.Raw(http.MethodGet, "/invoices/hosted/:link/pdf", hostedInvoicePDF)
}

// The prose for the three raw routes, declared once (see cards.go).
func init() {
	spec.Describe("/v1/billing/subscribe/invoice", http.MethodPost,
		"Order a plan by invoice, paid by wire",
		"Raises an OPEN invoice for an organization's order — the seats at the plan's price and the "+
			"starting balance — and sends it to the buyer. NOTHING STARTS until it is paid: "+
			"\"Your plan starts when we confirm your payment.\"\n\n"+
			"The body is a card sale's less the card and the coupon: `planId`, `quantity`, `lines`, "+
			"`interval`, `balanceCents`. The organization is the one chosen with X-Org-Id (none chosen is "+
			"400), and only its admin, as IAM says now, orders (403). The answer carries the invoice, the amount due, the due date (the plan's "+
			"`limits.invoiceDays`), when it is voided if unpaid (`limits.graceDays` later), the receiving "+
			"bank with the reference to send exactly, the same as `howToPay` steps one field to a line, and "+
			"`hostedUrl` and `pdfUrl`. A plan already paid for, or one with an invoice still open, is 409; a "+
			"site that takes no order by invoice is 503. A retry under the same X-Idempotency-Key answers the "+
			"same order as it stands at 200, with a new link and the bank read again; nothing is mailed again.")
	spec.Describe("/v1/billing/invoices/hosted/{link}", http.MethodGet,
		"Read an invoice by its link",
		"The order invoice the link shows, as `{order, issuer, issuerAddress, billTo}`: the order — what it "+
			"is for, its status, and while it is open how to pay it (`wire`, `howToPay`) — and who bills whom. "+
			"The link is the only credential; an unknown one, or one read on another brand's host, is 404. "+
			"Answered `Cache-Control: no-store`, `X-Robots-Tag: noindex` and `Referrer-Policy: no-referrer`.")
	spec.Describe("/v1/billing/invoices/hosted/{link}/pdf", http.MethodGet,
		"Download an invoice by its link",
		"The order invoice the link shows, as a PDF, with how to pay it while it is open.")
}

// orderByInvoice orders a plan by invoice for the organization chosen with
// X-Org-Id, by one of its admins: the gate a card sale has (CSRF, an opened org,
// steward), less the card. The invoice is mailed to the buyer's address; nothing
// starts until it is paid.
func orderByInvoice(s *cloud.Service[state], c *zip.Ctx) error {
	if err := cloud.CSRF(c.Context()); err != nil {
		return err
	}
	if err := opened(c); err != nil {
		return err
	}
	st, err := steward(c, planAct)
	if err != nil {
		return err
	}
	org, subject := st.org, st.subject
	var body struct {
		PlanID       string            `json:"planId"`
		StoreID      string            `json:"storeId,omitempty"`
		Quantity     int               `json:"quantity,omitempty"`
		Lines        []client.SaleLine `json:"lines,omitempty"`
		Level        int               `json:"level,omitempty"`
		Interval     string            `json:"interval,omitempty"`
		BalanceCents int64             `json:"balanceCents,omitempty"`
		Consent      *string           `json:"consent,omitempty"`
	}
	if c.Bind(&body) != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	in := &client.InvoiceSaleIn{
		Host: c.Host(), PlanID: body.PlanID, Subject: subject, StoreID: body.StoreID,
		Quantity: body.Quantity, Lines: body.Lines, Level: body.Level, Interval: body.Interval,
		Email: callerEmail(c), IdempotencyKey: retryKey(c), BalanceCents: body.BalanceCents,
		Chosen: chose(c, org), Consent: consent.Consented(c, body.Consent),
	}
	out, err := ask(c.Forward(), org, "order by invoice", func(ctx context.Context) (*client.InvoiceOrdered, error) {
		return commercepeer.BillingSubscribeInvoice(ctx, in)
	})
	if err != nil {
		return err
	}
	if o := out.Open; o != nil {
		// The plane carries a refusal's code and sentence, not its data, so the
		// invoice this order was refused for arrives as a value and is refused here.
		return (&zip.HTTPError{Status: http.StatusConflict, Code: "invoice_open", Msg: out.Refusal}).
			With(map[string]any{"invoiceId": o.InvoiceID, "number": o.Number, "hostedUrl": o.HostedURL})
	}
	if len(out.Replayed) > 0 {
		return document(c, http.StatusOK, out.Replayed)
	}
	b, merr := json.Marshal(out.Order)
	if merr != nil {
		return zip.Errorf(http.StatusBadGateway, "order by invoice: could not read the order")
	}
	return document(c, http.StatusCreated, b)
}

// asBrand is the call a link's reader makes: the anonymous reader holds no
// caller, so it reads as the brand that serves the host, which is the brand whose
// bank the invoice is paid to.
func asBrand(c *zip.Ctx) context.Context {
	return cloud.For(context.Background(), brand.ForHost(c.Host()))
}

// unkept marks an answer read by a link as one to keep nowhere: no cache holds it,
// no search engine indexes it, and nothing it links to learns the link. It is set
// before the read, so a miss is answered the same way.
func unkept(c *zip.Ctx) {
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Robots-Tag", "noindex")
	c.SetHeader("Referrer-Policy", "no-referrer")
}

// hostedInvoice is the order invoice a link shows, read on the brand of the host
// it is asked on: a link read on another brand's host is a miss.
func hostedInvoice(c *zip.Ctx) error {
	unkept(c)
	h, err := ask(asBrand(c), brand.ForHost(c.Host()), "invoice", func(ctx context.Context) (*client.HostedInvoice, error) {
		return commercepeer.BillingOrderHosted(ctx, &client.InvoiceLinkIn{Link: c.Param("link")})
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h)
}

// hostedInvoicePDF is the order invoice a link shows, as a PDF to print or save.
func hostedInvoicePDF(c *zip.Ctx) error {
	unkept(c)
	doc, err := ask(asBrand(c), brand.ForHost(c.Host()), "invoice pdf", func(ctx context.Context) (*client.Document, error) {
		return commercepeer.BillingOrderHostedPDF(ctx, &client.InvoiceLinkIn{Link: c.Param("link")})
	})
	if err != nil {
		return err
	}
	c.SetHeader("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, doc.Filename))
	c.SetHeader("Content-Type", "application/pdf")
	return c.Bytes(http.StatusOK, doc.Body)
}
