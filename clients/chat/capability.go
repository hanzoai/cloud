package chat

import (
	"encoding/json"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// capability is a named chat mode. It frames one tool-calling round: the system
// prompt that instructs the model, the builtin tool defs offered alongside the
// caller's and the org's registered tools, and whether the model's tool calls are
// EXECUTED server-side (registry Dispatch → actions) or handed back to the client
// as ops (a graph/UI mutation the server cannot perform). Adding a capability is
// one entry in the map below — no other file changes.
type capability struct {
	Name         string
	SystemPrompt string
	BuiltinTools []openai.Tool
	// ServerExecuted gates the tool-call split: when true, a call the tool registry
	// knows is dispatched server-side and reported in actions; when false the round
	// is advisory — every call is returned as an op for the client to apply.
	ServerExecuted bool
}

// defaultCapability is used when a request omits `capability`.
const defaultCapability = "graph"

// capabilities is the registry. Small and closed: more modes add cleanly here.
var capabilities = map[string]capability{
	"graph": {
		Name:           "graph",
		ServerExecuted: false,
		SystemPrompt: strings.TrimSpace(`
You are Hanzo Studio Copilot. You read and mutate a node workflow graph
(LiteGraph / ComfyUI-compatible). The user describes an outcome; you respond with
tool calls that describe the graph operations to apply. You never execute the
graph yourself — the Studio client applies each operation and re-queues the graph.
Reference nodes you create by the id you give them in add_node. Prefer the fewest
operations that achieve the request; explain briefly in text when helpful.`),
		BuiltinTools: []openai.Tool{
			toolDef("add_node", "Add a node to the graph.", `{
				"type":"object",
				"properties":{
					"id":{"type":"string","description":"caller label to reference this node in later ops"},
					"type":{"type":"string","description":"node class, e.g. KSampler"},
					"pos":{"type":"array","items":{"type":"number"},"description":"[x,y] canvas position"}
				},
				"required":["type"]
			}`),
			toolDef("set_widget", "Set a widget value on a node.", `{
				"type":"object",
				"properties":{
					"node_id":{"type":"string"},
					"name":{"type":"string","description":"widget name"},
					"value":{}
				},
				"required":["node_id","name","value"]
			}`),
			toolDef("set_prompt", "Set the prompt text on a node.", `{
				"type":"object",
				"properties":{"node_id":{"type":"string"},"text":{"type":"string"}},
				"required":["node_id","text"]
			}`),
			toolDef("connect", "Connect an output slot to an input slot.", `{
				"type":"object",
				"properties":{
					"from":{"type":"string","description":"source node id"},
					"from_slot":{"description":"output slot index or name"},
					"to":{"type":"string","description":"target node id"},
					"to_slot":{"description":"input slot index or name"}
				},
				"required":["from","to"]
			}`),
			toolDef("move_node", "Move a node to a new position.", `{
				"type":"object",
				"properties":{"node_id":{"type":"string"},"pos":{"type":"array","items":{"type":"number"}}},
				"required":["node_id","pos"]
			}`),
			toolDef("delete_node", "Remove a node from the graph.", `{
				"type":"object",
				"properties":{"node_id":{"type":"string"}},
				"required":["node_id"]
			}`),
			toolDef("layout", "Auto-arrange the graph.", `{"type":"object","properties":{}}`),
			toolDef("queue", "Queue the graph for execution.", `{"type":"object","properties":{}}`),
		},
	},
	"create": {
		Name:           "create",
		ServerExecuted: true,
		SystemPrompt: strings.TrimSpace(`
You are Hanzo Create, a fashion and product render assistant. You help the user
generate, edit, and refine product and fashion imagery. Use the tools available to
this workspace — the org's connected render and MCP services — to run and iterate
on renders. Call a tool when it advances the request; describe the result briefly.`),
		// No builtin tools: the callable set is the org's registered MCP/registry
		// tools, resolved per request and dispatched server-side.
		BuiltinTools: nil,
	},
}

// capabilityFor resolves a capability by name, defaulting an empty name to graph.
// ok is false for an unknown name so the handler answers 400 rather than guessing.
func capabilityFor(name string) (capability, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultCapability
	}
	c, ok := capabilities[name]
	return c, ok
}

// toolDef builds an OpenAI function tool from a JSON-Schema string. The schema is
// held as json.RawMessage so it reaches the model verbatim.
func toolDef(name, desc, schema string) openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        name,
			Description: desc,
			Parameters:  json.RawMessage(schema),
		},
	}
}
