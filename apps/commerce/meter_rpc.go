// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	credit "github.com/hanzoai/cloud/apps/money"
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

// exposeMeter publishes the gate and the debit. Mount calls it.
func exposeMeter(m *metering.Client) {
	p := cloud.Plane()

	zip.Post[plane.AuthorizeIn, plane.Verdict](p, "/finance/authorize",
		func(ctx context.Context, in *plane.AuthorizeIn) (*plane.Verdict, error) {
			org, err := callerOrg(ctx, "authorize")
			if err != nil {
				return nil, err
			}
			if m == nil || !m.Enabled() {
				// This process owns the money plane. If ITS meter is unconfigured the
				// honest answer is "unknown", not "allowed" — the caller fails closed on
				// a reason it can log, rather than handing out work nobody can bill.
				return &plane.Verdict{Reason: "commerce has no metering client"}, nil
			}
			amount, err := in.Amount.Parse()
			if err != nil {
				// A charge that cannot be read is not a charge of zero. A gate that
				// treated it as one would let the work through free.
				return nil, zip.ErrBadRequest("authorize: " + err.Error())
			}
			subject := in.Subject
			if subject == "" {
				subject = org
			}
			aerr := m.Authorize(ctx, metering.AuthInput{
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
		},
		zip.WithOperationID(plane.FinanceAuthorize),
		zip.WithSummary("Authorize one prepaid spend"))

	zip.Post[plane.RecordIn, plane.Recorded](p, "/finance/record",
		func(ctx context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
			org, err := callerOrg(ctx, "record")
			if err != nil {
				return nil, err
			}
			if m == nil || !m.Enabled() {
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
			// User and Org are set HERE from the caller, never from the argument: a
			// caller that could name the billed org could bill someone else.
			if _, rerr := m.Record(ctx, metering.Usage{
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
		},
		zip.WithOperationID(plane.FinanceRecord),
		zip.WithSummary("Debit one metered act"))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
