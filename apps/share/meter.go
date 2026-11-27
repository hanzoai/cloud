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
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	acct "github.com/hanzoai/account"
	"github.com/hanzoai/authz"
)

// feeEnv is the operator knob: SHARE_FEE_CENTS_ACCOUNT, or SHARE_FEE_CENTS for
// every billed act on this surface.
const feeEnv = "SHARE_FEE_CENTS"

// account is the billed act: one tunnel account provisioned on the fabric.
const account = "account"

// subCheckTimeout bounds a subscription lookup on the commerce/IAM plane.
const subCheckTimeout = 5 * time.Second

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
//
// An active paid subscription (Pro, Max, Team, Unlimited, etc.) already covers
// provisioning the caller's tunnel account, even when their prepaid wallet sits
// at zero; callers without a paid plan pay the one-time provision fee from their
// prepaid wallet as usual.
func afford(s *cloud.Service[state], ctx context.Context, org string) (*cloud.Charge, error) {
	if fee() <= 0 {
		return &cloud.Charge{}, nil
	}
	payer := cloud.PayerOf(ctx)
	if subscribed(ctx, s.State.plans, org, payer) {
		return &cloud.Charge{}, nil
	}
	return s.Bill.Reserve(ctx, payer, account, fee())
}

// subscribed reports whether the caller holds a live paid subscription covering
// this tunnel account — either in their customer org, under their own subject in
// a held org, or in a personal/team org they belong to when calling from CLI on
// the shared signup org.
func subscribed(ctx context.Context, plans host.PlanChecker, org string, payer cloud.Payer) bool {
	if !authz.IsHeldOrg(org) {
		return orgHasPaidSub(ctx, plans, org, "")
	}
	p, _ := iam.SelfFrom(ctx)
	for _, sub := range uniqueNonEmpty(p.ID, p.Row, payer.Wallet.Subject()) {
		if orgHasPaidSub(ctx, plans, org, sub) {
			return true
		}
	}
	checked := map[string]bool{}
	for _, memberOrg := range principal.OrgsFrom(ctx) {
		if authz.IsHeldOrg(memberOrg) || checked[memberOrg] {
			continue
		}
		checked[memberOrg] = true
		if orgHasPaidSub(ctx, plans, memberOrg, "") {
			return true
		}
	}
	// CLI tokens (`hanzo link`) often carry owner=hanzo without X-User-Orgs while
	// the user's paid subscription sits on their personal org `<username>`. Verify
	// strictly that `<username>` belongs to this person (via subscription UserID
	// or IAM membership) so a coincidental org name never admits a stranger.
	if owner, username, ok := strings.Cut(p.Row, "/"); ok &&
		owner == acct.SignupOrg &&
		!authz.IsHeldOrg(username) &&
		principal.NamesOrg(username) &&
		!checked[username] {
		if personalOrgHasPaidSub(ctx, plans, username, p) {
			return true
		}
	}
	return false
}

func orgHasPaidSub(ctx context.Context, plans host.PlanChecker, org, userID string) bool {
	if org == "" {
		return false
	}
	if userID == "" && plans != nil {
		if slug, ok, err := plans.ActivePaidPlan(ctx, org); err == nil && ok && isPaidSlug(slug) {
			return true
		}
	}
	rows, ok := listSubs(ctx, org, userID)
	if !ok {
		return false
	}
	for _, r := range rows {
		if userID != "" && !strings.EqualFold(strings.TrimSpace(r.UserID), userID) {
			continue
		}
		if isLivePaidSub(r) {
			return true
		}
	}
	return false
}

func personalOrgHasPaidSub(ctx context.Context, plans host.PlanChecker, personalOrg string, p iam.Self) bool {
	rows, ok := listSubs(ctx, personalOrg, "")
	if ok {
		hasLive := false
		for _, r := range rows {
			if !isLivePaidSub(r) {
				continue
			}
			hasLive = true
			if uid := strings.TrimSpace(r.UserID); uid != "" && p.Is(uid) {
				return true
			}
		}
		if hasLive && memberOfOrg(ctx, personalOrg, uniqueNonEmpty(p.ID, p.Row)) {
			return true
		}
	}
	if plans != nil {
		if slug, ok, err := plans.ActivePaidPlan(ctx, personalOrg); err == nil && ok && isPaidSlug(slug) {
			return memberOfOrg(ctx, personalOrg, uniqueNonEmpty(p.ID, p.Row))
		}
	}
	return false
}

func listSubs(parent context.Context, org, userID string) ([]client.Sub, bool) {
	pctx, cancel := planeCtx(parent, org)
	defer cancel()
	out, err := commerce.FinanceSubs(pctx, &client.SubsIn{UserID: userID})
	if err != nil || out == nil {
		return nil, false
	}
	return out.Rows, true
}

func planeCtx(parent context.Context, org string) (context.Context, context.CancelFunc) {
	timeout := subCheckTimeout
	if dl, ok := parent.Deadline(); ok {
		if rem := time.Until(dl); rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), timeout)
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func isLivePaidSub(r client.Sub) bool {
	switch strings.ToLower(strings.TrimSpace(r.Status)) {
	case "active", "trialing":
	default:
		return false
	}
	if r.MRRCents > 0 {
		return true
	}
	return isPaidSlug(r.PlanSlug)
}

func isPaidSlug(slug string) bool {
	s := strings.ToLower(strings.TrimSpace(slug))
	if s == "" {
		return false
	}
	return s != "free" && !strings.HasPrefix(s, "world-free") && !strings.HasSuffix(s, "-free")
}

func uniqueNonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// charge debits one provision, after the account exists.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one. It names an ACT — and since an org
	// provisions once, that act happens once; a caller re-reading their token never
	// reaches this line at all.
	ch.Debit(meter.Usage{Model: account, AmountCents: fee()})
}
