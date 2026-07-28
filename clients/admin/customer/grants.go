package customer

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
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

// GrantsIn is the GET /v1/admin/grants filter.
type GrantsIn struct {
	// Org filters by the ACTOR's org (the staff org that issued the grant), which is
	// rarely what a reader wants — the target org is a row field, not a filter.
	Org string `json:"org"`
	// Result filters by outcome: "success" or "error". Empty returns both, which is
	// the point of this view — a refused grant is as interesting as a granted one.
	Result string `json:"result"`
	// Limit caps the rows returned. Default 200.
	Limit string `json:"limit"`
}

// GrantsOut is the GET /v1/admin/grants envelope. data2 is the store's total for the
// filter, which can exceed len(data) when limit truncates.
type GrantsOut struct {
	Status string     `json:"status"`
	Msg    string     `json:"msg"`
	Data   []GrantRow `json:"data"`
	Data2  *int       `json:"data2,omitempty"`
}

// Grants reads the credit-grant ledger across ALL orgs, newest first — who granted what
// to whom, when, and from which money bucket.
//
// It is a PROJECTION of the tamper-evident audit trail, not a second store: every grant
// is written there as action "admin.customer.credit", so this view cannot drift from
// what actually happened, and FAILED grants appear too.
//
// A deployment with no local audit store has no history to project, and says so with an
// empty list and a msg rather than an error.
//
// Example: {"result":"success","limit":"50"}
// Response: {"status":"ok","msg":"","data":[{"org":"acme","amountCents":5000,"currency":"usd",
// "source":"trial","reason":"launch comp","actor":"z@hanzo.ai","createdAt":"2026-07-26T18:00:00Z",
// "transactionId":"tx_01J","result":"success"}],"data2":1}
func (o ops) Grants(ctx context.Context, in *GrantsIn) (*GrantsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if s.State.AuditStore == nil {
		return &GrantsOut{
			Status: core.OK,
			Msg:    "grant history is unavailable (no local audit store configured on this deployment)",
			Data:   []GrantRow{},
			Data2:  core.Total(0),
		}, nil
	}

	limit := 200
	if v := strings.TrimSpace(in.Limit); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	f := audit.Filter{
		Resource: "credit", // res_type of every grant audit row
		Action:   "admin.customer.credit",
		Org:      strings.TrimSpace(in.Org),    // actor org (rarely filtered)
		Result:   strings.TrimSpace(in.Result), // "" = all (success+error)
		Limit:    limit,
	}

	rows, total, err := s.State.AuditStore.Query(ctx, f)
	if err != nil {
		return &GrantsOut{Status: core.Err, Msg: err.Error()}, nil
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

	return &GrantsOut{Status: core.OK, Data: out, Data2: core.Total(total)}, nil
}

// GrantIn is the input of BOTH credit-grant ops. They differ only in where the target
// org comes from — the path on /v1/admin/customers/:org/credit, the body on
// /v1/admin/grants — and the URL wins where both are present, so one type serves both
// and there is one contract to read.
type GrantIn struct {
	// Org is the tenant to credit. Required.
	Org string `json:"org"`
	// User optionally names a MEMBER to credit, by bare IAM username. Empty credits
	// the org. Which of the two the money actually lands on is decided by
	// account.Payer, not here: a pooled org keeps one balance whatever is named.
	User string `json:"user"`
	// AmountCents is the credit, in whole cents. Must be positive and within the
	// per-grant cap.
	AmountCents int64 `json:"amountCents"`
	// Currency is the ISO code, lower-cased. Empty means usd.
	Currency string `json:"currency"`
	// Reason is the operator's justification, recorded on the audit row.
	Reason string `json:"reason"`
	// Source is the money bucket: "trial" (default) for a non-cash comp that is never
	// refundable, or "prepaid" for real money. Anything unknown falls back to trial.
	Source string `json:"source"`
}

// credit projects the request onto the ONE credit-write contract. Org is not part of it
// — it addresses the ledger, and ApplyGrant takes it separately.
func (in *GrantIn) credit() core.CreditRequest {
	return core.CreditRequest{
		User:        in.User,
		AmountCents: in.AmountCents,
		Currency:    in.Currency,
		Reason:      in.Reason,
		Source:      in.Source,
	}
}

// IssueGrant issues a credit grant to any org from the operator Grants view, with the
// target named in the body. It funnels through the SAME core.ApplyGrant that
// POST /v1/admin/customers/:org/credit uses, so there is exactly ONE credit-write path
// and one audit trail behind both.
//
// Example: {"org":"acme","amountCents":5000,"currency":"usd","reason":"launch comp","source":"trial"}
// Response: {"status":"ok","msg":"","data":{"org":"acme","subject":"acme","grantedCents":5000,
// "currency":"usd","source":"trial","balanceCents":10000,
// "balanceExact":"100.000000000000000000","transactionId":"tx_01J"}}
func (o ops) IssueGrant(ctx context.Context, in *GrantIn) (*core.GrantOut, error) {
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
