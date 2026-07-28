package customer

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// The GRANTS surface (/v1/admin/grants) — the operator cockpit's credit-grant ledger. A
// grant is a staff-issued credit (a comp/refund/promo) written to a customer org's
// commerce ledger by POST /v1/admin/customers/:org/credit (or POST /v1/admin/grants).
// Every grant is recorded in cloud's tamper-evident audit store as action
// "admin.customer.credit", so THIS view is a projection of that trail — the ONE source of
// truth for "who granted what to whom, when, and from which bucket". SuperAdmin only.
//
// A grant's `source` splits it into the two commerce money buckets:
//   - trial   — a non-cash promo/comp credit (never refundable cash, never paid out).
//   - prepaid — real money added to the customer's cash balance.

// GrantRow is one row in GET /v1/admin/grants.
type GrantRow struct {
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

// grantAfter is the audit record's After payload emitted by core.ApplyGrant. Success
// carries grantedCents+transactionId; a failed attempt carries amountCents+error.
type grantAfter struct {
	GrantedCents  int64  `json:"grantedCents"`
	AmountCents   int64  `json:"amountCents"`
	Currency      string `json:"currency"`
	Reason        string `json:"reason"`
	Source        string `json:"source"`
	TransactionID string `json:"transactionId"`
}

// GrantFilter is the ONE audit query that identifies a credit grant. Both the grants
// ledger and the consolidated money board select rows through it, so "what counts as a
// grant" is defined once. org filters the ACTOR's org; result is "" (all), "success" or
// "error".
func GrantFilter(org, result string, limit int) audit.Filter {
	return audit.Filter{
		Resource: "credit", // res_type of every grant audit row
		Action:   "admin.customer.credit",
		Org:      org,
		Result:   result,
		Limit:    limit,
	}
}

// Grants answers GET /v1/admin/grants — the credit-grant ledger across ALL orgs, newest
// first, projected from the audit trail. Filters: ?org, ?result (success|error), ?limit.
// Honest empty when no local audit store is configured.
func Grants(s *cloud.Service[core.State], c *zip.Ctx) error {
	limit := 200
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	out, total, err := GrantRows(s, c.Context(),
		GrantFilter(strings.TrimSpace(c.Query("org")), strings.TrimSpace(c.Query("result")), limit))
	if err != nil {
		return core.Fail(c, err.Error())
	}
	msg := ""
	if s.State.AuditStore == nil {
		msg = "grant history is unavailable (no local audit store configured on this deployment)"
	}

	return c.JSON(200, map[string]any{
		"status": "ok",
		"msg":    msg,
		"data":   out,
		"data2":  total,
	})
}

// GrantRows projects the audit trail into grant rows. It is split out of the handler so
// the consolidated money board (/v1/admin/money) totals the SAME grants this endpoint
// lists — one projection of the trail, two views. No audit store is an honest empty
// result, not an error: the caller decides how to report that (the board marks the
// source not-ok).
func GrantRows(s *cloud.Service[core.State], ctx context.Context, f audit.Filter) ([]GrantRow, int, error) {
	if s.State.AuditStore == nil {
		return []GrantRow{}, 0, nil
	}

	rows, total, err := s.State.AuditStore.Query(ctx, f)
	if err != nil {
		return nil, 0, err
	}

	out := make([]GrantRow, 0, len(rows))
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
		out = append(out, GrantRow{
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
	return out, total, nil
}

// issueGrantRequest is the POST /v1/admin/grants body: the credit fields plus the target
// org (which the per-customer route carries in its path instead).
type issueGrantRequest struct {
	Org         string `json:"org"`
	User        string `json:"user"` // optional member to credit; empty is the org itself
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
	Source      string `json:"source"` // "trial" (default) | "prepaid"
}

// IssueGrant answers POST /v1/admin/grants — issue a credit grant to any org from the
// operator Grants view (org in the body). It funnels through the SAME core.ApplyGrant
// POST /v1/admin/customers/:org/credit uses, so there is exactly ONE credit-write path.
func IssueGrant(s *cloud.Service[core.State], c *zip.Ctx) error {
	var body issueGrantRequest
	if err := c.Bind(&body); err != nil {
		return core.Fail(c, "invalid request body")
	}
	org := strings.TrimSpace(body.Org)
	if org == "" {
		return core.Fail(c, "org is required")
	}
	return core.ApplyGrant(s, c, org, core.CreditRequest{
		User:        body.User,
		AmountCents: body.AmountCents,
		Currency:    body.Currency,
		Reason:      body.Reason,
		Source:      body.Source,
	})
}
