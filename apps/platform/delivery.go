package platform

// delivery.go is the READ half of /v1/platform/apps as typed ops.
//
// The four addresses here answer what a deploy console and a deploy AGENT ask:
// what has this org declared, what does one declaration say, and what has the
// delivery plane done with it. They were raw handlers behind cloud.Guard, which
// meant the whole delivery board published an operationId and nothing else — no
// schema, no MCP tool, no CLI command, no typed SDK method. An agent asked "what
// is deployed" had nothing to call.
//
// THE GATE MOVED INSIDE, and that is a correctness requirement rather than a
// style: cloud.Guard is middleware on a ROUTE, and a typed op is also reached by
// tools/call and by the CLI, neither of which passes through a router. A wrapper
// left where it was would have published an UNGUARDED alias of a SuperAdmin
// surface — the apps/exec incident exactly. `admit` is the same predicate the
// wrapper applied (cloud.Scope.Admits over cloud.AuthorityOf, refused with the
// scope's own sentence), called as the first line of every op, so all three
// endpoints ask one question. TestTheDeliveryBoardIsShutToANonAdminOnEveryEndpoint
// drives the MCP server specifically, because that is the one a route test cannot
// see.
//
// The wire did not move. Each of these reads NO body, so zip's decode is skipped
// and there is no 400-before-403 to trade — which is why the four GETs converted
// and the POST beside them did not (typed_wire_test.go names it).

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// delivery holds BOTH services for the reason appsRoutes did: a declaration is
// read from git through the platform service and its reconciliation from the
// cluster through the fleet service, and joining them is this surface's job.
type delivery struct {
	s  *cloud.Service[state]
	fs *cloud.Service[fleetState]
}

// declarationsQuery narrows a read to another organisation's declarations.
type declarationsQuery struct {
	// Org names the organisation whose declarations to read, defaulting to the
	// caller's own. Only a SuperAdmin may name one that is not theirs; anyone
	// else naming a foreign org is refused, so this widens nothing by itself.
	Org string `json:"org"`
}

// declarationRef addresses ONE declaration.
type declarationRef struct {
	// App is the DNS-1123 label of the declaration. The URL is the addressing
	// authority — a path segment binds after the body and after the query — so
	// the address decides which app is read whatever else is sent.
	App string `json:"app"`
	// Org names the organisation the declaration lives in, defaulting to the
	// caller's own and subject to the same SuperAdmin rule as the listing.
	Org string `json:"org"`
}

// listDeclarations answers what this organisation has declared, joined with what
// the delivery plane has done about it.
//
// The join is best-effort BY DESIGN and says so when it is missing: the
// declarations ARE the answer to "what have I deployed", so refusing the whole
// board because the cluster is unreadable would lose the half that is readable.
// What must never happen is a silent null — an unreadable plane is reported as
// `cd.unavailable` carrying the reason, never as an app with no reconciliation.
func (d delivery) listDeclarations(ctx context.Context, in *declarationsQuery) (*declaredResp, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, principal.Refused(c)
	}
	dir, err := resolveOrg(org, in.Org, principal.IsSuperAdmin(c))
	if err != nil {
		return nil, err
	}
	ds, err := declarations(d.s, ctx, dir)
	if err != nil {
		d.s.Log.Error("inventory read failed", "org", dir, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "could not read the declarations in %s: %v", dir, err)
	}
	out := declaredResp{Org: dir, Apps: make([]declared, 0, len(ds))}
	for _, dec := range ds {
		out.Apps = append(out.Apps, declared{Declaration: dec})
	}
	apps, cdErr := cdApps(d.fs, ctx, requestPrincipal(c))
	if cdErr != nil {
		out.CD = &unreadable{Reason: cdErr.Error()}
	} else {
		by := map[string]*CDApp{}
		for i := range apps {
			by[apps[i].Name] = &apps[i]
		}
		for i := range out.Apps {
			out.Apps[i].CD = by[out.Apps[i].Application]
		}
	}
	return &out, nil
}

// getDeclaration answers ONE declaration — what git says this app is, before the
// delivery plane has had any say in it.
func (d delivery) getDeclaration(ctx context.Context, in *declarationRef) (*Declaration, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, principal.Refused(c)
	}
	if !slugRE.MatchString(in.App) {
		return nil, zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	dir, err := resolveOrg(org, in.Org, principal.IsSuperAdmin(c))
	if err != nil {
		return nil, err
	}
	ds, err := declarations(d.s, ctx, dir)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not read the declarations in %s: %v", dir, err)
	}
	for i := range ds {
		if ds[i].Name == in.App {
			return &ds[i], nil
		}
	}
	return nil, zip.ErrNotFound("no declaration for " + in.App + " in " + dir)
}

// getReconciliation answers ONE app's reconciliation alone — the poll a deploy
// console makes while it waits, without re-reading the whole inventory each time.
func (d delivery) getReconciliation(ctx context.Context, in *declarationRef) (*CDApp, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, principal.Refused(c)
	}
	if !slugRE.MatchString(in.App) {
		return nil, zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	dir, err := resolveOrg(org, in.Org, principal.IsSuperAdmin(c))
	if err != nil {
		return nil, err
	}
	apps, err := cdApps(d.fs, ctx, requestPrincipal(c))
	if err != nil {
		return nil, err
	}
	want := dir + "-" + in.App
	for i := range apps {
		if apps[i].Name == want {
			return &apps[i], nil
		}
	}
	return nil, zip.ErrNotFound("the delivery plane has no Application " + want +
		" — a declaration on a branch has none until the branch is merged")
}

// listReconciliations answers every Application the delivery plane holds.
//
// Scoped to the namespaces the caller's own validated org owns: the ROLE admits
// the caller and the tenant boundary is applied inside, so an admin of one org
// never observes another's.
func (d delivery) listReconciliations(ctx context.Context, _ *noDeliveryQuery) (*cdResp, error) {
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	apps, err := cdApps(d.fs, ctx, requestPrincipal(c))
	if err != nil {
		return nil, err
	}
	return &cdResp{Applications: apps}, nil
}

// noDeliveryQuery is an op that takes nothing. It publishes no request body on a GET and
// exists so the op still has an In to bind, which zip's signature requires.
type noDeliveryQuery struct{}
