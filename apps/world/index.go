package world

import (
	"context"
)

// indexQuery is GET /v1/world's input: nothing. The public endpoint answers the
// same addresses to every caller, so there is no parameter that could change
// them.
type indexQuery struct{}

// worldIndex is what World is, and every wire this product answers on.
type worldIndex struct {
	// Product is the product's name as customers know it.
	Product string `json:"product"`
	// Summary is one sentence naming what this surface serves.
	Summary string `json:"summary"`
	// Wires is every protocol entry point onto World, REST first. It is
	// deliberately NOT a list of REST operations: GET /v1/openapi.json is the one
	// enumeration of those, and a second copy here would be a second thing to keep
	// true.
	Wires []worldWire `json:"wires"`
}

// worldWire is one protocol entry point onto World: where it is, what it
// speaks, and what it asks of the caller.
type worldWire struct {
	// Name is the wire's short id — rest, mcp or zap.
	Name string `json:"name"`
	// Path is the address the wire answers on, under this same origin.
	Path string `json:"path"`
	// Protocol names what the wire speaks, so a caller knows which client to
	// point at it.
	Protocol string `json:"protocol"`
	// Auth states what the wire asks of the caller, including which parts of it
	// answer without a token.
	Auth string `json:"auth"`
	// Spec is where this wire's operations are enumerated, when they are
	// enumerated in a document at all. Empty for a wire that describes itself
	// over its own protocol.
	Spec string `json:"spec,omitempty"`
}

// index answers GET /v1/world — the product's public endpoint, naming every wire
// this surface answers on.
//
// It exists because two of those wires are INVISIBLE to the generated document.
// /v1/world/mcp and /v1/world/zap are carved off the cloud catch-all by the
// ingress and answered by world-gw, so the cloud router never serves them — and
// openapi.Describe renders prose only for a route the router actually serves,
// which is the very property that keeps the document from being able to claim an
// operation nothing answers. Both addresses are real and public, so without this
// op the only way to learn they exist is to read the ingress config. This is
// where that fact lives, in the product's own surface.
//
// Public on purpose: discovery precedes credentials. It reports addresses and
// protocols only — never feed data, and never the caller's plan, which
// GET /v1/world/limits owns — so there is nothing here to leak.
func (s *service) index(context.Context, *indexQuery) (*worldIndex, error) {
	return &worldIndex{
		Product: "Hanzo World",
		Summary: "Real-time global intelligence: a per-org, per-project news feed over REST " +
			"and server-sent events, and read-only access to World's planetary-intelligence " +
			"data over MCP and ZAP.",
		Wires: []worldWire{{
			Name:     "rest",
			Path:     "/v1/world",
			Protocol: "HTTP/JSON",
			Auth: "Bearer token. GET /v1/world and GET /v1/world/limits answer without one; " +
				"every feed and pipeline operation is scoped to the caller's org and project " +
				"and answers 403 without a validated principal.",
			Spec: "/v1/openapi.json",
		}, {
			Name:     "mcp",
			Path:     "/v1/world/mcp",
			Protocol: "Model Context Protocol over streamable HTTP (JSON-RPC 2.0, server-sent events)",
			Auth: "Bearer token. initialize answers without one; tools/list and every tool " +
				"call are fail-closed and answer JSON-RPC error -32001.",
		}, {
			Name:     "zap",
			Path:     "/v1/world/zap",
			Protocol: "ZAP over WebSocket",
			Auth:     "Bearer token, fail-closed: 401 missing_token without one.",
		}},
	}, nil
}
