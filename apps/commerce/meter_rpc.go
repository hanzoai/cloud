// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/client"
	financeclient "github.com/hanzoai/cloud/finance"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// The prepaid GATE and the debit, published on the internal plane for the same
// reason the balance read is: the ledger has one writer and it lives here.
//
// They are what makes "the biller is one socket away" answerable. An app in its
// own process holds no local ledger and asks these; only a deployment that
// publishes neither — plane.ErrNoPeer — bills nothing, and that is a different
// fact from a biller that did not answer.

// meterOps binds the metering client to the two plane ops that need it. A plane
// handler is func(context.Context, *In) (*Out, error) — no parameter for the
// client — so it arrives as a RECEIVER and each op is a bound method value, which
// is also the only bound form zipdoc can lift prose from.
type meterOps struct{ m *meter.Client }

// exposeMeter publishes the gate and the debit. Mount calls it.
func exposeMeter(m *meter.Client) {
	p := cloud.Plane()
	o := meterOps{m: m}

	p.Post("/finance/authorize", o.authorize,
		zip.WithOperationID(client.FinanceAuthorize),
		zip.WithSummary("Authorize one prepaid spend"))

	p.Post("/finance/record", o.record,
		zip.WithOperationID(client.FinanceRecord),
		zip.WithSummary("Count one served call and debit what it moved"))
}

// Answers whether one proposed prepaid spend may proceed, so an app in its own
// process can gate priced work against a ledger it cannot open.
//
// The verdict is a VALUE, not an error: ok, no-funds, cap-spent, cap-unread, or a
// reason string. Only the first admits the work — a caller that treats anything
// else as permission has misread it. The distinction matters because no-funds and
// cap-spent are the customer's to fix while cap-unread and a reason are the
// operator's.
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
func (o meterOps) authorize(ctx context.Context, in *client.AuthorizeIn) (*client.Verdict, error) {
	org, err := callerOrg(ctx, "authorize")
	if err != nil {
		return nil, err
	}
	if o.m == nil || !o.m.Enabled() {
		return &client.Verdict{Reason: "commerce has no metering client"}, nil
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("authorize: " + err.Error())
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	aerr := o.m.Authorize(ctx, meter.AuthInput{
		User: subject, Org: org,
		Amount:           credit.FromDecimal(amount.Decimal()).Unwrap(),
		Project:          in.Project,
		ProjectValidated: in.ProjectValidated,
		Service:          in.Service,
		Currency:         amount.Currency().Code,
	})
	switch {
	case aerr == nil:
		return &client.Verdict{OK: true}, nil
	case errors.Is(aerr, meter.ErrInsufficientBalance):
		return &client.Verdict{NoFunds: true}, nil
	case errors.Is(aerr, meter.ErrSpendCapExceeded):
		return &client.Verdict{CapSpent: true}, nil
	case errors.Is(aerr, meter.ErrSpendCapUnknown):
		return &client.Verdict{CapUnread: true, Reason: aerr.Error()}, nil
	default:
		return &client.Verdict{Reason: aerr.Error()}, nil
	}
}

// Counts one served call in the ledger and debits the wallet when the call is the
// wallet's to pay, recording what it was for — model, provider, project, service,
// class, units, our cost, who pays, request id and client address — so usage a
// process cannot write locally still lands in the one ledger of record. A $0 call is
// counted like any other.
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
func (o meterOps) record(ctx context.Context, in *client.RecordIn) (*client.Recorded, error) {
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
	cost, err := client.CostOf(in.Usage.Cost)
	if err != nil {
		return nil, zip.ErrBadRequest("record: cost: " + err.Error())
	}
	if _, rerr := o.m.Record(ctx, meter.Usage{
		User: subject, Org: org,
		Amount:   credit.FromDecimal(amount.Decimal()).Unwrap(),
		Model:    cmp.Or(in.Usage.Model, in.Usage.Service),
		Project:  in.Usage.Project,
		Provider: cmp.Or(in.Usage.Provider, in.Usage.Service),
		Service:  in.Usage.Service,
		// The act's name crosses as the act's name and the correlation id as the
		// correlation id. Folding the second into the first is what let a client's
		// X-Request-Id key a ledger entry; the peer's Ref is a server-assigned identity
		// and an absent one lets the ledger mint its own.
		Ref:       in.Usage.Ref,
		RequestID: in.Usage.RequestID,
		ClientIP:  in.Usage.ClientIP,
		Currency:  amount.Currency().Code,
		Status:    "success",
		// Who acted, and the work the amount prices. Reconstructed here because the
		// ledger row is written HERE — a field the crossing carries but this handler
		// drops is lost just as completely as one the crossing never had.
		Actor:            in.Usage.Actor,
		Cash:             in.Usage.Cash,
		PromptTokens:     in.Usage.PromptTokens,
		CompletionTokens: in.Usage.CompletionTokens,
		TotalTokens:      in.Usage.TotalTokens,
		// What it cost us, its class, the work and who pays: the usage log counts
		// every one of them, a $0 call included.
		Cost:   cost,
		Class:  in.Usage.Class,
		Units:  in.Usage.Units,
		PaidBy: in.Usage.PaidBy,
		Key:    in.Usage.Key,
	}); rerr != nil {
		// A ref already spent on another amount is the CALLER'S conflict, and it
		// crosses the plane as a status: an error string would arrive as a fault the
		// caller can only retry, and that retry will never clear.
		if errors.Is(rerr, financeclient.ErrRefReused) {
			return nil, zip.Errorf(http.StatusConflict, "record: %v", rerr)
		}
		return nil, fmt.Errorf("record: %w", rerr)
	}
	return &client.Recorded{Amount: in.Amount}, nil
}
