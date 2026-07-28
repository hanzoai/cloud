// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// The prepaid GATE and the debit, published on the internal plane for the same
// reason the balance read is: the ledger has one writer and it lives here.
//
// Without these an app in its own process finds no metering client, and
// ResourceMeter.Gate is documented to ALLOW when billing is unconfigured — which
// is right for a deployment that does not bill and catastrophic for one that does.
// Split into per-app binaries, every priced create became free: the gate could not
// tell "nobody bills here" from "the biller is one socket away".
const (
	authorizeMethod = "finance.authorize"
	recordMethod    = "finance.record"
)

// exposeMeter publishes the gate and the debit. Mount calls it.
func exposeMeter(m *metering.Client) {
	cloud.Expose(authorizeMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		subject, amount, project, service, validated, err := cloud.AuthorizeReq(req)
		if err != nil {
			return nil, err
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("authorize: no org on the capability")
		}
		if m == nil || !m.Enabled() {
			// This process owns the money plane. If ITS meter is unconfigured the
			// honest answer is "unknown", not "allowed" — the caller fails closed on
			// a reason it can log, rather than handing out work nobody can bill.
			return cloud.PutVerdict(cloud.Verdict{Reason: "commerce has no metering client"}), nil
		}
		if subject == "" {
			subject = org
		}
		// The ledger's gate still speaks minor units; the WIRE is exact so the
		// contract does not lose anything the ledger later gains.
		cents := amount.Minor().Int64()
		aerr := m.Authorize(ctx, metering.AuthInput{
			User: subject, Org: org,
			AmountCents:      cents,
			Project:          project,
			ProjectValidated: validated,
			Service:          service,
			Currency:         amount.Currency().Code,
		})
		switch {
		case aerr == nil:
			return cloud.PutVerdict(cloud.Verdict{OK: true}), nil
		case errors.Is(aerr, metering.ErrInsufficientBalance):
			return cloud.PutVerdict(cloud.Verdict{NoFunds: true}), nil
		case errors.Is(aerr, metering.ErrSpendCapExceeded):
			return cloud.PutVerdict(cloud.Verdict{CapSpent: true}), nil
		default:
			return cloud.PutVerdict(cloud.Verdict{Reason: aerr.Error()}), nil
		}
	})

	cloud.Expose(recordMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		subject, amount, project, service, _, err := cloud.AuthorizeReq(req)
		if err != nil {
			return nil, err
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("record: no org on the capability")
		}
		if m == nil || !m.Enabled() {
			return nil, fmt.Errorf("record: commerce has no metering client")
		}
		if subject == "" {
			subject = org
		}
		// User and Org are set HERE from the capability, never from the payload: a
		// caller that could name the billed org could bill someone else.
		if _, rerr := m.Record(ctx, metering.Usage{
			User: subject, Org: org,
			AmountCents: amount.Minor().Int64(),
			Model:       service,
			Project:     project,
			Provider:    service,
			Service:     service,
			Currency:    amount.Currency().Code,
			Status:      "success",
		}); rerr != nil {
			return nil, fmt.Errorf("record: %w", rerr)
		}
		return cloud.PutI64(amount.Minor().Int64()), nil
	})
}
