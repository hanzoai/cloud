// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"net/http"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Who a money READ is for, and how a caller's identity crosses the in-process hop
// to commerce.
//
// ReaderOrg was exported from apps/account and billing and commerce imported that
// app to reach it; it is [principal.Org] and nothing else. It used to also admit
// a shared service token naming any tenant in X-Org-Id — commerce authenticating
// a caller IAM had never seen — and that arm is gone with the token. A service
// that reads a tenant's books over the plane states the tenant through cloud.For
// and reaches callerOrg there; over HTTP it is a validated principal or nobody.

// ReaderOrg answers which tenant a money read is scoped to: the validated
// principal's org, or nothing.
func ReaderOrg(c *zip.Ctx) (string, bool) {
	return principal.Org(c)
}

// carryIdentity is the transport's identity carrier (transport.SetIdentity): it
// says who a request the commerce transport dispatches in-process is from. It
// is the platform — the reserved admin org as home owner, which is what IAM
// mints for the platform's own application — scoped to the org the sender named
// in X-Org-Id, or failing that the org this call acts for (a validated
// principal's, or the one a background job stated through cloud.For). A call
// that acts for nobody names no org, and commerce refuses it.
//
// That is exactly what the shared service token used to assert, minus the
// secret: nothing outside this process can dispatch through the transport, so
// the hop itself is the proof, the way a plane call's caller is. Commerce's
// money routes are Admin-masked for the platform, not for a tenant's user, and
// this is the platform reading a tenant's books on its own behalf — the edge
// gate authorizing a spend, the billing app answering a balance — never the
// user acting as themselves.
func carryIdentity(ctx context.Context, h http.Header) {
	if h.Get(authz.HeaderOrg) == "" {
		if org, ok := Tenant(ctx); ok {
			h.Set(authz.HeaderOrg, org)
		}
	}
	h.Set(authz.HeaderUser, "platform")
	h.Set(authz.HeaderUserOwner, authz.AdminOrg)
}
