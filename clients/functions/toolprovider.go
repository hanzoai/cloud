package functions

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/clients/tools"
)

// functionToolProvider registers an org's user functions into the unified tool
// plane as SourceFunction tools (name "function_<name>"). It reuses the SAME
// org-scoped store + sandbox exec path invoke uses — the plane owns activation,
// pricing, metering + audit; this provider owns only the function-specific list +
// run. A function's argument surface is untyped ({input:string}), so the schema is
// the one-field input contract the sandbox consumes.
type functionToolProvider struct{}

var functionSchema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"stdin passed to the function"}}}`)

func (functionToolProvider) Source() tools.Source { return tools.SourceFunction }

func (functionToolProvider) List(ctx context.Context, scope tools.Scope) ([]tools.Tool, error) {
	if mounted == nil || scope.Org == "" {
		return nil, nil
	}
	store, err := storeFor(mounted, scope.Org)
	if err != nil {
		return nil, err
	}
	fns, err := store.List(ctx, scope.Org)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Tool, 0, len(fns))
	for _, f := range fns {
		out = append(out, tools.Tool{
			Name:         "function_" + f.Name,
			Source:       tools.SourceFunction,
			Description:  "user function (" + f.Runtime + ")",
			Schema:       functionSchema,
			Dispatchable: true,
		})
	}
	return out, nil
}

func (functionToolProvider) Dispatch(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error) {
	if mounted == nil {
		return nil, tools.ErrUnknownTool
	}
	fnName := strings.TrimPrefix(name, "function_")
	store, err := storeFor(mounted, p.Org)
	if err != nil {
		return nil, err
	}
	f, err := store.Get(ctx, p.Org, fnName)
	if err != nil {
		return nil, tools.ErrUnknownTool
	}
	input, _ := args["input"].(string)
	res, err := mounted.State.exec.run(ctx, f, input, f.TimeoutSec)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": res.Ok, "statusCode": res.StatusCode, "output": res.Output, "error": res.Errout}, nil
}
