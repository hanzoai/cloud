// Package tools is everything your org can call, in one list: connector
// actions, functions, agents, skills and your own MCP servers.
//
// It is the ONE tool plane for Hanzo Cloud's PER-TENANT capabilities — a single
// registry where every callable thing an ORG owns is a Tool with a Source, a
// JSON-Schema, a per-(org,project) activation state, and an optional price.
//
// Per-tenant is the whole boundary. Cloud's OWN typed ops are not here and never
// were a Source: they are code, known at build time, and the fleet publishes them
// as MCP tools straight from the typed-op registry onto the host's one door
// (plugin/<app>/mcp.json → zip.Plugin.Tools). What lives here is ROWS — a tool
// whose existence, price and activation depend on which org is asking — reached
// from that same door through the typed POST /v1/tools/call.
//
// Decomplected on the Rich Hickey seam: a Source knows how to LIST its tools and
// DISPATCH one; the registry knows nothing about how any single source works. Each
// source REGISTERS a Provider into the registry from its own Mount — no source
// duplicates listing or dispatch logic, and the registry never grows a per-source
// branch. Adding a source is: implement Provider, call tools.Register.
//
// Every dispatch flows through ONE per-principal plane: the caller's VALIDATED org
// (principal.Org) gates the call, the tool must be ACTIVATED for that (org,project)
// or the call is 403, a priced tool settles through the explicit x402 Charger seam,
// and the platform meters one unit. One plane, one policy, every source.
package tools

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Source names WHERE a tool comes from. It is the ONE dimension precedence and
// activation-default policy key on, so it is a closed set of values, not free text.
type Source string

const (
	// SourceConnector is a connector action from clients/automations.
	SourceConnector Source = "connector"
	// SourceFunction is a user-defined function from clients/functions.
	SourceFunction Source = "function"
	// SourceZAPService is a zap-proto service route (tunneled over /zap).
	SourceZAPService Source = "zap-service"
	// SourceAgent is an org agent from clients/agents, callable as a tool.
	SourceAgent Source = "agent"
	// SourceSkill is an agent skill (apps/skills): discovery + activation
	// metadata, attached to agents rather than called directly.
	SourceSkill Source = "skill"
	// SourceMCP is a tool on an org's own registered EXTERNAL MCP server.
	SourceMCP Source = "mcp"
)

// precedence ranks sources so a name collision resolves deterministically: the
// LOWEST rank wins. A first-party connector outranks an external tool of the same
// name, and an org's external MCP server ranks last, so it can never shadow
// anything first-party. This is the ONE precedence policy, honored by both List
// (dedup) and Dispatch (which source runs).
var precedence = map[Source]int{
	SourceConnector:  1,
	SourceFunction:   2,
	SourceZAPService: 3,
	SourceAgent:      4,
	SourceSkill:      5,
	SourceMCP:        6,
}

// rank returns a source's precedence; an unknown source sorts last.
func rank(s Source) int {
	if r, ok := precedence[s]; ok {
		return r
	}
	return len(precedence) + 1
}

// Price declares what a monetized tool call costs and who is paid. Enforcement is
// the x402 Charger seam (registry.go) — this is only the DECLARATION a marketplace
// listing carries. A nil Price means the tool is free (no x402 settlement).
type Price struct {
	// Amount is what ONE call costs, EXACTLY: an 18-decimal USD value, so a
	// per-call price of $0.0025 is $0.0025 and not a cent-floored zero. Cents
	// cannot hold a per-token price, and a tool plane is where per-token prices
	// live.
	Amount money.Amount `json:"amount"`
	// Currency is the ISO 4217 code, e.g. "USD". Empty means USD.
	Currency string `json:"currency"`
	// Recipient is the payout wallet ref the marketplace seller is paid at.
	Recipient string `json:"recipient"`
}

// Tool is the ONE description of a callable capability, uniform across sources.
// Schema is the JSON-Schema of the call arguments (MCP inputSchema); Dispatchable
// is false for a listing-only entry (a skill is activated + attached, not called).
type Tool struct {
	// Name is the tool's id in the flat, fleet-wide tool namespace — the value a
	// tools/call passes. Unique across sources: a collision is resolved by source
	// precedence before the caller ever sees it.
	Name string `json:"name"`
	// Source is where the tool comes from: connector, function, zap-service,
	// agent, skill or mcp.
	Source Source `json:"source"`
	// Description is the prose a model reads to decide whether to call the tool.
	Description string `json:"description"`
	// Schema is the JSON Schema of the call arguments — the MCP inputSchema.
	// Absent for a tool that takes none.
	Schema json.RawMessage `json:"inputSchema,omitempty"`
	// Price is what a call costs and who is paid, absent for a free tool.
	// Enforcement is the x402 settlement seam; this is the declaration.
	Price *Price `json:"price,omitempty"`
	// Dispatchable is whether the tool can be CALLED. False for a listing-only
	// entry: a skill is activated and attached to an agent, never called.
	Dispatchable bool `json:"dispatchable"`
	// Activated is filled by the registry from the activation store for the
	// requesting (org,project); providers leave it zero. An unactivated tool is
	// discoverable but refused 403 at dispatch.
	Activated bool `json:"activated"`
}

// Scope is the (org, project) a listing is resolved for. project == "" or the
// literal default project both denote the org's default scope.
type Scope struct {
	Org     string
	Project string
}

// Principal is the VALIDATED caller a dispatch runs as — resolved once from the
// request and threaded to every source. It is the IAM-native identity and nothing
// else: cloud's SanitizeIdentity strips minted authority headers on ingress and
// re-mints them ONLY from a re-validated credential, so by the time a dispatch
// sees a Principal the authority question is already settled upstream.
type Principal struct {
	Org     string
	Project string
	User    string
	Owner   string
	IsAdmin bool
}

// PrincipalFrom resolves the validated caller from a request context. It returns
// ok=false (the caller must answer 403) unless a validated principal carries a
// non-empty org — the SAME gate principal.Org enforces.
func PrincipalFrom(c *zip.Ctx) (Principal, bool) {
	org, ok := principal.Org(c)
	if !ok {
		return Principal{}, false
	}
	return Principal{
		Org:     org,
		Project: principal.Project(c),
		User:    c.User(),
		Owner:   principal.Owner(c),
		IsAdmin: c.IsAdmin(),
	}, true
}

// Provider is one tool SOURCE. It lists the tools it offers to a (org,project) and
// dispatches a call to one of them bound to the principal. The registry composes
// providers; it never learns how any single source lists or runs its tools. A
// source implements this once and calls Register — that is the whole contract.
type Provider interface {
	// Source is the provider's source tag (a provider serves exactly one source).
	Source() Source
	// List returns the tools this source offers for scope. It is scope-aware:
	// a connector source lists an org's connected connectors; the external-MCP
	// source lists tools from the org's registered servers.
	List(ctx context.Context, scope Scope) ([]Tool, error)
	// Dispatch invokes the named tool with JSON args, bound to the principal, and
	// returns the JSON-encodable result. A source that only lists (skills) returns
	// ErrNotDispatchable.
	Dispatch(ctx context.Context, p Principal, name string, args map[string]any) (any, error)
}
