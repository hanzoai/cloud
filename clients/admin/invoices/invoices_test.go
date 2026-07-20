package invoices

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/admin/core"
)

// TestInvoiceRowsFromRows proves the warehouse-row → InvoiceRow mapping (JSON-shape
// contract): amount coerced from driver ints, status folded from the latest event,
// display honestly the org slug (no fan-out).
func TestInvoiceRowsFromRows(t *testing.T) {
	rows := []map[string]any{
		{
			"id": "inv_1", "number": "INV-0042", "org": "acme",
			"status": "open", "amount_cents": int64(4900), "currency": "usd",
			"issued": "2026-07-01T00:00:00Z", "due": "2026-07-15T00:00:00Z",
			"last_event": core.EvInvoicePaid,
		},
		{
			"id": "inv_2", "number": "INV-0043", "org": "beta",
			"status": "open", "amount_cents": uint64(1200), "currency": "usd",
			"issued": "2026-07-02T00:00:00Z", "due": "",
			"last_event": core.EvInvoiceVoid,
		},
	}
	out := invoiceRowsFromRows(rows)
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].ID != "inv_1" || out[0].Number != "INV-0042" || out[0].Org != "acme" || out[0].Display != "acme" {
		t.Fatalf("row0 identity wrong: %+v", out[0])
	}
	if out[0].AmountCents != 4900 || out[0].Currency != "usd" {
		t.Fatalf("row0 amount/currency wrong: %+v", out[0])
	}
	if out[0].Status != "paid" {
		t.Fatalf("row0 status = %q, want paid (paid event folds)", out[0].Status)
	}
	if out[0].Issued != "2026-07-01T00:00:00Z" || out[0].Due != "2026-07-15T00:00:00Z" {
		t.Fatalf("row0 dates wrong: %+v", out[0])
	}
	if out[1].Status != "void" {
		t.Fatalf("row1 status = %q, want void", out[1].Status)
	}
}

func TestFoldInvoiceStatus(t *testing.T) {
	if got := foldInvoiceStatus(core.EvInvoicePaid, "open"); got != "paid" {
		t.Fatalf("paid fold = %q", got)
	}
	if got := foldInvoiceStatus(core.EvInvoiceVoid, "open"); got != "void" {
		t.Fatalf("void fold = %q", got)
	}
	if got := foldInvoiceStatus(core.EvInvoiceFinalized, "open"); got != "open" {
		t.Fatalf("finalized snapshot = %q", got)
	}
	if got := foldInvoiceStatus(core.EvInvoiceFinalized, ""); got != "open" {
		t.Fatalf("finalized default = %q", got)
	}
}

func TestInvoicesSQLInjectionSafe(t *testing.T) {
	sql := invoicesSQL()
	if !strings.Contains(sql, core.BillingEventsTable) {
		t.Fatalf("query must read %s: %q", core.BillingEventsTable, sql)
	}
	for _, ev := range core.InvoiceEvents {
		if !strings.Contains(sql, "'"+ev+"'") {
			t.Fatalf("query missing event %q", ev)
		}
	}
	if strings.Contains(sql, "?") {
		t.Fatalf("invoices state query takes no positional args: %q", sql)
	}
}
