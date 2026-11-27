package tax

// iris.go — the payer's return for a tax year, in the form IRIS takes it, and the
// record of what the payer did with it.
//
// IRIS (the Information Returns Intake System) is the only intake for information
// returns from tax year 2026 (Pub 1099, 2026). It has two channels, and what the
// IRS publishes about each decides what can be built here:
//
//   - A2A, an XML transmission over REST. Pub 5718 documents its envelope
//     (IRTransmission → IRTransmissionManifest, IRSubmission1Grp →
//     IRSubmission1Header, IRSubmission1Detail → Form1099NECDetail), but the
//     schemas themselves are released only through the Secure Object Repository
//     to an e-Services account holding an IRIS TCC ("IRIS schemas and business
//     rules", irs.gov), and a transmitter "must use approved software that has
//     passed IRIS ATS" (TY2025 IRIS ATS examples). A file built against a schema
//     this code has never seen would be a guess, so it is not built.
//   - The Taxpayer Portal, which takes a CSV upload. Pub 5717 §3.5 documents the
//     rules every file must meet — one form type and one tax year per file, at most
//     250 records, UTF-8, TINs and ZIPs as bare digits, no periods or "#" in a name
//     or address line, no line breaks or leading, trailing or double spaces, money
//     cells blank rather than 0.00, check boxes Y or N — and the portal hands a
//     logged-in TCC holder its template for each form.
//
// So this exports the portal CSV: every rule Pub 5717 §3.5 publishes is applied to
// every value and checked by [validate] before a file is answered, one file per
// 250 records, originals and corrections apart. The column set is the form's own
// fields, named after them, because the IRS distributes its template's header row
// only inside the portal — the payer carries these columns into that template
// (Pub 5717 §3.5.1) when it uploads. That, and the upload, are owner steps.
//
// Nothing here files anything. A filing's status moves only when the payer
// records what IRIS answered it: submitted with a Receipt ID, then accepted,
// accepted with errors, or rejected.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// maxRecords is the most records one portal file takes (Pub 5717 §3.5, revised
// 01/2026: "Files can now contain a maximum of 250 records").
const maxRecords = 250

// Layout names the file format, so a reader of a filing knows what it holds.
const Layout = "IRIS Taxpayer Portal CSV — Pub 5717 §3.5 file rules; columns are the form's own fields, " +
	"in form order, to be carried into the template the IRIS portal issues the TCC holder"

// cell kinds, which decide both how a value is written and how it is checked.
type cellKind int

const (
	cText  cellKind = iota // a code or free value
	cName                  // a name or address line: no '.', '#', line breaks or stray spaces
	cTIN                   // nine digits, or empty when no TIN is held
	cZIP                   // five or nine digits
	cUSD                   // 1234.56, or blank for zero
	cCheck                 // Y or N
)

// column is one field of the file.
type column struct {
	name     string
	kind     cellKind
	required bool
	value    func(r irisRecord) string
}

// irisRecord is one form, with the TINs opened for the one place they print in full.
type irisRecord struct {
	c        content
	payer    TIN
	payee    TIN
	correct  bool
	boxCents map[string]int64
}

func party(prefix string, p func(irisRecord) Party, t func(irisRecord) TIN, phone bool) []column {
	cols := []column{
		{prefix + " TIN Type", cText, false, func(r irisRecord) string { return strings.ToUpper(string(t(r).Type)) }},
		{prefix + " TIN", cTIN, false, func(r irisRecord) string { return t(r).digits }},
		{prefix + " Name", cName, true, func(r irisRecord) string { return p(r).Name }},
		{prefix + " Name Line 2", cName, false, func(r irisRecord) string { return p(r).BusinessName }},
		{prefix + " Address Line 1", cName, true, func(r irisRecord) string { return p(r).Address.Line1 }},
		{prefix + " Address Line 2", cName, false, func(r irisRecord) string { return p(r).Address.Line2 }},
		{prefix + " City", cName, true, func(r irisRecord) string { return p(r).Address.City }},
		{prefix + " State", cText, true, func(r irisRecord) string { return p(r).Address.State }},
		{prefix + " ZIP", cZIP, true, func(r irisRecord) string { return p(r).Address.ZIP }},
		{prefix + " Country", cText, true, func(r irisRecord) string { return cmp.Or(p(r).Address.Country, "US") }},
	}
	if phone {
		cols = append(cols, column{prefix + " Phone", cText, false, func(r irisRecord) string { return digits(p(r).Phone) }})
	}
	return cols
}

func amount(box string) func(irisRecord) string {
	return func(r irisRecord) string { return cents(r.boxCents[box]) }
}

func no(irisRecord) string { return "N" }

// layouts are the columns of each form's file, in the order the form lays them out.
var layouts = func() map[Kind][]column {
	head := func(k Kind) []column {
		return []column{
			{"Form Type", cText, true, func(irisRecord) string { return string(k) }},
			{"Tax Year", cText, true, func(r irisRecord) string { return strconv.Itoa(r.c.Year) }},
			{"Corrected", cCheck, true, func(r irisRecord) string { return yn(r.correct) }},
		}
	}
	payer := party("Payer", func(r irisRecord) Party { return r.c.Payer }, func(r irisRecord) TIN { return r.payer }, true)
	payer[1].required = true // the filer's TIN is never absent and never truncated
	payee := party("Recipient", func(r irisRecord) Party { return r.c.Recipient }, func(r irisRecord) TIN { return r.payee }, false)
	account := column{"Account Number", cText, true, func(r irisRecord) string { return r.c.Account }}
	nec := slices.Concat(head(NEC), payer, payee, []column{account,
		{"Second TIN Notice", cCheck, true, no},
		{"Box 1a Nonemployee Compensation", cUSD, false, amount("1a")},
		{"Box 2 Direct Sales of $5000 or More", cCheck, true, no},
		{"Box 4 Federal Income Tax Withheld", cUSD, false, amount("4")},
	})
	misc := slices.Concat(head(MISC), payer, payee, []column{account,
		{"Second TIN Notice", cCheck, true, no},
		{"FATCA Filing Requirement", cCheck, true, no},
		{"Box 1 Rents", cUSD, false, amount("1")},
		{"Box 2 Royalties", cUSD, false, amount("2")},
		{"Box 3 Other Income", cUSD, false, amount("3")},
		{"Box 4 Federal Income Tax Withheld", cUSD, false, amount("4")},
		{"Box 7 Direct Sales of $5000 or More", cCheck, true, no},
	})
	return map[Kind][]column{NEC: nec, MISC: misc}
}()

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// cents writes an amount as the portal takes it: dollars and cents, no symbol or
// separator, and a blank cell for zero ("ensure there are no money amount cells
// reporting $0.00. These should be left blank" — Pub 5717 §3.5.3.1).
func cents(c int64) string {
	if c == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", c/100, c%100)
}

// scrub makes a name or address line one the portal accepts: no periods, no "#",
// no double dash, no line breaks, single spaces, nothing leading or trailing — and
// nothing a spreadsheet reads as a formula.
func scrub(s string) string {
	s = strings.NewReplacer(".", "", "#", "", "--", "-", "\r", " ", "\n", " ", "\t", " ").Replace(s)
	return defuse(strings.Join(strings.Fields(s), " "))
}

// formula is every character that makes a spreadsheet read a cell as a formula
// when it leads it (OWASP, "CSV Injection"); tab and carriage return, the other
// two, are already never at the start of a cell here. The payee writes its own
// name and address, and the payer opens this file in a spreadsheet to carry it
// into the portal's template — so a payee name of =HYPERLINK(…) would run in the
// PAYER's spreadsheet, beside every TIN the file holds.
const formula = "=+-@"

// defuse drops whatever formula characters (and the spaces between them) lead s.
// Names and addresses are refused with one at the profile; this is the export's
// own guarantee, whatever a stored form says.
func defuse(s string) string { return strings.TrimLeft(s, formula+" ") }

// leads reports whether s begins with a formula character.
func leads(s string) bool { return s != "" && strings.ContainsRune(formula, rune(s[0])) }

// write renders one cell by its kind.
func (col column) write(r irisRecord) string {
	v := col.value(r)
	switch col.kind {
	case cName:
		return scrub(v)
	case cZIP, cTIN:
		return digits(v)
	}
	return defuse(strings.TrimSpace(v))
}

// Part is one file of a filing.
type Part struct {
	// Name is the file name, e.g. "1099-NEC-2026-original-1.csv".
	Name string `json:"name"`
	// Type is original or correction: the portal takes them apart.
	Type string `json:"type"`
	// Records is how many forms it holds, at most 250.
	Records int `json:"records"`
	// Digest is the SHA-256 of its bytes, hex, so a re-read proves it is the same file.
	Digest string `json:"digest"`
	// CSV is the file itself. It carries full TINs, and is answered only to an org
	// admin of the payer, on the audit trail.
	CSV string `json:"csv,omitempty"`
}

// render builds a file from its records, and refuses to answer one that breaks a
// rule.
func render(k Kind, recs []irisRecord) ([]byte, error) {
	cols := layouts[k]
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = c.name
	}
	_ = w.Write(head)
	for _, r := range recs {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = c.write(r)
		}
		_ = w.Write(row)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	if errs := validate(k, buf.Bytes()); len(errs) > 0 {
		return nil, fmt.Errorf("the file breaks the IRIS portal rules: %s", strings.Join(errs, "; "))
	}
	return buf.Bytes(), nil
}

var (
	reUSD  = regexp.MustCompile(`^[0-9]+\.[0-9]{2}$`)
	reName = regexp.MustCompile(`^[^.#\r\n\t]*$`)
)

// validate checks a file against every rule Pub 5717 §3.5 publishes and the
// layout this package writes. render calls it on every file before answering it,
// so a file that breaks a rule is refused here rather than at the portal.
func validate(k Kind, file []byte) []string {
	cols, ok := layouts[k]
	if !ok {
		return []string{fmt.Sprintf("no layout for %s", k)}
	}
	var errs []string
	if bytes.HasPrefix(file, []byte{0xEF, 0xBB, 0xBF}) {
		errs = append(errs, "a byte-order mark: the file is UTF-8 without one")
	}
	rows, err := csv.NewReader(bytes.NewReader(file)).ReadAll()
	if err != nil {
		return append(errs, "not CSV: "+err.Error())
	}
	if len(rows) == 0 {
		return append(errs, "no header row")
	}
	for i, c := range cols {
		if i >= len(rows[0]) || rows[0][i] != c.name {
			errs = append(errs, fmt.Sprintf("header column %d is not %q", i+1, c.name))
		}
	}
	if n := len(rows) - 1; n > maxRecords {
		errs = append(errs, fmt.Sprintf("%d records, more than the %d a file takes", n, maxRecords))
	}
	year := ""
	for ri, row := range rows[1:] {
		at := func(msg string) { errs = append(errs, fmt.Sprintf("record %d: %s", ri+1, msg)) }
		if len(row) != len(cols) {
			at(fmt.Sprintf("%d cells, want %d", len(row), len(cols)))
			continue
		}
		amounts := false
		for i, c := range cols {
			v := row[i]
			if v != strings.TrimSpace(v) || strings.Contains(v, "  ") || strings.Contains(v, "--") {
				at(c.name + " has a leading, trailing or double space, or a double dash")
			}
			if c.required && v == "" {
				at(c.name + " is required")
			}
			if leads(v) {
				at(c.name + " begins with a character a spreadsheet reads as a formula")
			}
			if v == "" {
				continue
			}
			switch c.kind {
			case cName:
				if !reName.MatchString(v) {
					at(c.name + " has a period, a '#' or a line break")
				}
			case cTIN:
				if len(v) != 9 || digits(v) != v {
					at(c.name + " is not nine digits")
				}
			case cZIP:
				if (len(v) != 5 && len(v) != 9) || digits(v) != v {
					at(c.name + " is not five or nine digits")
				}
			case cUSD:
				if !reUSD.MatchString(v) || v == "0.00" {
					at(c.name + " is not an amount like 1234.56 (zero is left blank)")
				}
				amounts = true
			case cCheck:
				if v != "Y" && v != "N" {
					at(c.name + " is not Y or N")
				}
			}
		}
		if row[0] != string(k) {
			at("form type is " + row[0])
		}
		if year == "" {
			year = row[1]
		} else if row[1] != year {
			at("a second tax year in one file")
		}
		if !amounts {
			at("no amount: a record reports at least one")
		}
	}
	return errs
}

// Filing is one export of a year's return, and what the payer recorded of it.
type Filing struct {
	// ID is the filing's id, "iris_"-prefixed.
	ID string `json:"id"`
	// Year is the tax year.
	Year int `json:"year"`
	// Kind is 1099-NEC or 1099-MISC — one per filing, as IRIS takes them.
	Kind Kind `json:"kind"`
	// Status is exported, submitted, accepted, accepted_with_errors or rejected.
	// Only the payer's own record moves it past exported: nothing here submits.
	Status string `json:"status"`
	// Forms are the form ids the filing carries.
	Forms []string `json:"forms"`
	// Parts are its files.
	Parts []Part `json:"parts"`
	// Layout says what the files are.
	Layout string `json:"layout"`
	// Due is when the return is due to the IRS.
	Due string `json:"due"`
	// MissingTIN are forms filed with no recipient TIN.
	MissingTIN []string `json:"missingTin,omitempty"`
	// TCC is the payer's IRIS Transmitter Control Code, once it recorded a submission.
	TCC string `json:"tcc,omitempty"`
	// ReceiptID is the Receipt ID IRIS returned, once the payer recorded one.
	ReceiptID string `json:"receiptId,omitempty"`
	// Steps are what remains for the payer to do, in order.
	Steps []string `json:"steps"`
	// CreatedAt is when it was exported, unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when its status last moved, unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// filingData is what a filing row stores: never the file, which carries full
// TINs and is rebuilt from the sealed forms whenever it is read.
type filingData struct {
	Forms      []string `json:"forms"`
	Corrects   []string `json:"corrects"`
	Parts      []Part   `json:"parts"`
	MissingTIN []string `json:"missingTin,omitempty"`
	TCC        string   `json:"tcc,omitempty"`
	ReceiptID  string   `json:"receiptId,omitempty"`
}

func steps(status string) []string {
	switch status {
	case "exported":
		return []string{
			"Sign in to the IRIS Taxpayer Portal with this payer's IRIS Transmitter Control Code (apply at IRS e-Services; approval takes up to 45 days).",
			"Download the portal's CSV template for this form and tax year, carry these columns into it, and upload it (Pub 5717 §3.5); corrections go in their own file.",
			"Record the Receipt ID IRIS returns: POST /v1/tax/filings/{id}/receipt.",
		}
	case "submitted":
		return []string{"Check the acknowledgment in IRIS and record it: POST /v1/tax/filings/{id}/receipt with the status IRIS gave."}
	case "rejected":
		return []string{"Fix what IRIS rejected — correct a form whose figures, name or TIN were wrong — and export again: a rejected submission filed nothing, so the next export carries its forms again, as originals."}
	}
	return []string{}
}

// exportIn names the return to export.
type exportIn struct {
	// Year is the tax year.
	Year int `json:"year"`
	// Kind is 1099-NEC or 1099-MISC.
	Kind Kind `json:"kind"`
}

// Exports the caller org's return for a tax year and form, as the files the IRIS
// Taxpayer Portal takes, and records the export. Org admins of the payer only: the
// files carry TINs in full, so a SuperAdmin acting inside the org is refused here
// exactly as at the TIN read.
//
// It carries every furnished (or owed) form of that year and kind not already in
// an earlier export that IRIS did not reject — a rejected submission filed
// nothing, so its forms go out again as originals. Originals are in one set of
// files and corrections of forms already exported in another, at most 250 records
// a file, each written to every rule Pub 5717 §3.5 sets and checked against them
// before it is answered. The files carry full TINs: the export is on the audit
// trail and the files are not stored, only rebuilt from the sealed forms when read
// again.
//
// It DOES NOT FILE WITH THE IRS. Submitting through IRIS needs the payer's own
// Transmitter Control Code and a person at the portal; the answer lists those
// steps, and POST /v1/tax/filings/{id}/receipt records what IRIS answered.
//
// Example: {"year": 2026, "kind": "1099-NEC"}
func (o ops) export(ctx context.Context, in *exportIn) (*Filing, error) {
	org, st, err := o.own(ctx, "export a return", true)
	if err != nil {
		return nil, err
	}
	if _, ok := layouts[in.Kind]; !ok {
		return nil, zip.ErrBadRequest("kind is 1099-NEC or 1099-MISC")
	}
	if _, err := YearOf(in.Year); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "tax: %v", err)
	}
	forms, err := st.forms(ctx, in.Year)
	if err != nil {
		return nil, err
	}
	past, err := st.filings(ctx)
	if err != nil {
		return nil, err
	}
	filed := map[string]bool{}
	for _, f := range past {
		if f.status == "rejected" {
			continue // IRIS took none of it: its forms are still unfiled
		}
		var d filingData
		if err := decode(f.data, &d); err != nil {
			return nil, err
		}
		for _, id := range d.Forms {
			filed[id] = true
		}
	}
	var d filingData
	for i := range forms {
		r := &forms[i]
		if r.kind != in.Kind || !r.status.final() || filed[r.id] {
			continue
		}
		next, err := st.successor(ctx, r.id)
		if err != nil {
			return nil, err
		}
		if next != "" {
			continue // a correction carries this form's figures now
		}
		d.Forms = append(d.Forms, r.id)
		if r.supersedes != "" && filed[r.supersedes] {
			d.Corrects = append(d.Corrects, r.id)
		}
	}
	if len(d.Forms) == 0 {
		return nil, zip.Errorf(http.StatusConflict, "no furnished %s for %d that is not already in an export", in.Kind, in.Year)
	}
	f := filingRow{id: mint.ID("iris"), year: in.Year, kind: in.Kind, status: "exported", created: now()}
	f.updated = f.created
	parts, missing, err := o.files(ctx, org, st, f, d)
	if err != nil {
		return nil, err
	}
	d.MissingTIN = missing
	for _, p := range parts {
		d.Parts = append(d.Parts, Part{Name: p.Name, Type: p.Type, Records: p.Records, Digest: p.Digest})
	}
	f.data = encode(d)
	if err := st.putFiling(ctx, f); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, org, "tax.filing.exported", audit.Resource{Type: "tax.filing", ID: f.id},
		map[string]any{"year": f.year, "kind": f.kind, "forms": len(d.Forms), "corrections": len(d.Corrects)}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: the export could not be recorded, so its files were not answered")
	}
	noStore(ctx)
	return filingOf(f, d, parts), nil
}

// files builds a filing's parts from its forms, opening each form's TINs.
func (o ops) files(ctx context.Context, org string, st *store, f filingRow, d filingData) ([]Part, []string, error) {
	var orig, corr []irisRecord
	var missing []string
	for _, id := range d.Forms {
		r, err := st.form(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		var c content
		if err := decode(r.content, &c); err != nil {
			return nil, nil, err
		}
		payer, payee := r.payerTIN, r.payeeTIN
		if payee.Zero() {
			missing = append(missing, id)
		}
		rec := irisRecord{c: c, payer: payer, payee: payee, correct: slices.Contains(d.Corrects, id), boxCents: map[string]int64{}}
		for _, b := range c.Boxes {
			rec.boxCents[b.Box] = b.Cents
		}
		if rec.correct {
			corr = append(corr, rec)
		} else {
			orig = append(orig, rec)
		}
	}
	var parts []Part
	for _, set := range []struct {
		typ  string
		recs []irisRecord
	}{{"original", orig}, {"correction", corr}} {
		for n, chunk := 1, set.recs; len(chunk) > 0; n++ {
			take := min(len(chunk), maxRecords)
			file, err := render(f.kind, chunk[:take])
			if err != nil {
				return nil, nil, zip.Errorf(http.StatusUnprocessableEntity, "tax: %v", err)
			}
			sum := sha256.Sum256(file)
			parts = append(parts, Part{
				Name: fmt.Sprintf("%s-%d-%s-%d.csv", f.kind, f.year, set.typ, n),
				Type: set.typ, Records: take, Digest: hex.EncodeToString(sum[:]), CSV: string(file),
			})
			chunk = chunk[take:]
		}
	}
	return parts, missing, nil
}

func filingOf(f filingRow, d filingData, parts []Part) *Filing {
	if parts == nil {
		parts = d.Parts
	}
	return &Filing{ID: f.id, Year: f.year, Kind: f.kind, Status: f.status, Forms: d.Forms, Parts: parts,
		Layout: Layout, Due: DeadlineOf(f.kind, f.year).File, MissingTIN: d.MissingTIN, TCC: d.TCC,
		ReceiptID: d.ReceiptID, Steps: steps(f.status), CreatedAt: f.created, UpdatedAt: f.updated}
}

// filingList is the caller's exports.
type filingList struct {
	// Data are the filings, oldest first, without their files.
	Data []Filing `json:"data"`
}

// Lists the caller org's IRS return exports and where each stands, without their
// files. Org admins only.
func (o ops) filings(ctx context.Context, _ *cloud.Unit) (*filingList, error) {
	_, st, err := o.admin(ctx, "read returns", false)
	if err != nil {
		return nil, err
	}
	rows, err := st.filings(ctx)
	if err != nil {
		return nil, err
	}
	out := &filingList{Data: []Filing{}}
	for _, f := range rows {
		var d filingData
		if err := decode(f.data, &d); err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *filingOf(f, d, nil))
	}
	noStore(ctx)
	return out, nil
}

// filingRef addresses one filing.
type filingRef struct {
	// ID is the filing id, "iris_"-prefixed.
	ID string `json:"id"`
}

// Reads one IRS return export WITH its files, rebuilt from the sealed forms and
// proven identical to what was exported. The files carry full TINs: org admins of
// the payer only — a SuperAdmin is refused — and every read is on the audit trail.
func (o ops) filing(ctx context.Context, in *filingRef) (*Filing, error) {
	org, st, err := o.own(ctx, "read a return", false)
	if err != nil {
		return nil, err
	}
	f, err := st.filing(ctx, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("filing not found")
	}
	if err != nil {
		return nil, err
	}
	var d filingData
	if err := decode(f.data, &d); err != nil {
		return nil, err
	}
	parts, _, err := o.files(ctx, org, st, *f, d)
	if err != nil {
		return nil, err
	}
	for i := range parts {
		if i >= len(d.Parts) || parts[i].Digest != d.Parts[i].Digest {
			return nil, zip.Errorf(http.StatusInternalServerError, "tax: filing %s no longer rebuilds to what was exported", f.id)
		}
	}
	if err := o.audited(ctx, org, "tax.filing.read", audit.Resource{Type: "tax.filing", ID: f.id},
		map[string]any{"year": f.year, "kind": f.kind}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: the read could not be recorded, so it was not answered")
	}
	noStore(ctx)
	return filingOf(*f, d, parts), nil
}

// receiptIn is what IRIS answered, as the payer reports it.
type receiptIn struct {
	// ID is the filing, from the path.
	ID string `json:"id"`
	// Status is submitted (IRIS returned a Receipt ID), accepted,
	// accepted_with_errors or rejected (IRIS's acknowledgment).
	Status string `json:"status"`
	// ReceiptID is the Receipt ID IRIS returned for the submission.
	ReceiptID string `json:"receiptId,omitempty"`
	// TCC is the IRIS Transmitter Control Code the payer submitted under: five
	// uppercase letters and digits.
	TCC string `json:"tcc,omitempty"`
}

var reTCC = regexp.MustCompile(`^[A-Z0-9]{5}$`)

// Records what IRIS answered for an exported return — the Receipt ID on
// submission, then the acknowledgment. Org admins only.
//
// This is the payer's report of its own act at the IRIS portal, made under its
// own Transmitter Control Code; nothing here reaches IRIS, and a status is only
// ever what the payer recorded. Moves: exported → submitted (with the Receipt ID
// and TCC) → accepted, accepted_with_errors or rejected.
//
// Example: {"status": "submitted", "receiptId": "2026-12345678901-abc123def", "tcc": "D1234"}
func (o ops) receipt(ctx context.Context, in *receiptIn) (*Filing, error) {
	org, st, err := o.admin(ctx, "record an IRS receipt", true)
	if err != nil {
		return nil, err
	}
	f, err := st.filing(ctx, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("filing not found")
	}
	if err != nil {
		return nil, err
	}
	var d filingData
	if err := decode(f.data, &d); err != nil {
		return nil, err
	}
	switch {
	case in.Status == "submitted" && f.status == "exported":
		tcc, receipt := strings.ToUpper(strings.TrimSpace(in.TCC)), strings.TrimSpace(in.ReceiptID)
		if !reTCC.MatchString(tcc) {
			return nil, zip.ErrBadRequest("tcc is the five-character IRIS Transmitter Control Code")
		}
		if receipt == "" {
			return nil, zip.ErrBadRequest("receiptId is the Receipt ID IRIS returned")
		}
		d.TCC, d.ReceiptID = tcc, receipt
	case slices.Contains([]string{"accepted", "accepted_with_errors", "rejected"}, in.Status) && f.status == "submitted":
	default:
		return nil, zip.Errorf(http.StatusConflict, "the filing is %s; it cannot become %s", f.status, in.Status)
	}
	prev := f.status
	f.status, f.data, f.updated = in.Status, encode(d), now()
	if err := st.putFiling(ctx, *f); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, org, "tax.filing."+in.Status, audit.Resource{Type: "tax.filing", ID: f.id},
		map[string]any{"year": f.year, "kind": f.kind, "from": prev, "status": in.Status}); err != nil {
		return nil, err
	}
	return filingOf(*f, d, nil), nil
}
