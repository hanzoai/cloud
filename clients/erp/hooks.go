package erp

import (
	"context"
	"fmt"
	"math"

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
//
// Posting is append-only: submit writes new immutable ledger rows; it never mutates
// a running balance, so there is no read-modify-write race on the single-writer
// SQLite file. "Current stock" and "account balance" are SUM queries over the ledger.

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
}

// glLeg is one side of a general-ledger posting (a debit XOR a credit, in practice).
type glLeg struct {
	account       string
	debit, credit float64
	remarks       string
}

// ---- before_save: derived totals ----

// computeSalesTotals fills each line's amount = qty * rate and the document's
// grand_total = Σ amount, for Sales Order / Sales Invoice / Purchase Order. It
// mutates the persisted document (before_save runs after validation, before write).
func computeSalesTotals(_ context.Context, ev *framework.Event) error {
	var total float64
	for _, row := range childRows(ev.Doc.Data["items"]) {
		amount := toFloat(row["qty"]) * toFloat(row["rate"])
		row["amount"] = amount // mutates the row map embedded in the document
		total += amount
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
	ev.Doc.Data["total_debit"] = td
	ev.Doc.Data["total_credit"] = tc
	return nil
}

// ---- on_submit: gates + postings ----

// requireNonEmptyOrder gates Sales Order / Purchase Order submit: an order must
// carry at least one line and a positive total.
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
// submit". Accounts default to the conventional codes when the invoice leaves them
// unset, so an org with no chart of accounts can still post a balanced entry.
func salesInvoiceSubmit(ctx context.Context, ev *framework.Event) error {
	total := toFloat(ev.Doc.Data["grand_total"])
	if total <= 0 {
		return fmt.Errorf("cannot submit an invoice with a non-positive total")
	}
	date := toStr(ev.Doc.Data["posting_date"])
	debitTo := firstNonEmpty(toStr(ev.Doc.Data["debit_to"]), "Debtors")
	income := firstNonEmpty(toStr(ev.Doc.Data["income_account"]), "Sales")
	remark := "Sales Invoice " + ev.Doc.Name
	return postGL(ctx, ev, date, []glLeg{
		{account: debitTo, debit: total, remarks: remark},
		{account: income, credit: total, remarks: remark},
	})
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
	// Gate every line before any write.
	for _, row := range rows {
		if toStr(row["item"]) == "" || toFloat(row["qty"]) <= 0 {
			return fmt.Errorf("every stock line needs an item and a positive quantity")
		}
		if toStr(row["source_warehouse"]) == "" && toStr(row["target_warehouse"]) == "" {
			return fmt.Errorf("every stock line needs a source or target warehouse")
		}
	}
	date := toStr(ev.Doc.Data["posting_date"])
	for _, row := range rows {
		item, qty := toStr(row["item"]), toFloat(row["qty"])
		if tgt := toStr(row["target_warehouse"]); tgt != "" {
			if err := appendSLE(ctx, ev, date, item, tgt, qty); err != nil {
				return err
			}
		}
		if src := toStr(row["source_warehouse"]); src != "" {
			if err := appendSLE(ctx, ev, date, item, src, -qty); err != nil {
				return err
			}
		}
	}
	return nil
}

// journalEntrySubmit GATES double-entry integrity — Σ debit must equal Σ credit and
// be positive — then posts one GL entry per account row. This is the accounting
// invariant a manual voucher must satisfy; it cannot be bypassed by a plain edit
// (the engine forbids editing a submitted document) nor by a forged total (postGL
// posts the row values, not the header totals).
func journalEntrySubmit(ctx context.Context, ev *framework.Event) error {
	rows := childRows(ev.Doc.Data["accounts"])
	if len(rows) == 0 {
		return fmt.Errorf("cannot submit a journal entry with no account rows")
	}
	remark := toStr(ev.Doc.Data["user_remark"])
	var td, tc float64
	legs := make([]glLeg, 0, len(rows))
	for _, row := range rows {
		account := toStr(row["account"])
		if account == "" {
			return fmt.Errorf("every journal row needs an account")
		}
		debit, credit := toFloat(row["debit"]), toFloat(row["credit"])
		td, tc = td+debit, tc+credit
		legs = append(legs, glLeg{account: account, debit: debit, credit: credit, remarks: remark})
	}
	if !floatEq(td, tc) {
		return fmt.Errorf("journal entry is unbalanced: debit %.2f != credit %.2f", td, tc)
	}
	if td <= 0 {
		return fmt.Errorf("journal entry total must be positive")
	}
	return postGL(ctx, ev, toStr(ev.Doc.Data["posting_date"]), legs)
}

// paymentEntrySubmit gates a positive amount and posts the balanced cash/party GL
// pair, reusing the SAME postGL as the invoice and journal — one posting path.
func paymentEntrySubmit(ctx context.Context, ev *framework.Event) error {
	amount := toFloat(ev.Doc.Data["paid_amount"])
	if amount <= 0 {
		return fmt.Errorf("payment amount must be positive")
	}
	date := toStr(ev.Doc.Data["posting_date"])
	remark := toStr(ev.Doc.Data["payment_type"]) + " payment " + ev.Doc.Name
	// Receive: Dr Cash, Cr Debtors. Pay: Dr Creditors, Cr Cash.
	if toStr(ev.Doc.Data["payment_type"]) == "Pay" {
		return postGL(ctx, ev, date, []glLeg{
			{account: "Creditors", debit: amount, remarks: remark},
			{account: "Cash", credit: amount, remarks: remark},
		})
	}
	return postGL(ctx, ev, date, []glLeg{
		{account: "Cash", debit: amount, remarks: remark},
		{account: "Debtors", credit: amount, remarks: remark},
	})
}

// ---- posting primitives (org-scoped, append-only) ----

// postGL writes one immutable erp-gl-entry document per leg, stamped with the
// source voucher, into ev.Org via the store. The store bypasses DocPerm, so the
// read-only ledger is written only here. A missing ledger DocType aborts the submit
// (fail secure — the org's ERP install is incomplete).
func postGL(ctx context.Context, ev *framework.Event, date string, legs []glLeg) error {
	glDT, err := ev.Store.GetDocType(ctx, ev.Org, dtGLEntry)
	if err != nil {
		return fmt.Errorf("cannot post GL: %s not installed: %w", dtGLEntry, err)
	}
	for _, leg := range legs {
		data := map[string]any{
			"posting_date": date,
			"account":      leg.account,
			"debit":        leg.debit,
			"credit":       leg.credit,
			"voucher_type": ev.DocType,
			"voucher_no":   ev.Doc.Name,
			"remarks":      leg.remarks,
		}
		if _, err := ev.Store.CreateDocument(ctx, ev.Org, &glDT, data, ""); err != nil {
			return fmt.Errorf("post GL entry: %w", err)
		}
	}
	return nil
}

// appendSLE writes one immutable erp-stock-ledger-entry (signed qty) into ev.Org.
func appendSLE(ctx context.Context, ev *framework.Event, date, item, warehouse string, qty float64) error {
	sleDT, err := ev.Store.GetDocType(ctx, ev.Org, dtStockLedger)
	if err != nil {
		return fmt.Errorf("cannot post stock ledger: %s not installed: %w", dtStockLedger, err)
	}
	data := map[string]any{
		"posting_date": date,
		"item":         item,
		"warehouse":    warehouse,
		"qty":          qty,
		"voucher_type": ev.DocType,
		"voucher_no":   ev.Doc.Name,
	}
	if _, err := ev.Store.CreateDocument(ctx, ev.Org, &sleDT, data, ""); err != nil {
		return fmt.Errorf("post stock ledger entry: %w", err)
	}
	return nil
}

// ---- value coercions (wire → typed) ----

// childRows normalizes a Table field value to the row maps to iterate. A freshly
// validated document carries []map[string]any; a document reloaded from the store
// (the submit path) carries []any of map[string]any after JSON round-trip. Both are
// handled, and the returned maps are the LIVE maps (mutating a row mutates the doc).
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

// toStr returns a trimmed string value ("" for a non-string).
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
