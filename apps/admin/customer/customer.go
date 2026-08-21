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
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/hanzoai/cloud/audit"
)

// ── wire shapes (operator contract) ──────────────────────────────────────────

// CustomerRow is one row in GET /v1/admin/customers — a fleet customer at a glance.
type CustomerRow struct {
	// Org is the tenant slug — the row's identity, and the value every per-customer route
	// takes in its path. The list is sorted by it.
	Org string `json:"org"`
	// Display is the org's display name, falling back to the slug when it has none. For
	// humans; never key on it.
	Display string `json:"display"`
	// OwnerEmail is the org admin's email, or the first member with one. Empty when no
	// member carries an email at all — IAM does not require one.
	OwnerEmail string `json:"ownerEmail"`
	// Plan is the name of the org's live subscription tier, or "pay-as-you-go" for a
	// metered customer with no subscription. A trial names its plan here even though it
	// contributes no MRR.
	Plan string `json:"plan"`
	// Status is `active` or `suspended`, derived from the members' IAM isForbidden flags.
	// Suspended requires the org to have members and EVERY one of them to be forbidden —
	// a partly-forbidden org still reads active, because some of it can still sign in.
	Status string `json:"status"`
	// Users is how many members IAM lists for the org, up to the 200-member page this
	// read takes. Every other IAM-derived field on the row is folded from that same page.
	Users int `json:"users"`
	// BalanceCents is the prepaid wallet the org still holds, in USD cents. What is left
	// to spend, not what was granted.
	BalanceCents int64 `json:"balanceCents"`
	// SpendCents is metered consumption over the TRAILING 30 DAYS, in USD cents — the one
	// window every fleet money surface means by "spend". Positive, and it does not reset
	// on the first of the month.
	SpendCents int64 `json:"spendCents"`
	// MRRCents is the org's monthly recurring revenue, in USD cents: commerce's own
	// per-subscription figure, already interval-normalized and multiplied by seats, summed
	// over the subscriptions commerce counts as revenue. A trial contributes zero.
	MRRCents int64 `json:"mrrCents"`
	// Created is when IAM created the org, RFC3339 — the signup date.
	Created string `json:"created"`
	// LastActive is the most recent sign-in across every member, RFC3339. The best
	// activity signal IAM carries, so it tracks people logging in, not API traffic. Empty
	// when no member has ever signed in.
	LastActive string `json:"lastActive"`
}

// CustomerUser is one member in the customer detail (no secrets — the AccessKey PRESENCE
// is surfaced as hasApiKey, never the key itself).
type CustomerUser struct {
	// Name is the IAM username, unique within the org. `<org>/<name>` is the id the
	// suspend and reactivate actions address the user by.
	Name string `json:"name"`
	// Email is the member's address, empty when IAM holds none.
	Email string `json:"email"`
	// IsAdmin is admin OF THIS ORG — the customer's own account owner, who manages their
	// members and apps. It is NOT platform privilege: SuperAdmin is membership of the
	// reserved `admin` org and never appears on a customer row.
	IsAdmin bool `json:"isAdmin"`
	// Forbidden is IAM's isForbidden. True means IAM refuses this user at login AND at
	// token issuance, so they can neither sign in nor mint a fresh one. It is what
	// suspend sets and reactivate clears, per user — which is why an org can sit in a
	// mixed state after a partial failure.
	Forbidden bool `json:"forbidden"`
	// HasAPIKey reports that the member holds an access key. PRESENCE only: the key
	// itself is never read onto this surface.
	HasAPIKey bool `json:"hasApiKey"`
	// LastSignin is the member's most recent sign-in, RFC3339. Empty means never signed
	// in — which is different from signed in long ago.
	LastSignin string `json:"lastSignin"`
	// Created is when IAM created the member, RFC3339.
	Created string `json:"created"`
}

// CustomerTxn is one ledger row in the detail's top-up/usage history.
type CustomerTxn struct {
	// ID is the commerce ledger entry id — what reconciliation against commerce joins on.
	ID string `json:"id"`
	// Type is `deposit` (money in: a top-up or a staff grant) or `withdraw` (money out:
	// metered usage). It is the only thing that gives Cents its direction.
	Type string `json:"type"`
	// Cents is the entry's magnitude in minor units, and it is UNSIGNED — a withdraw
	// does not arrive negative, so a running balance has to be folded by reading Type.
	Cents int64 `json:"cents"`
	// Currency is the entry's ISO code, lower-cased. "usd" throughout the fleet today.
	Currency string `json:"currency"`
	// Notes is the free-text memo the entry was written with — for a staff grant, the
	// reason an operator typed. Omitted when blank.
	Notes string `json:"notes,omitempty"`
	// Time is when the entry was recorded, RFC3339. The history is newest first.
	Time string `json:"time"`
}

// CustomerDetailData is the GET /v1/admin/customers/:org payload.
type CustomerDetailData struct {
	// Org is the tenant slug, echoed from the path.
	Org string `json:"org"`
	// Display is the org's display name, falling back to the slug when it has none.
	Display string `json:"display"`
	// OwnerEmail is the org admin's email, or the first member with one. Empty when no
	// member carries an email.
	OwnerEmail string `json:"ownerEmail"`
	// Plan is the live subscription tier's name, or "pay-as-you-go" with no subscription.
	Plan string `json:"plan"`
	// Status is `active` or `suspended` — suspended only when the org has members and
	// every one of them is forbidden. The per-member truth is in users[].forbidden.
	Status string `json:"status"`
	// Created is when IAM created the org, RFC3339.
	Created string `json:"created"`
	// BalanceCents is the prepaid wallet still held, in USD cents.
	BalanceCents int64 `json:"balanceCents"`
	// SpendCents is metered consumption over the trailing 30 days, in USD cents.
	SpendCents int64 `json:"spendCents"`
	// MRRCents is monthly recurring revenue, in USD cents, over the subscriptions
	// commerce counts as revenue. A trial contributes zero while still naming Plan.
	MRRCents int64 `json:"mrrCents"`
	// APIKeys is how many members hold an access key — the org's programmatic reach,
	// counted from users[] and never the keys themselves.
	APIKeys int `json:"apiKeys"`
	// Users is the org's members, up to a 200-member page. Suspend and reactivate act on
	// exactly this set.
	Users []CustomerUser `json:"users"`
	// Transactions is the org's ledger, newest first, capped at the last 50 entries. A
	// sample of the history, not the whole of it, so the balance above cannot be
	// recomputed by folding this.
	Transactions []CustomerTxn `json:"transactions"`
}

// CustomersOut is the GET /v1/admin/customers envelope. total == len(data): the list is
// every customer, unpaginated.
type CustomersOut struct {
	// Status is "ok" or "error". An error here means the IAM org directory could not be
	// read; a per-org money or plan failure does not fail the fleet, it degrades that
	// row's field to its zero.
	Status string `json:"status"`
	// Msg is the failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is every customer org, sorted by slug.
	Data []CustomerRow `json:"data"`
	// Total is len(data). The list is unpaginated, so it is a convenience, never a
	// count of rows withheld. Omitted on an error.
	Total *int `json:"total,omitempty"`
}

// ── GET /v1/admin/customers — the fleet customer list ────────────────────────

// Customers lists every customer org at a glance, sorted by slug: owner email, plan,
// suspend status, member count, balance, month-to-date spend and MRR.
//
// Each row costs one IAM read plus the org's money reads, fanned out under a fixed
// concurrency ceiling so a large fleet cannot stampede the upstreams. Every read is
// best-effort per row: an upstream miss degrades THAT field to its honest zero rather
// than failing the fleet.
//
// Response: {"status":"ok","msg":"","data":[{"org":"acme","display":"Acme",
// "ownerEmail":"ada@acme.com","plan":"pro","status":"active","users":7,"balanceCents":5000,
// "spendCents":12500,"mrrCents":9900,"created":"2026-01-04T00:00:00Z",
// "lastActive":"2026-07-26T18:00:00Z"}],"total":1}
func (o ops) Customers(ctx context.Context, _ *core.None) (*CustomersOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	cr := core.CallerCreds(c)
	// Money is read per org, and three of these folds run their orgs in parallel, so
	// the principal is lifted off the request ONCE here and re-pointed per tenant.
	ctx = core.Acting(c)
	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return &CustomersOut{Status: core.Err, Msg: err.Error()}, nil
	}

	// ONE delegation for the whole fan-out — see core.Delegate.
	money := core.Delegate(ctx)
	rows := make([]CustomerRow, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i] = enrichCustomer(s, ctx, money, cr, o)
		}(i, o)
	}
	wg.Wait()

	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return &CustomersOut{Status: core.OK, Data: rows, Total: core.Total(len(rows))}, nil
}

// enrichCustomer folds one org's real IAM + commerce reads into a customer row. Each read
// is best-effort: an upstream miss degrades that field to its honest zero/empty (never a
// fabricated value), so one flaky org never fails the fleet.
func enrichCustomer(s *cloud.Service[core.State], ctx context.Context, money core.Delegated, cr iam.Creds, o iam.Org) CustomerRow {
	users, _ := orgUsers(s, ctx, cr, o.Name)
	spend, credits, _ := core.OrgMoney(s, money, o.Name)
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
	// Money is read per org, and three of these folds run their orgs in parallel, so
	// the principal is lifted off the request ONCE here and re-pointed per tenant.
	ctx = core.Acting(c)
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
	spend, credits, _ := core.OrgMoney(s, core.Delegate(ctx), org)
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

// GrantCredit issues a staff credit grant to the org named in the path — a comp, refund
// or promo — through the ONE credit-write path core.ApplyGrant, which validates the
// amount against the per-grant cap, checks the org exists, moves the money and records
// the tamper-evident audit row.
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
	// Status is "ok" or "error". An unknown org is an error and also carries HTTP 404 —
	// the status code is the addition, not a different contract.
	Status string `json:"status"`
	// Msg is the failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the customer. Null exactly when Status is "error".
	Data *CustomerDetailData `json:"data"`
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
	// Status is "ok" or "error", and it answers whether the action RAN, not whether every
	// user was updated. A partial failure is an ok answer with a non-empty data.failed —
	// read that before treating the org as fully suspended.
	Status string `json:"status"`
	// Msg is why the action could not run at all: no org named, the org unknown (also
	// HTTP 404), or IAM's member list unreadable. Empty on success.
	Msg string `json:"msg"`
	// Data is the per-user breakdown of what changed. Null exactly when Status is
	// "error", which means no user was touched.
	Data *AccessChange `json:"data"`
}

// ── POST /v1/admin/customers/:org/{suspend,reactivate} — access control ──────

// SuspendCustomer cuts off every member of the org: IAM refuses a forbidden user at
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
		full, gerr := s.State.IAM.User(ctx, cr, u.Owner, u.Name)
		if gerr != nil {
			failed = append(failed, u.Name)
			continue
		}
		full["isForbidden"] = forbidden
		if uerr := s.State.IAM.SetUser(ctx, cr, full); uerr != nil {
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
