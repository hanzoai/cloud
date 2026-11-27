// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A family's routing is a SuperAdmin's alone, on the plane as on the route: an org
// admin acting in its org and a caller with no authority are refused before the
// catalog is read, and a family that is not one is refused by name.
func TestRoutingOnThePlaneIsASuperAdminsAlone(t *testing.T) {
	status := func(err error) int {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return he.Status
		}
		return 0
	}
	for _, who := range []zip.Caller{{}, {Org: "acme", User: "u", OrgAdmin: true}, {Org: "admin", User: "u", OrgAdmin: true}} {
		ctx := zip.WithCaller(context.Background(), who)
		if _, err := planeRouting(ctx, &client.RoutingIn{Family: "enso"}); status(err) != http.StatusForbidden {
			t.Errorf("read as %+v: %v", who, err)
		}
		if _, err := planeRoutingApply(ctx, &client.RoutingApplyIn{Family: "enso", Catalog: []byte(`{}`), Dry: true}); status(err) != http.StatusForbidden {
			t.Errorf("apply as %+v: %v", who, err)
		}
	}
	super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "u", Owner: "admin", Name: "z", Admin: true})
	if _, err := planeRouting(super, &client.RoutingIn{Family: "kai"}); status(err) != http.StatusBadRequest {
		t.Errorf("a family that is not one: %v", err)
	}
	if _, err := planeRoutingApply(super, &client.RoutingApplyIn{Family: "enso"}); status(err) != http.StatusBadRequest {
		t.Errorf("an apply with no catalog: %v", err)
	}
	if got := actor(zip.Caller{Owner: "admin", Name: "z", Email: "z@hanzo.ai"}); got != "admin/z" {
		t.Errorf("actor %q, want admin/z as the catalog editor records one", got)
	}
}
