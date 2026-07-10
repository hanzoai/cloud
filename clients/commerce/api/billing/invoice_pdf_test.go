package billing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRenderInvoicePDF_ValidStructure proves the hand-rolled writer emits a
// well-formed, deterministic PDF whose number and amount-due are greppable in
// the uncompressed content stream.
func TestRenderInvoicePDF_ValidStructure(t *testing.T) {
	inv := &billinginvoice.BillingInvoice{}
	inv.SetNumber(42)
	inv.Status = billinginvoice.Open
	inv.PeriodStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inv.PeriodEnd = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	inv.LineItems = []billinginvoice.LineItem{
		{Description: "Pro subscription", Amount: 2000, Quantity: 1, UnitPrice: 2000},
		{Description: "API usage (metered)", Amount: 1234, Quantity: 100, UnitPrice: 12},
	}
	inv.Subtotal = 3234
	inv.Tax = 100
	inv.Discount = 0
	inv.CreditApplied = 0
	inv.AmountDue = 3334

	pdf := renderInvoicePDF(inv, "Acme Inc")

	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("PDF must start with %%PDF-")
	}
	if !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("PDF must end with %%%%EOF")
	}
	if len(pdf) < 400 {
		t.Fatalf("PDF suspiciously short: %d bytes", len(pdf))
	}
	if !bytes.Contains(pdf, []byte("INV-0042")) {
		t.Fatalf("PDF missing invoice number INV-0042")
	}
	if !bytes.Contains(pdf, []byte("$33.34")) {
		t.Fatalf("PDF missing amount-due $33.34")
	}
	if !bytes.Contains(pdf, []byte("Acme Inc")) {
		t.Fatalf("PDF missing org name")
	}

	// Deterministic: same invoice → identical bytes (idempotent render).
	if pdf2 := renderInvoicePDF(inv, "Acme Inc"); !bytes.Equal(pdf, pdf2) {
		t.Fatalf("render is not deterministic")
	}
}

// TestRenderInvoicePDF_EscapesAndDefaults proves PDF string metacharacters in
// user-controlled text are escaped (no stream break) and the org name defaults
// to Hanzo when empty.
func TestRenderInvoicePDF_EscapesAndDefaults(t *testing.T) {
	inv := &billinginvoice.BillingInvoice{}
	inv.SetNumber(1)
	inv.Status = billinginvoice.Open
	inv.LineItems = []billinginvoice.LineItem{
		{Description: "weird (name) with \\ and ) paren", Amount: 500},
	}
	inv.Subtotal = 500
	inv.AmountDue = 500

	pdf := renderInvoicePDF(inv, "")
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("escaped PDF is malformed")
	}
	if !bytes.Contains(pdf, []byte("Hanzo")) {
		t.Fatalf("empty org name must default to Hanzo")
	}
	// The raw '(' / ')' / '\' must appear only in escaped form inside the item text.
	if !bytes.Contains(pdf, []byte(`\(name\)`)) {
		t.Fatalf("parens in item text were not escaped")
	}
	if !bytes.Contains(pdf, []byte(`\\`)) {
		t.Fatalf("backslash in item text was not escaped")
	}
}

// pdfHandlerCtx builds a gin context scoped to org `ns`, matching the gateway
// plumbing so DownloadInvoicePDF resolves the ae datastore in that namespace.
func pdfHandlerCtx(w http.ResponseWriter, ns, id string) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	org := &organization.Organization{}
	org.Name = ns
	c.Set("organization", org)
	c.Set("context", nscontext.WithNamespace(context.Background(), ns))
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/billing/invoices/"+id+"/pdf", nil)
	return c
}

// TestDownloadInvoicePDF_TenantIsolation is the RED-focus test: an org can
// download ITS OWN invoice (200, application/pdf), but a DIFFERENT org handed
// the exact same invoice id gets 404 — the invoice is invisible outside its
// owning namespace.
func TestDownloadInvoicePDF_TenantIsolation(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	// org A seeds an invoice in its OWN namespace.
	dbA := datastore.New(nscontext.WithNamespace(context.Background(), "pdf-org-a"))
	invA := billinginvoice.New(dbA)
	invA.UserId = "pdf-org-a/alice"
	invA.Status = billinginvoice.Open
	invA.SetNumber(7)
	invA.LineItems = []billinginvoice.LineItem{{Description: "Pro subscription", Amount: 5000}}
	invA.RecalculateSubtotal()
	invA.AmountDue = 5000
	if err := invA.Create(); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	idA := invA.Id()

	// org A downloads its OWN invoice: 200 + PDF.
	{
		w := httptest.NewRecorder()
		DownloadInvoicePDF(pdfHandlerCtx(w, "pdf-org-a", idA))
		if w.Code != 200 {
			t.Fatalf("org A own download: status = %d, body=%s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/pdf") {
			t.Fatalf("Content-Type = %q, want application/pdf", ct)
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "INV-0007.pdf") {
			t.Fatalf("Content-Disposition = %q, want attachment INV-0007.pdf", cd)
		}
		if !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
			t.Fatalf("org A body is not a PDF")
		}
	}

	// org B handed org A's id: MUST 404 (cross-tenant read is impossible).
	{
		w := httptest.NewRecorder()
		DownloadInvoicePDF(pdfHandlerCtx(w, "pdf-org-b", idA))
		if w.Code != 404 {
			t.Fatalf("CROSS-TENANT LEAK: org B got status %d for org A's invoice %s (want 404); body=%s",
				w.Code, idA, w.Body.String())
		}
		if bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
			t.Fatalf("CROSS-TENANT LEAK: org B received PDF bytes for org A's invoice")
		}
	}
}
