package automations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud/clients/tools"
)

// connectorToolProvider registers every connector action into the unified tool
// plane (clients/tools) as a SourceConnector tool. It reuses the SAME resolution +
// dispatch the /v1/automations/mcp endpoint uses (resolveTool → lookupAction →
// Action.Run, RunContext.Token pinned to the caller's validated org), so there is
// ONE connector-dispatch path — the tool plane composes it, never re-implements it.
// The plane owns activation, pricing, metering + audit; this provider owns only the
// connector-specific list + run.
type connectorToolProvider struct{}

func (connectorToolProvider) Source() tools.Source { return tools.SourceConnector }

// List is the connector catalogue as tool.Tools, reusing propsToSchema (the ONE
// PropSpec→JSON-Schema mapping). Connectors are catalogue-wide; the plane's
// activation gate decides which an org may actually call.
func (connectorToolProvider) List(_ context.Context, _ tools.Scope) ([]tools.Tool, error) {
	var out []tools.Tool
	for _, c := range sortedConnectors() {
		for _, a := range sortedActions(c) {
			schema, _ := json.Marshal(propsToSchema(a.Props))
			out = append(out, tools.Tool{
				Name:         c.Name + "_" + a.Name,
				Source:       tools.SourceConnector,
				Description:  a.Description,
				Schema:       schema,
				Dispatchable: true,
			})
		}
	}
	return out, nil
}

// Dispatch runs a connector action bound to the principal's validated org, with the
// per-org concurrency bound and the KMS-sealed token source the durable + MCP paths
// already use.
func (connectorToolProvider) Dispatch(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error) {
	connector, action, ok := resolveTool(name)
	if !ok {
		return nil, tools.ErrUnknownTool
	}
	_, act, err := lookupAction(connector, action)
	if err != nil {
		return nil, tools.ErrUnknownTool
	}
	if !orgRunLimiter.acquire(p.Org) {
		return nil, fmt.Errorf("too many concurrent tool calls for this org")
	}
	defer orgRunLimiter.release(p.Org)
	return act.Run(ctx, RunContext{
		Org:   p.Org,
		Input: args,
		Token: func(secretName string) ([]byte, error) {
			return tokenSource(ctx, p.Org, connector, secretName)
		},
	})
}
