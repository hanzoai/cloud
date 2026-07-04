package admin

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// The GRANTS surface (/v1/admin/grants) — the operator cockpit's credit-grant
// ledger. A grant is a staff-issued credit (a comp/refund/promo) written to a
// customer org's commerce ledger by POST /v1/admin/customers/:org/credit (or
// POST /v1/admin/grants). Every grant is recorded in cloud's tamper-evident audit
// store as action "admin.customer.credit" (resource type "credit", resource id =
// the target org), so THIS view is a projection of that trail — the ONE source of
// truth for "who granted what to whom, when, and from which bucket". Global-admin
// only (mounted behind s.guard, like every /v1/admin/* route).
//
// A grant's `source` splits it into the two commerce money buckets:
//   - trial   — a non-cash promo/comp credit (never refundable cash, never paid
//               out, non-premium usage only).
//   - prepaid — real money added to the customer's cash balance.
// The staff-issue default is trial (we never silently mint payout-able money).

// grantRow is one row in GET /v1/admin/grants.
type grantRow struct {
	Org           string `json:"org"`
	AmountCents   int64  `json:"amountCents"`
	Currency      string `json:"currency"`
	Source        string `json:"source"` // "trial" | "prepaid"
	Reason        string `json:"reason,omitempty"`
	Actor         string `json:"actor"` // staff email (or sub) who issued it
	CreatedAt     string `json:"createdAt"`
	TransactionID string `json:"transactionId,omitempty"`
	Result        string `json:"result"` // success | error
}

// grantAfter is the audit record's After payload emitted by applyGrant. Success
// carries grantedCents+transactionId; a failed attempt carries amountCents+error.
type grantAfter struct {
	GrantedCents  int64  `json:"grantedCents"`
	AmountCents   int64  `json:"amountCents"`
	Currency      string `json:"currency"`
	Reason        string `json:"reason"`
	Source        string `json:"source"`
	TransactionID string `json:"transactionId"`
}

// grants answers GET /v1/admin/grants — the credit-grant ledger across ALL orgs,
// newest first, projected from the audit trail. Filters: ?org, ?result
// (success|error), ?limit. Honest empty when no local audit store is configured
// (the grants view is audit-backed; without the store there is no grant history
// to read — never a fabricated list).
//
//	GET /v1/admin/grants
func (s *svc) grants(c *zip.Ctx) error {
	if s.auditStore == nil {
		return c.JSON(200, map[string]any{
			"status": "ok",
			"msg":    "grant history is unavailable (no local audit store configured on this deployment)",
			"data":   []grantRow{},
			"data2":  0,
		})
	}

	limit := 200
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	f := audit.Filter{
		Resource: "credit", // res_type of every grant audit row
		Action:   "admin.customer.credit",
		Org:      strings.TrimSpace(c.Query("org")),    // actor org (rarely filtered)
		Result:   strings.TrimSpace(c.Query("result")), // "" = all (success+error)
		Limit:    limit,
	}

	rows, total, err := s.auditStore.Query(c.Context(), f)
	if err != nil {
		return fail(c, err.Error())
	}

	out := make([]grantRow, 0, len(rows))
	for _, r := range rows {
		var a grantAfter
		if len(r.After) > 0 {
			_ = json.Unmarshal(r.After, &a)
		}
		amount := a.GrantedCents
		if amount == 0 {
			amount = a.AmountCents
		}
		currency := a.Currency
		if currency == "" {
			currency = "usd"
		}
		source := a.Source
		if source == "" {
			source = "trial" // legacy rows predate the source field; a comp is trial
		}
		actor := r.Actor.Email
		if actor == "" {
			actor = r.Actor.Sub
		}
		out = append(out, grantRow{
			Org:           r.Resource.ID, // the TARGET org the credit landed on
			AmountCents:   amount,
			Currency:      currency,
			Source:        source,
			Reason:        a.Reason,
			Actor:         actor,
			CreatedAt:     r.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
			TransactionID: a.TransactionID,
			Result:        r.Outcome.Result,
		})
	}

	return c.JSON(200, map[string]any{
		"status": "ok",
		"msg":    "",
		"data":   out,
		"data2":  total,
	})
}

// issueGrantRequest is the POST /v1/admin/grants body: the credit fields plus the
// target org (which the per-customer route carries in its path instead).
type issueGrantRequest struct {
	Org         string `json:"org"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
	Source      string `json:"source"` // "trial" (default) | "prepaid"
}

// issueGrant answers POST /v1/admin/grants — issue a credit grant to any org from
// the operator Grants view (org in the body). It funnels through the SAME
// applyGrant core POST /v1/admin/customers/:org/credit uses, so there is exactly
// ONE credit-write path (validate + deposit trial/prepaid + audit).
//
//	POST /v1/admin/grants  { org, amountCents, currency?, reason?, source? }
func (s *svc) issueGrant(c *zip.Ctx) error {
	var body issueGrantRequest
	if err := c.Bind(&body); err != nil {
		return fail(c, "invalid request body")
	}
	org := strings.TrimSpace(body.Org)
	if org == "" {
		return fail(c, "org is required")
	}
	return s.applyGrant(c, org, creditRequest{
		AmountCents: body.AmountCents,
		Currency:    body.Currency,
		Reason:      body.Reason,
		Source:      body.Source,
	})
}
