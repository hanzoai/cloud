// Copyright © 2026 Hanzo AI. MIT License.

package ai

// routing_rpc.go — a model family's routing catalog, read and written by the
// process that serves it.
//
// A family's catalog decides which upstreams answer each of its models, in what
// order. The ai process holds the catalog's versions and the price guard that
// keeps each Enso tier's route under what the tier bills (hanzoai/ai routing.go and
// rates.go), so the one write path onto it lives there; these two ops are that
// path on the internal plane, for the Enso policy registry in train, which
// promotes a learned policy's tier routes through it. A SuperAdmin's alone: a
// catalog names the upstreams behind every model.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	aictl "github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// exposeRouting publishes the routing catalogs on the internal plane. Use calls it.
func exposeRouting() {
	cloud.Plane().Post("/ai/routing", planeRouting,
		zip.WithOperationID(client.AIRouting),
		zip.WithSummary("A family's routing catalog as the family serves it"))
	cloud.Plane().Post("/ai/routing/apply", planeRoutingApply,
		zip.WithOperationID(client.AIRoutingApply),
		zip.WithSummary("Propose or apply an edited routing catalog"))
}

// family is the routing family a call names, or a refusal: enso or zen.
func family(name string) (string, error) {
	switch f := strings.TrimSpace(name); f {
	case "enso", "zen":
		return f, nil
	}
	return "", zip.ErrBadRequest("routing: family must be enso or zen")
}

// Answers one family's routing catalog as the family serves it: the catalog, the
// newest version applied to it, whether it serves that version, and whether it
// serves a deployment that version was not made over. A family that cannot be read
// answers its reason in error, never an empty catalog. A SuperAdmin's alone.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRouting(ctx context.Context, in *client.RoutingIn) (*client.Routing, error) {
	if !principal.SuperFrom(ctx) {
		return nil, zip.ErrForbidden("routing: a SuperAdmin's alone")
	}
	if in == nil {
		in = &client.RoutingIn{}
	}
	f, err := family(in.Family)
	if err != nil {
		return nil, err
	}
	v := aictl.RoutingRead(ctx, f)
	out := &client.Routing{Family: f, Catalog: v.Catalog, Drift: v.Drift, Redeployed: v.Redeployed, Error: v.Error}
	if v.Version != nil {
		out.Version = v.Version.Id
	}
	return out, nil
}

// Proposes or applies an edited catalog for one family. Dry answers the diff
// against what the family serves, the version it would be made from, and why the
// family's price guard would refuse it; nothing is applied. Otherwise the catalog
// is applied against the version it was made from and recorded as a version, its
// actor the caller: refused 409 when another version was applied since, 400 when
// the guard would refuse it or the family refuses it, 502 when the family cannot
// be read. A SuperAdmin's alone.
func planeRoutingApply(ctx context.Context, in *client.RoutingApplyIn) (*client.RoutingApplied, error) {
	if !principal.SuperFrom(ctx) {
		return nil, zip.ErrForbidden("routing: a SuperAdmin's alone")
	}
	if in == nil || len(in.Catalog) == 0 {
		return nil, zip.ErrBadRequest("routing: a catalog is required")
	}
	f, err := family(in.Family)
	if err != nil {
		return nil, err
	}
	if in.Dry {
		p, err := aictl.RoutingPropose(ctx, f, in.Catalog)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "routing: %s", err)
		}
		return &client.RoutingApplied{Base: p.Base, Diff: p.Diff, Refused: p.Refused}, nil
	}
	v, status, err := aictl.RoutingApply(ctx, f, in.Catalog, in.Base, actor(cloud.Who(ctx)), in.Note)
	if err != nil {
		return nil, zip.Errorf(status, "routing: %s", err)
	}
	return &client.RoutingApplied{Version: v.Id, Base: v.Base, Diff: v.Diff}, nil
}

// actor is how a version names who applied it: owner/name, as the catalog editor's
// own route records a SuperAdmin.
func actor(who zip.Caller) string {
	name := who.Name
	if name == "" {
		name = who.Email
	}
	if name == "" {
		name = who.User
	}
	if who.Owner == "" {
		return name
	}
	return who.Owner + "/" + name
}
