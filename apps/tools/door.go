package tools

import (
	"context"
	"encoding/json"

	"github.com/zap-proto/zip"
)

// door.go is this plane's half of the FLEET'S ONE MCP DOOR.
//
// The other half is a build artifact and has to be: 549 typed ops across 112
// lazily-mounted plugins, projected once and served as bytes, so tools/list —
// the method an MCP client calls constantly — costs a memcpy and starts no
// process (e247e255).
//
// This half cannot be. An org's connected connectors, its authored skills, its
// agents and functions, and the tools on the external MCP servers it enabled are
// ROWS: they exist because of WHO is asking, so no projection can hold them and
// no answer can be shared between callers. Before this they were reachable only
// THROUGH a tool — POST /v1/tools/call, one door-tool standing in front of every
// tenant capability — which is a second registry wearing a different hat. Now
// they are tools, on the same door, in the same list.
//
// It adds NO policy. Tools is the registry's own per-principal listing and Call
// is literally callTool, so activation, source precedence, the x402 price gate,
// the metered unit and the audit record are the ones the REST route already
// enforces. A tool that is refused at POST /v1/tools/call is refused here, for
// the same reason, with the same words. One plane, one policy, two doors onto it.
//
// zip.Source is the client (zip >= v1.18.14): the host declares the tools plugin
// OPEN (manifest/apps.go), asks it for the caller's tools on a tools/list that
// NAMES a caller, and hands it a tools/call no catalogue claimed. An anonymous
// list still costs a memcpy and starts nothing.

// Door is the per-caller tool source this subsystem contributes to its own MCP
// door. The composition root hands it to cloud.Serve (plugin/tools/main.go), which
// is the one place a subsystem's contributions to the binary are stated.
func Door() zip.Source { return door{} }

// door reads the registry through the SAME context resolvers the typed ops use —
// scopeOf for a listing, principalOf for a dispatch — so the caller a tool is
// listed for and the caller it runs as are resolved by one function each, and
// never from anything the caller wrote.
type door struct{}

// Tools is the caller's own callable set: every tool the tool plane offers this
// (org, project) that is both ACTIVATED and DISPATCHABLE.
//
// Activated, because activation is what makes a tool callable — listing an
// unactivated one would advertise a name that answers 403, which is worse than
// not listing it. Dispatchable, because a skill is discovery metadata attached to
// an agent, not something a model can call; naming it as a tool would be a lie
// about what happens next.
//
// Names are already namespaced by whoever owns them — an external server's tools
// are "<server>_<tool>", so two servers' "search" cannot collide — and zip drops
// any name the fleet's projection already holds, so a tenant can never shadow a
// product op.
func (door) Tools(ctx context.Context) []map[string]any {
	scope, err := scopeOf(ctx)
	if err != nil {
		// An empty list and a broken identity hop look identical from outside, and
		// the second is the one that would be silent forever: in the split-process
		// deployment the host forwards this to the tools plugin, which re-runs its
		// own identity boundary, and a credential that does not survive that hop
		// makes every org's per-caller list empty with nothing logged anywhere.
		if mounted != nil {
			mounted.Log.Warn("mcp door: no validated caller, serving no per-caller tools", "err", err)
		}
		return nil
	}
	all := Default().List(ctx, scope)
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		if !t.Activated || !t.Dispatchable {
			continue
		}
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

// Call runs one of them. It is callTool with the MCP envelope peeled off: the
// same resolve, the same activation gate, the same settlement, the same metered
// unit and audit record. A refusal comes back as the tool plane's own error text,
// which zip renders as MCP isError content — so a model reads "tool not activated
// for this org/project" rather than a transport failure it cannot act on.
func (door) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	in := toolCall{Name: name, Arguments: map[string]any{}}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in.Arguments); err != nil {
			return nil, zip.ErrBadRequest("arguments must be a JSON object")
		}
	}
	if mounted == nil {
		return nil, zip.Errorf(503, "the tool plane is not mounted")
	}
	out, err := (toolOps{s: mounted}).callTool(ctx, &in)
	if err != nil {
		return nil, err
	}
	return out.Result, nil
}
