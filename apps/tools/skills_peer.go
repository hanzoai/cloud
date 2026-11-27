package tools

import (
	"context"
	"errors"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
	skillspeer "github.com/hanzoai/cloud/client/skills"
	"github.com/hanzoai/cloud/sdk"
)

// The brand's skills, listed by the process that does not hold them.
//
// apps/skills embeds the brand's catalogue; this app serves /v1/tool. The fleet
// runs one process per app, so this plane asks the skills app for its catalogue
// (client.SkillsCatalog) and lists each entry as a skill an org can activate.
//
// The catalogue is fixed for a release, so one answer is kept for brandSkillsTTL
// and a new skills binary is picked up within that window after a deploy. A
// failed ask is not kept: the next listing asks again.

const (
	// brandSkillsTTL is how long one catalogue answer is reused.
	brandSkillsTTL = 5 * time.Minute
	// brandSkillsTimeout bounds one ask, including waking a cold skills app.
	brandSkillsTimeout = 15 * time.Second
)

// newBrandSkills is the source that asks the skills app over the plane.
func newBrandSkills(log luxlog.Logger) *brandSkills {
	return &brandSkills{ask: skillspeer.SkillsCatalog, log: log}
}

// brandSkills lists the deployment brand's skills as SourceSkill tools. Like an
// org's authored skills they are listed and activated, never dispatched.
type brandSkills struct {
	// ask is the plane call (skillspeer.SkillsCatalog); a test substitutes it.
	ask func(context.Context, *client.SkillCatalogIn) (*client.SkillCatalog, error)
	log luxlog.Logger

	mu    sync.Mutex
	tools []Tool
	at    time.Time
}

func (*brandSkills) Source() Source { return SourceSkill }

// List answers the cached catalogue, asking the skills app when it is absent or
// older than brandSkillsTTL. A deployment with no skills app lists none; any other
// failure is returned, so the registry skips this source for the one listing.
func (b *brandSkills) List(ctx context.Context, _ Scope) ([]Tool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tools != nil && time.Since(b.at) < brandSkillsTTL {
		return b.tools, nil
	}
	ctx, cancel := context.WithTimeout(ctx, brandSkillsTimeout)
	defer cancel()
	out, err := b.ask(ctx, &client.SkillCatalogIn{})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return nil, nil
	case err != nil:
		if b.log != nil {
			b.log.Warn("tools: brand skills unavailable", "err", err)
		}
		return nil, err
	case out == nil:
		return nil, errors.New("tools: the skills app answered nothing")
	}
	tools := make([]Tool, 0, len(out.Skills))
	for _, sk := range out.Skills {
		if sk.Name == "" {
			continue
		}
		tools = append(tools, Tool{
			Name:         "skill_" + sk.Name,
			Source:       SourceSkill,
			Description:  sk.Description,
			Dispatchable: false,
		})
	}
	b.tools, b.at = tools, time.Now()
	return tools, nil
}

func (*brandSkills) Dispatch(_ context.Context, _ Principal, _ string, _ map[string]any) (any, error) {
	return nil, ErrNotDispatchable
}
