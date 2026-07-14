// Package tools is the ONE tool plane for Hanzo Cloud: a single registry where
// every callable capability — a connector action, a user function, a zap service
// route, a cloud /v1 control ("full-cloud-control"), an agent, a skill, or a tool
// on an org's own external MCP server — is a Tool with a Source, a JSON-Schema, a
// per-(org,project) activation state, and an optional price.
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
// and the platform meters one unit. The same isolation boundary the connectors MCP
// endpoint (clients/automations) already ships — generalized across every source.
package tools

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Source names WHERE a tool comes from. It is the ONE dimension precedence and
// activation-default policy key on, so it is a closed set of values, not free text.
type Source string

const (
	// SourceBuiltin is a cloud /v1 route exposed as a tool ("full-cloud-control"):
	// a per-user token does over MCP exactly what that user may do over HTTP, gated
	// by the SAME IAM check because dispatch replays the request in-process.
	SourceBuiltin Source = "builtin"
	// SourceConnector is a connector action from clients/automations.
	SourceConnector Source = "connector"
	// SourceFunction is a user-defined function from clients/functions.
	SourceFunction Source = "function"
	// SourceZAPService is a zap-proto service route (tunneled over /zap).
	SourceZAPService Source = "zap-service"
	// SourceAgent is an org agent from clients/agents, callable as a tool.
	SourceAgent Source = "agent"
	// SourceSkill is an agent skill (clients/agentskills): discovery + activation
	// metadata, attached to agents rather than called directly.
	SourceSkill Source = "skill"
	// SourceMCP is a tool on an org's own registered EXTERNAL MCP server.
	SourceMCP Source = "mcp"
)

// precedence ranks sources so a name collision resolves deterministically: the
// LOWEST rank wins. A native cloud control (builtin) can never be shadowed by an
// org's external MCP server; a first-party connector outranks an external tool of
// the same name. This is the ONE precedence policy, honored by both List (dedup)
// and Dispatch (which source runs).
var precedence = map[Source]int{
	SourceBuiltin:    0,
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
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`  // ISO 4217, e.g. "USD"; empty ⇒ "USD".
	Recipient   string `json:"recipient"` // payout wallet ref for the marketplace seller.
}

// Tool is the ONE description of a callable capability, uniform across sources.
// Schema is the JSON-Schema of the call arguments (MCP inputSchema); Dispatchable
// is false for a listing-only entry (a skill is activated + attached, not called).
type Tool struct {
	Name         string          `json:"name"`
	Source       Source          `json:"source"`
	Description  string          `json:"description"`
	Schema       json.RawMessage `json:"inputSchema,omitempty"`
	Price        *Price          `json:"price,omitempty"`
	Dispatchable bool            `json:"dispatchable"`
	// Activated is filled by the registry from the activation store for the
	// requesting (org,project); providers leave it zero.
	Activated bool `json:"activated"`
}

// Scope is the (org, project) a listing is resolved for. project == "" or the
// literal default project both denote the org's default scope.
type Scope struct {
	Org     string
	Project string
}

// Principal is the VALIDATED caller a dispatch runs as — resolved once from the
// request and threaded to every source. Org/Project/User/Owner/IsAdmin are the
// IAM-native identity; credential is the caller's OWN credential headers, replayed
// by a builtin tool so a /v1 route runs under the SAME IAM check as a direct HTTP
// call (cloud's SanitizeIdentity strips minted headers on ingress and re-mints them
// ONLY from a re-validated credential, so replaying the credential — never the
// minted headers — is the one way an in-process call carries the caller's identity;
// this is exactly the zapface in-process-dispatch contract).
type Principal struct {
	Org     string
	Project string
	User    string
	Owner   string
	IsAdmin bool

	credential map[string]string
}

// credentialHeaders are the request headers a builtin replay carries: the caller's
// own credential (which cloud re-validates) plus request-shaping context. NO minted
// authority header (X-Org-Id/X-User-Id/…) is replayed — those are stripped on
// ingress and re-derived from the credential, so replaying them is a no-op at best
// and confusing at worst.
var credentialHeaders = []string{"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For"}

// PrincipalFrom resolves the validated caller from a request context. It returns
// ok=false (the caller must answer 403) unless a validated principal carries a
// non-empty org — the SAME gate principal.Org enforces. Because c.User() is set
// ONLY from a re-validated credential, a successful resolve guarantees a replayable
// credential is present; it is captured so a builtin tool can act as this exact
// caller and never escalate.
func PrincipalFrom(c *zip.Ctx) (Principal, bool) {
	org, ok := principal.Org(c)
	if !ok {
		return Principal{}, false
	}
	cred := map[string]string{}
	for _, h := range credentialHeaders {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	return Principal{
		Org:        org,
		Project:    principal.Project(c),
		User:       c.User(),
		Owner:      principal.Owner(c),
		IsAdmin:    c.IsAdmin(),
		credential: cred,
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
	// source lists tools from the org's registered servers; builtin lists routes.
	List(ctx context.Context, scope Scope) ([]Tool, error)
	// Dispatch invokes the named tool with JSON args, bound to the principal, and
	// returns the JSON-encodable result. A source that only lists (skills) returns
	// ErrNotDispatchable.
	Dispatch(ctx context.Context, p Principal, name string, args map[string]any) (any, error)
}
