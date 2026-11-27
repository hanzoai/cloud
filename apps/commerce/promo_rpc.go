// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// promo_rpc.go — THE PLATFORM PLAN PROMO, read and set where the store is.
//
// The promo is the one platform-wide percent off the plans a SuperAdmin keeps
// (hanzoai/commerce api/promo). It lives in commerce's reserved platform namespace
// beside the plan coupons, so the two ops that read and replace it are this
// process's, for the reason every store op is: one process holds the store and
// everyone else asks it. The admin surface used to reach it by an HTTP request to
// commerce's /v1/platform/promo, which this binary does not compose, so every read
// and write answered 404.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hanzoai/commerce/api/promo"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

// promoOps binds the two ops to the audit trail the promo write is recorded on.
// Method values, not closures, so zipdoc can lift each op's prose.
type promoOps struct{ trail *audit.Recorder }

// exposePromo publishes the promo read and write. Use calls it.
func exposePromo(trail *audit.Recorder) {
	o := promoOps{trail: trail}
	cloud.Plane().Post("/billing/promo", o.promo,
		zip.WithOperationID(client.BillingPromo),
		zip.WithSummary("The platform plan promo"))
	cloud.Plane().Post("/billing/promo/set", o.setPromo,
		zip.WithOperationID(client.BillingPromoSet),
		zip.WithSummary("Replace the platform plan promo"))
}

// Reads the platform plan promo as stored, on sale now or not: its percent off,
// the window it runs in, the plans it covers and whether it is on. With none
// configured it is a zeroed, inactive promo.
//
// SuperAdmin only: the promo discounts the platform's own catalog.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o promoOps) promo(ctx context.Context, _ *client.PromoIn) (*client.Promo, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("promo: SuperAdmin required")
	}
	v, err := promo.ReadPromo(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "promo: %v", err)
	}
	return promoWire(v), nil
}

// Replaces the platform plan promo: a percent off every plan it covers (0-100),
// the window it runs in (RFC3339; an empty bound is open, and an end before the
// start is refused), the plan slugs it covers (empty is every paid plan) and
// whether it is on.
//
// SuperAdmin only. The change is on the audit trail before it is made, as
// admin.promo.set with the promo before and the request after; a trail that does
// not take it refuses the change (503), and nothing moves. A promo that cannot be
// stored is 400.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o promoOps) setPromo(ctx context.Context, in *client.Promo) (*client.Promo, error) {
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("promo: SuperAdmin required")
	}
	spec := promo.Promo{PercentOff: in.PercentOff, Plans: append([]string{}, in.Plans...), Active: in.Active}
	var err error
	if spec.Start, err = instant("promo", "start", in.Start); err != nil {
		return nil, err
	}
	if spec.End, err = instant("promo", "end", in.End); err != nil {
		return nil, err
	}
	if o.trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made")
	}
	before, _ := promo.ReadPromo(ctx)
	rec := audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "admin.promo.set",
		Resource:  audit.Resource{Type: "promo", ID: "platform"},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		Before:    promoJSON(promoWire(before)),
		After:     promoJSON(in),
	}
	if _, err := o.trail.Append(ctx, rec); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the audit trail did not take the change, so it was not made: %v", err)
	}
	v, err := promo.SetPromo(ctx, spec)
	if err != nil {
		status := http.StatusInternalServerError
		if promo.IsPromoSpecRefused(err) {
			status = http.StatusBadRequest
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: err.Error()}
		_, _ = o.trail.Append(ctx, rec)
		return nil, zip.Errorf(status, "promo: %v", err)
	}
	return promoWire(v), nil
}

// promoWire is the stored promo as the plane carries it.
func promoWire(v promo.Promo) *client.Promo {
	out := &client.Promo{PercentOff: v.PercentOff, Plans: append([]string{}, v.Plans...), Active: v.Active}
	if v.Start != nil {
		out.Start = v.Start.UTC().Format(time.RFC3339)
	}
	if v.End != nil {
		out.End = v.End.UTC().Format(time.RFC3339)
	}
	return out
}

// promoJSON is a promo as the audit trail records it.
func promoJSON(v *client.Promo) json.RawMessage {
	b, _ := json.Marshal(v)
	return audit.Redact(b)
}
