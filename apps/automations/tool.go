package automations

// tool.go is the mapping between a connector ACTION and the ONE tool plane: the
// name a tool is known by, and the JSON Schema its arguments have.
//
// There is no MCP door here. Every connector action is published into the unified
// registry by connectorToolProvider (automations.go's tools.Register), which is
// where discovery (GET /v1/tools) and dispatch (POST /v1/tools/call) read it — and
// through that registry it reaches the fleet's ONE agent door. A second JSON-RPC
// envelope at /v1/automations/mcp used to serve the same catalogue from the same
// registry with its own schema derivation; it was a duplicate projection of one
// value, so it is gone rather than dark.

// propsToSchema derives a JSON-Schema object from an action's Props — the ONE
// mapping from PropSpec to the wire schema, shared by every tool.
func propsToSchema(props []PropSpec) map[string]any {
	properties := make(map[string]any, len(props))
	required := make([]string, 0, len(props))
	for _, p := range props {
		properties[p.Name] = map[string]any{"type": jsonType(p.Type), "description": p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

// jsonType normalizes a PropSpec type to a JSON-Schema type (default "string").
func jsonType(t string) string {
	switch t {
	case "number", "boolean", "object", "array", "string":
		return t
	default:
		return "string"
	}
}

// resolveTool maps a "<connector>_<action>" tool name back to its (connector,action)
// pair. Connector and action names both contain underscores, so an unambiguous
// resolution walks the registry rather than splitting the string.
func resolveTool(name string) (connector, action string, ok bool) {
	for cn, c := range registry {
		for an := range c.Actions {
			if cn+"_"+an == name {
				return cn, an, true
			}
		}
	}
	return "", "", false
}
