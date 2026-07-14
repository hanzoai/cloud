package agentskills

import (
	"context"
	"encoding/json"
	"io/fs"
	"path"

	"github.com/hanzoai/cloud/clients/tools"
)

// skillToolProvider surfaces the deployment brand's agent skills into the unified
// tool plane as SourceSkill entries. Skills are DISCOVERY + ACTIVATION metadata —
// hanzo.chat / hanzo.app toggle them per org (task 5) and attach them to agents —
// so they are listed but NOT directly dispatchable (Dispatch → ErrNotDispatchable).
type skillToolProvider struct {
	fsys  fs.FS
	brand string
}

func (skillToolProvider) Source() tools.Source { return tools.SourceSkill }

func (p skillToolProvider) List(_ context.Context, _ tools.Scope) ([]tools.Tool, error) {
	b, err := fs.ReadFile(p.fsys, path.Join(p.brand, "index.json"))
	if err != nil {
		return nil, nil // no catalogue for this brand — an empty skill set, not an error.
	}
	var idx struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	if json.Unmarshal(b, &idx) != nil {
		return nil, nil
	}
	out := make([]tools.Tool, 0, len(idx.Skills))
	for _, sk := range idx.Skills {
		out = append(out, tools.Tool{
			Name:         "skill_" + sk.Name,
			Source:       tools.SourceSkill,
			Description:  sk.Description,
			Dispatchable: false,
		})
	}
	return out, nil
}

func (skillToolProvider) Dispatch(_ context.Context, _ tools.Principal, _ string, _ map[string]any) (any, error) {
	return nil, tools.ErrNotDispatchable
}
