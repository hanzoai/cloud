package agents

// door.go — where a run's tools come from once the fleet is more than one
// process: the fleet's OWN agent MCP server, asked over the internal socket.
//
// # Why this is not a new mechanism
//
// The fleet already aggregates. fleet.Door asks every composed app what it
// serves right now, merges the answers, remembers which app listed which name,
// and forwards a tools/call to that app (fleet/mcp.go). It is what serves
// api.hanzo.ai/v1/mcp and what a Slack MCP client already talks to. Building a
// tools_catalog/tools_call op pair on the tool plane would have been a SECOND
// aggregation over the same children, with a second place for the curation rule
// to be applied — or forgotten.
//
// So nothing here aggregates. The host publishes the MCP server it already
// built on the socket every child can already reach (cmd/cloud/wake.go), and an
// agent is simply another MCP client of it. Same JSON-RPC, same union, same
// order, same [fleet] denylist — which is enforced inside gather, where the
// routing table is written, so a name the MCP server will not project is not
// routable for anyone. An agent therefore CANNOT see a surface an external
// client cannot; there is no second surface to see.
//
// # The address, and what "no MCP server" means
//
// plane.HostApp is the router's own socket — the one plane.Reach dials to wake a
// cold app — and the MCP server rides it at manifest.MCPPath. Reaching for it
// answers one of exactly three things, which is the rule plane/ask.go already
// states:
//
//	listening      ask it; this is production
//	no listener    THIS PROCESS IS THE FLEET — a single-app binary, a test, a dev
//	               box. Fall back to [registryTools], which is the real answer
//	               there and empty everywhere else.
//	unusable       an outage. Zero tools, recorded on the run's span, never
//	               laundered into "this fleet has no tools".
//
// # Identity is stated by the RUN, and the model never touches it
//
// The org and actor a dispatch carries are the run's own — the pair its fee is
// billed under — passed as arguments from executeRun and written onto the
// request as zip's identity headers here. The model contributes a tool NAME and
// an ARGUMENTS object and nothing else, so there is no path by which it can name
// a tenant. The inbound caller's headers are deliberately NOT forwarded: a
// scheduled run has no inbound request at all, and a nested one may be running
// for a different principal than whoever made the outermost HTTP call.
//
// The socket carries no credential and needs none: it is 0700 in the fleet's own
// run directory and the kernel attests the peer, which is the same trust
// zip.WithCaller rides on for every other internal call.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// doorTools is the tool plane read from the FLEET's composed agent MCP server.
type doorTools struct{}

// errNoDoor reports that this process is not part of a fleet: nothing is
// listening on the router's socket, so there is no composed MCP server to ask.
//
// It is the ONE error a caller may read as "fall back", exactly as plane.ErrNoPeer
// is on the peer plane. Every other failure is an outage and is reported as one —
// an MCP server that is present and broken must never read as a fleet with no
// tools.
var errNoDoor = errors.New("agents: no fleet MCP server on this host")

// catalog resolves the agent's declared names against the fleet's own surface.
//
// It asks the MCP server WHAT IS OFFERED and then, for the handful of names this
// agent declared, what each one takes. Those are two questions because the MCP
// server's tools/list answers only the first: it publishes one tool per
// subsystem, whose `op` enum carries the operation names and no schemas, since
// the flat list of this fleet's operations was 977 KB that no model can hold and
// every client truncates (fleet/grouped.go). fleet.Describe answers the second,
// one operation at a time, out of the same gathered set — so a declared name the
// MCP server does not offer is simply absent, which is the same rule
// registryTools follows: offering a tool that would be refused at dispatch
// teaches the model a lie.
func (doorTools) catalog(ctx context.Context, org, actor string, want []string) []types.ToolDef {
	if org == "" || len(want) == 0 {
		return nil
	}
	// ToolsAll is the ONE way to say "whatever the fleet serves", and it has to be
	// said rather than implied.
	//
	// An agent that declares nothing gets nothing — that default is correct and
	// stays, because a user-defined agent's tool list is its authority and an
	// empty one means it asked for none. But the DEFAULT ASSISTANT cannot enumerate
	// its tools: the MCP server's surface is discovered at runtime (88 grouped
	// tools today, and the whole point of grouping was that the set changes
	// without a code edit), so any list written here would be stale the next time
	// a subsystem ships.
	//
	// Not stating it cost a full turn of user-visible wrongness: the assistant was
	// told in its instructions that it had tools and how to call them, then handed
	// an empty offer by this function, so it correctly reported that it could not
	// reach the cloud — while the MCP server was serving 88 tools one socket away.
	// The two halves have to agree, and this is the half that was missing.
	all := false
	for _, n := range want {
		if strings.TrimSpace(n) == ToolsAll {
			all = true
			break
		}
	}
	wanted := make(map[string]bool, len(want))
	for _, n := range want {
		if n = strings.TrimSpace(n); n != "" && n != ToolsAll {
			wanted[n] = true
		}
	}
	if len(wanted) == 0 && !all {
		return nil
	}
	res, err := askDoor(ctx, org, actor, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if errors.Is(err, errNoDoor) {
		return registryTools{}.catalog(ctx, org, actor, want)
	}
	if err != nil {
		// An outage, and it is SAID so. The run continues with no tools — killing
		// a turn the org has paid for because a sibling is down is the worse
		// answer — but "declared 3, offered 0" is already a number on the step
		// span, and this is the reason beside it.
		trace.SpanFromContext(ctx).RecordError(err)
		return nil
	}
	var listed struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		// Meta is the MCP server's own account of why its list may be SHORT: the
		// subsystems it could not ask, and how many names policy withheld. The MCP
		// server went to the trouble of never shortening quietly, so throwing it
		// away here would put the silence back one layer down.
		Meta json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(res, &listed); err != nil {
		trace.SpanFromContext(ctx).RecordError(fmt.Errorf("agents: the fleet MCP server's tools/list is not a tool list: %w", err))
		return nil
	}
	offered := map[string]bool{}
	for _, t := range listed.Tools {
		for _, op := range opsOf(t.InputSchema) {
			offered[op] = true
		}
	}
	// ToolsAll offers the MCP server's tools AS THE MCP SERVER GROUPS THEM — one
	// per subsystem, named for it, carrying an `op` enum, plus [fleet.Describe] —
	// and not the ops flattened back out.
	//
	// The grouping is the whole reason the surface is affordable: 1,189 flat tools
	// were 977 KB (~244k tokens) merely to LIST, and the same operations grouped
	// are 88 tools in 63 KB. Expanding them here would hand back every byte the
	// MCP server just saved and blow the context before the question is read.
	//
	// It is also what the assistant's instructions describe — pick a subsystem,
	// choose an op from its enum, call [fleet.Describe] for a shape you do not know.
	// The prose and the offer have to be the same surface or the model is being
	// taught a protocol it cannot practise.
	if all {
		out := make([]types.ToolDef, 0, len(listed.Tools))
		for _, t := range listed.Tools {
			out = append(out, types.ToolDef{
				Name: t.Name, Description: t.Description, Schema: t.InputSchema,
			})
		}
		return out
	}

	// In the agent's own declared order, which is the order the model meets them
	// in, and once each however often it was declared.
	out := make([]types.ToolDef, 0, len(wanted))
	done := make(map[string]bool, len(wanted))
	for _, n := range want {
		n = strings.TrimSpace(n)
		if !wanted[n] || done[n] || !offered[n] {
			continue
		}
		done[n] = true
		def, err := describe(ctx, org, actor, n)
		if err != nil {
			// It was offered a moment ago, so this is an outage between the two
			// asks and not a refusal. Same policy as above: the turn goes on with
			// one fewer tool, and the reason is on the span.
			trace.SpanFromContext(ctx).RecordError(err)
			continue
		}
		out = append(out, def)
	}
	// A declared name that resolved to nothing has two very different causes — a
	// subsystem that is DOWN and a tool the fleet REFUSES to project — and the
	// MCP server already distinguishes them. Carrying its answer onto the span is
	// what makes "declared 3, offered 1" diagnosable instead of a shrug.
	if len(out) < len(wanted) && len(listed.Meta) > 0 {
		trace.SpanFromContext(ctx).SetAttributes(
			attribute.String("hanzo.agent.tools_meta", clip(string(listed.Meta), maxDoorMeta)))
	}
	return out
}

// opsOf reads the operation names out of one subsystem tool's schema — its `op`
// enum, which is where the MCP server carries them.
func opsOf(schema json.RawMessage) []string {
	var s struct {
		Properties struct {
			Op struct {
				Enum []string `json:"enum"`
			} `json:"op"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	return s.Properties.Op.Enum
}

// describe fetches ONE operation's descriptor through the MCP server's own
// fleet.Describe, and reads the owning subsystem's bytes back out of it.
//
// What the client guarantees is that the model is offered exactly what it will
// CALL, and op is that name: it came out of a subsystem tool's `op` enum a
// moment ago, and a tools/call naming it reaches the operation's own handler.
// So the offer is named op, with the owner's own description and schema behind
// it.
//
// The descriptor's own `name` is NOT compared to op, and that is a change. The
// MCP server publishes an operation as a verb on an object — `deploy_project`
// for `post_v1_projects_by_slug_deploy` (fleet/verbs.go) — while the descriptor
// it hands back is the owning subsystem's, carried verbatim, so it still says
// the id. Requiring the two to match would reject 1,730 of the fleet's 2,229
// operations for being correctly named.
func describe(ctx context.Context, org, actor, op string) (types.ToolDef, error) {
	args, err := json.Marshal(map[string]string{"op": op})
	if err != nil {
		return types.ToolDef{}, err
	}
	body, err := toolCallBody(fleet.Describe, string(args))
	if err != nil {
		return types.ToolDef{}, err
	}
	res, err := askDoor(ctx, org, actor, body)
	if err != nil {
		return types.ToolDef{}, err
	}
	text, err := toolResult(res)
	if err != nil {
		return types.ToolDef{}, err
	}
	var d struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal([]byte(text), &d); err != nil || d.Name == "" || len(d.InputSchema) == 0 {
		return types.ToolDef{}, fmt.Errorf("agents: %s did not answer %s's own descriptor", fleet.Describe, op)
	}
	return types.ToolDef{Name: op, Description: d.Description, Schema: d.InputSchema}, nil
}

// maxDoorMeta bounds what one span attribute may carry: `_meta` names every
// subsystem that did not answer, and a fleet-wide outage would otherwise put a
// hundred rows on every run's trace.
const maxDoorMeta = 1024

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return shorten.To(s, n) + "…"
}

// call runs one tool through the MCP server's own dispatch: the MCP server names
// the app that listed it and forwards this message verbatim to that app's
// registry, so the host can only ever ROUTE a call and never invoke something
// the owner did not declare.
func (doorTools) call(ctx context.Context, org, actor, name, args string) (string, error) {
	body, err := toolCallBody(name, args)
	if err != nil {
		return "", err
	}
	res, err := askDoor(ctx, org, actor, body)
	if errors.Is(err, errNoDoor) {
		return registryTools{}.call(ctx, org, actor, name, args)
	}
	if err != nil {
		return "", err
	}
	return toolResult(res)
}

// toolCallBody builds one MCP tools/call, with the model's arguments carried
// VERBATIM.
//
// The arguments are validated as a JSON OBJECT and then embedded unparsed: they
// belong to the tool that declared the schema, which is the only thing that
// knows how to read them, and re-encoding them here would be this process having
// an opinion about a shape it does not own. A model that emits something else is
// told so — the same sentence registryTools gives it — and the turn goes on.
func toolCallBody(name, args string) ([]byte, error) {
	raw := json.RawMessage("{}")
	if s := strings.TrimSpace(args); s != "" && s != "null" {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &probe); err != nil {
			return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
		}
		raw = json.RawMessage(s)
	}
	return json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{Name: name, Arguments: raw},
	})
}

// toolResult reads one MCP tool result into the text the model is handed.
//
// isError is a FAILURE and comes back as one, so dispatchOne renders it as a
// tool result the model can react to rather than as a success it would believe.
// That is the same distinction the MCP server itself draws when a hop fails.
func toolResult(res json.RawMessage) (string, error) {
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("agents: the fleet MCP server answered a tool result that will not decode: %w", err)
	}
	parts := make([]string, 0, len(out.Content))
	for _, c := range out.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if out.IsError {
		// The tool's OWN sentence, so the model reads what actually went wrong.
		if text == "" {
			text = "the tool reported a failure with no message"
		}
		return "", errors.New(truncateToolResult(text))
	}
	return truncateToolResult(text), nil
}

// askDoor puts one JSON-RPC message to the fleet's MCP server as (org, actor)
// and returns the `result` member.
//
// A JSON-RPC ERROR is an error here, deliberately: a tool the MCP server will
// not route answers -32602, and folding that into an empty result would make
// "this tool is not yours to call" indistinguishable from "it ran and said
// nothing".
func askDoor(ctx context.Context, org, actor string, body []byte) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr, err := doorAddr()
	if err != nil {
		return nil, err
	}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetHost(plane.HostApp)
	req.URI().SetPath(manifest.MCPPath)
	// The RUN's identity, in zip's own spelling, and nothing else. The MCP server
	// copies these onto every hop it makes, so a subsystem whose tools depend on
	// the tenant answers for the org this run is billed to. A blank subject is a
	// run with no person behind it (a schedule, a service token); the org is the
	// authority either way and inventing a user would attribute the call to
	// nobody.
	req.Header.Set(zip.HeaderOrg, org)
	if sub := actorSub(org, actor); sub != "" {
		req.Header.Set(zip.HeaderUser, sub)
	}
	req.SetBody(body)

	if err := doorClient(addr).Do(req, resp); err != nil {
		return nil, fmt.Errorf("agents: the fleet MCP server at %s did not answer: %w", addr, err)
	}
	if code := resp.StatusCode(); code < 200 || code > 299 {
		return nil, fmt.Errorf("agents: the fleet MCP server answered %d", code)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.Body(), &env); err != nil {
		return nil, fmt.Errorf("agents: the fleet MCP server answered something that is not JSON-RPC: %w", err)
	}
	if env.Error != nil {
		return nil, errors.New(env.Error.Message)
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

// doorAddr resolves the fleet MCP server's socket, or says which of the two
// failures it is. See [errNoDoor].
//
// It probes by CONNECTING, because the file does not answer the question: a
// socket path outlives the process that bound it wherever the run directory is a
// volume. plane.Listening is the one implementation of that rule.
func doorAddr() (string, error) {
	plane.Bind()
	path := zip.SocketPath(plane.HostApp)
	up, err := plane.Listening(path)
	if err != nil {
		return "", fmt.Errorf("agents: the fleet MCP server's socket is unusable: %w", err)
	}
	if !up {
		return "", fmt.Errorf("%w (%s)", errNoDoor, path)
	}
	return path, nil
}

// doorClients is one pooled transport per ADDRESS, for the reason fleet keeps
// one: a transport holds a connection pool, so dialing per ask turns every tool
// call into a fresh connect. Keyed by address rather than kept in a single var
// because a test points the run directory somewhere else.
var doorClients sync.Map // addr -> *zaphttp.Transport

func doorClient(addr string) *zaphttp.Transport {
	if c, ok := doorClients.Load(addr); ok {
		return c.(*zaphttp.Transport)
	}
	t := zaphttp.Dial("unix", addr)
	// The whole run's ceiling, not the library's 30s. A tools/list is a fan-out
	// across every composed app and the first ask of a cold one pays that app's
	// startup, so a transport that gave up sooner than the run does would report
	// an outage for a fleet that was merely waking up.
	t.SetReadTimeout(toolRunBudget)
	c, _ := doorClients.LoadOrStore(addr, t)
	return c.(*zaphttp.Transport)
}
