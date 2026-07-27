// Package invoices is the fleet INVOICE view (/v1/admin/invoices) — every issued
// invoice across every tenant: number, org, amount, status, issue + due date, plus the
// id a future detail view fetches /v1/billing/invoices/:id with. SuperAdmin only
// (core.Guard).
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
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/zap-proto/zip"
)

// defaultLimit caps the fleet invoice list when the caller sends none.
const defaultLimit = 500

// InvoiceRow is one row of GET /v1/admin/invoices — an issued invoice at a glance,
// tagged with its owning org. Money is USD cents; timestamps are RFC3339 strings.
type InvoiceRow struct {
	ID          string `json:"id"`
	Number      string `json:"number"`
	Org         string `json:"org"`
	Display     string `json:"display"`
	Status      string `json:"status"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Issued      string `json:"issued"`
	Due         string `json:"due"`
}

// Invoices answers GET /v1/admin/invoices.
//
//	GET /v1/admin/invoices?org=&status=&limit=
func Invoices(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	wantOrg := strings.TrimSpace(c.Query("org"))
	limit := parseLimit(c.Query("limit"))

	// Honest-empty when the warehouse is not connected or the collector's events
	// table is not provisioned yet (the emitter is still being wired).
	if !core.BillingEventsReady(ctx) {
		return core.OKList(c, []InvoiceRow{}, 0)
	}

	rows, err := datastore.Query(ctx, invoicesSQL())
	if err != nil {
		return core.Fail(c, "invoices query: "+err.Error())
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
	return core.OKList(c, out, total)
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
