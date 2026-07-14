package agents

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/clients/tools"
)

// agentToolProvider registers an org's agents into the unified tool plane as
// SourceAgent tools (name "agent_<name>"), callable with a text input. It reuses
// the SAME in-process seams other subsystems use — ListForOrg to list, RunOnBehalf
// to run bound to the caller's org + user (which bills the agent's org and records
// the run). The plane owns activation, pricing, metering + audit.
type agentToolProvider struct{}

var agentSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"the agent prompt / task"}},"required":["input"]}`)

func (agentToolProvider) Source() tools.Source { return tools.SourceAgent }

func (agentToolProvider) List(ctx context.Context, scope tools.Scope) ([]tools.Tool, error) {
	if scope.Org == "" {
		return nil, nil
	}
	list, err := ListForOrg(ctx, scope.Org)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Tool, 0, len(list))
	for _, a := range list {
		desc := a.Description
		if desc == "" {
			desc = "agent " + a.Name
		}
		out = append(out, tools.Tool{
			Name:         "agent_" + a.Name,
			Source:       tools.SourceAgent,
			Description:  desc,
			Schema:       agentSchema,
			Dispatchable: true,
		})
	}
	return out, nil
}

func (agentToolProvider) Dispatch(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error) {
	ref := strings.TrimPrefix(name, "agent_")
	input, _ := args["input"].(string)
	run, err := RunOnBehalf(ctx, p.Org, p.User, ref, input)
	if err != nil {
		return nil, err
	}
	return run, nil
}
