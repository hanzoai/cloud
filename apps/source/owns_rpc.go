// Copyright © 2026 Hanzo AI. MIT License.

package source

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// Ownership and dispatch ops for linked sources, published on the internal plane.
//
// The Hanzo Link relay and /v1/dev session router (apps/dev) run in the sandbox
// binary, and the registry lives in this app's per-org store, so both questions
// cross a process boundary — which is what the plane is.

// exposeOwns publishes the internal plane ops for apps/source. Use calls it.
func exposeOwns() {
	cloud.Plane().Post("/sources/owns",
		owns,
		zip.WithOperationID(client.SourcesOwns),
		zip.WithSummary("Whether a person owns a linked source"))
	cloud.Plane().Post("/sources/dispatch",
		dispatchRPC,
		zip.WithOperationID(client.SourcesDispatch),
		zip.WithSummary("Resolve and lease a linked desktop login for a /v1/dev session"))
	cloud.Plane().Post("/sources/release",
		releaseRPC,
		zip.WithOperationID(client.SourcesRelease),
		zip.WithSummary("Release a lease taken by SourcesDispatch"))
}

// owns answers for the caller's org: the subject owns id and it is not revoked.
func owns(ctx context.Context, in *client.SourceOwnsIn) (*client.SourceOwned, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("owns: no org on the call")
	}
	id, subject := strings.TrimSpace(in.ID), strings.TrimSpace(in.Subject)
	if id == "" || subject == "" {
		return nil, zip.ErrBadRequest("owns: id and subject are required")
	}
	return &client.SourceOwned{Owned: Owns(ctx, org, subject, id) == nil}, nil
}

// dispatchRPC runs the same dispatch policy, leasing and audit as POST /v1/sources/:id/dispatch.
func dispatchRPC(ctx context.Context, in *client.SourceDispatchIn) (*client.SourceRouteOut, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, zip.ErrUnauthorized("dispatch: no org on the call")
	}
	id, subject := strings.TrimSpace(in.ID), strings.TrimSpace(in.Subject)
	if id == "" || subject == "" {
		return nil, zip.ErrBadRequest("dispatch: id and subject are required")
	}
	if strings.HasPrefix(subject, appSubject) {
		return nil, zip.ErrForbidden("sources belong to a person: a machine token cannot dispatch one")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "source: not mounted")
	}
	o := ops{s: mounted}
	sto, err := o.open(org)
	if err != nil {
		return nil, err
	}
	route, err := o.dispatchFor(ctx, sto, subject, &SourceDispatch{
		ID:       id,
		Provider: strings.TrimSpace(in.Provider),
		Instance: strings.TrimSpace(in.Instance),
	})
	if err != nil {
		return nil, err
	}
	return &client.SourceRouteOut{
		Zap:      route.Zap,
		Provider: route.Provider,
		Instance: route.Instance,
		Lease:    route.Lease,
		Expires:  route.Expires,
	}, nil
}

// releaseRPC drops a lease held by subject on id.
func releaseRPC(ctx context.Context, in *client.SourceLeaseIn) (*client.SourceReleasedOut, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, zip.ErrUnauthorized("release: no org on the call")
	}
	id, subject, lease := strings.TrimSpace(in.ID), strings.TrimSpace(in.Subject), strings.TrimSpace(in.Lease)
	if id == "" || subject == "" || lease == "" {
		return nil, zip.ErrBadRequest("release: id, subject and lease are required")
	}
	if strings.HasPrefix(subject, appSubject) {
		return nil, zip.ErrForbidden("sources belong to a person: a machine token cannot release one")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "source: not mounted")
	}
	o := ops{s: mounted}
	sto, err := o.open(org)
	if err != nil {
		return nil, err
	}
	out, err := o.releaseFor(ctx, sto, subject, &SourceLease{ID: id, Lease: lease})
	if err != nil {
		return nil, err
	}
	return &client.SourceReleasedOut{Lease: out.Lease, Released: out.Released}, nil
}
