// Copyright © 2026 Hanzo AI. MIT License.

package integrations

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// Whether an org has connected a provider, published on the internal plane.
//
// This package owns the connection store, and every other app is a different
// PROCESS. The answer used to be an exported function reading a package-level
// handle to the mounted service, which is only correct inside this process — a
// caller anywhere else got a silent "not connected" for a workspace that was
// plainly connected, with no error and no log to say the question never really
// arrived. It is measured: /v1/integrations reported the Slack workspace, its team
// id and nine scopes, while /v1/channels reported connected:false for that same
// install and refused every send.
//
// A question that crosses a process boundary has to look like one. So it is an op,
// and the in-process accessors it replaces are unexported — a footgun removed is
// better than a footgun documented.

// exposeConnection publishes the connection read. Mount calls it.
func exposeConnection() {
	zip.Post[plane.ConnectionIn, plane.Connection](cloud.Plane(), "/integrations/connection",
		planeConnection,
		zip.WithOperationID(plane.IntegrationsConnection),
		zip.WithSummary("Whether the caller's org has connected a provider"))
}

// planeConnection answers for the CALLER'S OWN org, which is read from the peer
// context and can never be named in an argument — an org a caller could pass is an
// org whose connections a caller could read. A named function, not a closure, so
// zipdoc lifts this prose into the registry.
//
// A provider is connected when ANY of its accounts is, and the ORG-held account is
// the one described: a caller acting for the org acts as the WORKSPACE, never as
// some member's personal link. Nothing secret crosses — an app that must ACT on a
// connection asks this package to act instead of fetching the credential.
func planeConnection(ctx context.Context, in *plane.ConnectionIn) (*plane.Connection, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("connection: no org on the call")
	}
	if in == nil || in.Provider == "" {
		return nil, zip.ErrBadRequest("connection: provider is required")
	}
	if mounted == nil {
		return nil, zip.Errorf(503, "connection: integrations is not serving")
	}
	conns, err := mounted.State.store.ListFor(ctx, org, in.Provider)
	if err != nil {
		return nil, err
	}
	c, ok := orgHeld(conns)
	if !ok {
		return &plane.Connection{}, nil
	}
	return &plane.Connection{
		Connected:    true,
		Account:      c.ExternalID,
		AccountLabel: c.AccountLabel,
		Scopes:       c.Scopes,
	}, nil
}

// orgHeld picks the account an org acts as: the one no person owns, else the
// first. Pure, so the choice is testable without a serving app, and stated once
// so two callers cannot disagree about which account "the org's" means.
func orgHeld(conns []Connection) (Connection, bool) {
	for _, c := range conns {
		if c.User == "" {
			return c, true
		}
	}
	if len(conns) > 0 {
		return conns[0], true
	}
	return Connection{}, false
}
