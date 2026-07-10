package billing

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// DownloadInvoicePDF renders and serves an invoice as a PDF.
//
//	GET /v1/billing/invoices/:id/pdf
//
// Tenant isolation: the invoice is loaded from the caller's OWN org namespace
// (datastore.New(org.Namespaced(c))). GetById scopes the lookup to that
// namespace at the storage layer, so an org can only ever fetch its own
// invoice — a foreign id resolves to nothing and returns 404. It is mounted on
// the user group so a normal authenticated org member can download their own
// invoice.
func DownloadInvoicePDF(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	inv := billinginvoice.New(db)
	if err := inv.GetById(id); err != nil {
		http.Fail(c, 404, "invoice not found", err)
		return
	}

	orgName := org.FullName
	if orgName == "" {
		orgName = org.Name
	}

	pdf := renderInvoicePDF(inv, orgName)

	filename := inv.NumberStr
	if filename == "" {
		filename = "invoice-" + inv.Id()
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, filename))
	c.Data(200, "application/pdf", pdf)
}

// ── Deterministic, dependency-free PDF writer ────────────────────────────────
//
// A single-page PDF-1.4 with the built-in Helvetica font. No images, no
// external fonts, no compression — the content stream is plain text so the
// invoice number and totals are greppable in the raw bytes. The output is a
// pure function of the invoice: same invoice → identical bytes (no timestamps,
// no random ids), so a render is idempotent.

// textLine is one positioned line of text in PDF user space (origin bottom-left).
type textLine struct {
	x, y, size int
	text       string
}

// renderInvoicePDF produces the PDF bytes for an invoice. orgName defaults to
// "Hanzo" when empty (white-label: the org's own name is used when present).
func renderInvoicePDF(inv *billinginvoice.BillingInvoice, orgName string) []byte {
	if orgName == "" {
		orgName = "Hanzo"
	}

	content := buildContentStream(invoiceLines(inv, orgName))

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// offsets[i] is the byte offset of object (i+1).
	offsets := make([]int, 0, 5)
	writeObj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	writeObj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	// Object 5: the content stream (length = raw content byte count).
	offsets = append(offsets, buf.Len())
	fmt.Fprintf(&buf, "5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)

	// Cross-reference table.
	xrefOffset := buf.Len()
	size := len(offsets) + 1 // + the free object 0
	fmt.Fprintf(&buf, "xref\n0 %d\n", size)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}

	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xrefOffset)

	return buf.Bytes()
}

// invoiceLines lays out the invoice as positioned text lines, top to bottom.
func invoiceLines(inv *billinginvoice.BillingInvoice, orgName string) []textLine {
	const (
		left    = 72
		qtyX    = 300
		unitX   = 360
		amountX = 460
	)
	y := 750
	var lines []textLine
	at := func(x, size int, text string) {
		lines = append(lines, textLine{x: x, y: y, size: size, text: text})
	}

	// Header: org name, "Hanzo" issuer, invoice number, period, status.
	at(left, 22, orgName)
	y -= 28
	at(left, 10, "Billed by Hanzo")
	y -= 22

	num := inv.NumberStr
	if num == "" {
		num = "INV"
	}
	at(left, 14, "Invoice "+num)
	y -= 18
	at(left, 10, "Status: "+string(inv.Status))
	y -= 14
	at(left, 10, "Period: "+fmtDate(inv.PeriodStart)+" to "+fmtDate(inv.PeriodEnd))
	y -= 28

	// Line-items table header.
	at(left, 10, "Description")
	at(qtyX, 10, "Qty")
	at(unitX, 10, "Unit")
	at(amountX, 10, "Amount")
	y -= 16

	for _, li := range inv.LineItems {
		at(left, 10, truncate(li.Description, 45))
		if li.Quantity != 0 {
			at(qtyX, 10, fmt.Sprintf("%d", li.Quantity))
		}
		if li.UnitPrice != 0 {
			at(unitX, 10, money(li.UnitPrice))
		}
		at(amountX, 10, money(li.Amount))
		y -= 14
	}
	y -= 12

	// Totals.
	total := func(label string, cents int64) {
		at(qtyX, 10, label)
		at(amountX, 10, money(cents))
		y -= 14
	}
	total("Subtotal", inv.Subtotal)
	total("Tax", inv.Tax)
	total("Discount", -inv.Discount)
	total("Credit Applied", -inv.CreditApplied)
	total("Amount Due", inv.AmountDue)

	return lines
}

// buildContentStream emits the PDF text-showing operators for the lines. Each
// line sets the font, an absolute text matrix, then shows the (escaped) text.
func buildContentStream(lines []textLine) string {
	var b strings.Builder
	b.WriteString("BT\n")
	for _, ln := range lines {
		fmt.Fprintf(&b, "/F1 %d Tf\n", ln.size)
		fmt.Fprintf(&b, "1 0 0 1 %d %d Tm\n", ln.x, ln.y)
		fmt.Fprintf(&b, "(%s) Tj\n", escapePDFText(ln.text))
	}
	b.WriteString("ET")
	return b.String()
}

// escapePDFText escapes a string for a PDF literal string `( … )`. The special
// bytes `\`, `(`, `)` are backslash-escaped; control/non-ASCII bytes are
// dropped so the stream stays in Helvetica's printable range and deterministic.
func escapePDFText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '(':
			b.WriteString(`\(`)
		case ')':
			b.WriteString(`\)`)
		default:
			if r < 0x20 || r > 0x7e {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// money renders a cents amount as $X.XX, with a leading minus for negatives.
func money(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

// fmtDate renders a UTC calendar date (no time-of-day) for determinism.
func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
