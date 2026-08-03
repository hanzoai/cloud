// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
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
func Mount(host *zip.App, path string, apps []string, at At) *Door {
	d := &Door{host: host, at: at, apps: apps, owner: map[string]string{}}
	host.Post(path, d.serve)
	return d
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
	tools, down := d.gather(c)
	result := map[string]any{"tools": tools}
	if len(down) > 0 {
		result["_meta"] = map[string]any{Unavailable: down}
		d.host.Logger().Warn("fleet mcp: tools/list is INCOMPLETE — subsystems did not answer",
			"unavailable", len(down), "apps", len(d.apps), "tools", len(tools))
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
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)

	app := d.ownerOf(p.Name)
	if app == "" {
		d.gather(c)
		app = d.ownerOf(p.Name)
	}
	if app == "" {
		return c.JSON(200, rpcErr(req.ID, -32602, "unknown tool: "+p.Name))
	}
	// The caller's OWN message, at the child's own door. The child's registry
	// invokes it, so the host can only ever name a tool and never invoke one the
	// child did not declare.
	ans := Ask(d.at, []string{app}, c.Fiber().Request(), manifest.FrameworkMCPPath)[0]
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

// named is one tool with its name lifted out, so the composed list sorts without
// re-parsing and each descriptor is carried VERBATIM — the bytes the child's own
// registry projected, never a re-encoding.
type named struct {
	name string
	raw  json.RawMessage
}

// gather asks every app what it serves, right now, and returns the union plus
// the outages.
//
// The request it sends is the CALLER's, with the body replaced by a canonical
// tools/list: the headers ride along, so a child whose tools depend on who is
// asking answers for this caller, while the body cannot be a tools/call the
// discovery path would otherwise execute on every child in the fleet.
func (d *Door) gather(c *zip.Ctx) ([]json.RawMessage, []Outage) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	c.Fiber().Request().CopyTo(req)
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBody([]byte(`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`))

	var all []named
	var down []Outage
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
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })

	d.mu.Lock()
	d.owner = owner
	d.mu.Unlock()

	// Never nil: `"tools": null` is a client-visible difference from an empty
	// fleet, and JSON has one way to say "no tools".
	out := make([]json.RawMessage, 0, len(all))
	for _, t := range all {
		out = append(out, t.raw)
	}
	return out, down
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
