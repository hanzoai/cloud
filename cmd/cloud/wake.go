package main

import (
	"context"
	"net"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// wake.go — the SECOND entry point to a lazy app, and the only one an internal call
// can use.
//
// A lazy plugin has exactly one trigger: a request reaching one of its prefixes.
// That is what makes 112 services affordable, and it is also why the internal
// plane was inert for all of them. A plane call goes straight to the app's
// canonical socket (cloud.Ask / zip.DialApp) and never touches this router — so
// nothing started the child, nothing bound the socket, and the caller dialled a
// path that does not exist. The x402 rail is the case that forced it: settling a
// tool call means tools→x402→marketplace→wallets→commerce, five processes, four
// of them cold, none of them reachable by HTTP from inside the surface.
//
// The host is the ONLY process that can fix that: it holds the manifest and the
// child table. zip already exposes the operation (App.Start — idempotent, and it
// goes through the same single-flighted path a prefix request takes, so a burst
// of first callers still produces one child). This publishes it.
//
// It is a plane endpoint but NOT an app's plane: the host has no manifest row,
// no Mount, no Deps and no tenant, so it builds its own one-op app rather than
// reaching for cloud.Plane() — which would pull the entire surface's package graph
// into a router that deliberately links none of it. One op, one socket, one
// import of the leaf both halves already share.
//
// # The AGENT MCP SERVER is on this socket too, and for the same reason
//
// A subsystem cannot enumerate its siblings — a plugin is a process, and
// MCPTools() is in-process — so an agent run inside `agents` could resolve its
// declared tool names against nothing but its own registry, which in the split
// surface holds what `agents` itself registered and no more. The thing that DOES
// aggregate already exists and is already mounted: client.MCP, at
// api.hanzo.ai/v1/mcp. The only part it was missing was an address reachable
// from inside.
//
// It is the SAME MCP object, not a second one. So an agent gets the same union
// its external MCP clients get, ordered by the same rank and — the part that
// matters — narrowed by the same [surface] curation rule, which is applied inside
// gather where the routing table is written. A tool the MCP server will not project
// to Slack is not routable for an agent either, and both hear the same -32602.

// serveWake opens the router's start endpoint at client.HostApp's socket.
//
// It returns NOTHING and takes its teardown from the app's own shutdown hooks, on
// purpose. The version that returned a stop function was called
// `defer func() { _ = serveWake(app)() }()`, which evaluates serveWake at defer-RUN
// time — so the endpoint was opened during shutdown, for an instant, and was never
// open while the surface served. Every test passed, because they called serveWake
// directly. An API with no handle cannot be deferred into never happening.
//
// A bind failure is NOT fatal: the surface still routes and still serves every HTTP
// prefix without this endpoint. What it loses is internal calls to cold apps. It is
// logged where the socket is named, so the degradation is visible rather than
// inferred — and it does not return until the socket ACCEPTS, so "listening" in the
// log is a fact rather than an intention.
func serveWake(app *zip.App, mcp *client.MCP) {
	host := zip.New(zip.Config{AppName: "plane", Logger: app.Logger()})

	// The surface's agent MCP server, at its OWN address (manifest.MCPPath) on this
	// socket. One name for one MCP server across both transports: over HTTP it is the
	// edge's /v1/mcp, over ZAP it is the surface's own. Nothing here re-aggregates
	// and nothing here filters — [client.MCP.Serve] publishes the object main.go
	// already built.
	//
	// It forwards INSIDE, which is the whole of what this address adds. A caller
	// on this socket is a sibling with a principal it resolved server-side and no
	// bearer to replay for it; sent into a subsystem's edge endpoint, its statement
	// is deleted by the identity boundary and every org-scoped tool refuses it. Sent
	// into the subsystem's plane endpoint it is read as what it is — see cloud.UseMCP.
	// The edge's own mount is untouched and still forwards to the edge.
	mcp.Serve(host, manifest.MCPPath, inside(app))

	zip.Post[client.StartIn, client.Started](host, "/host/start",
		func(_ context.Context, in *client.StartIn) (*client.Started, error) {
			// No tenancy check, because there is no tenant: starting a process
			// reads nobody's books and returns nobody's data. The boundary is the
			// socket — 0700 in the surface's own run dir, reachable only by the
			// children this host spawned.
			addr, err := app.Start(in.App)
			if err != nil {
				// "This surface does not run that app" is an ANSWER and it goes back
				// as one — a 200 saying Known=false — because it is the single fact
				// a caller is allowed to read as "nothing is priced here". As a 404
				// it was indistinguishable from zip's own "unknown op" 404, which is
				// exactly what a host pod on an older build answers, so a rolling
				// deploy would have read a version skew as a deployment fact and
				// given away every priced tool for the length of the window.
				//
				// Anything else is a deployed app that would not start: an outage,
				// and an outage must fail the call rather than describe the surface.
				if isUnknownApp(app, in.App) {
					return &client.Started{}, nil
				}
				return nil, zip.Errorf(503, "start %s: %v", in.App, err)
			}
			return &client.Started{Addr: addr, Known: true}, nil
		},
		zip.WithOperationID(client.HostStart),
		zip.WithSummary("Start one lazily-composed app"))

	// done is CLOSED when Listen returns, rather than carrying the error itself: the
	// wait below has to observe the same event, and a one-value channel would let
	// whichever side read first starve the other — here, a failed bind consumed by
	// the wait would hang shutdown forever on a value that had already been taken.
	// A closed channel broadcasts.
	// Bind the SHARED runtime dir first. This router links the plane leaf and not the
	// surface, so it cannot reach cloud's binder — and without binding, zip resolved the
	// start endpoint to a private temp path. The endpoint then existed nowhere any
	// child looked, so waking a lazy app failed with "this process runs under a router
	// whose start socket is not there" and every call to a not-yet-started app was
	// unreachable.
	client.BindRuntimeDir()
	path := zip.SocketPath(client.HostApp)
	done := make(chan struct{})
	var listenErr error
	go func() { listenErr = host.Listen(path); close(done) }()
	app.OnShutdown(func(context.Context) error {
		if err := host.Shutdown(); err != nil {
			return err
		}
		<-done // Listen has returned; the socket is released
		return nil
	})

	// Wait for it to ACCEPT, and watch the listener while waiting — a stale socket
	// file fails Listen instantly with "address already in use", and a poll that only
	// looked at the path would spend its whole budget waiting for a listener that
	// already gave up. (cloud.ServePlane does the same for an app's own socket; it is
	// not shared because this router must not link the surface's package graph to bind
	// one socket.)
	deadline := time.Now().Add(endpointBindWait)
	for {
		select {
		case <-done:
			app.Logger().Error("surface start endpoint is not listening — an internal call cannot wake a cold app",
				"sock", path, "err", listenErr)
			return
		default:
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			app.Logger().Info("surface start endpoint listening", "sock", path)
			return
		}
		if time.Now().After(deadline) {
			app.Logger().Error("surface start endpoint did not bind — an internal call cannot wake a cold app",
				"sock", path, "waited", endpointBindWait)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// endpointBindWait bounds how long boot waits for the start endpoint's socket.
const endpointBindWait = 5 * time.Second

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
