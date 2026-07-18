// Package subscriptions is the fleet SUBSCRIPTION view (/v1/admin/subscriptions) —
// every tenant's plan subscription: customer/org, plan, status, monthly-normalized MRR,
// and the current-period start/renews. SuperAdmin only (core.Guard).
//
// Like invoices (and revenue) it fans out the org directory concurrently and reads each
// org's subscriptions via the admin S2S seam, tagging every row with its owning org. The
// MRR is monthly-normalized in the commerce reader so a yearly plan is comparable to a
// monthly one. Best-effort per org (a failed read contributes no rows, never fabricated
// ones); optional ?org= scopes to one tenant, ?status= filters, ?limit= caps.
package subscriptions

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

// defaultLimit caps the merged fleet subscription list when the caller sends none.
const defaultLimit = 500

// SubscriptionRow is one row of GET /v1/admin/subscriptions — a tenant's subscription at
// a glance, tagged with its owning org. MRR is USD cents; timestamps are RFC3339 strings.
type SubscriptionRow struct {
	ID       string `json:"id"`
	Org      string `json:"org"`
	Display  string `json:"display"`
	User     string `json:"user"`
	Plan     string `json:"plan"`
	Status   string `json:"status"`
	MRRCents int64  `json:"mrrCents"`
	Started  string `json:"started"`
	Renews   string `json:"renews"`
}

// Subscriptions answers GET /v1/admin/subscriptions.
//
//	GET /v1/admin/subscriptions?org=&status=&limit=
func Subscriptions(s *cloud.Service[core.State], c *zip.Ctx) error {
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

	// Per-org subscriptions, fanned out concurrently (best-effort per org).
	perOrg := make([][]SubscriptionRow, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			perOrg[i] = subscriptionsOf(s, ctx, o, status)
		}(i, o)
	}
	wg.Wait()

	rows := make([]SubscriptionRow, 0)
	for _, r := range perOrg {
		rows = append(rows, r...)
	}
	// Highest-MRR first (ties broken by most-recent start); cap to the merged limit.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].MRRCents != rows[j].MRRCents {
			return rows[i].MRRCents > rows[j].MRRCents
		}
		return rows[i].Started > rows[j].Started
	})
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return core.OKList(c, rows, total)
}

// subscriptionsOf reads one org's subscriptions into fleet rows, tagged with the org.
// Best-effort: a failed read yields no rows so the fleet view degrades honestly.
func subscriptionsOf(s *cloud.Service[core.State], ctx context.Context, o iam.Org, status string) []SubscriptionRow {
	entries, err := s.State.Commerce.Subscriptions(ctx, o.Name, status)
	if err != nil {
		return nil
	}
	display := core.Display(o.DisplayName, o.Name)
	rows := make([]SubscriptionRow, 0, len(entries))
	for _, sub := range entries {
		rows = append(rows, SubscriptionRow{
			ID:       sub.ID,
			Org:      o.Name,
			Display:  display,
			User:     sub.User,
			Plan:     sub.Plan,
			Status:   sub.Status,
			MRRCents: int64(sub.MRR),
			Started:  sub.Started,
			Renews:   sub.Renews,
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
