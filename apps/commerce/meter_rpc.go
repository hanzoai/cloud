// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The prepaid GATE and the debit, published on the internal plane for the same
// reason the balance read is: the ledger has one writer and it lives here.
//
// Without these an app in its own process finds no metering client, and
// ResourceMeter.Gate is documented to ALLOW when billing is unconfigured — which
// is right for a deployment that does not bill and catastrophic for one that does.
// Split into per-app binaries, every priced create became free: the gate could not
// tell "nobody bills here" from "the biller is one socket away".

// meterOps binds the metering client to the two plane ops that need it. A plane
// handler is func(context.Context, *In) (*Out, error) — no parameter for the
// client — so it arrives as a RECEIVER and each op is a bound method value, which
// is also the only bound form zipdoc can lift prose from.
type meterOps struct{ m *metering.Client }

// exposeMeter publishes the gate and the debit. Mount calls it.
func exposeMeter(m *metering.Client) {
	p := cloud.Plane()
	o := meterOps{m: m}

	zip.Post[plane.AuthorizeIn, plane.Verdict](p, "/finance/authorize", o.authorize,
		zip.WithOperationID(plane.FinanceAuthorize),
		zip.WithSummary("Authorize one prepaid spend"))

	zip.Post[plane.RecordIn, plane.Recorded](p, "/finance/record", o.record,
		zip.WithOperationID(plane.FinanceRecord),
		zip.WithSummary("Debit one metered act"))
}

// Answers whether one proposed prepaid spend may proceed, so an app in its own
// process can gate priced work against a ledger it cannot open.
//
// The verdict is a VALUE, not an error: ok, no-funds, cap-spent, or a reason
// string. Only the first admits the work — a caller that treats anything else as
// permission has misread it. The distinction matters because no-funds and
// cap-spent are the customer's to fix while a reason is the operator's.
//
// It FAILS CLOSED on an unconfigured meter. This process owns the money plane, so
// if ITS meter is unconfigured the honest answer is unknown rather than allowed —
// the caller refuses on a reason it can log instead of handing out work nobody
// can bill. An amount that cannot be PARSED is refused outright, because a charge
// that cannot be read is not a charge of zero and a gate that treated it as one
// would let the work through free.
//
// The org is the CALLER'S and can never be named in the input; an empty subject
// gates the org's own account. Authorizing does not debit — the record op does.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o meterOps) authorize(ctx context.Context, in *plane.AuthorizeIn) (*plane.Verdict, error) {
	org, err := callerOrg(ctx, "authorize")
	if err != nil {
		return nil, err
	}
	if o.m == nil || !o.m.Enabled() {
		return &plane.Verdict{Reason: "commerce has no metering client"}, nil
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("authorize: " + err.Error())
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	aerr := o.m.Authorize(ctx, metering.AuthInput{
		User: subject, Org: org,
		Amount:           credit.FromDecimal(amount.Decimal()),
		Project:          in.Project,
		ProjectValidated: in.ProjectValidated,
		Service:          in.Service,
		Currency:         amount.Currency().Code,
	})
	switch {
	case aerr == nil:
		return &plane.Verdict{OK: true}, nil
	case errors.Is(aerr, metering.ErrInsufficientBalance):
		return &plane.Verdict{NoFunds: true}, nil
	case errors.Is(aerr, metering.ErrSpendCapExceeded):
		return &plane.Verdict{CapSpent: true}, nil
	default:
		return &plane.Verdict{Reason: aerr.Error()}, nil
	}
}

// Debits one metered act against the prepaid ledger and records what it was for —
// model, provider, project, service, request id and client address — so usage a
// process cannot write locally still lands in the one ledger of record.
//
// It is the DEBIT half and is deliberately a separate op from the credit: two
// separate acts with separate idempotency and separate authority, because folding
// them into one signed amount would make a sign error a transfer in the wrong
// direction. It is also separate from the authorize gate, which decides and does
// not move money.
//
// The billed USER and ORG are set here from the CALLER, never from the argument:
// a caller that could name the billed org could bill someone else. An empty
// subject bills the org's own account. Unlike the gate, an unconfigured meter is
// an ERROR here rather than a verdict — there is no honest way to acknowledge a
// debit that was never written.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o meterOps) record(ctx context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
	org, err := callerOrg(ctx, "record")
	if err != nil {
		return nil, err
	}
	if o.m == nil || !o.m.Enabled() {
		return nil, fmt.Errorf("record: commerce has no metering client")
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("record: " + err.Error())
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	if _, rerr := o.m.Record(ctx, metering.Usage{
		User: subject, Org: org,
		Amount:    credit.FromDecimal(amount.Decimal()),
		Model:     firstNonEmpty(in.Usage.Model, in.Usage.Service),
		Project:   in.Usage.Project,
		Provider:  firstNonEmpty(in.Usage.Provider, in.Usage.Service),
		Service:   in.Usage.Service,
		RequestID: in.Usage.RequestID,
		ClientIP:  in.Usage.ClientIP,
		Currency:  amount.Currency().Code,
		Status:    "success",
	}); rerr != nil {
		return nil, fmt.Errorf("record: %w", rerr)
	}
	return &plane.Recorded{Amount: in.Amount}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
