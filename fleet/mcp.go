// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/hanzoai/cloud/manifest"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// The fleet's ONE agent door, composed AT THE MOMENT OF ASKING.
//
// zip serves this door for an app out of that app's own typed-op registry, plus
// whatever build-time catalogues a host handed it. The light host has neither: it
// registers no op of its own, and the catalogues are gone. What it has is
// CHILDREN, and a door whose whole content is "what my children serve right now"
// is a different mechanism from "what I was compiled knowing", so the host owns
// it here rather than configuring zip's (cmd/cloud disables that one, so there is
// exactly one handler at this address and not two chained by the router).
//
// Every method below is answered from the children. Nothing is remembered
// between requests except which app listed a tool name, and that is a routing
// table, not a catalogue: see [Door.owner].

// protocolVersion is the MCP spec revision this door speaks. It is zip's
// (mcpProtocolVersion) — the children answer initialize with the same string,
// and a host that claimed a different one would be describing a protocol none of
// its subsystems implement.
const protocolVersion = "2025-06-18"

// Unavailable is the _meta key under which tools/list names the subsystems it
// could not ask.
//
// It exists because a short list and a stale file are the SAME defect: the
// caller cannot tell a subsystem that serves nothing from one that did not
// answer, so it reads a partial catalogue as a complete one. MCP puts extension
// data on the result's _meta, so the outage travels with the answer it qualifies
// and a client that only reads `tools` still gets every tool that exists —
// blanking a working fleet because one app is down would be a worse answer than
// a shorter list, but an UNANNOUNCED shorter list is worse than both.
const Unavailable = "hanzo.ai/unavailable"

// Outage is one subsystem that could not be asked, and why.
type Outage struct {
	App   string `json:"app"`
	Error string `json:"error"`
}

// Door is the composed agent door: the apps it fronts, how to reach one, and the
// tool→app routing it learned from the last time it asked.
type Door struct {
	host *zip.App
	at   At
	apps []string

	// owner is tool name → the app that LISTED it, written by every gather and
	// read by tools/call. It is not a catalogue and cannot go stale in a way that
	// matters: the host only ever NAMES an app to forward to, the child's own
	// registry decides whether the tool exists, and a name it no longer serves
	// yields that child's own -32602 rather than a mis-dispatch. A name nobody
	// listed is discovered by asking, not by guessing.
	mu    sync.RWMutex
	owner map[string]string
}

// Mount serves the fleet's agent door at path, over apps, reaching one with at.
//
// apps is the deployment's COMPOSED set (cmd/cloud's `composed`), never the whole
// manifest: a deployment that does not run a subsystem must not offer its tools,
// for the same reason it must not publish its routes.
//
// It also signposts the address the door LEFT, and that belongs here rather than
// in the console's terminal handler, which is where it used to live. The console
// answered an unclaimed [manifest.FrameworkMCPPath] with "the door moved" on the
// reasoning that a plugin serving its own door there would match a real route and
// never reach it. Untrue: zip mounts that route only when the app has something
// to expose (installMCP), so a plugin whose typed ops live on the internal plane
// — kms, whose four secret ops are on cloud.Plane() precisely so no route runs
// from the edge to a secret — has NO route at its own door, fell through to the
// console, and was told its door was at an address only a host serves. Measured:
// POST /mcp -> 308, then POST /v1/mcp -> 404, and the fleet reads that non-2xx as
// an outage for a child that is serving fine.
//
// A signpost is only true where the door actually moved, and this is the one
// place that knows it did — the same call that registers the target names it.
func Mount(host *zip.App, path string, apps []string, at At) *Door {
	d := &Door{host: host, at: at, apps: apps, owner: map[string]string{}}
	d.Serve(host, path)
	if path != manifest.FrameworkMCPPath {
		host.All(manifest.FrameworkMCPPath, signpost(path))
	}
	return d
}

// Serve publishes THIS door at another address — the same gather, the same
// routing table, the same [refuse] gate.
//
// It exists because the fleet's own subsystems need the door too, and the
// address a subsystem can reach is not the edge's. An agent inside `agents`
// that asked api.hanzo.ai for its tools would leave the fleet, re-enter through
// the front door and arrive back one process away carrying whatever credential
// it could find; the host's internal socket is one hop with no edge on it (see
// cmd/cloud/wake.go, which is the one caller).
//
// A SECOND Door over the same children would be a second routing table and,
// worse, a second place the curation rule could be applied — or forgotten. This
// is the same object reached from another direction: one aggregation, one
// policy, one owner map. Which is also why an internal caller cannot be offered
// a wider surface than an external one: there is no wider surface to offer.
func (d *Door) Serve(on *zip.App, path string) { on.Post(path, d.serve) }

// signpost answers the framework default on a host that moved its door.
//
// 308 preserves method AND body, so a POSTed initialize or tools/list arrives at
// the real door instead of being retried as a GET or answered with the console
// shell. The body is JSON for the same reason the hop exists at all: a caller who
// reads bytes rather than following it must never get HTML here. It carries no
// tool list — a signpost is not a second door.
func signpost(door string) zip.Handler {
	body := map[string]string{"error": "the MCP door moved", "door": door}
	return func(c *zip.Ctx) error {
		c.SetHeader("Location", door)
		return c.JSON(http.StatusPermanentRedirect, body)
	}
}

// message is one JSON-RPC 2.0 envelope, in the shape this door reads it.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (d *Door) serve(c *zip.Ctx) error {
	var req message
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.JSON(200, rpcErr(nil, -32700, "parse error"))
	}
	switch req.Method {
	case "initialize":
		return c.JSON(200, rpcResult(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "cloud", "version": protocolVersion},
		}))
	case "tools/list":
		return d.list(c, req)
	case "tools/call":
		return d.call(c, req)
	case "ping":
		return c.JSON(200, rpcResult(req.ID, map[string]any{}))
	default:
		// notifications/* carry no id and expect no result — ack with 202.
		if len(req.ID) == 0 {
			return c.Status(202).JSON(202, map[string]any{})
		}
		return c.JSON(200, rpcErr(req.ID, -32601, "method not found: "+req.Method))
	}
}

// list answers tools/list from the subsystems themselves, and NAMES the ones it
// could not reach.
func (d *Door) list(c *zip.Ctx, req message) error {
	tools, down, held := d.gather(c)
	// ONE TOOL PER SUBSYSTEM, the operation carried in an argument. The flat
	// projection was 1,189 tools in 977 KB, which no model holds and every client
	// truncates. See fleet/grouped.go.
	result := map[string]any{"tools": group(tools)}
	meta := map[string]any{}
	if held > 0 {
		// The same obligation as Unavailable, for a different cause: a list
		// shortened by POLICY must say so too. See fleet/surface.go.
		meta[Refused] = map[string]any{"count": held, "rule": TheRule}
	}
	if len(down) > 0 {
		meta[Unavailable] = down
		d.host.Logger().Warn("fleet mcp: tools/list is INCOMPLETE — subsystems did not answer",
			"unavailable", len(down), "apps", len(d.apps), "tools", len(tools))
	}
	if len(meta) > 0 {
		result["_meta"] = meta
	}
	return c.JSON(200, rpcResult(req.ID, result))
}

// call runs one tool: find the app that lists the name, hand it the caller's own
// message, and return that app's own reply verbatim.
//
// A name nobody has listed in this process yet costs ONE discovery — the same
// ask tools/list makes — rather than a guess or a fan-out per call. If it is
// still nobody's, that is a -32602 and not an outage: every app answered, and
// none of them serves it.
func (d *Door) call(c *zip.Ctx, req message) error {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)

	// [Describe] is the door's OWN tool — the fetch half of a surface whose enums
	// carry names and no schemas — so it is answered here rather than routed.
	if p.Name == Describe {
		return d.describe(c, req, p.Arguments)
	}

	// A subsystem tool is an ENVELOPE over one operation. Unwrapping it yields
	// exactly the name and message a direct call carries, so everything below is
	// ONE dispatch for both spellings: the same routing table, the same gate on
	// the way into it, the same hop, the same reply.
	msg := c.Fiber().Request().Body()
	if composed(p.Name) {
		op, body, ok := unwrap(req.ID, p.Arguments)
		if !ok {
			return c.JSON(200, rpcErr(req.ID, -32602, p.Name+` needs {"op":"<operation>","input":{}}`))
		}
		p.Name, msg = op, body
	}

	app := d.ownerOf(p.Name)
	if app == "" {
		d.gather(c)
		app = d.ownerOf(p.Name)
	}
	if app == "" {
		// Either nobody serves it, or refuse() withheld it — and the caller gets
		// the same answer for both. Telling a client which of the two it hit would
		// turn the door into an oracle for the identity surface it just declined
		// to expose.
		return c.JSON(200, rpcErr(req.ID, -32602, "unknown tool: "+p.Name))
	}
	// The caller's own REQUEST — its headers, so identity propagates — carrying
	// msg, which for a direct call is the caller's own body byte for byte and for
	// an envelope is that same call spelled out. The child's registry invokes it,
	// so the host can only ever name a tool and never invoke one the child did
	// not declare.
	hop := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(hop)
	c.Fiber().Request().CopyTo(hop)
	hop.SetBody(msg)
	ans := Ask(d.at, []string{app}, hop, manifest.FrameworkMCPPath)[0]
	if ans.Err != nil {
		// A hop failure is MCP isError content, per the spec: the model reads "this
		// tool is not available right now" and reacts, where a 503 body is a
		// transport failure it cannot interpret.
		return c.JSON(200, rpcResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": ans.Err.Error()}},
			"isError": true,
		}))
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(200, ans.Body)
}

// named is one tool with its name and its OWNER lifted out, so the composed list
// sorts and groups without re-parsing and each descriptor is carried VERBATIM —
// the bytes the child's own registry projected, never a re-encoding.
type named struct {
	app  string
	name string
	raw  json.RawMessage
}

// gather asks every app what it serves, right now, and returns the PROJECTABLE
// union, the outages, and how many tools policy withheld.
//
// The request it sends is the CALLER's, with the body replaced by a canonical
// tools/list: the headers ride along, so a child whose tools depend on who is
// asking answers for this caller, while the body cannot be a tools/call the
// discovery path would otherwise execute on every child in the fleet.
//
// This is also where the tool surface is GATED, and it is the only place, on
// purpose. The routing table [Door.owner] is written here and nowhere else, so a
// name that refuse() rejects is never written, is never routable, and a
// tools/call naming it gets the same -32602 as a tool that does not exist —
// including from a client that cached the name before the rule existed. A filter
// applied in list() instead would have been a suggestion.
//
// It returns the tools themselves rather than their bytes because every caller
// needs the OWNER too: list() groups by it (fleet/grouped.go) and describe()
// answers out of the same gated set.
func (d *Door) gather(c *zip.Ctx) ([]named, []Outage, int) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	c.Fiber().Request().CopyTo(req)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBody([]byte(`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`))

	var all []named
	var down []Outage
	held := 0
	owner := map[string]string{}
	for _, a := range Ask(d.at, d.apps, req, manifest.FrameworkMCPPath) {
		if a.Err != nil {
			down = append(down, Outage{App: a.App, Error: a.Err.Error()})
			continue
		}
		tools, err := toolsOf(a.Body)
		if err != nil {
			down = append(down, Outage{App: a.App, Error: err.Error()})
			continue
		}
		for _, t := range tools {
			// The gate, before the routing table. See fleet/surface.go.
			if refuse(t.name) {
				held++
				continue
			}
			// One name is one dispatch, so two owners would make it unroutable. The
			// manifest's order is the router's order, so the first claimant wins here
			// exactly as it wins a prefix — and the loser is logged rather than
			// silently dropped, because a tool that vanished into a collision looks
			// identical to one that was never declared.
			if held, dup := owner[t.name]; dup {
				d.host.Logger().Warn("fleet mcp: two subsystems claim one tool name; the first in mount order serves it",
					"tool", t.name, "serving", held, "shadowed", a.App)
				continue
			}
			owner[t.name] = a.App
			t.app = a.App
			all = append(all, t)
		}
	}
	// Product surface FIRST, then alphabetical — because clients truncate, and a
	// list sorted only by name put 128 o11y console ops in front of every product
	// tool the fleet has. rank() states the mechanism; nothing is hidden by it.
	sort.Slice(all, func(i, j int) bool {
		ri, rj := rank(all[i].name), rank(all[j].name)
		if ri != rj {
			return ri < rj
		}
		return all[i].name < all[j].name
	})

	d.mu.Lock()
	d.owner = owner
	d.mu.Unlock()

	return all, down, held
}

func (d *Door) ownerOf(tool string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.owner[tool]
}

// toolsOf lifts the descriptors out of one child's tools/list reply.
//
// A JSON-RPC ERROR reply is an outage and is reported as one: the child is up,
// but it did not answer the question, and folding that into "this app has no
// tools" is the silent shortening this package exists to remove.
func toolsOf(body []byte) ([]named, error) {
	var env struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, jsonrpcError(env.Error.Message)
	}
	out := make([]named, 0, len(env.Result.Tools))
	for _, raw := range env.Result.Tools {
		var hdr struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Name == "" {
			return nil, errNamelessTool
		}
		out = append(out, named{name: hdr.Name, raw: raw})
	}
	return out, nil
}

type jsonrpcError string

func (e jsonrpcError) Error() string { return "tools/list refused: " + string(e) }

const errNamelessTool = jsonrpcError("a tool descriptor has no name")

func rpcResult(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "result": result}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": idOrNull(id), "error": map[string]any{"code": code, "message": msg}}
}

func idOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
