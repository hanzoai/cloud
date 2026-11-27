// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// The entitlement op served by a commerce whose store is not booted refuses: a read
// nobody could make is an error at the caller, never "not entitled", which would
// refuse a paying customer.
func TestTheEntitlementOpFailsClosedWithoutTheStore(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	prev := currentEmbedded()
	PublishEmbedded(nil)
	t.Cleanup(func() { PublishEmbedded(prev) })

	exposeEntitlement()
	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane(commerce): %v", err)
	}
	defer func() { _ = stop() }()

	if ent, err := commercepeer.BillingEntitlement(plane.For(context.Background(), "acme"), &client.EntitlementIn{Product: "engine"}); err == nil {
		t.Fatalf("an unreadable entitlement answered %+v, want an error", ent)
	}
}
