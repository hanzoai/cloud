package tax

// pdf.go — Copy B and the payer's copy, rendered from the form's own content.
//
// The writer is the shape commerce renders an invoice with (hanzoai/commerce
// api/billing/invoice_pdf.go): one page of PDF-1.4, the built-in Helvetica, no
// images and no compression, and a PURE function of what it is given — no clock,
// no ids — so a statement renders the same bytes however often it is asked for.
// That writer is unexported in another module, so this is the same fifty lines
// rather than an import.
//
// It is a substitute statement (Pub 1179): the form number, the OMB number, the
// tax year, CORRECTED when it is, the box captions as the form words them, and
// the recipient's legend verbatim. The recipient's TIN is truncated as Pub 1099
// allows on a payee statement; the payer's prints in full, because a filer's TIN
// may not be truncated on any form.
//
// Two copies exist. Copy B is the recipient's. The payer's own is marked "For
// Payer" — current revisions of Forms 1099-NEC and 1099-MISC print no lettered
// payer copy, and Pub 1099 ("Keeping copies") asks the payer to keep a copy for
// at least three years, four when backup withholding applied.

import (
	"bytes"
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

func init() {
	spec.Describe("/v1/tax/forms/:id/pdf", http.MethodGet,
		"Download a 1099 the org prepared, as a PDF",
		"Answers one 1099 the caller org prepared as payer as a one-page PDF — `application/pdf` under "+
			"a Content-Disposition naming the form — rendered from the form's own content: the payer's copy by "+
			"default, or Copy B (For Recipient) with `?copy=B` for a statement owed on paper. The recipient's TIN "+
			"is truncated; the payer's prints in full, as the law requires on every form.\n\n"+
			"The render is a pure function of the form, so the same form renders the same bytes every time. "+
			"Org admins only; another org's form is not found. It files nothing and mails nothing.")
	spec.Describe("/v1/tax/inbox/:id/pdf", http.MethodGet,
		"Download a Copy B furnished to the org, as a PDF",
		"Answers one Copy B furnished to the caller org as a one-page PDF — `application/pdf` under a "+
			"Content-Disposition naming the form — rendered from the statement exactly as it was furnished, "+
			"which can never change. The recipient's own TIN is truncated; the payer's prints in full.\n\n"+
			"Any member of the recipient org may download it; another org's statement is not found.")
}

// copyOf is which copy a render is.
type copyOf struct {
	label  string // "Copy B" / "For Payer"
	legend []string
}

var (
	copyB = copyOf{"Copy B - For Recipient", []string{
		"This is important tax information and is being furnished to the IRS. If you are",
		"required to file a return, a negligence penalty or other sanction may be imposed",
		"on you if this income is taxable and the IRS determines that it has not been reported.",
	}}
	copyPayer = copyOf{"For Payer - keep for your records", []string{
		"The payer's retained copy. Keep it, or the ability to reconstruct it, for at least",
		"3 years from the due date of the return; 4 years when backup withholding applied",
		"(Publication 1099, Keeping copies).",
	}}
)

var omb = map[Kind]string{NEC: "OMB No. 1545-0116", MISC: "OMB No. 1545-0115"}
var title = map[Kind]string{NEC: "Nonemployee Compensation", MISC: "Miscellaneous Information"}

// pdfOf renders a form's content as one copy. payerTIN is the filer's TIN in
// full; the recipient's arrives already masked in the content.
func pdfOf(c content, payerTIN TIN, cp copyOf) []byte {
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	if c.Corrected {
		add("CORRECTED")
	}
	add(fmt.Sprintf("Form %s  %s  %s  For calendar year %d", c.Kind, title[c.Kind], omb[c.Kind], c.Year))
	add(cp.label)
	add("")
	add("PAYER'S name, street address, city or town, state or province, country, ZIP, telephone")
	add(partyLine(c.Payer))
	if c.Payer.Phone != "" {
		add("Telephone " + c.Payer.Phone)
	}
	add("PAYER'S TIN  " + payerTIN.full())
	add("")
	add("RECIPIENT'S TIN  " + cmp.Or(c.Recipient.TIN, "not provided"))
	add("RECIPIENT'S name  " + c.Recipient.Name)
	if c.Recipient.BusinessName != "" {
		add("  " + c.Recipient.BusinessName)
	}
	add("Street address, city or town, state or province, country, ZIP")
	add(addressLine(c.Recipient.Address))
	add("Account number  " + c.Account)
	add("")
	for _, b := range c.Boxes {
		add(fmt.Sprintf("%-4s %-44s $%s", b.Box, b.Label, cents0(b.Cents)))
	}
	add("")
	for _, l := range cp.legend {
		add(l)
	}
	return writePDF(lines)
}

func partyLine(p Party) string {
	name := p.Name
	if p.BusinessName != "" {
		name += " (" + p.BusinessName + ")"
	}
	return name + ", " + addressLine(p.Address)
}

func addressLine(a Address) string {
	if !a.complete() {
		return "not provided"
	}
	return oneLine(a)
}

// cents0 is an amount on the printed form: 0.00 prints, where the upload leaves it blank.
func cents0(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

// writePDF lays text lines top to bottom on one US Letter page.
func writePDF(lines []string) []byte {
	var content bytes.Buffer
	content.WriteString("BT\n/F1 9 Tf\n11 TL\n54 740 Td\n")
	for _, l := range lines {
		fmt.Fprintf(&content, "(%s) Tj T*\n", pdfText(l))
	}
	content.WriteString("ET")

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	offsets = append(offsets, buf.Len())
	fmt.Fprintf(&buf, "5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", content.Len(), content.Bytes())
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

// pdfText escapes a line for a PDF string and keeps it to the ASCII the built-in
// font encodes.
func pdfText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '—' || r == '–':
			b.WriteByte('-')
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func sendPDF(c *zip.Ctx, name string, body []byte) error {
	c.SetHeader("Content-Type", "application/pdf")
	c.SetHeader("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, name))
	c.SetHeader("Cache-Control", "no-store")
	return c.Bytes(http.StatusOK, body)
}

// formPDF answers a form the caller's org prepared, as the payer's copy or Copy B.
func formPDF(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	if !cloud.Admin.Admits(cloud.AuthorityOf(c)) {
		return cloud.Admin.Refusal()
	}
	o := ops{s: s}
	st, err := o.storeOf(org)
	if err != nil {
		return err
	}
	r, err := st.form(c.Context(), c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("form not found")
	}
	if err != nil {
		return err
	}
	var ct content
	if err := decode(r.content, &ct); err != nil {
		return err
	}
	cp, suffix := copyPayer, "payer"
	if strings.EqualFold(c.Query("copy"), "B") {
		cp, suffix = copyB, "copy-b"
	}
	return sendPDF(c, fmt.Sprintf("%s-%d-%s-%s", ct.Kind, ct.Year, r.id, suffix), pdfOf(ct, r.payerTIN, cp))
}

// statementPDF answers a Copy B furnished to the caller's org.
func statementPDF(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	o := ops{s: s}
	st, err := o.storeOf(org)
	if err != nil {
		return err
	}
	r, err := st.statement(c.Context(), c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("statement not found")
	}
	if err != nil {
		return err
	}
	var ct content
	if err := decode(r.content, &ct); err != nil {
		return err
	}
	return sendPDF(c, fmt.Sprintf("%s-%d-%s-copy-b", ct.Kind, ct.Year, r.id), pdfOf(ct, r.payerTIN, copyB))
}
