// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// planlink.go — what a plan link says about an org's plan.
//
// A link names the account whose recorded subscriptions an org's plan is read from when
// the org holds no live paid plan of its own. It names no tier and moves no money: the
// plan is DERIVED, per subject, by the same read every gate already trusts (ReadTier),
// so the rule that nobody names a tier holds. Reading by subject matters: in the shared
// signup org a scan of the org's subscriptions would read other people's plans.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/account"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercetier "github.com/hanzoai/commerce/billing/tier"
	"github.com/zap-proto/zip"
)

// onPlan reports whether a tier read finds a live plan: a paid one, or a trial the
// paywall honours as ActivePaidPlan does.
func onPlan(v *commercebilling.TierView) bool {
	return v.Tier.Name != commercetier.Free
}

// sourcePlan is the plan from's own subscriptions confer right now, read through the
// canonical per-subject tier read in the org that holds the account. live is false when
// the account holds no live plan; an error is a read that could not be completed.
func sourcePlan(ctx context.Context, from account.Account) (view *commercebilling.TierView, live bool, err error) {
	if from.Zero() {
		return nil, false, fmt.Errorf("plan link: no source account")
	}
	org, err := resolveOrg(ctx, from.Org())
	if err != nil {
		return nil, false, fmt.Errorf("plan link: resolve %q: %w", from.Org(), err)
	}
	view, err = commercebilling.ReadTier(ctx, org, from.Subject())
	if err != nil {
		return nil, false, fmt.Errorf("plan link: read %s: %w", from.Subject(), err)
	}
	return view, onPlan(view) && strings.TrimSpace(view.Plan) != "", nil
}

// borrowed is the plan org borrows through its link, when it has one and the source's
// plan is live. A link whose source has lapsed confers nothing (nil, false, nil); a link
// that cannot be read, or a store that is not open, is an error and never "no link".
func borrowed(ctx context.Context, org string) (*commercebilling.TierView, bool, error) {
	view, _, ok, err := borrowedFrom(ctx, org)
	return view, ok, err
}

// borrowedFrom is borrowed along with the canonical account that holds the subscription.
func borrowedFrom(ctx context.Context, org string) (*commercebilling.TierView, account.Account, bool, error) {
	s, err := links()
	if err != nil {
		return nil, account.Account{}, false, err
	}
	l, ok, err := s.ByOrg(ctx, org)
	if err != nil {
		return nil, account.Account{}, false, zip.Errorf(500, "failed to read the plan link")
	}
	if !ok {
		return nil, account.Account{}, false, nil
	}
	view, live, err := sourcePlan(ctx, l.From)
	if err != nil || !live {
		return nil, account.Account{}, false, err
	}
	return view, l.From, true, nil
}
