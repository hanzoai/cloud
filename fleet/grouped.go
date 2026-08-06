// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"
)

// ONE TOOL PER SUBSYSTEM, with the operation as an argument.
//
// [rank] answered half of the truncation problem: a client that keeps 128 tools
// should keep the useful ones. It cannot answer the other half. Measured at
// https://api.hanzo.ai/v1/mcp on the deployed door, tools/list is 1,189 tools in
// 977,636 bytes — about 244,000 tokens to merely ENUMERATE what can be called,
// which no model holds, and Slack keeps the first 128 of them, so 1,061
// operations are unreachable however well they are ordered. Ordering a list
// nobody can read is a preference applied to a broken surface.
//
// The surface is what is wrong. MCP's unit is a TOOL, and this fleet's unit is
// an OPERATION, and there are two orders of magnitude between them. So the door
// projects one tool per SUBSYSTEM and carries the operation in an argument:
//
//	hanzo_git   {"op":"post_v1_git_repos","input":{…}}
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
//   - THE GATE. [Door.gather] refuses a name before it writes the routing table,
//     and that is still the only gate. A refused name never reaches [group], so
//     it is in no enum; never reaches [Door.ownerOf], so [Door.call] cannot
//     dispatch it through an envelope any more than it could directly; and
//     [Door.describe] answers out of the same gathered set, so it cannot be read
//     either. One rule, one place, three paths through it.
//   - ONE DISPATCH. The envelope is a DECODING, not a second route: it yields
//     the (name, message) a direct tools/call carries, and [Door.call] runs the
//     same owner lookup and the same hop on both.
//   - NOTHING REMEMBERED. Describe re-asks. A descriptor kept between requests
//     would be plugin/<app>/mcp.json again — the committed catalogue this
//     package exists to delete — and it could go stale in the one way that
//     matters, by describing an operation the rule has since refused.

// groupPrefix namespaces the tools this door composes ITSELF, as opposed to the
// ones its children declare. A child's operation id is either `<method>_<path>`
// or a declared PascalCase verb, so nothing a subsystem serves lands in here.
const groupPrefix = "hanzo_"

// Describe is the door's own tool: the input schema of ONE operation, by name.
//
// It is the fetch half of the surface — the enums say what exists, this says
// what an operation takes — and it is exported because the fleet's own agent
// runs are clients of this door like any other (apps/agents/door.go).
const Describe = groupPrefix + "describe"

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
// from an empty fleet, and [Describe] is offered even by a fleet that is
// serving nothing, because "one way to ask" does not depend on how much there
// is to ask about.
func group(all []named) []map[string]any {
	ops := map[string][]string{}
	best := map[string]int{}
	for _, t := range all {
		if r := rank(t.name); len(ops[t.app]) == 0 || r < best[t.app] {
			best[t.app] = r
		}
		ops[t.app] = append(ops[t.app], t.name)
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
func subsystemTool(app string, ops []string) map[string]any {
	return map[string]any{
		"name": groupPrefix + app,
		"description": app + ": " + strconv.Itoa(len(ops)) + " operations. Name one in \"op\" and pass " +
			"that operation's own arguments in \"input\". " + Describe + " returns an operation's input schema.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op":    map[string]any{"type": "string", "enum": ops},
				"input": map[string]any{"type": "object", "description": "arguments for the chosen op"},
			},
			"required": []string{"op"},
		},
	}
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
	}
}

// composed reports whether a tools/call names one of THIS door's own tools
// rather than an operation a child declared. See [groupPrefix].
func composed(tool string) bool { return strings.HasPrefix(tool, groupPrefix) }

// envelope is what a subsystem tool carries: the operation to run, and that
// operation's own arguments.
type envelope struct {
	Op    string          `json:"op"`
	Input json.RawMessage `json:"input"`
}

// unwrap reads the envelope back into what a DIRECT tools/call for the same
// operation is: its name, and the canonical message that runs it. ok is false
// when the model named a subsystem without naming an operation in it.
//
// The operation's arguments are carried UNPARSED. They belong to the subsystem
// that declared the schema, which is the only thing that knows how to read them,
// and re-encoding them here would be this process having an opinion about a
// shape it does not own.
func unwrap(id, args json.RawMessage) (op string, msg []byte, ok bool) {
	var e envelope
	if err := json.Unmarshal(args, &e); err != nil || e.Op == "" {
		return "", nil, false
	}
	if len(e.Input) == 0 {
		e.Input = json.RawMessage("{}")
	}
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      idOrNull(id),
		"method":  "tools/call",
		"params":  map[string]any{"name": e.Op, "arguments": e.Input},
	})
	if err != nil {
		return "", nil, false
	}
	return e.Op, msg, true
}

// describe answers the one question a surface of names leaves open: what does
// this operation take?
//
// It ASKS — a fresh [Door.gather], which is also the gate — and hands back the
// owning subsystem's OWN descriptor bytes, the same ones the flat list used to
// carry. So an operation is describable exactly when it is listable and exactly
// when it is callable: there is one set, computed one way, and no third answer.
func (d *Door) describe(c *zip.Ctx, req message, args json.RawMessage) error {
	var in struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(args, &in)

	tools, _, _ := d.gather(c)
	for _, t := range tools {
		if t.name == in.Op {
			return c.JSON(200, rpcResult(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(t.raw)}},
			}))
		}
	}
	// The same answer for "nobody serves it" and "policy withheld it", for the
	// same reason [Door.call] gives one answer for both: naming which it was
	// would turn the door into an oracle for the surface it just declined to
	// expose.
	return c.JSON(200, rpcErr(req.ID, -32602, "unknown tool: "+in.Op))
}
