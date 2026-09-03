// Copyright © 2026 Hanzo AI. MIT License.

package surface

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// ONE TOOL PER SUBSYSTEM, with the operation as an argument.
//
// [rank] answered half of the truncation problem: a client that keeps 128 tools
// should keep the useful ones. It cannot answer the other half. Measured at
// https://api.hanzo.ai/v1/mcp on the deployed MCP server, tools/list is 1,189
// tools in 977,636 bytes — about 244,000 tokens to merely ENUMERATE what can be
// called, which no model holds, and Slack keeps the first 128 of them, so 1,061
// operations are unreachable however well they are ordered. Ordering a list
// nobody can read is a preference applied to a broken surface.
//
// The surface is what is wrong. MCP's unit is a TOOL, and this surface's unit is
// an OPERATION, and there are two orders of magnitude between them. So the
// server projects one tool per SUBSYSTEM and carries the operation in an
// argument:
//
//	git   {"op":"post_v1_git_repos","input":{…}}
//
// The `op` enum carries NAMES ONLY. That is the whole saving — the 977 KB is
// almost entirely input schemas, and a schema is only needed for the ONE
// operation a model has chosen. [Describe] is where it fetches that one, which
// makes this the ordinary search-then-fetch shape rather than a truncation:
// nothing is hidden, every surviving operation is named in exactly one enum and
// callable through exactly one tool.
//
// Three properties this must not lose, and how it keeps them:
//
//   - THE GATE. [MCP.gather] refuses a name before it writes the routing table,
//     and that is still the only gate. A refused name never reaches [group], so
//     it is in no enum; never reaches [MCP.lookup], so [MCP.call] cannot
//     dispatch it through an envelope any more than it could directly; and
//     [MCP.describe] answers out of the same gathered set, so it cannot be read
//     either. One rule, one place, three paths through it.
//   - ONE DISPATCH. The envelope is a DECODING, not a second route: it yields
//     the (name, message) a direct tools/call carries, and [MCP.call] runs the
//     same owner lookup and the same hop on both.
//   - NO DESCRIPTOR REMEMBERED. Describe re-asks its owner, always. Names and
//     prose come from the catalog for a subsystem that is not running (see
//     surface/catalog.go) and SCHEMAS never do — a descriptor kept between requests
//     would be plugin/<app>/mcp.json again, and the way that one went stale is
//     the way this one cannot: the gate runs over catalog entries and live ones
//     alike, in the same loop, so an operation the rule has since refused is in
//     no enum whichever it came from.

// A TOOL IS NAMED FOR WHAT IT IS, and nothing else.
//
// These tools were `hanzo_<app>` for a few hours. The server is Hanzo — the MCP
// server IS the namespace, and a client reaches these names through it and
// through nothing else — so the prefix said, once per tool per turn, a thing
// every one of its neighbours also said. It was not disambiguating anything:
// there is no second `git` in here to tell it apart from. So it is gone, and the
// server's tools are the app names themselves.
//
// The prefix was also carrying a second job, and that is the part worth stating
// rather than rediscovering: [MCP.composed] used it to tell one of THIS
// server's tools from an operation a child declared. A convention doing
// load-bearing work is a convention that will be broken by someone who thinks it
// is cosmetic — so that test is now a membership check against the server's own
// app set, which is exact where a prefix was only probable.

// Describe is the server's own tool: the input schema of ONE operation, by name.
//
// It is the fetch half of the surface — the enums say what exists, this says
// what an operation takes — and it is exported because the surface's own agent
// runs are clients of this server like any other (apps/agents/surface.go).
//
// It shares a namespace with the app names, so no subsystem may be called
// `describe` — asserted against the manifest in surface/grouped_test.go, which is
// where a fact about the app list can be checked before it ships rather than
// discovered as a shadowed tool at runtime.
const Describe = "describe"

// group projects the surviving operations as one tool per OWNING app, behind
// [Describe].
//
// The apps are ordered by the best [rank] among the operations they own, then by
// name: the same preference the flat list had, lifted one level, so a client
// with a tiny window still meets chat before it meets the console. Within an app
// the enum is in the order gather sorted it — (rank, name) — so the head of an
// enum is that subsystem's product surface too.
//
// [Describe] leads, and that is the one place [rank]'s preference is overruled
// rather than applied. It is not more important than chat; it is what makes
// every other tool USABLE, since the enums carry names and no schemas. A
// truncating client drops the tail, and a client left holding 128 enums and no
// way to read one of them has a surface it cannot fill in — so the tool the rest
// depends on cannot be in the tail.
//
// Never nil, and never empty: `"tools": null` is a client-visible difference
// from an empty surface, and [Describe] is offered even by a surface that is
// serving nothing, because "one way to ask" does not depend on how much there
// is to ask about.
func group(all []named) []map[string]any {
	ops := map[string][]named{}
	best := map[string]int{}
	for _, t := range all {
		if r := rank(t.name); len(ops[t.app]) == 0 || r < best[t.app] {
			best[t.app] = r
		}
		ops[t.app] = append(ops[t.app], t)
	}
	apps := make([]string, 0, len(ops))
	for a := range ops {
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool {
		if best[apps[i]] != best[apps[j]] {
			return best[apps[i]] < best[apps[j]]
		}
		return apps[i] < apps[j]
	})

	out := make([]map[string]any, 0, len(apps)+1)
	out = append(out, describeTool())
	for _, a := range apps {
		out = append(out, subsystemTool(a, ops[a]))
	}
	return out
}

// subsystemTool is one app's whole operation set as a single MCP tool.
//
// The enum carries PUBLISHED names — `deploy_project`, not
// `post_v1_projects_by_slug_deploy` (surface/verbs.go) — and beside the product
// ones it carries a line of their own documentation, which is the half that
// removes a round trip. A name says what an operation is called and a model can
// still be wrong about what it does; `create_project_fork — Creates a project
// seeded from a PUBLISHED EXAMPLE.` leaves nothing to guess and nothing to fetch.
//
// PROSE IS RATIONED, and [productStems] is the ration, because it is already the
// answer to "which of these does an agent actually reach for". Measured over the
// surface's own ~2,230 offered operations — surface/verbs_internal_test.go prints
// these to the byte, and reprints them as the surface grows:
//
//	routes, as they shipped           63 KB
//	verb phrases                      50 KB   a phrase is SHORTER than a route
//	+ a summary on the ~140 ranked    64 KB   under a KB more than the routes ← shipped
//	+ a summary on ALL of them       255 KB   four times over
//
// So the whole change is close to free: the naming pays for the prose. Giving
// every operation a sentence would not — the point of grouping was 977 KB down
// to 71, and four times the enum puts most of it back. The product surface reads
// without asking, the console tail is named well enough to recognise, and
// [Describe] is one call away for the rest. One curated list doing the one job of
// saying what matters, rather than a second list to keep in step with the first.
func subsystemTool(app string, ops []named) map[string]any {
	names := make([]string, len(ops))
	var doc strings.Builder
	reads := true
	for i, t := range ops {
		names[i] = t.as
		reads = reads && t.read
		if rank(t.name) == len(productStems) {
			continue
		}
		if s := summary(t.desc); s != "" {
			if doc.Len() > 0 {
				doc.WriteByte('\n')
			}
			doc.WriteString(t.as + " — " + s)
		}
	}
	op := map[string]any{"type": "string", "enum": names}
	if doc.Len() > 0 {
		op["description"] = doc.String()
	}
	tool := map[string]any{
		"name": app,
		"description": app + ": " + strconv.Itoa(len(ops)) + " operations. Name one in \"op\" and pass " +
			"that operation's own arguments in \"input\". " + Describe + " returns an operation's input schema.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op":    op,
				"input": map[string]any{"type": "object", "description": "arguments for the chosen op"},
			},
			"required": []string{"op"},
		},
	}
	// readOnlyHint is set only when it is TRUE OF THE WHOLE TOOL: a subsystem
	// whose every offered operation is a GET. A tool that mixes reads and writes
	// carries no hint rather than a false one — the hint is per tool, and a client
	// that trusts it skips the confirmation a write deserves.
	if reads && len(ops) > 0 {
		tool["annotations"] = map[string]any{"readOnlyHint": true}
	}
	return tool
}

func describeTool() map[string]any {
	return map[string]any{
		"name": Describe,
		"description": "The description and input schema of ONE operation, named as it appears in a " +
			"subsystem tool's \"op\" enum. Read it before filling \"input\".",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"op": map[string]any{"type": "string"}},
			"required":   []string{"op"},
		},
		"annotations": map[string]any{"readOnlyHint": true},
	}
}

// composed reports whether a tools/call names one of THIS server's own tools —
// a SUBSYSTEM, carrying its operation in an argument — rather than an operation
// a child declared.
//
// The server's tools ARE its apps, so the question is membership in the set it
// was mounted over. Nothing a subsystem serves can be mistaken for one: a
// child's operation id is either `<method>_<path>` or a declared PascalCase
// verb, and an app name is a bare lowercase word, which is neither.
//
// It reads d.apps rather than the last gather because a call must be classified
// BEFORE anything is asked — and because the composed set is what the deployment
// runs, which does not change between requests, while what an app is serving at
// this instant does.
func (d *MCP) composed(tool string) bool { return slices.Contains(d.apps, tool) }

// envelope is what a subsystem tool carries: the operation to run, and that
// operation's own arguments.
type envelope struct {
	Op    string          `json:"op"`
	Input json.RawMessage `json:"input"`
}

// unwrap reads the envelope back into the two things a DIRECT tools/call for the
// same operation carries: the operation named, and its own arguments. ok is false
// when the model named a subsystem without naming an operation in it.
//
// The operation's arguments come back UNPARSED. They belong to the subsystem that
// declared the schema, which is the only thing that knows how to read them, and
// re-encoding them here would be this process having an opinion about a shape it
// does not own.
func unwrap(args json.RawMessage) (op string, input json.RawMessage, ok bool) {
	var e envelope
	if err := json.Unmarshal(args, &e); err != nil || e.Op == "" {
		return "", nil, false
	}
	return e.Op, e.Input, true
}

// callBody is the tools/call a child is asked, written out.
//
// It exists once, and only for the calls that could not be forwarded verbatim —
// an envelope to open, or a published name to read back to an id. Both arrive as
// (name, arguments) by then, which is the whole of a tools/call, so there is one
// spelling of the request no matter which decoding produced it.
//
// Arguments that will not re-encode become `{}` rather than an error: the bytes
// came out of a document this server already parsed, so the only way here is a
// caller who sent something the child was going to reject anyway, and the child
// is the thing that owns that judgement.
func callBody(id json.RawMessage, op string, input json.RawMessage) []byte {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      idOrNull(id),
		"method":  "tools/call",
		"params":  map[string]any{"name": op, "arguments": input},
	})
	if err != nil {
		return callBody(id, op, nil)
	}
	return msg
}

// describe answers the one question a surface of names leaves open: what does
// this operation take?
//
// It ASKS — a fresh [MCP.gather], which is also the gate — and hands back the
// owning subsystem's OWN descriptor bytes, the same ones the flat list used to
// carry. So an operation is describable exactly when it is listable and exactly
// when it is callable: there is one set, computed one way, and no third answer.
// descriptor asks t's owner for the operation as that owner declares it, and
// reports whether it got one. It is the fetch half of a surface whose enums
// carry names: one subsystem is reached, and only because a caller named an
// operation it serves.
func (d *MCP) descriptor(c *zip.Ctx, t named, at At) (json.RawMessage, bool) {
	hop := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(hop)
	c.Fiber().Request().CopyTo(hop)
	hop.Header.SetMethod("POST")
	hop.Header.SetContentType("application/json")
	hop.SetBody([]byte(`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`))

	ans := Ask(c.Context(), at, []string{t.app}, hop)[0]
	if ans.Err != nil {
		return nil, false
	}
	tools, err := toolsOf(ans.Body)
	if err != nil {
		return nil, false
	}
	for _, live := range tools {
		if live.name == t.name {
			return live.raw, true
		}
	}
	return nil, false
}

func (d *MCP) describe(c *zip.Ctx, req message, args json.RawMessage, at At) error {
	var in struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(args, &in)

	tools, _, _ := d.gather(c, at)
	for _, t := range tools {
		// Either spelling: the name the enum published, or the id the owner knows.
		// The gathered set carries both, so this needs no table and cannot answer
		// out of a different one than list() and call() read.
		if t.as != in.Op && t.name != in.Op {
			continue
		}
		// THE FETCH IS WHERE A SUBSYSTEM STARTS. The catalog holds names and prose
		// and no schema, so an operation read from it is described by asking its
		// owner — one subsystem, the one the caller picked, rather than the surface
		// that a listing used to wake. An owner that cannot be reached answers with
		// what was published, which is more than nothing and honest about its
		// shape.
		raw := t.raw
		if live, ok := d.descriptor(c, t, at); ok {
			raw = live
		}
		return c.JSON(200, rpcResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(raw)}},
		}))
	}
	// The same answer for "nobody serves it" and "policy withheld it", for the
	// same reason [MCP.call] gives one answer for both: naming which it was
	// would turn the server into an oracle for the surface it just declined to
	// expose.
	return c.JSON(200, rpcErr(req.ID, -32602, "unknown tool: "+in.Op))
}
