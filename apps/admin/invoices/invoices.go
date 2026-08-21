// Package invoices is the fleet INVOICE view (/v1/admin/invoices) — every issued
// invoice across every tenant: number, org, amount, status, issue + due date, plus the
// id a future detail view fetches /v1/billing/invoices/:id with. SuperAdmin only
// (core.Admit).
//
// It reads the ONE shared warehouse (commerce.events) — the table the commerce
// analytics collector lands every invoice-lifecycle event in — over the SAME client
// (datastore.Query) the o11y/compute lenses use, with ZERO per-org fan-out:
// one GROUP BY resolves each invoice's LATEST lifecycle state (argMax by timestamp),
// so the whole fleet is one query, not N per-org commerce reads. Honest by
// construction: no datastore connected or the collector's table not provisioned yet →
// the real empty list, never a fabricated row. Optional ?org= scopes to one tenant,
// ?status= filters the LATEST status, ?limit= caps the list.
package invoices

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
)

// defaultLimit caps the fleet invoice list when the caller sends none.
const defaultLimit = 500

// InvoiceRow is one row of GET /v1/admin/invoices — an issued invoice at a glance,
// tagged with its owning org. Money is USD cents; timestamps are RFC3339 strings.
type InvoiceRow struct {
	// ID is commerce's invoice id — the row's identity, and what a detail view fetches
	// /v1/billing/invoices/:id with.
	ID string `json:"id"`
	// Number is the human invoice number the customer sees on the document. Distinct from
	// ID, which is the machine handle.
	Number string `json:"number"`
	// Org is the tenant the invoice was issued to, and what ?org= matches exactly.
	Org string `json:"org"`
	// Display is the same slug as Org. The warehouse holds no friendly name and this
	// read does no per-org IAM fan-out, so it repeats the slug rather than inventing one.
	Display string `json:"display"`
	// Status is the EFFECTIVE lifecycle state, folded from the invoice's latest event:
	// `paid` and `void` are terminal and come from the event itself; anything else is the
	// last status the events carried, defaulting to `open` for a finalized invoice.
	Status string `json:"status"`
	// AmountCents is the invoice total in minor units of Currency, as of its latest
	// event. It is the amount BILLED — a partially paid invoice does not report a
	// remainder here.
	AmountCents int64 `json:"amountCents"`
	// Currency is the invoice's ISO code. AmountCents is minor units of THIS, so a list
	// spanning currencies must not be summed without reading it.
	Currency string `json:"currency"`
	// Issued is when the invoice was issued, RFC3339 as commerce emitted it. The list is
	// sorted by it, newest first.
	Issued string `json:"issued"`
	// Due is when payment is due, RFC3339. Empty when the invoice carries no due date.
	Due string `json:"due"`
}

// Invoices answers GET /v1/admin/invoices.
//
//	GET /v1/admin/invoices?org=&status=&limit=
func Invoices(ctx context.Context, in *InvoicesIn) (*InvoicesOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	wantOrg := strings.TrimSpace(in.Org)
	limit := parseLimit(in.Limit)

	// Honest-empty when the warehouse is not connected or the collector's events
	// table is not provisioned yet (the emitter is still being wired).
	if !core.BillingEventsReady(ctx) {
		return &InvoicesOut{Status: core.OK, Data: []InvoiceRow{}, Total: core.Total(0)}, nil
	}

	rows, err := datastore.Query(ctx, invoicesSQL())
	if err != nil {
		return &InvoicesOut{Status: core.Err, Msg: "invoices query: " + err.Error()}, nil
	}
	all := invoiceRowsFromRows(rows)

	// Filter (latest status / org) then newest issued first, cap to limit.
	out := make([]InvoiceRow, 0, len(all))
	for _, r := range all {
		if wantOrg != "" && r.Org != wantOrg {
			continue
		}
		if status != "" && strings.ToLower(r.Status) != status {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Issued > out[j].Issued })
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return &InvoicesOut{Status: core.OK, Data: out, Total: core.Total(total)}, nil
}

// InvoicesIn is the GET /v1/admin/invoices filter.
type InvoicesIn struct {
	// Status filters on the invoice's LATEST lifecycle status (paid, open, void, …),
	// matched case-insensitively.
	Status string `json:"status"`
	// Org filters to one tenant, matched exactly.
	Org string `json:"org"`
	// Limit caps the rows returned. total still reports the full match count.
	Limit string `json:"limit"`
}

// InvoicesOut is the GET /v1/admin/invoices envelope. total is the count BEFORE limit
// truncates, so the console can say "showing 50 of 812".
type InvoicesOut struct {
	// Status is "ok" or "error". A warehouse that is not connected answers ok with an
	// empty list and total 0 — the honest not-yet-wired state, not a claim that the fleet
	// has never invoiced anyone.
	Status string `json:"status"`
	// Msg is the query failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the matching invoices, newest issued first, capped by limit.
	Data []InvoiceRow `json:"data"`
	// Total is how many matched BEFORE limit truncated, so a console can say "showing 50
	// of 812". Omitted on an error.
	Total *int `json:"total,omitempty"`
}

// invoicesSQL resolves each invoice's LATEST lifecycle state from commerce.events
// (argMax by timestamp). Static SQL over a closed event-name set (SQLInList of
// server constants) — no user input is interpolated, so it is injection-safe.
func invoicesSQL() string {
	return "SELECT JSONExtractString(properties, 'invoice_id') AS id, " +
		"argMax(JSONExtractString(properties, 'number'), timestamp) AS number, " +
		"argMax(organization_id, timestamp) AS org, " +
		"argMax(JSONExtractString(properties, 'status'), timestamp) AS status, " +
		"argMax(JSONExtractInt(properties, 'amount_cents'), timestamp) AS amount_cents, " +
		"argMax(JSONExtractString(properties, 'currency'), timestamp) AS currency, " +
		"argMax(JSONExtractString(properties, 'issued'), timestamp) AS issued, " +
		"argMax(JSONExtractString(properties, 'due'), timestamp) AS due, " +
		"argMax(event, timestamp) AS last_event " +
		"FROM " + core.BillingEventsTable + " " +
		"WHERE event IN (" + core.SQLInList(core.InvoiceEvents) + ") " +
		"AND JSONExtractString(properties, 'invoice_id') != '' " +
		"GROUP BY id"
}

// invoiceRowsFromRows maps the datastore rows onto []InvoiceRow (pure). Display is
// the org slug — the warehouse holds no friendly name and admin does no per-org IAM
// fan-out here (honest, not fabricated). Status folds the lifecycle from the latest
// event so a paid/voided invoice reads correctly regardless of the status snapshot.
func invoiceRowsFromRows(rows []map[string]any) []InvoiceRow {
	out := make([]InvoiceRow, 0, len(rows))
	for _, r := range rows {
		org := core.CHStr(r["org"])
		out = append(out, InvoiceRow{
			ID:          core.CHStr(r["id"]),
			Number:      core.CHStr(r["number"]),
			Org:         org,
			Display:     org,
			Status:      foldInvoiceStatus(core.CHStr(r["last_event"]), core.CHStr(r["status"])),
			AmountCents: core.CHInt64(r["amount_cents"]),
			Currency:    core.CHStr(r["currency"]),
			Issued:      core.CHStr(r["issued"]),
			Due:         core.CHStr(r["due"]),
		})
	}
	return out
}

// foldInvoiceStatus resolves the effective status from the latest lifecycle event
// (paid / void terminal), falling back to the last-emitted status snapshot (open
// for a finalized invoice) when the event is a finalize.
func foldInvoiceStatus(lastEvent, snapshot string) string {
	switch lastEvent {
	case core.EvInvoicePaid:
		return "paid"
	case core.EvInvoiceVoid:
		return "void"
	}
	if s := strings.TrimSpace(snapshot); s != "" {
		return s
	}
	return "open"
}

// parseLimit clamps the fleet-list cap to [1,5000], defaulting to defaultLimit.
func parseLimit(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > 5000 {
		return 5000
	}
	return n
}
