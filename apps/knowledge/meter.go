package knowledge

// meter.go — who pays for a connector run.
//
// Most of this subsystem is storage and search over the org's own rows, and that
// stays free. One act is not: a long-tail connector's pull is a JavaScript piece
// EXECUTED on the auto engine's sandbox pods (sync_piece.go, AUTO_UPSTREAM →
// auto.hanzo.svc). That is the same capacity apps/auto owns and correctly prices;
// reaching it by in-cluster URL rather than through its endpoint does not make the
// pod cheaper, it only makes the charge disappear. A tenant could run pieces all
// day through /v1/knowledge and never appear on auto's ledger.
//
// THE CHARGE BELONGS TO THE CALLER, NOT THE ADDRESS. This is the lesson the
// vendor-credential guard could not have caught: there is no vendor here and no
// key that says "paid", only a service name that resolves inside the cluster. The
// price is a property of the WORK, so it applies wherever the work is asked for.
//
// GITHUB IS FREE, deliberately: that connector is native Go in this process
// (sync.go's syncGitHub) and starts no pod. So the fee is not "a sync" — it is a
// PIECE RUN, and only the providers whose pull is a piece pay it.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob: CLOUD_KB_FEE_CENTS_PIECE, or CLOUD_KB_FEE_CENTS
// for every billed act on this surface.
const feeEnv = "CLOUD_KB_FEE_CENTS"

// piece is the billed act: one connector piece executed on the engine's pods.
const piece = "piece"

// defaultFeeCents is one cent per piece run.
//
// A policy default sized like the compute it is — one bounded JS execution on a
// pod we already hold — rather than the platform's $1.00 provision fee, which is
// sized for creating a database. Operators move it with the knob above; 0 makes a
// piece run free again, and un-gated with it.
const defaultFeeCents int64 = 1

func fee() int64 { return cloud.FeeCents(feeEnv, piece, defaultFeeCents) }

// paid reports whether a piece run would actually reach the engine HERE. Without
// the runner secret runPiece refuses before dialling, so nothing is bought and
// nothing may be charged — the same rule every metered surface keeps: the
// credential is what makes the act paid.
func paid() bool { return pieceRunSecret() != "" }

// afford authorizes one piece run BEFORE the engine is asked, so a caller who
// cannot cover it never occupies a pod.
func afford(s *cloud.Service[state], ctx context.Context) (*cloud.Charge, error) {
	if !paid() {
		return nil, nil
	}
	return s.Bill.Allow(ctx, cloud.PayerOf(ctx), piece, fee())
}

// charge debits one piece run, after the engine has answered. A run that errored
// or that was refused before dialling — the unset-secret path — bills nothing.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT, and re-syncing a
	// connector is a second run. Keyed on the provider, only the first would bill.
	ch.Debit(metering.Usage{Model: piece, AmountCents: fee()})
}
