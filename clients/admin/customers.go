package admin

// The CUSTOMER management surface (/v1/admin/customers*) — the operator cockpit's
// core: the live fleet customer list (incl. new self-serve signups), one-customer
// detail, and the audited management ACTIONS (grant credit, suspend, reactivate).
//
// It aggregates the SAME real upstreams the rest of admin reads — IAM for the org
// directory + user/owner/status, commerce for balance/spend/plan/ledger — and adds
// the two write levers an operator needs to run the paid cloud:
//
//   - GRANT CREDIT is a real commerce Deposit (refunds/comps/support) landing in
//     the org's own wallet, symmetric with the balance read.
//   - SUSPEND / REACTIVATE flips IAM `isForbidden` on the org's users. That is the
//     platform's REAL access lever: IAM refuses a forbidden user at login AND at
//     token issuance (object/check.go + object/token_oauth.go), so a suspended
//     customer cannot sign in or mint a fresh token — no new enforcement path is
//     invented, and it is fully reversible.
//
// SECURITY. Every route is mounted behind s.guard (global-admin only, fail-closed)
// exactly like the read surface. The write actions REPLAY THE CALLER'S OWN global-
// admin credential to IAM (no service credential added — IAM re-checks
// IsGlobalAdmin, so admin can never mutate a boundary the caller couldn't already
// cross), and each is recorded to cloud's tamper-evident audit trail with a
// redacted BEFORE/AFTER (the AU "before/after on config-affecting change"), on top
// of the uniform request record the audit middleware already writes for every
// /v1/admin/* mutation. No customer card data is ever read or exposed here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// ── wire shapes (operator contract) ──────────────────────────────────────────

// customerRow is one row in GET /v1/admin/customers — a fleet customer at a glance.
type customerRow struct {
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

// customerUser is one member in the customer detail (no secrets — the AccessKey
// PRESENCE is surfaced as hasApiKey, never the key itself).
type customerUser struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	IsAdmin    bool   `json:"isAdmin"`
	Forbidden  bool   `json:"forbidden"`
	HasAPIKey  bool   `json:"hasApiKey"`
	LastSignin string `json:"lastSignin"`
	Created    string `json:"created"`
}

// customerTxn is one ledger row in the detail's top-up/usage history.
type customerTxn struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "deposit" (credit) | "withdraw" (usage)
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
	Notes    string `json:"notes,omitempty"`
	Time     string `json:"time"`
}

// customerDetail is GET /v1/admin/customers/:org.
type customerDetail struct {
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
	Users        []customerUser `json:"users"`
	Transactions []customerTxn  `json:"transactions"`
}

// ── GET /v1/admin/customers — the fleet customer list ────────────────────────

// maxCustomerConcurrency bounds the per-org enrichment fan-out so a large fleet
// does not open one upstream connection per org at once. Admin is low-QPS; 8 keeps
// latency low without hammering IAM/commerce.
const maxCustomerConcurrency = 8

func (s *svc) customers(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	orgs, err := s.listOrgs(ctx, cr)
	if err != nil {
		return fail(c, err.Error())
	}

	rows := make([]customerRow, len(orgs))
	sem := make(chan struct{}, maxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iamOrg) {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i] = s.enrichCustomer(ctx, cr, o)
		}(i, o)
	}
	wg.Wait()

	sort.Slice(rows, func(i, j int) bool { return rows[i].Org < rows[j].Org })
	return okList(c, rows, len(rows))
}

// enrichCustomer folds one org's real IAM + commerce reads into a customer row.
// Each read is best-effort: an upstream miss degrades that field to its honest
// zero/empty (never a fabricated value), so one flaky org never fails the fleet.
func (s *svc) enrichCustomer(ctx context.Context, cr creds, o iamOrg) customerRow {
	subj := orgSubject(o.Name)
	users, _ := s.orgUsers(ctx, cr, o.Name)
	spend, credits := s.orgMoney(ctx, o.Name)
	sub, _ := s.commerce.subscriptionSummary(ctx, o.Name, subj)

	return customerRow{
		Org:          o.Name,
		Display:      display(o.DisplayName, o.Name),
		OwnerEmail:   ownerEmail(users),
		Plan:         sub.Plan,
		Status:       statusOf(users),
		Users:        len(users),
		BalanceCents: credits,
		SpendCents:   spend,
		MRRCents:     sub.MRR,
		Created:      o.CreatedTime,
		LastActive:   lastActiveOf(users),
	}
}

// ── GET /v1/admin/customers/:org — one customer's detail ─────────────────────

func (s *svc) customerDetail(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	org := customerOrgParam(c)
	if org == "" {
		return fail(c, "org is required")
	}

	o, err := s.findOrg(ctx, cr, org)
	if err != nil {
		return fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	subj := orgSubject(org)
	users, _ := s.orgUsers(ctx, cr, org)
	spend, credits := s.orgMoney(ctx, org)
	sub, _ := s.commerce.subscriptionSummary(ctx, org, subj)
	txns, _ := s.commerce.transactions(ctx, org, subj, 50)

	rows := make([]customerUser, 0, len(users))
	apiKeys := 0
	for _, u := range users {
		hasKey := strings.TrimSpace(u.AccessKey) != ""
		if hasKey {
			apiKeys++
		}
		rows = append(rows, customerUser{
			Name:       u.Name,
			Email:      u.Email,
			IsAdmin:    u.IsAdmin,
			Forbidden:  u.IsForbidden,
			HasAPIKey:  hasKey,
			LastSignin: u.LastSigninTime,
			Created:    u.CreatedTime,
		})
	}

	ledger := make([]customerTxn, 0, len(txns))
	for _, t := range txns {
		ledger = append(ledger, customerTxn{
			ID:       t.ID,
			Type:     t.Type,
			Cents:    t.Amount,
			Currency: t.Currency,
			Notes:    t.Notes,
			Time:     t.CreatedAt,
		})
	}

	return ok(c, customerDetail{
		Org:          org,
		Display:      display(o.DisplayName, org),
		OwnerEmail:   ownerEmail(users),
		Plan:         sub.Plan,
		Status:       statusOf(users),
		Created:      o.CreatedTime,
		BalanceCents: credits,
		SpendCents:   spend,
		MRRCents:     sub.MRR,
		APIKeys:      apiKeys,
		Users:        rows,
		Transactions: ledger,
	})
}

// ── POST /v1/admin/customers/:org/credit — grant credit ──────────────────────

// creditRequest is the grant body. AmountCents is the credit to add (positive
// only — a grant, never a silent debit). Reason is the operator's justification,
// recorded in the audit trail's before/after (refund / comp / support).
type creditRequest struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
}

// maxGrantCents caps a single grant at $100,000 — a guardrail against a fat-finger
// operator credit, not a policy limit. A larger comp is deliberate + should be
// deliberate (two grants), and the cap keeps a typo from minting a fortune.
const maxGrantCents int64 = 100 * 100 * 1000

func (s *svc) grantCredit(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	org := customerOrgParam(c)
	if org == "" {
		return fail(c, "org is required")
	}

	var req creditRequest
	if err := c.Bind(&req); err != nil {
		return fail(c, "invalid request body")
	}
	if req.AmountCents <= 0 {
		return fail(c, "amountCents must be positive")
	}
	if req.AmountCents > maxGrantCents {
		return fail(c, fmt.Sprintf("amountCents exceeds the %d-cent per-grant cap", maxGrantCents))
	}
	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "usd"
	}

	// Validate the target is a REAL org (never mint an orphan wallet on a typo).
	o, err := s.findOrg(ctx, cr, org)
	if err != nil {
		return fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	subj := orgSubject(org)
	before, _ := s.commerce.creditsCents(ctx, org, subj)

	notes := grantNote(c, req.Reason)
	res, derr := s.commerce.deposit(ctx, org, subj, req.AmountCents, currency, notes, "admin-grant")
	if derr != nil {
		// The grant did not land — record the FAILED attempt (accountability), then
		// surface the error. Never report a grant that failed as success.
		s.emitAudit(c, "admin.customer.credit", "credit", org,
			map[string]any{"balanceCents": before},
			map[string]any{"amountCents": req.AmountCents, "currency": currency, "reason": req.Reason, "error": derr.Error()},
			audit.Outcome{Result: "error", Status: 200, Reason: "grant failed"})
		return fail(c, "grant failed: "+derr.Error())
	}

	after, _ := s.commerce.creditsCents(ctx, org, subj)
	s.emitAudit(c, "admin.customer.credit", "credit", org,
		map[string]any{"balanceCents": before},
		map[string]any{"balanceCents": after, "grantedCents": req.AmountCents, "currency": currency, "reason": req.Reason, "transactionId": res.TransactionID},
		audit.Outcome{Result: "success", Status: 200})

	return ok(c, map[string]any{
		"org":           org,
		"grantedCents":  req.AmountCents,
		"currency":      currency,
		"balanceCents":  after,
		"transactionId": res.TransactionID,
	})
}

// ── POST /v1/admin/customers/:org/{suspend,reactivate} — access control ──────

func (s *svc) suspendCustomer(c *zip.Ctx) error  { return s.setForbidden(c, true) }
func (s *svc) reactivateCustomer(c *zip.Ctx) error { return s.setForbidden(c, false) }

// setForbidden flips IAM `isForbidden` on every member of the org — suspend
// (forbidden=true) cuts login + token issuance; reactivate restores it. Each
// user's FULL object is read, the one field flipped, and written back (update-user
// replaces the row), replaying the caller's global-admin credential so IAM
// authorizes it. Best-effort per user with an aggregated result: a partial failure
// is reported honestly (affected vs failed), never masked as a clean success. The
// action is recorded with a redacted before/after user tally.
func (s *svc) setForbidden(c *zip.Ctx, forbidden bool) error {
	ctx := c.Context()
	cr := callerCreds(c)
	org := customerOrgParam(c)
	if org == "" {
		return fail(c, "org is required")
	}

	o, err := s.findOrg(ctx, cr, org)
	if err != nil {
		return fail(c, err.Error())
	}
	if o == nil {
		return c.JSON(404, map[string]any{"status": "error", "msg": "customer not found", "data": nil})
	}

	users, err := s.orgUsers(ctx, cr, org)
	if err != nil {
		return fail(c, err.Error())
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
		full, gerr := s.iam.getUserRaw(ctx, cr, id)
		if gerr != nil {
			failed = append(failed, u.Name)
			continue
		}
		full["isForbidden"] = forbidden
		if uerr := s.iam.updateUserRaw(ctx, cr, id, full); uerr != nil {
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
	s.emitAudit(c, action, "customer", org,
		map[string]any{"suspended": beforeForbidden == len(users) && len(users) > 0, "forbiddenUsers": beforeForbidden, "totalUsers": len(users)},
		map[string]any{"suspended": forbidden, "affected": affected, "failed": failed},
		audit.Outcome{Result: result, Status: 200, Reason: reason})

	return ok(c, map[string]any{
		"org":       org,
		"suspended": forbidden,
		"affected":  affected,
		"failed":    failed,
	})
}

// ── aggregation + derivation helpers ─────────────────────────────────────────

// orgUsers reads an org's members (a bounded page) as the typed subset the
// customer surface folds over. It is the ONE IAM read that yields the user count,
// the owner email, the suspend status, and the API-key presence — so a customer
// row costs a single get-users call, not four.
func (s *svc) orgUsers(ctx context.Context, cr creds, org string) ([]iamUser, error) {
	q := url.Values{}
	q.Set("owner", org)
	q.Set("p", "1")
	q.Set("pageSize", "200")
	res, err := s.iam.getList(ctx, cr, "/v1/iam/get-users", q)
	if err != nil {
		return nil, err
	}
	var raw []iamUser
	if len(res.rows) > 0 {
		if err := json.Unmarshal(res.rows, &raw); err != nil {
			return nil, fmt.Errorf("users decode: %w", err)
		}
	}
	return raw, nil
}

// findOrg returns the IAM org by slug (nil, nil when it does not exist) so a
// management action can validate its target before acting — never credit or
// suspend an org that isn't real.
func (s *svc) findOrg(ctx context.Context, cr creds, org string) (*iamOrg, error) {
	orgs, err := s.listOrgs(ctx, cr)
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		if orgs[i].Name == org {
			return &orgs[i], nil
		}
	}
	return nil, nil
}

// ownerEmail picks the org's admin user's email (the account owner), falling back
// to the first user with an email. Empty when no user carries one.
func ownerEmail(users []iamUser) string {
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

// statusOf derives the suspend status: an org is "suspended" only when it has at
// least one user and EVERY user is forbidden (a partial forbid is still "active" —
// the operator sees the per-user state in the detail). Honest by construction.
func statusOf(users []iamUser) string {
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

// lastActiveOf returns the most recent user sign-in across the org (RFC3339), the
// best "last active" signal available from IAM. Empty when no user has signed in.
func lastActiveOf(users []iamUser) string {
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

// grantNote composes the deposit note from the operator's reason (bounded), so the
// commerce ledger row itself carries the justification alongside the audit trail.
func grantNote(c *zip.Ctx, reason string) string {
	r := strings.TrimSpace(reason)
	if len(r) > 200 {
		r = r[:200]
	}
	by := strings.TrimSpace(c.UserEmail())
	if by == "" {
		by = strings.TrimSpace(c.User())
	}
	if r == "" {
		r = "operator credit"
	}
	if by != "" {
		return fmt.Sprintf("Admin grant by %s: %s", by, r)
	}
	return "Admin grant: " + r
}

// emitAudit writes ONE compliance record for a management action to cloud's
// tamper-evident trail: who (the validated global admin from the sanitized
// identity — the gate already proved it), what (action + resource), the redacted
// before/after, and the outcome. This is the "before/after on a config-affecting
// change" the request-level middleware record cannot carry (it never reads bodies).
// Best-effort: the audit MIDDLEWARE is the AU-5 fail-closed authority for the
// request; a failure here is logged loud, never silent, and never double-fails the
// response. A nil store (unconfigured deployment) is a no-op, like the middleware.
func (s *svc) emitAudit(c *zip.Ctx, action, resType, resID string, before, after any, outcome audit.Outcome) {
	if s.auditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: strings.TrimSpace(c.Org()), Sub: strings.TrimSpace(c.User()), Email: strings.TrimSpace(c.UserEmail())},
		Action:    action,
		Resource:  audit.Resource{Type: resType, ID: resID},
		Auth:      audit.AuthContext{Method: "jwt", IsAdmin: c.IsAdmin()},
		Outcome:   outcome,
		UserAgent: c.Header("User-Agent"),
		RequestID: c.RequestID(),
		Method:    c.Method(),
		Path:      c.Path(),
		Before:    audit.Redact(mustJSON(before)),
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := s.auditStore.Append(c.Context(), rec); err != nil {
		c.Log().Error("admin: audit emit failed (request-level record still applies)",
			"action", action, "resource", resType, "id", resID, "err", err)
	}
}

// mustJSON marshals v to raw JSON for the audit before/after, returning an empty
// object on the (unexpected) marshal error rather than panicking — a metadata
// diff must never crash a money/access action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
