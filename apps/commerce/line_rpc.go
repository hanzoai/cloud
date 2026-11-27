// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// line_rpc.go — THE ORG'S CREDIT LINE, read and set where the books are.
//
// The limit lives in the org's own books beside the debt it allows (finance/line.go),
// so the two ops that read and set it are the ledger owner's, for the reason every
// finance op is: one process holds the books and everyone else asks it.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/client"
	financeclient "github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// lineOps binds the two ops to the audit trail the limit write is recorded on.
// Method values, not closures, so zipdoc can lift each op's prose.
type lineOps struct{ trail *audit.Recorder }

// exposeLine publishes the line read and the limit write. Use calls it.
func exposeLine(trail *audit.Recorder) {
	o := lineOps{trail: trail}
	cloud.Plane().Post("/finance/line", o.line,
		zip.WithOperationID(client.FinanceLine),
		zip.WithSummary("This org's account and credit line"))
	cloud.Plane().Post("/finance/limit", o.limit,
		zip.WithOperationID(client.FinanceLimit),
		zip.WithSummary("Set this org's credit limit"))
	cloud.Plane().Post("/finance/waive", o.waive,
		zip.WithOperationID(client.FinanceWaive),
		zip.WithSummary("Waive this org's amount due"))
}

// liner is the ledger that keeps credit lines.
func liner(op string) (financeclient.Liner, error) {
	fin, err := books(op)
	if err != nil {
		return nil, err
	}
	l, ok := fin.(financeclient.Liner)
	if !ok {
		return nil, fmt.Errorf("%s: this ledger keeps no credit line", op)
	}
	return l, nil
}

// Reads the caller org's own account: the cash it paid and has not spent, the credit
// granted to it, what it owes — usage its credit line paid for, the amount due — the
// line: its limit and what is left to draw, and the total it may spend now, which is
// the balance op's Amount for the org's own wallet (finance.Funds.Total). It says too
// whether the org is on account (a line with no limit, client.OnAccount), when what it
// owes began to be owed, and what its own account used since the month began.
//
// The org is the CALLER'S and cannot be named in the input. It opens nothing: an org
// with no ledger answers books false rather than being handed an empty file.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o lineOps) line(ctx context.Context, _ *cloud.Unit) (*client.Line, error) {
	org, err := callerOrg(ctx, "line")
	if err != nil {
		return nil, err
	}
	led, err := liner("line")
	if err != nil {
		return nil, err
	}
	l, err := led.Line(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "line %s: %v", org, err)
	}
	return lineWire(l), nil
}

// Sets the caller org's credit limit: how far below zero its own account may go on
// metered usage, owed and invoiced, before a priced call is refused. A limit below
// what the org already owes stops further drawing and neither forgives nor calls in
// the debt.
//
// SuperAdmin only. The change is on the audit trail before it is made, as
// admin.credit.limit with the account before and after; a trail that does not take
// it refuses the change (503), and nothing moves.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o lineOps) limit(ctx context.Context, in *client.LimitIn) (*client.Line, error) {
	org, err := callerOrg(ctx, "limit")
	if err != nil {
		return nil, err
	}
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("limit: SuperAdmin required")
	}
	want, err := in.Limit.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("limit: " + err.Error())
	}
	limit := money.FromDecimal(want.Decimal())
	if limit.IsNeg() {
		return nil, zip.ErrBadRequest("limit: a credit limit is zero or more")
	}
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
	}
	led, err := liner("limit")
	if err != nil {
		return nil, err
	}
	before, err := led.Line(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limit %s: %v", org, err)
	}
	after := before
	after.Limit, after.Line = limit, money.Zero()
	if left := limit.Sub(before.Owed); left.Sign() > 0 {
		after.Line = left
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, org), Sub: who.User, Email: who.Email},
		Action:    "admin.credit.limit",
		Resource:  audit.Resource{Type: "credit_line", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: in.Reason},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    audit.Redact(lineJSON(before)),
		After:     audit.Redact(lineJSON(after)),
	}
	if _, err := o.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made: %v", err)
	}
	got, err := led.SetLimit(ctx, org, false, limit)
	if err != nil {
		rec.Outcome = audit.Outcome{Result: "error", Status: http.StatusInternalServerError, Reason: "the ledger did not take the limit"}
		_, _ = o.trail.Append(ctx, rec)
		return nil, zip.Errorf(http.StatusInternalServerError, "limit %s: %v", org, err)
	}
	mirrorOrgBook(org)
	return lineWire(got), nil
}

// Forgives what the caller org's own account currently owes (cash and/or legacy
// credit below zero), bringing Owed to zero without minting cash or touching
// granted credit. Idempotent when nothing is owed.
//
// SuperAdmin only. The change is on the audit trail before it is made, as
// admin.credit.waive with the account before and after; a trail that does not
// take it refuses the waiver (503), and nothing moves.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o lineOps) waive(ctx context.Context, in *client.WaiveIn) (*client.Line, error) {
	org, err := callerOrg(ctx, "waive")
	if err != nil {
		return nil, err
	}
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("waive: SuperAdmin required")
	}
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the waiver, so it was not made")
	}
	led, err := liner("waive")
	if err != nil {
		return nil, err
	}
	before, err := led.Line(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "waive %s: %v", org, err)
	}
	after := before
	after.Owed = money.Zero()
	if after.Limit.Sign() > 0 {
		after.Line = after.Limit
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, org), Sub: who.User, Email: who.Email},
		Action:    "admin.credit.waive",
		Resource:  audit.Resource{Type: "credit_line", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: in.Reason},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    audit.Redact(lineJSON(before)),
		After:     audit.Redact(lineJSON(after)),
	}
	if _, err := o.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the waiver, so it was not made: %v", err)
	}
	got, err := led.Waive(ctx, org, false, in.Reason)
	if err != nil {
		rec.Outcome = audit.Outcome{Result: "error", Status: http.StatusInternalServerError, Reason: "the ledger did not take the waiver"}
		_, _ = o.trail.Append(ctx, rec)
		return nil, zip.Errorf(http.StatusInternalServerError, "waive %s: %v", org, err)
	}
	mirrorOrgBook(org)
	return lineWire(got), nil
}

// lineWire is the account as the plane carries it.
func lineWire(l financeclient.Line) *client.Line {
	return &client.Line{
		Books:   l.Books,
		Cash:    client.Amount(l.Cash.Unwrap()),
		Credit:  client.Amount(l.Credit.Unwrap()),
		Owed:    client.Amount(l.Owed.Unwrap()),
		Limit:   client.Amount(l.Limit.Unwrap()),
		Left:    client.Amount(l.Line.Unwrap()),
		Total:   client.Amount(l.Total().Unwrap()),
		Account: l.Account,
		Since:   l.Since,
		Used:    client.Amount(l.Used.Unwrap()),
		Period:  l.Period,
	}
}

// lineJSON is the account as the audit trail records it, in exact decimals.
func lineJSON(l financeclient.Line) json.RawMessage {
	b, _ := json.Marshal(map[string]string{
		"limit": l.Limit.String(), "owed": l.Owed.String(), "line": l.Line.String(),
		"cash": l.Cash.String(), "credit": l.Credit.String(),
	})
	return b
}
