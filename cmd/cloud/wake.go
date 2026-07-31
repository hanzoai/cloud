package main

import (
	"context"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// wake.go — the SECOND door onto a lazy app, and the only one an internal call
// can use.
//
// A lazy plugin has exactly one trigger: a request reaching one of its prefixes.
// That is what makes 112 services affordable, and it is also why the internal
// plane was inert for all of them. A plane call goes straight to the app's
// canonical socket (cloud.Peer / zip.DialApp) and never touches this router — so
// nothing started the child, nothing bound the socket, and the caller dialled a
// path that does not exist. The x402 rail is the case that forced it: settling a
// tool call means tools→x402→marketplace→wallets→commerce, five processes, four
// of them cold, none of them reachable by HTTP from inside the fleet.
//
// The host is the ONLY process that can fix that: it holds the manifest and the
// child table. zip already exposes the operation (App.Start — idempotent, and it
// goes through the same single-flighted path a prefix request takes, so a burst
// of first callers still produces one child). This publishes it.
//
// It is a plane door but NOT an app's plane: the host has no manifest row, no
// Mount, no Deps and no tenant, so it builds its own one-op app rather than
// reaching for cloud.Plane() — which would pull the entire fleet's package graph
// into a router that deliberately links none of it. One op, one socket, one
// import of the leaf both halves already share.

// serveWake binds the router's start door at plane.HostApp's socket and returns the
// stop for it.
//
// A bind failure is NOT fatal and is not returned: the fleet still routes and still
// serves every HTTP prefix without this door. What it loses is internal calls to cold
// apps, and the caller's answer to that — ErrNoPeer, "not deployed here" — is already
// the right one for a fleet that has no router. It is logged where the socket is
// named, so the degradation is visible rather than inferred.
func serveWake(app *zip.App) func() error {
	door := zip.New(zip.Config{AppName: "plane", Logger: app.Logger()})

	zip.Post[plane.StartIn, plane.Started](door, "/host/start",
		func(_ context.Context, in *plane.StartIn) (*plane.Started, error) {
			// No tenancy check, because there is no tenant: starting a process
			// reads nobody's books and returns nobody's data. The boundary is the
			// socket — 0700 in the fleet's own run dir, reachable only by the
			// children this host spawned.
			addr, err := app.Start(in.App)
			if err != nil {
				// zip says "no plugin named X" for a name this fleet does not run.
				// That is a 404 and it is an ANSWER — the caller reads it as "not
				// deployed here" and falls back. Anything else is a deployed app
				// that would not start, which is an outage and must not be
				// mistaken for an absence.
				if isUnknownApp(app, in.App) {
					return nil, zip.ErrNotFound("no app named " + in.App + " in this fleet")
				}
				return nil, zip.Errorf(503, "start %s: %v", in.App, err)
			}
			return &plane.Started{Addr: addr}, nil
		},
		zip.WithOperationID(plane.HostStart),
		zip.WithSummary("Start one lazily-mounted app"))

	path := zip.SocketPath(plane.HostApp)
	errs := make(chan error, 1)
	go func() {
		if err := door.Listen(path); err != nil {
			app.Logger().Error("fleet start door is not listening — internal calls cannot wake a cold app",
				"sock", path, "err", err)
		}
		errs <- nil
	}()
	app.Logger().Info("fleet start door listening", "sock", path)
	return func() error {
		if err := door.Shutdown(); err != nil {
			return err
		}
		<-errs
		return nil
	}
}

// isUnknownApp reports whether name is absent from this host's plugin table.
//
// It is asked of the table rather than matched on the error text, because the two
// failures Start reports — "there is no such app" and "the app would not start" —
// have to be told apart by the caller and a string comparison is not a fact. The
// table is the fact.
func isUnknownApp(app *zip.App, name string) bool {
	for _, p := range app.Plugins() {
		if p.Name == name {
			return false
		}
	}
	return true
}
