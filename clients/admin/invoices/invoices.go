// Package invoices is the fleet INVOICE view (/v1/admin/invoices) — every issued
// invoice across every tenant: number, org, amount, status, issue + due date, plus the
// id a future detail view fetches /v1/billing/invoices/:id with. SuperAdmin only
// (core.Guard).
//
// Commerce billing is per-tenant (an invoice lives in its org's own datastore
// namespace), so — like revenue — this fans out the org directory concurrently and
// reads each org's invoices via the admin S2S seam, tagging every row with its owning
// org. Best-effort per org: an org whose invoice read fails contributes NO rows rather
// than failing the fleet view (the SAME honest-degradation contract the customer list
// uses; an unreachable commerce yields an empty list, never fabricated rows). Optional
// ?org= scopes to one tenant, ?status= filters, ?limit= caps the merged list.
package invoices

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// defaultLimit caps the merged fleet invoice list when the caller sends none.
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
	cr := core.CallerCreds(c)
	status := strings.TrimSpace(c.Query("status"))
	wantOrg := strings.TrimSpace(c.Query("org"))
	limit := parseLimit(c.Query("limit"))

	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	if wantOrg != "" {
		orgs = filterOrg(orgs, wantOrg)
	}

	// Per-org invoices, fanned out concurrently (best-effort per org).
	perOrg := make([][]InvoiceRow, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			perOrg[i] = invoicesOf(s, ctx, o, status)
		}(i, o)
	}
	wg.Wait()

	rows := make([]InvoiceRow, 0)
	for _, r := range perOrg {
		rows = append(rows, r...)
	}
	// Newest issued first; cap to the merged limit (total reports the full pre-cap count).
	sort.Slice(rows, func(i, j int) bool { return rows[i].Issued > rows[j].Issued })
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return core.OKList(c, rows, total)
}

// invoicesOf reads one org's invoices into fleet rows, tagged with the org. Best-effort:
// a failed read yields no rows so the fleet view degrades honestly, never fabricating.
func invoicesOf(s *cloud.Service[core.State], ctx context.Context, o iam.Org, status string) []InvoiceRow {
	entries, err := s.State.Commerce.Invoices(ctx, o.Name, status)
	if err != nil {
		return nil
	}
	display := core.Display(o.DisplayName, o.Name)
	rows := make([]InvoiceRow, 0, len(entries))
	for _, inv := range entries {
		rows = append(rows, InvoiceRow{
			ID:          inv.ID,
			Number:      inv.Number,
			Org:         o.Name,
			Display:     display,
			Status:      inv.Status,
			AmountCents: int64(inv.AmountDue),
			Currency:    inv.Currency,
			Issued:      inv.Issued,
			Due:         inv.Due,
		})
	}
	return rows
}

// filterOrg narrows the directory to the one requested org (empty when it does not
// exist — an honest empty list, never a fabricated tenant).
func filterOrg(orgs []iam.Org, want string) []iam.Org {
	for _, o := range orgs {
		if o.Name == want {
			return []iam.Org{o}
		}
	}
	return nil
}

// parseLimit clamps the merged-list cap to [1,5000], defaulting to defaultLimit.
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
