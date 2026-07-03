package erp

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/hanzoai/cloud/clients/framework"
)

// ERP business logic — native-Go lifecycle hooks on the framework engine.
//
// Each hook is trusted first-party Go compiled into the cloud binary, keyed by
// DocType name and applied PER-ORG: the Event carries the VALIDATED org
// (framework derives it via principal.Tenant, never a client header) and every
// sibling read/write goes through ev.Store scoped by ev.Org, so a hook can never
// cross a tenant. Hooks run OUTSIDE the store's write transaction (see hook.go),
// so a posting hook composes plain store writes.
//
// The division of labor mirrors Frappe:
//   - before_save  — derive stored values (line amounts, document totals). Runs on
//     create AND update; it OVERWRITES client-supplied totals, so a computed total
//     can never be forged from the wire (defense in depth over the readOnly UI hint).
//   - on_submit    — GATE the transition (reject an unbalanced/empty voucher) and
//     POST the immutable side effects (GL entries, stock ledger entries). A non-nil
//     return aborts the submit (HTTP 422) BEFORE docstatus flips — fail secure.
//   - on_cancel    — REVERSE the postings (append negating ledger rows). A cancelled
//     voucher must not keep contributing to a balance (Red MEDIUM).
//
// # Exactly-once posting (Red HIGH: TOCTOU double-post)
//
// on_submit side effects run BEFORE the atomic docstatus flip, so N concurrent
// submits of one voucher all read docstatus 0 and all run the posting. Postings are
// therefore IDEMPOTENT by DETERMINISTIC leg naming: each ledger row's name derives
// from the voucher no + a fixed leg index (both frozen for a submitted doc), so the
// store's unique key (org, doctype, name) makes a re-post a no-op (postLeg treats an
// already-present leg as success). This yields exactly-once posting under any number
// of concurrent submitters (the losers still 409 on the docstatus flip) AND makes a
// partially-completed posting REPLAYABLE — a retry fills only the missing legs, so a
// mid-loop store failure can never strand a half-posted voucher. Balances remain
// SUM(ledger); no running total is mutated, so there is no read-modify-write race on
// the single-writer SQLite file.

// registerHooks wires every ERP behavior to its DocType. Called once from init().
func registerHooks() {
	// Totals: line amounts + document total, on create and update.
	for _, dt := range []string{dtSalesOrder, dtSalesInvoice, dtPurchaseOrder} {
		framework.RegisterHook(dt, framework.ActionBeforeSave, computeSalesTotals)
	}
	framework.RegisterHook(dtJournalEntry, framework.ActionBeforeSave, computeJournalTotals)

	// Submit gates + postings.
	framework.RegisterHook(dtSalesOrder, framework.ActionOnSubmit, requireNonEmptyOrder)
	framework.RegisterHook(dtPurchaseOrder, framework.ActionOnSubmit, requireNonEmptyOrder)
	framework.RegisterHook(dtSalesInvoice, framework.ActionOnSubmit, salesInvoiceSubmit)
	framework.RegisterHook(dtStockEntry, framework.ActionOnSubmit, stockEntrySubmit)
	framework.RegisterHook(dtJournalEntry, framework.ActionOnSubmit, journalEntrySubmit)
	framework.RegisterHook(dtPaymentEntry, framework.ActionOnSubmit, paymentEntrySubmit)

	// Cancel reversals: a cancelled voucher's postings are reversed (append-only).
	framework.RegisterHook(dtSalesInvoice, framework.ActionOnCancel, salesInvoiceCancel)
	framework.RegisterHook(dtStockEntry, framework.ActionOnCancel, stockEntryCancel)
	framework.RegisterHook(dtJournalEntry, framework.ActionOnCancel, journalEntryCancel)
	framework.RegisterHook(dtPaymentEntry, framework.ActionOnCancel, paymentEntryCancel)
}

// glLeg is one side of a general-ledger posting (a debit XOR a credit, in practice).
type glLeg struct {
	account       string
	debit, credit float64
	remarks       string
}

// sleMove is one stock movement: a signed quantity for an item at a warehouse.
type sleMove struct {
	item, warehouse string
	qty             float64
}

// ---- before_save: derived totals (finite-guarded) ----

// computeSalesTotals fills each line's amount = qty * rate and the document's
// grand_total = Σ amount, for Sales Order / Sales Invoice / Purchase Order. It
// mutates the persisted document (before_save runs after validation, before write).
// Non-finite results (qty*rate overflow → ±Inf/NaN) are rejected here as a clean 422
// rather than surfacing as a store marshal 500 (Red LOW).
func computeSalesTotals(_ context.Context, ev *framework.Event) error {
	var total float64
	for _, row := range childRows(ev.Doc.Data["items"]) {
		amount := toFloat(row["qty"]) * toFloat(row["rate"])
		if !finite(amount) {
			return fmt.Errorf("line amount is not a finite number (qty*rate overflow)")
		}
		row["amount"] = amount // mutates the row map embedded in the document
		total += amount
	}
	if !finite(total) {
		return fmt.Errorf("grand total is not a finite number")
	}
	ev.Doc.Data["grand_total"] = total
	return nil
}

// computeJournalTotals fills total_debit / total_credit from the account rows.
func computeJournalTotals(_ context.Context, ev *framework.Event) error {
	var td, tc float64
	for _, row := range childRows(ev.Doc.Data["accounts"]) {
		td += toFloat(row["debit"])
		tc += toFloat(row["credit"])
	}
	if !finite(td) || !finite(tc) {
		return fmt.Errorf("journal totals are not finite numbers")
	}
	ev.Doc.Data["total_debit"] = td
	ev.Doc.Data["total_credit"] = tc
	return nil
}

// ---- on_submit: gates + postings ----

// requireNonEmptyOrder gates Sales Order / Purchase Order submit: an order must
// carry at least one line and a positive total. These orders do not post a ledger,
// so they need no on_cancel reversal.
func requireNonEmptyOrder(_ context.Context, ev *framework.Event) error {
	if len(childRows(ev.Doc.Data["items"])) == 0 {
		return fmt.Errorf("cannot submit an order with no items")
	}
	if toFloat(ev.Doc.Data["grand_total"]) <= 0 {
		return fmt.Errorf("cannot submit an order with a non-positive total")
	}
	return nil
}

// salesInvoiceSubmit posts the balanced GL pair for an invoice — Dr the receivable
// account, Cr the income account, each for grand_total — the "GL entry on invoice
// submit".
func salesInvoiceSubmit(ctx context.Context, ev *framework.Event) error {
	if toFloat(ev.Doc.Data["grand_total"]) <= 0 {
		return fmt.Errorf("cannot submit an invoice with a non-positive total")
	}
	date, legs := invoiceLegs(ev)
	return postGL(ctx, ev, date, legs)
}

// stockEntrySubmit validates every movement line THEN appends the stock ledger —
// the "stock update on submit". It gates first (no partial ledger on a bad line),
// then writes one immutable stock-ledger-entry per movement (+qty at the target,
// −qty at the source).
func stockEntrySubmit(ctx context.Context, ev *framework.Event) error {
	rows := childRows(ev.Doc.Data["items"])
	if len(rows) == 0 {
		return fmt.Errorf("cannot submit a stock entry with no items")
	}
	for _, row := range rows {
		if toStr(row["item"]) == "" || toFloat(row["qty"]) <= 0 {
			return fmt.Errorf("every stock line needs an item and a positive quantity")
		}
		if toStr(row["source_warehouse"]) == "" && toStr(row["target_warehouse"]) == "" {
			return fmt.Errorf("every stock line needs a source or target warehouse")
		}
	}
	date, moves := stockMovements(ev)
	return postSLE(ctx, ev, date, moves)
}

// journalEntrySubmit GATES double-entry integrity — Σ debit must equal Σ credit and
// be positive — then posts one GL entry per account row. The gate recomputes from
// the rows, not the forgeable header total.
func journalEntrySubmit(ctx context.Context, ev *framework.Event) error {
	rows := childRows(ev.Doc.Data["accounts"])
	if len(rows) == 0 {
		return fmt.Errorf("cannot submit a journal entry with no account rows")
	}
	var td, tc float64
	for _, row := range rows {
		if toStr(row["account"]) == "" {
			return fmt.Errorf("every journal row needs an account")
		}
		td, tc = td+toFloat(row["debit"]), tc+toFloat(row["credit"])
	}
	if !floatEq(td, tc) {
		return fmt.Errorf("journal entry is unbalanced: debit %.2f != credit %.2f", td, tc)
	}
	if td <= 0 {
		return fmt.Errorf("journal entry total must be positive")
	}
	date, legs := journalAccountLegs(ev)
	return postGL(ctx, ev, date, legs)
}

// paymentEntrySubmit gates a positive amount and posts the balanced cash/party GL
// pair, reusing the SAME postGL as the invoice and journal — one posting path.
func paymentEntrySubmit(ctx context.Context, ev *framework.Event) error {
	if toFloat(ev.Doc.Data["paid_amount"]) <= 0 {
		return fmt.Errorf("payment amount must be positive")
	}
	date, legs := paymentLegs(ev)
	return postGL(ctx, ev, date, legs)
}

// ---- on_cancel: reversals (append negating rows, idempotent) ----

func salesInvoiceCancel(ctx context.Context, ev *framework.Event) error {
	date, legs := invoiceLegs(ev)
	return reverseGL(ctx, ev, date, legs)
}
func journalEntryCancel(ctx context.Context, ev *framework.Event) error {
	date, legs := journalAccountLegs(ev)
	return reverseGL(ctx, ev, date, legs)
}
func paymentEntryCancel(ctx context.Context, ev *framework.Event) error {
	date, legs := paymentLegs(ev)
	return reverseGL(ctx, ev, date, legs)
}
func stockEntryCancel(ctx context.Context, ev *framework.Event) error {
	date, moves := stockMovements(ev)
	return reverseSLE(ctx, ev, date, moves)
}

// ---- leg / movement computation (frozen doc data; shared by submit AND cancel) ----

// invoiceLegs derives the balanced GL pair for an invoice. Accounts default to the
// conventional codes when the invoice leaves them unset, so an org with no chart of
// accounts can still post a balanced entry.
func invoiceLegs(ev *framework.Event) (string, []glLeg) {
	total := toFloat(ev.Doc.Data["grand_total"])
	debitTo := firstNonEmpty(toStr(ev.Doc.Data["debit_to"]), "Debtors")
	income := firstNonEmpty(toStr(ev.Doc.Data["income_account"]), "Sales")
	remark := "Sales Invoice " + ev.Doc.Name
	return toStr(ev.Doc.Data["posting_date"]), []glLeg{
		{account: debitTo, debit: total, remarks: remark},
		{account: income, credit: total, remarks: remark},
	}
}

// journalAccountLegs derives one GL leg per journal account row.
func journalAccountLegs(ev *framework.Event) (string, []glLeg) {
	remark := toStr(ev.Doc.Data["user_remark"])
	rows := childRows(ev.Doc.Data["accounts"])
	legs := make([]glLeg, 0, len(rows))
	for _, row := range rows {
		legs = append(legs, glLeg{
			account: toStr(row["account"]),
			debit:   toFloat(row["debit"]),
			credit:  toFloat(row["credit"]),
			remarks: remark,
		})
	}
	return toStr(ev.Doc.Data["posting_date"]), legs
}

// paymentLegs derives the balanced cash/party GL pair (Receive: Dr Cash, Cr Debtors;
// Pay: Dr Creditors, Cr Cash).
func paymentLegs(ev *framework.Event) (string, []glLeg) {
	amount := toFloat(ev.Doc.Data["paid_amount"])
	remark := toStr(ev.Doc.Data["payment_type"]) + " payment " + ev.Doc.Name
	date := toStr(ev.Doc.Data["posting_date"])
	if toStr(ev.Doc.Data["payment_type"]) == "Pay" {
		return date, []glLeg{
			{account: "Creditors", debit: amount, remarks: remark},
			{account: "Cash", credit: amount, remarks: remark},
		}
	}
	return date, []glLeg{
		{account: "Cash", debit: amount, remarks: remark},
		{account: "Debtors", credit: amount, remarks: remark},
	}
}

// stockMovements derives the signed movements of a stock entry (+qty at target,
// −qty at source, one movement each).
func stockMovements(ev *framework.Event) (string, []sleMove) {
	var moves []sleMove
	for _, row := range childRows(ev.Doc.Data["items"]) {
		item, qty := toStr(row["item"]), toFloat(row["qty"])
		if tgt := toStr(row["target_warehouse"]); tgt != "" {
			moves = append(moves, sleMove{item: item, warehouse: tgt, qty: qty})
		}
		if src := toStr(row["source_warehouse"]); src != "" {
			moves = append(moves, sleMove{item: item, warehouse: src, qty: -qty})
		}
	}
	return toStr(ev.Doc.Data["posting_date"]), moves
}

// ---- idempotent posting primitives (org-scoped, append-only, exactly-once) ----

// postGL writes the forward GL legs of a voucher (submit). reverseGL writes the
// negating legs (cancel), swapping debit/credit so the net effect returns to zero.
func postGL(ctx context.Context, ev *framework.Event, date string, legs []glLeg) error {
	return writeGL(ctx, ev, date, legs, "gl")
}

func reverseGL(ctx context.Context, ev *framework.Event, date string, legs []glLeg) error {
	rev := make([]glLeg, len(legs))
	for i, l := range legs {
		rev[i] = glLeg{account: l.account, debit: l.credit, credit: l.debit, remarks: "Reversal — " + l.remarks}
	}
	return writeGL(ctx, ev, date, rev, "glrev")
}

// writeGL appends the legs as immutable erp-gl-entry rows with DETERMINISTIC names
// (voucher-<kind>-<index>), so a concurrent or retried post is idempotent.
func writeGL(ctx context.Context, ev *framework.Event, date string, legs []glLeg, kind string) error {
	glDT, err := ev.Store.GetDocType(ctx, ev.Org, dtGLEntry)
	if err != nil {
		return fmt.Errorf("cannot post GL: %s not installed: %w", dtGLEntry, err)
	}
	for i, leg := range legs {
		data := map[string]any{
			"posting_date": date,
			"account":      leg.account,
			"debit":        leg.debit,
			"credit":       leg.credit,
			"voucher_type": ev.DocType,
			"voucher_no":   ev.Doc.Name,
			"remarks":      leg.remarks,
		}
		if err := postLeg(ctx, ev, &glDT, legName(ev.Doc.Name, kind, i), data); err != nil {
			return err
		}
	}
	return nil
}

func postSLE(ctx context.Context, ev *framework.Event, date string, moves []sleMove) error {
	return writeSLE(ctx, ev, date, moves, "sle")
}

func reverseSLE(ctx context.Context, ev *framework.Event, date string, moves []sleMove) error {
	rev := make([]sleMove, len(moves))
	for i, m := range moves {
		rev[i] = sleMove{item: m.item, warehouse: m.warehouse, qty: -m.qty}
	}
	return writeSLE(ctx, ev, date, rev, "slerev")
}

// writeSLE appends the movements as immutable erp-stock-ledger-entry rows with
// DETERMINISTIC names (voucher-<kind>-<index>) — idempotent under concurrency/retry.
func writeSLE(ctx context.Context, ev *framework.Event, date string, moves []sleMove, kind string) error {
	sleDT, err := ev.Store.GetDocType(ctx, ev.Org, dtStockLedger)
	if err != nil {
		return fmt.Errorf("cannot post stock ledger: %s not installed: %w", dtStockLedger, err)
	}
	for i, m := range moves {
		data := map[string]any{
			"posting_date": date,
			"item":         m.item,
			"warehouse":    m.warehouse,
			"qty":          m.qty,
			"voucher_type": ev.DocType,
			"voucher_no":   ev.Doc.Name,
		}
		if err := postLeg(ctx, ev, &sleDT, legName(ev.Doc.Name, kind, i), data); err != nil {
			return err
		}
	}
	return nil
}

// legName is the deterministic, slug-clean ledger row name for a voucher's leg:
// "<voucher_no>-<kind>-<index+1>" (e.g. erp-sinv-00001-gl-1). The voucher_no is
// unique per org (series prefix + counter) and the leg set is frozen for a submitted
// doc, so this name uniquely identifies a semantic leg — the store's unique key makes
// re-posting it a no-op.
func legName(voucherNo, kind string, i int) string {
	return voucherNo + "-" + kind + "-" + strconv.Itoa(i+1)
}

// postLeg appends ONE ledger row idempotently. It is a no-op if the deterministically
// named row already exists (a prior or concurrent post of the same voucher leg), so
// the posting is exactly-once under N concurrent submits and REPLAYABLE after a
// partial failure. Uses only the store's existing API — a pre-read short-circuits the
// common case, and a create failure is re-checked to distinguish a lost race (now
// present → success) from a real error. Writes go through the store, which does not
// consult DocPerm, which is why the ledger stays System-Manager read-only on the HTTP
// surface while the hook can still post it.
func postLeg(ctx context.Context, ev *framework.Event, dt *framework.DocType, name string, data map[string]any) error {
	if _, err := ev.Store.GetDocument(ctx, ev.Org, dt.Name, name); err == nil {
		return nil // already posted — idempotent no-op
	}
	if _, err := ev.Store.CreateDocument(ctx, ev.Org, dt, data, name); err != nil {
		// A concurrent poster may have inserted this exact leg between our read and
		// create. If it now exists, that is success (idempotent), not a failure.
		if _, gerr := ev.Store.GetDocument(ctx, ev.Org, dt.Name, name); gerr == nil {
			return nil
		}
		return fmt.Errorf("post %s %q: %w", dt.Name, name, err)
	}
	return nil
}

// ---- value coercions (wire → typed) ----

// childRows normalizes a Table field value to the row maps to iterate. A freshly
// validated document carries []map[string]any; a document reloaded from the store
// (the submit/cancel path) carries []any of map[string]any after JSON round-trip.
// Both are handled, and the returned maps are the LIVE maps (mutating a row mutates
// the doc).
func childRows(v any) []map[string]any {
	switch rows := v.(type) {
	case []map[string]any:
		return rows
	case []any:
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			if m, ok := r.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

// toFloat coerces a JSON/Go numeric to float64 (0 for a non-number). Handles the
// float64 of a JSON round-trip and the int64/float64 of a freshly coerced field.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}

// toStr returns a string value ("" for a non-string).
func toStr(v any) string {
	s, _ := v.(string)
	return s
}

// firstNonEmpty returns the first non-empty string, else "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// floatEq compares currency amounts within half a cent (avoids float drift on sums).
func floatEq(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// finite reports whether f is a real, storable number (not ±Inf/NaN).
func finite(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }
