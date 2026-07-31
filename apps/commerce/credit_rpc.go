// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"

	"github.com/hanzoai/cloud"
	credit "github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The CREDIT — the other side of a settlement, published for the same reason the
// gate and the debit are: the prepaid ledger has one writer and it is this process.
//
// finance_record debits; this credits. They are separate ops because they are
// separate acts with separate idempotency and separate authority, and folding them
// into one signed amount would make a sign error a transfer in the wrong direction.
//
// It exists for x402: a purchase moves money twice, out of the buyer's ledger and
// into the seller's, and until now the second half could only happen in a process
// that had the ledger linked — which the payment rail never does. A settlement that
// can debit but not credit is not a settlement, so this is what makes "both sides
// or neither" reachable from another binary.
//
// SAY WHAT THIS IS: the first op on the internal plane that CREATES money. The
// others move it out of a ledger, read it, or gate it; this one puts it in. The
// plane's whole boundary is the socket — 0700 in the fleet's own run dir, reachable
// only by the children the router spawned — and that was already the boundary
// protecting a secret read (plane.KMSGet) and a debit (plane.FinanceRecord). It is
// the same boundary and it is now carrying more weight. Narrowing it means peer
// credentials on the plane itself (SO_PEERCRED, per-op), which is a fleet-wide seam
// and belongs to whoever owns it — not smuggled in behind a payment fix.

// exposeCredit publishes the ledger credit. Mount calls it.
func exposeCredit() {
	zip.Post[plane.CreditIn, plane.Credited](cloud.Plane(), "/finance/credit", planeCredit,
		zip.WithOperationID(plane.FinanceCredit),
		zip.WithSummary("Credit one subject's prepaid ledger, exactly once per ref"))
}

// Puts money INTO one subject's prepaid ledger — the seller's half of a
// settlement, and the only op on this plane that creates a balance rather than
// moving, reading or gating one.
//
// REF IS REQUIRED and is the idempotency key. Without it this op is a money
// printer: a settlement retries by construction, and a retry would credit twice.
// It is required rather than defaulted, because a default nobody chose is a key
// that collides. The amount must PARSE and must be positive — a credit is not a
// debit spelled with a sign.
//
// The ORG comes from the CALLER, never the argument: a caller able to name the
// credited org could pay itself out of someone else's books. An empty subject
// credits the org's own account. The amount is deposited as the EXACT decimal
// rather than its minor unit, because a settlement is routinely a fraction of a
// cent and rounding to the minor unit would round it to nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCredit(ctx context.Context, in *plane.CreditIn) (*plane.Credited, error) {
	org, err := callerOrg(ctx, "credit")
	if err != nil {
		return nil, err
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("credit: " + err.Error())
	}
	if amount.Sign() <= 0 {
		return nil, zip.ErrBadRequest("credit: amount must be positive")
	}
	if in.Ref == "" {
		return nil, zip.ErrBadRequest("credit: ref is required (the idempotency key)")
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	fin, err := books("credit")
	if err != nil {
		return nil, err
	}
	if _, derr := fin.Deposit(ctx, types.DepositInput{
		Org: org, Subject: subject, Currency: amount.Currency().Code,
		Amount: credit.FromDecimal(amount.Decimal()),
		Ref:    in.Ref, Notes: in.Notes, Tags: in.Tags,
	}); derr != nil {
		return nil, zip.Errorf(500, "credit %s/%s: %v", org, subject, derr)
	}
	return &plane.Credited{Amount: in.Amount}, nil
}
