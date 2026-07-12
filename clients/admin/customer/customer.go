// Package customer is the CUSTOMER management surface (/v1/admin/customers*) — the
// operator cockpit's core: the live fleet customer list (incl. new self-serve signups),
// one-customer detail, and the audited management ACTIONS (grant credit, suspend,
// reactivate).
//
// It aggregates the SAME real upstreams the rest of admin reads — IAM for the org
// directory + user/owner/status, commerce for balance/spend/plan/ledger — and adds the
// two write levers an operator needs:
//
//   - GRANT CREDIT is a real commerce Deposit landing in the org's own wallet, via the
//     ONE core credit-write path (core.ApplyGrant).
//   - SUSPEND / REACTIVATE flips IAM `isForbidden` on the org's users — IAM refuses a
//     forbidden user at login AND at token issuance, so a suspended customer cannot sign
//     in or mint a fresh token. Fully reversible.
//
// SECURITY. Every route is mounted behind core.Guard (SuperAdmin only, fail-closed).
// The write actions REPLAY THE CALLER'S OWN SuperAdmin credential to IAM, and each is
// recorded to cloud's tamper-evident audit trail with a redacted BEFORE/AFTER.
package customer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// ── wire shapes (operator contract) ──────────────────────────────────────────

// CustomerRow is one row in GET /v1/admin/customers — a fleet customer at a glance.
type CustomerRow struct {
	Org          string `json:"org"`
	Display      string `json:"display"`
	OwnerEmail   string `json:"ownerEmail"`
	Plan         string `json:"plan"`
	Status       string `json:"status"` // "active" | "suspended"
	Users        int    `json:"users"`
	BalanceCents int64  `json:"balanceCents"`
	SpendCents   int64  `json:"spendCents"`
	MRRCents     int64  `json:"mrrCents"`
	Created      string `json:"created"`
	LastActive   string `json:"lastActive"`
}

// CustomerUser is one member in the customer detail (no secrets — the AccessKey PRESENCE
// is surfaced as hasApiKey, never the key itself).
type CustomerUser struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	IsAdmin    bool   `json:"isAdmin"`
	Forbidden  bool   `json:"forbidden"`
	HasAPIKey  bool   `json:"hasApiKey"`
	LastSignin string `json:"lastSignin"`
	Created    string `json:"created"`
}

// CustomerTxn is one ledger row in the detail's top-up/usage history.
type CustomerTxn struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "deposit" (credit) | "withdraw" (usage)
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
	Notes    string `json:"notes,omitempty"`
	Time     string `json:"time"`
}

// CustomerDetailData is the GET /v1/admin/customers/:org payload.
type CustomerDetailData struct {
	Org          string         `json:"org"`
	Display      string         `json:"display"`
	OwnerEmail   string         `json:"ownerEmail"`
	Plan         string         `json:"plan"`
	Status       string         `json:"status"`
	Created      string         `json:"created"`
	BalanceCents int64          `json:"balanceCents"`
	SpendCents   int64          `json:"spendCents"`
	MRRCents     int64          `json:"mrrCents"`
	APIKeys      int            `json:"apiKeys"`
	Users        []CustomerUser `json:"users"`
	Transactions []CustomerTxn  `json:"transactions"`
}

// ── GET /v1/admin/customers — the fleet customer list ────────────────────────

// Customers answers GET /v1/admin/customers.
func Customers(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return core.Fail(c, err.Error())
	}

	rows := make([]CustomerRow, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i] = enrichCustomer(s, ctx, cr, o)
		}(i, o)
	}
	wg.Wait()

	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return core.OKList(c, rows, len(rows))
}

// enrichCustomer folds one org's real IAM + commerce reads into a customer row. Each read
// is best-effort: an upstream miss degrades that field to its honest zero/empty (never a
// fabricated value), so one flaky org never fails the fleet.
func enrichCustomer(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds, o iam.Org) CustomerRow {
	users, _ := orgUsers(s, ctx, cr, o.Name)
	spend, credits, _ := core.OrgMoney(s, ctx, o.Name)
	plan, _ := s.State.Commerce.Plan(ctx, o.Name)

	return CustomerRow{
		Org:          o.Name,
		Display:      core.Display(o.DisplayName, o.Name),
		OwnerEmail:   ownerEmail(users),
		Plan:         plan.Name,
		Status:       statusOf(users),
		Users:        len(users),
		BalanceCents: credits,
		SpendCents:   spend,
		MRRCents:     int64(plan.MRR),
		Created:      o.CreatedTime,
		LastActive:   lastActiveOf(users),
	}
}

// ── GET /v1/admin/customers/:org — one customer's detail ─────────────────────

// CustomerDetail answers GET /v1/admin/customers/:org.
func CustomerDetail(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	org := customerOrgParam(c)
	if org == "" {
		return core.Fail(c, "org is required")
	}

	o, err := core.FindOrg(s, ctx, cr, org)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	users, _ := orgUsers(s, ctx, cr, org)
	spend, credits, _ := core.OrgMoney(s, ctx, org)
	plan, _ := s.State.Commerce.Plan(ctx, org)
	ledgerEntries, _ := s.State.Commerce.Ledger(ctx, org, 50)

	rows := make([]CustomerUser, 0, len(users))
	apiKeys := 0
	for _, u := range users {
		hasKey := strings.TrimSpace(u.AccessKey) != ""
		if hasKey {
			apiKeys++
		}
		rows = append(rows, CustomerUser{
			Name:       u.Name,
			Email:      u.Email,
			IsAdmin:    u.IsAdmin,
			Forbidden:  u.IsForbidden,
			HasAPIKey:  hasKey,
			LastSignin: u.LastSigninTime,
			Created:    u.CreatedTime,
		})
	}

	ledger := make([]CustomerTxn, 0, len(ledgerEntries))
	for _, e := range ledgerEntries {
		ledger = append(ledger, CustomerTxn{
			ID:       e.ID,
			Type:     e.Kind,
			Cents:    int64(e.Amount),
			Currency: e.Currency,
			Notes:    e.Notes,
			Time:     e.At,
		})
	}

	return core.OK(c, CustomerDetailData{
		Org:          org,
		Display:      core.Display(o.DisplayName, org),
		OwnerEmail:   ownerEmail(users),
		Plan:         plan.Name,
		Status:       statusOf(users),
		Created:      o.CreatedTime,
		BalanceCents: credits,
		SpendCents:   spend,
		MRRCents:     int64(plan.MRR),
		APIKeys:      apiKeys,
		Users:        rows,
		Transactions: ledger,
	})
}

// ── POST /v1/admin/customers/:org/credit — grant credit ──────────────────────

// GrantCredit answers POST /v1/admin/customers/:org/credit — a staff credit grant to the
// path org, funneled through the ONE core credit-write path (core.ApplyGrant).
func GrantCredit(s *cloud.Service[core.State], c *zip.Ctx) error {
	org := customerOrgParam(c)
	if org == "" {
		return core.Fail(c, "org is required")
	}
	var req core.CreditRequest
	if err := c.Bind(&req); err != nil {
		return core.Fail(c, "invalid request body")
	}
	return core.ApplyGrant(s, c, org, req)
}

// ── POST /v1/admin/customers/:org/{suspend,reactivate} — access control ──────

// SuspendCustomer forbids every member of the org (cuts login + token issuance).
func SuspendCustomer(s *cloud.Service[core.State], c *zip.Ctx) error { return setForbidden(s, c, true) }

// ReactivateCustomer restores access for every member of the org.
func ReactivateCustomer(s *cloud.Service[core.State], c *zip.Ctx) error {
	return setForbidden(s, c, false)
}

// setForbidden flips IAM `isForbidden` on every member of the org — suspend
// (forbidden=true) cuts login + token issuance; reactivate restores it. Each user's FULL
// object is read, the one field flipped, and written back, replaying the caller's
// SuperAdmin credential so IAM authorizes it. Best-effort per user with an aggregated
// result: a partial failure is reported honestly (affected vs failed), never masked as a
// clean success. The action is recorded with a redacted before/after user tally.
func setForbidden(s *cloud.Service[core.State], c *zip.Ctx, forbidden bool) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	org := customerOrgParam(c)
	if org == "" {
		return core.Fail(c, "org is required")
	}

	o, err := core.FindOrg(s, ctx, cr, org)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	users, err := orgUsers(s, ctx, cr, org)
	if err != nil {
		return core.Fail(c, err.Error())
	}

	beforeForbidden := 0
	for _, u := range users {
		if u.IsForbidden {
			beforeForbidden++
		}
	}

	var affected, failed []string
	for _, u := range users {
		id := u.Owner + "/" + u.Name
		full, gerr := s.State.IAM.User(ctx, cr, id)
		if gerr != nil {
			failed = append(failed, u.Name)
			continue
		}
		full["isForbidden"] = forbidden
		if uerr := s.State.IAM.SetUser(ctx, cr, id, full); uerr != nil {
			failed = append(failed, u.Name)
			continue
		}
		affected = append(affected, u.Name)
	}

	action := "admin.customer.suspend"
	if !forbidden {
		action = "admin.customer.reactivate"
	}
	result := "success"
	reason := ""
	if len(failed) > 0 {
		result = "error"
		reason = fmt.Sprintf("%d user(s) not updated", len(failed))
	}
	core.EmitAudit(s, c, action, "customer", org,
		map[string]any{"suspended": beforeForbidden == len(users) && len(users) > 0, "forbiddenUsers": beforeForbidden, "totalUsers": len(users)},
		map[string]any{"suspended": forbidden, "affected": affected, "failed": failed},
		audit.Outcome{Result: result, Status: 200, Reason: reason})

	return core.OK(c, map[string]any{
		"org":       org,
		"suspended": forbidden,
		"affected":  affected,
		"failed":    failed,
	})
}

// ── aggregation + derivation helpers ─────────────────────────────────────────

// orgUsers reads an org's members (a bounded page) as the typed subset the customer
// surface folds over. It is the ONE IAM read that yields the user count, the owner email,
// the suspend status, and the API-key presence — so a customer row costs a single
// get-users call, not four.
func orgUsers(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds, org string) ([]iam.User, error) {
	q := url.Values{}
	q.Set("owner", org)
	q.Set("p", "1")
	q.Set("pageSize", "200")
	res, err := s.State.IAM.Users(ctx, cr, q)
	if err != nil {
		return nil, err
	}
	var raw []iam.User
	if len(res.Rows) > 0 {
		if err := json.Unmarshal(res.Rows, &raw); err != nil {
			return nil, fmt.Errorf("users decode: %w", err)
		}
	}
	return raw, nil
}

// ownerEmail picks the org's admin user's email (the account owner), falling back to the
// first user with an email. Empty when no user carries one.
func ownerEmail(users []iam.User) string {
	for _, u := range users {
		if u.IsAdmin && strings.TrimSpace(u.Email) != "" {
			return u.Email
		}
	}
	for _, u := range users {
		if strings.TrimSpace(u.Email) != "" {
			return u.Email
		}
	}
	return ""
}

// statusOf derives the suspend status: an org is "suspended" only when it has at least
// one user and EVERY user is forbidden (a partial forbid is still "active"). Honest by
// construction.
func statusOf(users []iam.User) string {
	if len(users) == 0 {
		return "active"
	}
	for _, u := range users {
		if !u.IsForbidden {
			return "active"
		}
	}
	return "suspended"
}

// lastActiveOf returns the most recent user sign-in across the org (RFC3339), the best
// "last active" signal available from IAM. Empty when no user has signed in.
func lastActiveOf(users []iam.User) string {
	last := ""
	for _, u := range users {
		if u.LastSigninTime > last {
			last = u.LastSigninTime
		}
	}
	return last
}

// customerOrgParam reads + trims the :org path param.
func customerOrgParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("org")) }
