// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// coupon_rpc.go — THE PLAN COUPONS, read and set where the store is.
//
// A plan coupon is a code a buyer types at checkout for a percent off the first
// months of one plan purchase — one month unless it says more. It lives in
// commerce's reserved platform namespace beside the platform promo
// (hanzoai/commerce api/promo), so the ops that read and write one are this
// process's, for the reason every store op is: one process holds the store and
// everyone else asks it.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/api/promo"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

// couponOps binds the ops to the audit trail the coupon writes are recorded on.
// Method values, not closures, so zipdoc can lift each op's prose.
type couponOps struct{ trail *audit.Recorder }

// exposeCoupon publishes the coupon read and write. Use calls it.
func exposeCoupon(trail *audit.Recorder) {
	o := couponOps{trail: trail}
	cloud.Plane().Post("/billing/coupon", o.coupon,
		zip.WithOperationID(client.BillingCoupon),
		zip.WithSummary("One plan coupon"))
	cloud.Plane().Post("/billing/coupon/set", o.setCoupon,
		zip.WithOperationID(client.BillingCouponSet),
		zip.WithSummary("Create or replace one plan coupon"))
	cloud.Plane().Post("/billing/coupon/mint", o.mintCoupon,
		zip.WithOperationID(client.BillingCouponMint),
		zip.WithSummary("Mint one single-use plan coupon under a fresh code"))
	cloud.Plane().Post("/billing/coupon/extend", o.extendCoupon,
		zip.WithOperationID(client.BillingCouponExtend),
		zip.WithSummary("Grant a plan coupon's months to the subscriptions already bought with it"))
}

// Reads one plan coupon by its code (case-insensitive): its percent off each month
// it discounts and how many months that is, the window it is redeemable in, its
// limit and how many uses it has had, the plans it covers and whether it is on. A
// code with no plan coupon is 404.
//
// SuperAdmin only: the coupons discount the platform's own catalog.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o couponOps) coupon(ctx context.Context, in *client.CouponRef) (*client.Coupon, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("coupon: SuperAdmin required")
	}
	v, err := promo.ReadCoupon(ctx, in.Code)
	switch {
	case errors.Is(err, promo.ErrCouponNotFound):
		return nil, zip.ErrNotFound("coupon not found")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "coupon: %v", err)
	}
	return couponWire(v), nil
}

// Creates or replaces one plan coupon: a percent (1-100) off each of the first
// Months monthly periods of a plan purchase (1-36; 0 is 1, the first month), the
// window it is redeemable in (RFC3339; an empty end leaves it open), how many uses
// it has in all (0 is no limit), the plan slugs it covers (at least one paid plan)
// and whether it is on. Its use count is never taken from the request. A change
// of months moves no subscription already sold; extend does that.
//
// SuperAdmin only. The change is on the audit trail before it is made, as
// admin.coupon.set with the coupon before and the request after; a trail that does
// not take it refuses the change (503), and nothing moves. A spec the coupon cannot
// be stored as is 400.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o couponOps) setCoupon(ctx context.Context, in *client.CouponSet) (*client.Coupon, error) {
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("coupon: SuperAdmin required")
	}
	spec := promo.CouponSpec{Percent: in.Percent, Months: in.Months, Limit: in.Limit, Plans: in.Plans, Enabled: in.Enabled, Campaign: in.Campaign}
	var err error
	if spec.Start, err = instant("coupon", "start", in.Start); err != nil {
		return nil, err
	}
	if spec.End, err = instant("coupon", "end", in.End); err != nil {
		return nil, err
	}
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
	}
	code := promo.CouponCode(in.Code)
	before, _ := promo.ReadCoupon(ctx, code)
	actor := cmp.Or(who.Email, who.User, who.Owner)
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "admin.coupon.set",
		Resource:  audit.Resource{Type: "coupon", ID: cmp.Or(code, strings.TrimSpace(in.Code))},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    couponJSON(before),
		After:     couponJSON(in),
	}
	if _, err := o.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made: %v", err)
	}
	v, err := promo.SetCoupon(ctx, code, spec, actor)
	if err != nil {
		status := http.StatusInternalServerError
		if promo.IsCouponSpecRefused(err) {
			status = http.StatusBadRequest
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: err.Error()}
		_, _ = o.trail.Append(ctx, rec)
		return nil, zip.Errorf(status, "coupon: %v", err)
	}
	return couponWire(v), nil
}

// Mints one single-use plan coupon under a fresh code, Prefix-XXXXXXXXXX (ten of
// 32 symbols that do not misread): Percent off the first Months months of every
// paid plan on sale (1-36; none is one, the first month), redeemable until End
// (RFC3339; empty never closes), and one of Campaign's codes, so an org and a
// card redeem one code of the campaign. A code already held is never replaced.
//
// The platform only: the caller states the admin org, as the marketing app does
// when a person files a program application, or is a SuperAdmin. The mint is on
// the audit trail before the coupon is stored, as admin.coupon.mint; a trail that
// does not take it refuses (503) and nothing is minted. A spec the coupon cannot
// be stored as is 400.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o couponOps) mintCoupon(ctx context.Context, in *client.CouponMint) (*client.Coupon, error) {
	who := cloud.Who(ctx)
	if who.Org != authz.AdminOrg && !who.Admin {
		return nil, zip.ErrForbidden("coupon mint: the platform mints")
	}
	end, err := instant("coupon", "end", in.End)
	if err != nil {
		return nil, err
	}
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the mint, so nothing was minted")
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "admin.coupon.mint",
		Resource:  audit.Resource{Type: "coupon", ID: promo.CouponCode(in.Prefix) + "-*"},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     couponJSON(in),
	}
	if _, err := o.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the mint, so nothing was minted: %v", err)
	}
	spec := promo.CouponSpec{Percent: in.Percent, Months: in.Months, End: end, Enabled: true, Campaign: in.Campaign}
	v, err := promo.MintCoupon(ctx, in.Prefix, spec, cmp.Or(who.Email, who.User, who.Org))
	if err != nil {
		status := http.StatusInternalServerError
		if promo.IsCouponSpecRefused(err) {
			status = http.StatusBadRequest
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: err.Error()}
		_, _ = o.trail.Append(ctx, rec)
		return nil, zip.Errorf(status, "coupon: %v", err)
	}
	return couponWire(v), nil
}

// Grants a plan coupon's months, as it now stands, to every live subscription
// already bought with it: each org that redeemed the code, each of its
// subscriptions the sale noted it on, counted from that subscription's own first
// paid period, at the percent it was sold. No month is ever taken away. Apply
// false is a dry run: it answers every subscription and the grant it would hold,
// and changes nothing. It moves no money: a cycle among the granted months already
// invoiced in full is counted as missed, for a person to settle.
//
// SuperAdmin only. An applied extension is on the audit trail before it is made,
// as admin.coupon.extend; a trail that does not take it refuses (503) and nothing
// moves. Each subscription it changes is recorded again in its own org, as
// subscription.coupon_extended.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o couponOps) extendCoupon(ctx context.Context, in *client.CouponExtend) (*client.CouponExtension, error) {
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("coupon: SuperAdmin required")
	}
	code := promo.CouponCode(in.Code)
	actor := cmp.Or(who.Email, who.User, who.Owner)
	var rec audit.Record
	if in.Apply {
		if o.trail == nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
		}
		rec = audit.Record{
			Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
			Action:    "admin.coupon.extend",
			Resource:  audit.Resource{Type: "coupon", ID: cmp.Or(code, strings.TrimSpace(in.Code))},
			Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
			Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
			SourceIP:  who.IP,
			RequestID: who.RequestID,
			After:     couponJSON(in),
		}
		if _, err := o.trail.Append(ctx, rec); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made: %v", err)
		}
	}
	x, err := commercebilling.ExtendCoupon(ctx, code, in.Apply, actor)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, promo.ErrCouponNotFound) {
			status = http.StatusNotFound
		}
		if in.Apply {
			rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: err.Error()}
			_, _ = o.trail.Append(ctx, rec)
		}
		return nil, zip.Errorf(status, "coupon: %v", err)
	}
	out := &client.CouponExtension{Code: x.Code, Months: x.Months, Applied: x.Applied, Orgs: x.Orgs, Subscriptions: []client.CouponExtended{}}
	for _, e := range x.Subscriptions {
		w := client.CouponExtended{
			Org: e.Org, SubscriptionID: e.SubscriptionID, UserID: e.UserID, Status: e.Status, Test: e.Test,
			Percent: e.Percent, Months: e.Months, Billed: e.Billed, Changed: e.Changed, Missed: e.Missed, Error: e.Error,
		}
		if e.From != nil {
			w.From = e.From.UTC().Format(time.RFC3339)
		}
		out.Subscriptions = append(out.Subscriptions, w)
	}
	return out, nil
}

// instant reads an RFC3339 bound of what (a coupon, the promo): nil for an open end.
func instant(what, field, s string) (*time.Time, error) {
	if s = strings.TrimSpace(s); s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, zip.ErrBadRequest(what + ": " + field + " must be RFC3339, like 2026-10-09T16:00:00Z")
	}
	return &t, nil
}

// couponWire is a stored coupon as the plane carries it.
func couponWire(v *promo.CouponView) *client.Coupon {
	out := &client.Coupon{
		Code: v.Code, Percent: v.Percent, Limit: v.Limit,
		Plans: append([]string{}, v.Plans...), Enabled: v.Enabled, Used: v.Used, Campaign: v.Campaign,
		Months: v.Months,
	}
	if v.Start != nil {
		out.Start = v.Start.UTC().Format(time.RFC3339)
	}
	if v.End != nil {
		out.End = v.End.UTC().Format(time.RFC3339)
	}
	return out
}

// couponJSON is a coupon as the audit trail records it; null when there is none.
func couponJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if c, ok := v.(*promo.CouponView); ok && c == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return audit.Redact(b)
}
