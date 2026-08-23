// Copyright © 2026 Hanzo AI. MIT License.

// Package fleet is how the light host answers a question about a subsystem: it
// ASKS the subsystem.
//
// The host links no subsystem (cmd/cloud), so it cannot read a registry it does
// not hold. For a long time it read a COMMITTED PROJECTION instead —
// plugin/<app>/mcp.json, the tool array each app's binary wrote when it was
// built, embedded into the host and handed to zip as Plugin.Tools. That file was
// a second source for a fact the child already knows, and a second source can
// only be stale or accidentally correct. It was stale: plugin/o11y/mcp.json held
// 12 tools while the o11y binary at the same commit served 365, because the 353
// missing ops live in github.com/hanzoai/o11y and a dependency bump in another
// repository invalidated an artifact in this one with nothing in the diff to say
// so.
//
// Regenerating that file more often does not fix it. A generator on a hook is
// still two sources with a race between them, and the trigger here is in a
// different repository, so no hook in this one can see it. The fix is that the
// file stops existing and the host asks.
//
// # What asking costs, and why it is affordable
//
// A child answers on its OWN MCP server — zip's default /mcp, which cloud.Serve
// deliberately leaves where the framework puts it (manifest.FrameworkMCPPath),
// over the private ZAP socket the host started it on; or, for a caller that
// reached this host from INSIDE, the same MCP server on the app's own plane
// socket (manifest.MCPPath, cloud.Door). [At] resolves the name to whichever of
// the two that caller may use, and zip.App.Start is what makes either reachable:
// idempotent, and the SAME single-flighted path a request to the app's prefix
// takes, so a burst of concurrent askers still produces one child.
//
// So the first ask of a cold app pays that app's start. That is the cost of the
// answer being true, and it is paid once per app per host: the child stays up
// afterwards, and every later ask is a unix round trip. Nothing is cached
// between requests, because a cache of a catalogue IS the file this package
// exists to delete.
//
// # A subsystem that does not answer is REPORTED
//
// [Answer.Err] is never swallowed. A catalogue that silently drops the app it
// could not reach is indistinguishable from one whose app serves nothing, and
// those are the same defect the stale file was: the caller cannot tell. Every
// caller here reports its failures by name — see [Door] for the wire shape.
package fleet

import (
	"fmt"
	"strings"
	"sync"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
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
// makes it (cloud.Door). Which of the two a caller may be forwarded to is a
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
// Order is the order given — the manifest's mount order, which is the fleet's
// routing order — so a caller that resolves a collision by taking the first
// resolves it the way the router would.
func Ask(at At, apps []string, req *fasthttp.Request) []Answer {
	out := make([]Answer, len(apps))
	var wg sync.WaitGroup
	for i, name := range apps {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			out[i] = ask(at, name, req)
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
func ask(at At, name string, req *fasthttp.Request) Answer {
	addr, path, err := at(name)
	if err != nil {
		return Answer{App: name, Err: err}
	}
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
	if err := clientFor(addr).Do(r, resp); err != nil {
		return Answer{App: name, Err: fmt.Errorf("%s at %s: %w", name, addr, err)}
	}
	if code := resp.StatusCode(); code < 200 || code > 299 {
		return Answer{App: name, Err: fmt.Errorf("%s answered %d for %s", name, code, path)}
	}
	return Answer{App: name, Body: append([]byte(nil), resp.Body()...)}
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
	c, _ := clients.LoadOrStore(addr, zaphttp.Dial(network(addr), addr))
	return c.(*zaphttp.Transport)
}

// network reads the plumbing off the address shape, the same rule zip's
// transport registry applies (zip networkOf): a filesystem path is a unix
// socket, anything else is host:port. A child of this host is always the former
// — zip starts it on a private socket — and a CLOUD_<NAME>_ADDR mount may be
// either.
func network(addr string) string {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "@") {
		return "unix"
	}
	return "tcp"
}
