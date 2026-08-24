package customer

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/audit"
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
	// Org is the TARGET tenant the credit landed on — the audit row's resource id, not
	// the actor's org. The `org` query filter matches the actor's, which is the one
	// asymmetry on this surface.
	Org string `json:"org"`
	// AmountCents is the credit in USD cents, always positive. On a successful grant it
	// is what was actually written to the ledger; on a REFUSED one it is what was asked
	// for and never moved, so summing this column without filtering on Result overstates
	// what the fleet gave away.
	AmountCents int64 `json:"amountCents"`
	// Currency is the ISO code the grant was denominated in, lower-cased. Defaults to
	// "usd" for a row that recorded none.
	Currency string `json:"currency"`
	Source   string `json:"source"` // "trial" | "prepaid"
	// Reason is the justification the operator typed. Omitted when blank — older rows
	// predate the field.
	Reason string `json:"reason,omitempty"`
	Actor  string `json:"actor"` // staff email (or sub) who issued it
	// CreatedAt is when the grant was attempted, RFC3339 in UTC. The list is newest first.
	CreatedAt string `json:"createdAt"`
	// TransactionID is the commerce ledger entry the money landed in, for reconciliation.
	// Omitted on a refused grant, which wrote no entry.
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

// GrantsOut is the GET /v1/admin/grants envelope. total is the store's total for the
// filter, which can exceed len(data) when limit truncates.
type GrantsOut struct {
	// Status is "ok" or "error". A deployment with no durable audit store has no history
	// to project and still answers ok — with an empty list and a msg saying so.
	Status string `json:"status"`
	// Msg carries that "no audit store" note ALONGSIDE an ok status. Everywhere else on
	// this app a non-empty msg means failure; here it means the history is unavailable,
	// which is why an empty list must not be read as "no grants were ever made".
	Msg string `json:"msg"`
	// Data is the matching grants, newest first, capped by the request's limit. Refused
	// grants are included — a refusal is as much a fact about who tried as a success is.
	Data []GrantRow `json:"data"`
	// Total is the store's count for the filter, which EXCEEDS len(data) when limit
	// truncated. Omitted on an error.
	Total *int `json:"total,omitempty"`
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
// "transactionId":"tx_01J","result":"success"}],"total":1}
func (o ops) Grants(ctx context.Context, in *GrantsIn) (*GrantsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	limit := 200
	if v := strings.TrimSpace(in.Limit); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	out, total, err := GrantRows(s, ctx,
		GrantFilter(strings.TrimSpace(in.Org), strings.TrimSpace(in.Result), limit))
	if err != nil {
		return &GrantsOut{Status: core.Err, Msg: err.Error()}, nil
	}
	msg := ""
	if s.State.AuditStore == nil {
		msg = "grant history is unavailable (no local audit store configured on this deployment)"
	}

	return &GrantsOut{Status: core.OK, Msg: msg, Data: out, Total: core.Total(total)}, nil
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
	c, err := core.Change(ctx)
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
