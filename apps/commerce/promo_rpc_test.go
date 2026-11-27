// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// The platform plan promo is read and replaced by ops on the plane, where the
// store is. The admin surface used to reach it with an HTTP request to commerce's
// /v1/platform/promo, which this binary does not compose: every read and write
// answered 404 (TestTheAdminPromoReachesTheStore in apps/admin holds the other
// half of the path).
func TestThePromoIsReadAndSetWhereTheStoreIs(t *testing.T) {
	_, trail, _ := authorityHarness(t)
	o := promoOps{trail: trail}
	super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", Owner: "admin", User: "admin/z", Email: "z@hanzo.ai", Admin: true})
	member := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", Owner: "acme", User: "acme/w"})
	status := func(err error) int {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return he.Status
		}
		if err != nil {
			return -1
		}
		return http.StatusOK
	}

	if _, err := o.promo(member, &client.PromoIn{}); status(err) != http.StatusForbidden {
		t.Fatalf("a tenant read the platform promo: %v", err)
	}
	if _, err := o.setPromo(member, &client.Promo{PercentOff: 90, Active: true}); status(err) != http.StatusForbidden {
		t.Fatalf("a tenant set the platform promo: %v", err)
	}
	got, err := o.promo(super, &client.PromoIn{})
	if err != nil || got.PercentOff != 0 || got.Active {
		t.Fatalf("with none set, the promo read %+v, %v; want a zeroed inactive promo", got, err)
	}
	set := &client.Promo{PercentOff: 30, Plans: []string{"pro"}, Active: true, End: "2099-01-01T00:00:00Z"}
	if _, err := o.setPromo(super, set); err != nil {
		t.Fatalf("SuperAdmin set: %v", err)
	}
	got, err = o.promo(super, &client.PromoIn{})
	if err != nil || got.PercentOff != 30 || !got.Active || len(got.Plans) != 1 || got.Plans[0] != "pro" || got.End != "2099-01-01T00:00:00Z" {
		t.Fatalf("the promo read %+v, %v after it was set to %+v", got, err, set)
	}
	for _, bad := range []*client.Promo{{PercentOff: 101}, {PercentOff: 10, End: "tomorrow"}, {PercentOff: 10, Start: "2099-02-01T00:00:00Z", End: "2099-01-01T00:00:00Z"}} {
		if _, err := o.setPromo(super, bad); status(err) != http.StatusBadRequest {
			t.Errorf("setPromo(%+v) = %v, want 400", bad, err)
		}
	}
	if _, err := (promoOps{}).setPromo(super, set); status(err) != http.StatusServiceUnavailable {
		t.Errorf("a change with no audit trail was made: %v", err)
	}
	if got, _ := o.promo(super, &client.PromoIn{}); got.PercentOff != 30 {
		t.Errorf("a refused change moved the promo: %+v", got)
	}
}
