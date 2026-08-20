package openapi

// THE AGENT DOOR'S PLACE IN THE DOCUMENT.
//
// POST /v1/mcp is served by the host (fleet.Mount), named by the manifest
// (manifest.MCPPath), and owned by no app — the fourth door beside the document
// endpoint, its well-known alias and the command projection. Like them it is
// projected rather than written down: [core] mounts a stub at the manifest's
// address and [Spec] reads it off that router, so the document can never name a
// door at an address the host does not answer. An app describing itself never
// carries it, which is what lets manifest refuse an app row that claims it.
//
// The prose and the bodies are declared here, where every other door's are, and
// for the same reason the doors had to be described at all: a published address
// with an operationId and nothing else is an SDK method nobody can explain and a
// CLI command with no help.

import (
	"net/http"

	"github.com/hanzoai/cloud/manifest/door"
	"github.com/zap-proto/zip"
)

// MCPRequest is one JSON-RPC 2.0 request as the door reads it. Method is
// initialize, tools/list, tools/call or ping; Params carries the method's own
// arguments — for tools/call, {"name": <tool>, "arguments": {"op": …, "input": …}}.
type MCPRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// MCPResponse is the door's answer: a result or an error, never both.
type MCPResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
	Error   *MCPError      `json:"error,omitempty"`
}

// MCPError is a JSON-RPC 2.0 error object.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func init() {
	Describe(door.Path, http.MethodPost,
		"The agent door: every subsystem's operations as MCP tools",
		"Model Context Protocol over JSON-RPC 2.0 — one POST per message, stateless, "+
			"protocol revision 2026-07-28. tools/list answers without a credential with one tool "+
			"per subsystem (its operations in the \"op\" enum) plus \"describe\", which returns one "+
			"operation's input schema. tools/call names a subsystem tool and carries "+
			"{\"op\": <operation>, \"input\": <its arguments>}; it takes the same bearer the REST "+
			"API does, and a call that carries none is answered 401 with a WWW-Authenticate header "+
			"naming the resource metadata at "+door.Metadata+", which names the "+
			"authorization server to sign in at. The tool surface is the public contract: the "+
			"operator's admin product is not offered, and a name that would disclose a secret or "+
			"mutate an identity is withheld — the list says how many, under _meta.")
	Register(door.Path, http.MethodPost, MCPRequest{}, MCPResponse{})
}

// mountDoor puts the agent door on core's throwaway router so Spec projects it.
// The handler is never reached: the real door is the host's.
func mountDoor(app *zip.App) {
	app.Post(door.Path, func(*zip.Ctx) error {
		return zip.ErrInternal("the document's stub for the agent door — the host serves it")
	})
}
