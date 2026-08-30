package share

// meter.go — who pays for a tunnel account.
//
// Listing shares is a read and stays free. Provisioning is not: POST
// /v1/share/enable creates an account on the share fabric using the PLATFORM's
// admin credential (ZROK_ADMIN_TOKEN) and hands the caller the token their CLI
// enables tunnels with. Any validated tenant reaches it — there is no admin gate
// here, and there should not be, because self-service is the product. What was
// missing is that the surface declared cloud.Free, so a tenant could mint
// accounts on our credential with nothing authorized and nothing recorded.
//
// THE UNIT IS THE ACCOUNT, ONCE. Enable is idempotent by construction: the
// account is keyed deterministically off the validated org, so a repeat call
// hands back the same credential. Charging per call would bill a caller for a
// no-op every time their CLI re-read its own token — the "keyed on a thing rather
// than an act" mistake. So the charge is on the PROVISION, and the read-back path
// is neither gated nor billed: a tenant who already has an account can always
// fetch it, whatever their balance.
//
// The bytes themselves never cross this process — a tunnel runs client-side
// against the public frontend — so there is no traffic here to meter. The account
// is the thing we grant and the thing we can see.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob: SHARE_FEE_CENTS_ACCOUNT, or SHARE_FEE_CENTS for
// every billed act on this surface.
const feeEnv = "SHARE_FEE_CENTS"

// account is the billed act: one tunnel account provisioned on the fabric.
const account = "account"

// defaultFeeCents is the platform's ordinary provision fee.
//
// This IS a provision — it creates a durable resource on shared infrastructure
// that the tenant then uses indefinitely — so it is priced like every other
// create in the fleet rather than like a per-request act. It happens once per org
// in that org's whole life. Operators move it with the knob above; 0 makes
// provisioning free again, and un-gated with it.
func fee() int64 { return cloud.ResourceFeeCents(feeEnv, account) }

// afford authorizes one provision BEFORE the fabric is asked, so a caller who
// cannot cover it never gets an account minted on the platform's credential.
func afford(s *cloud.Service[state], ctx context.Context) (*cloud.Charge, error) {
	return s.Bill.Reserve(ctx, cloud.PayerOf(ctx), account, fee())
}

// charge debits one provision, after the account exists.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one. It names an ACT — and since an org
	// provisions once, that act happens once; a caller re-reading their token never
	// reaches this line at all.
	ch.Debit(metering.Usage{Model: account, AmountCents: fee()})
}
