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
// SECURITY. Every op calls core.Admit (SuperAdmin only, fail-closed) on its first line.
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
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/iam"
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

// CustomersOut is the GET /v1/admin/customers envelope. data2 == len(data): the list is
// every customer, unpaginated.
type CustomersOut struct {
	Status string        `json:"status"`
	Msg    string        `json:"msg"`
	Data   []CustomerRow `json:"data"`
	Data2  *int          `json:"data2,omitempty"`
}

// ── GET /v1/admin/customers — the fleet customer list ────────────────────────

// Customers lists every customer org at a glance, sorted by slug. Each row carries owner
// email, plan, suspend status, member count, balance, month-to-date spend and MRR.
//
// Each row costs one IAM read plus the org's money reads, fanned out under a fixed
// concurrency ceiling so a large fleet cannot stampede the upstreams. Every read is
// best-effort per row: an upstream miss degrades THAT field to its honest zero rather
// than failing the fleet.
//
// Response: {"status":"ok","msg":"","data":[{"org":"acme","display":"Acme",
// "ownerEmail":"ada@acme.com","plan":"pro","status":"active","users":7,"balanceCents":5000,
// "spendCents":12500,"mrrCents":9900,"created":"2026-01-04T00:00:00Z",
// "lastActive":"2026-07-26T18:00:00Z"}],"data2":1}
func (o ops) Customers(ctx context.Context, _ *core.None) (*CustomersOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	cr := core.CallerCreds(c)
	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return &CustomersOut{Status: core.Err, Msg: err.Error()}, nil
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
	return &CustomersOut{Status: core.OK, Data: rows, Data2: core.Total(len(rows))}, nil
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
func (o ops) CustomerDetail(ctx context.Context, in *OrgIn) (*CustomerDetailOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	cr := core.CallerCreds(c)
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return &CustomerDetailOut{Status: core.Err, Msg: "org is required"}, nil
	}

	row, err := core.FindOrg(s, ctx, cr, org)
	if err != nil {
		return &CustomerDetailOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if row == nil {
		// 404 with the envelope body: the status is the addition, not a different
		// contract, so the console decodes this exactly like any other failure.
		c.Status(404)
		return &CustomerDetailOut{Status: core.Err, Msg: "customer not found"}, nil
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

	return &CustomerDetailOut{Status: core.OK, Data: &CustomerDetailData{
		Org:          org,
		Display:      core.Display(row.DisplayName, org),
		OwnerEmail:   ownerEmail(users),
		Plan:         plan.Name,
		Status:       statusOf(users),
		Created:      row.CreatedTime,
		BalanceCents: credits,
		SpendCents:   spend,
		MRRCents:     int64(plan.MRR),
		APIKeys:      apiKeys,
		Users:        rows,
		Transactions: ledger,
	}}, nil
}

// ── POST /v1/admin/customers/:org/credit — grant credit ──────────────────────

// GrantCredit issues a staff credit grant to the org named in the path. The grant is a
// comp, refund or promo, written through the ONE credit-write path core.ApplyGrant, which
// validates the amount against the per-grant cap, checks the org exists, moves the money
// and records the tamper-evident audit row.
//
// The credit lands on the account account.Payer resolves, NOT necessarily the org: name
// a member of a pooled org and the pool is credited. The receipt echoes the subject so
// the caller can see which.
//
// Example: {"amountCents":5000,"currency":"usd","reason":"launch comp","source":"trial"}
// Response: {"status":"ok","msg":"","data":{"org":"acme","subject":"acme","grantedCents":5000,
// "currency":"usd","source":"trial","balanceCents":10000,
// "balanceExact":"100.000000000000000000","transactionId":"tx_01J"}}
func (o ops) GrantCredit(ctx context.Context, in *GrantIn) (*core.GrantOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return &core.GrantOut{Status: core.Err, Msg: "org is required"}, nil
	}
	return core.ApplyGrant(s, c, org, in.credit())
}

// OrgIn addresses ONE customer by the org slug in the path. It is the input of every
// per-customer op that carries no body.
type OrgIn struct {
	// Org is the tenant slug from the path.
	Org string `json:"org"`
}

// CustomerDetailOut is the GET /v1/admin/customers/:org envelope.
type CustomerDetailOut struct {
	Status string              `json:"status"`
	Msg    string              `json:"msg"`
	Data   *CustomerDetailData `json:"data"`
}

// AccessChange is what a suspend or reactivate DID, per user. A partial failure is
// reported honestly here rather than masked as a clean success.
type AccessChange struct {
	// Org is the tenant acted on.
	Org string `json:"org"`
	// Suspended is the state applied: true for suspend, false for reactivate.
	Suspended bool `json:"suspended"`
	// Affected lists the usernames that were updated.
	Affected []string `json:"affected"`
	// Failed lists the usernames that were NOT updated. Non-empty means the org is in
	// a mixed state and the action should be retried.
	Failed []string `json:"failed"`
}

// AccessOut is the envelope of the suspend and reactivate ops.
type AccessOut struct {
	Status string        `json:"status"`
	Msg    string        `json:"msg"`
	Data   *AccessChange `json:"data"`
}

// ── POST /v1/admin/customers/:org/{suspend,reactivate} — access control ──────

// SuspendCustomer cuts off every member of the org. IAM refuses a forbidden user at
// login AND at token issuance, so a suspended customer can neither sign in nor mint a
// fresh token. Fully reversible with ReactivateCustomer.
//
// The result names every user updated and every user that was NOT — a partial failure
// leaves the org in a mixed state and says so instead of reporting a clean success.
//
// Response: {"status":"ok","msg":"","data":{"org":"acme","suspended":true,
// "affected":["ada","bob"],"failed":[]}}
func (o ops) SuspendCustomer(ctx context.Context, in *OrgIn) (*AccessOut, error) {
	return o.setForbidden(ctx, in.Org, true)
}

// ReactivateCustomer restores access for every member of the org, undoing a suspend. It
// reports the same per-user breakdown.
//
// Response: {"status":"ok","msg":"","data":{"org":"acme","suspended":false,
// "affected":["ada","bob"],"failed":[]}}
func (o ops) ReactivateCustomer(ctx context.Context, in *OrgIn) (*AccessOut, error) {
	return o.setForbidden(ctx, in.Org, false)
}

// setForbidden flips IAM `isForbidden` on every member of the org — suspend
// (forbidden=true) cuts login + token issuance; reactivate restores it. Each user's FULL
// object is read, the one field flipped, and written back, replaying the caller's
// SuperAdmin credential so IAM authorizes it. Best-effort per user with an aggregated
// result: a partial failure is reported honestly (affected vs failed), never masked as a
// clean success. The action is recorded with a redacted before/after user tally.
func (o ops) setForbidden(ctx context.Context, want string, forbidden bool) (*AccessOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	cr := core.CallerCreds(c)
	org := strings.TrimSpace(want)
	if org == "" {
		return &AccessOut{Status: core.Err, Msg: "org is required"}, nil
	}

	row, err := core.FindOrg(s, ctx, cr, org)
	if err != nil {
		return &AccessOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if row == nil {
		c.Status(404)
		return &AccessOut{Status: core.Err, Msg: "customer not found"}, nil
	}

	users, err := orgUsers(s, ctx, cr, org)
	if err != nil {
		return &AccessOut{Status: core.Err, Msg: err.Error()}, nil
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

	return &AccessOut{Status: core.OK, Data: &AccessChange{
		Org:       org,
		Suspended: forbidden,
		Affected:  affected,
		Failed:    failed,
	}}, nil
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
