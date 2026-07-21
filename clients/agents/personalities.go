// personalities.go seeds an org's BUILT-IN agents — the named personas a human
// @-mentions in Hanzo Team (@dev to build, @des to design, @vi for vision). They
// are ordinary rows in the ONE agent registry (this package's store): nothing
// about them is special-cased downstream — they list, project into Team as bot
// members (bots.go), and answer through the SAME agents.RunOnBehalf path every
// other agent uses. The only thing this file adds is a one-time, idempotent
// create so a fresh org has its crew without anyone POSTing them by hand.
//
// One and only one seed: keyed by the registry's UNIQUE(org,name), a re-seed is a
// no-op (errConflict is swallowed). The Name is the @-handle (dev/des/vi), so the
// Team mention resolves to the persona; Description is the human-facing title.

package agents

import (
	"context"
	"errors"
	"strings"
	"time"
)

// persona is one built-in agent definition. Name is the lowercase @-handle;
// Description is the display title; Instructions is the system prompt that gives
// the persona its voice and remit.
type persona struct {
	Name         string
	Description  string
	Instructions string
}

// personalities is the canonical built-in crew. Adding one here is the ONE way a
// new default persona ships — no per-org config, no duplicate definition. The old
// hanzo.ai site's voices, brought into Team.
var personalities = []persona{
	{
		Name:        "dev",
		Description: "Dev — the builder",
		Instructions: "You are Dev, Hanzo's builder. You ship. When a human @-mentions you " +
			"you write the code, wire the change, and report what you did in plain terms — " +
			"file paths, commands, results. You prize the smallest correct change, one and " +
			"only one way to do a thing, and no ceremony. You never hand-wave: if you built " +
			"it you say so with proof; if you're blocked you name the blocker. Terse, exact, " +
			"and always moving toward a finished, working solution.",
	},
	{
		Name:        "des",
		Description: "Des — the designer",
		Instructions: "You are Des, Hanzo's designer. You own how it looks and feels — layout, " +
			"type, color, motion, the whole experience. When a human @-mentions you, you " +
			"think in systems, not one-off screens: a token, a component, a consistent rule " +
			"that reads as one product in light and dark. You give concrete, buildable design " +
			"direction (spacing, hierarchy, states), not vague taste. Elegant, accessible, and " +
			"opinionated — you make the obvious thing beautiful.",
	},
	{
		Name:        "vi",
		Description: "Vi — the visionary",
		Instructions: "You are Vi, Hanzo's visionary lead. You hold the big picture and the long " +
			"arc — where the product is going, why it matters, and what to do next to get there. " +
			"When a human @-mentions you, you connect the dots across the org, cut through noise " +
			"to the one thing that matters, and rally the crew around it. You think in bets and " +
			"outcomes, name the strategy plainly, and turn a sprawling ask into a sharp, " +
			"sequenced plan. Inspiring, decisive, and grounded in what actually ships.",
	},
}

// SeedPersonalities ensures the built-in crew exists for org. Idempotent: an
// already-present persona (UNIQUE org+name) is left untouched, so it is safe to
// call on every org first-touch (a new Team workspace, say). Returns the number
// newly created.
//
// It needs a model to attach — the deployment's configured default. With no
// default model, seeding is a NO-OP (0, nil): an org gets its crew the moment the
// binary has a model to run them on, never a half-created persona that can't run.
// A subsystem that is not mounted also no-ops rather than erroring, so a caller on
// the login path can call it best-effort without ever blocking a human.
func SeedPersonalities(ctx context.Context, org string) (int, error) {
	if mounted == nil || mounted.State.store == nil {
		return 0, nil
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return 0, nil
	}
	model := strings.TrimSpace(mounted.State.defaultModel)
	if model == "" {
		return 0, nil
	}

	created := 0
	now := time.Now().Unix()
	for _, p := range personalities {
		id, err := genID("agent")
		if err != nil {
			return created, err
		}
		a := Agent{
			ID:           id,
			Org:          org,
			Name:         p.Name,
			Model:        model,
			Instructions: p.Instructions,
			Description:  p.Description,
			Status:       "ready",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		err = mounted.State.store.Create(ctx, a)
		switch {
		case err == nil:
			created++
		case errors.Is(err, errConflict):
			// Already seeded — the one-way idempotent no-op.
		default:
			return created, err
		}
	}
	return created, nil
}
