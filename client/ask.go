// Copyright © 2026 Hanzo AI. MIT License.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	zapmcp "github.com/zap-proto/mcp"
	"github.com/zap-proto/zip"
)

// At resolves one app to the ENDPOINT it answers on — the address, and the path
// on it — starting the app if it is cold.
//
// It is a function rather than a *zip.App because there are two ways an app is
// reachable and only one of them is a child of this host: zip.App.Start covers
// the spawned ones, and a deployment that points CLOUD_<NAME>_ADDR at an instance
// running elsewhere is mounted, never started, so Start does not know it. The
// composition root holds both facts (cmd/cloud), and this package holds neither.
//
// It answers the PATH too, because a subsystem serves its surface at two
// addresses and they are not interchangeable. Its EDGE address sits behind the
// identity boundary every public request must pass, which deletes any authority
// header a caller wrote and re-mints one only from a credential it verified; its
// PLANE address sits on the canonical socket no edge route reaches, where a
// caller's identity is its own statement, trusted exactly as far as that socket
// makes it (cloud.UseMCP). Which of the two a caller may be forwarded to is a
// property of where that caller reached THIS host, and the composition root is
// the only thing that knows both — so it says, rather than the dispatch
// assuming.
type At func(app string) (addr, path string, err error)

// Answer is one subsystem's reply to one question, or the reason there is none.
// Exactly one of Body and Err is meaningful.
type Answer struct {
	// App is the subsystem asked, as the manifest names it.
	App string
	// Body is what its own endpoint replied, verbatim.
	Body []byte
	// Err is why there is no reply: it would not start, or it did not answer.
	Err error
}

// Ask puts req to every named app's own endpoint at path, in PARALLEL, and
// returns one Answer per app in the order asked.
//
// req is the CALLER's own request and is copied per child rather than rebuilt,
// so a child answers as itself for the caller who asked: its headers ride along,
// which is how a subsystem whose tools depend on the tenant (the tool plane's
// connectors, skills and enabled servers) contributes rows no projection could
// have held. That is the mechanism zip already used for its single "open"
// plugin, applied to every app, which is what makes the one-open-plugin rule
// unnecessary.
//
// Order is the order given — the manifest's mount order, which is the surface's
// routing order — so a caller that resolves a collision by taking the first
// resolves it the way the router would.
func Ask(ctx context.Context, at At, apps []string, req *fasthttp.Request) []Answer {
	out := make([]Answer, len(apps))
	var wg sync.WaitGroup
	for i, name := range apps {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			out[i] = ask(ctx, at, name, req)
		}(i, name)
	}
	wg.Wait()
	return out
}

// ask is one hop: resolve, copy the caller's request onto the child's own path,
// and read the reply.
//
// The failure is returned, never logged-and-dropped. "This app would not start"
// and "this app serves nothing" are different answers and the difference is the
// whole point of this package.
//
// A PEER THIS PROCESS ALREADY SERVES IS REACHED IN MEMORY. zip.Serving is a fact
// about the process — something in this program bound that app's socket — so a
// co-resident subsystem costs no dial and no syscall: the request runs against
// the router that socket serves (serveHere).
//
// It is the SAME ENDPOINT the socket would have reached, at the path [At]
// resolved, and not the served app's own App.MCP. The two differ for exactly the
// endpoint an inside caller is sent to: a subsystem's plane socket is served by
// the process's plane app, and its MCP route there (cloud.UseMCP) answers with
// the SUBSYSTEM's tools. The plane app's own App.MCP holds plane ops, so an
// in-memory hop that called it answered every tool the subsystem serves with
// "unknown tool" — the socket, reaching the route, never did.
func ask(_ context.Context, at At, name string, req *fasthttp.Request) Answer {
	addr, path, err := at(name)
	if err != nil {
		return Answer{App: name, Err: err}
	}
	do := clientFor(addr).Do
	if a := zip.Serving(name); a != nil {
		do = func(r *fasthttp.Request, resp *fasthttp.Response) error { return serveHere(a, r, resp) }
	}
	return hop(name, path, req, do)
}

// serveHere runs r against a's own live router, in this process, into resp.
//
// zip.Serving is what makes this safe: an app is only there because something in
// this program BOUND its socket, so its router has already been prepared and this
// is the same handler the socket path would have reached. It is not App.Test,
// which prepares an app of its own and can therefore answer for routes the served
// one does not have.
//
// Nothing about identity is special here: r carries the caller's headers, and the
// handler reads them exactly as it does off the wire. The socket path's whole
// contribution was to copy those bytes through a syscall.
func serveHere(a *zip.App, r *fasthttp.Request, resp *fasthttp.Response) error {
	var rc fasthttp.RequestCtx
	rc.Init(r, localAddr{}, nil)
	a.Fiber().Handler()(&rc)
	rc.Response.CopyTo(resp)
	return nil
}

// ServeHere is [serveHere] answering a fresh response, exported for the tests
// that prove a co-resident hop never leaves the process.
func ServeHere(a *zip.App, r *fasthttp.Request) (*fasthttp.Response, error) {
	resp := fasthttp.AcquireResponse()
	return resp, serveHere(a, r, resp)
}

// localAddr names the peer of a hop that never left the process. A handler that
// logs its client, or rate-limits on one, sees a loopback address rather than the
// empty string an uninitialised RequestCtx would hand it.
type localAddr struct{}

func (localAddr) Network() string { return "unix" }
func (localAddr) String() string  { return "@here" }

// hop puts req to one child's endpoint at path, through do, and reads the reply.
//
// The two endpoints [At] resolves speak different wires, and the path says
// which. A subsystem's PLANE endpoint (cloud.UseMCP) is a hop between our own
// processes and carries the message as a ZAP frame; its FRAMEWORK endpoint is
// zip's own HTTP adapter and carries JSON-RPC, which is what an MCP client
// sends and what a remotely mounted app is reached with. Either way this
// package's callers hold JSON, so the plane hop transcodes at both ends —
// whether do crosses a socket or stays in this process.
func hop(name, path string, req *fasthttp.Request, do func(*fasthttp.Request, *fasthttp.Response) error) Answer {
	r := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(r)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	req.CopyTo(r)
	// The child's own address, on the child's own path. Host is the app name so a
	// child's logs and its caller-attribution name the asker's target rather than
	// the socket path.
	r.SetHost(name)
	r.URI().SetPath(path)
	frames := path == manifest.MCPPath
	if frames {
		body, err := frame(r.Body())
		if err != nil {
			return Answer{App: name, Err: fmt.Errorf("%s: %w", name, err)}
		}
		r.SetBody(body)
		r.Header.SetContentType(zip.CallContentType)
	}
	if err := do(r, resp); err != nil {
		return Answer{App: name, Err: fmt.Errorf("%s: %w", name, err)}
	}
	if code := resp.StatusCode(); code < 200 || code > 299 {
		return Answer{App: name, Err: fmt.Errorf("%s answered %d for %s", name, code, path)}
	}
	if frames {
		body, err := unframe(resp.Body())
		if err != nil {
			return Answer{App: name, Err: fmt.Errorf("%s: %w", name, err)}
		}
		return Answer{App: name, Body: body}
	}
	return Answer{App: name, Body: append([]byte(nil), resp.Body()...)}
}

// frame renders a JSON-RPC message as the ZAP bytes a plane endpoint reads.
func frame(b []byte) ([]byte, error) {
	var f zapmcp.Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return zapmcp.Marshal(&f)
}

// unframe reads a plane endpoint's answer back as the JSON-RPC every caller of
// this package holds. No bytes is a notification, which has no answer to render.
func unframe(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var f zapmcp.Frame
	if err := zapmcp.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return json.Marshal(&f)
}

// clients is one pooled transport per ADDRESS, for the same reason zip keeps one
// Conn per peer name (zip ask.go): a transport holds a connection pool, so
// dialing per ask turns every hop into a fresh connect. Keyed by address rather
// than by name because a reloaded child gets a new socket and must not be
// reached through the old one's pool.
var clients sync.Map // addr -> *zaphttp.Transport

func clientFor(addr string) *zaphttp.Transport {
	if c, ok := clients.Load(addr); ok {
		return c.(*zaphttp.Transport)
	}
	// A tools/call RUNS the operation, and a completion outlasts the transport's
	// 30s default: the model answers, is paid for, and the hop has already hung
	// up. plane.ReadCeiling is the one answer to how long a call between our own
	// processes may take (sdk/plane).
	t := zaphttp.Dial(plane.Network(addr), addr)
	t.SetReadTimeout(plane.ReadCeiling)
	c, _ := clients.LoadOrStore(addr, t)
	return c.(*zaphttp.Transport)
}
