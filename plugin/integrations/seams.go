package main

import (
	"context"

	"github.com/hanzoai/cloud/apps/automations"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/apps/git"
	"github.com/hanzoai/cloud/apps/integrations"
)

// The cross-subsystem seams integrations owns, wired in ITS OWN composition root.
//
// These used to live in package apps (wire_seams.go), which the whole fleet
// linked. That package is gone: a subsystem now runs as its own binary, so the
// seams it consumes are wired HERE, where integrations already links and where
// importing coding/git/automations makes no cycle (a main is a leaf). The linked
// packages import each other in ways that WOULD cycle — coding needs git, git
// needs integrations, integrations needs automations — which is exactly why the
// wiring lives at a composition root that imports all of them and none of them
// imports it. init() runs once at load, before cloud.Listen.
func init() {
	// The coding orchestrator needs git's CloneURL + VerifyRef, but clients/git
	// imports clients/integrations, so coding -> git would cycle. This root
	// imports all three and assembles the Dispatcher, injecting it into the Slack
	// trigger surface. The git functions are plain reads that resolve their state
	// at call time, so no mount ordering is required. The mirror-failure logger is
	// nil (those failures are non-fatal and dropped).
	integrations.SetCodingDispatcher(coding.NewDispatcher(git.CloneURL, git.VerifyRef, nil))

	// Inbound-event seam: a verified provider webhook (or chat channel) fires the
	// automations engine's Deliver here — the ONE place that imports both, so
	// integrations never has to import automations (which imports it, for
	// credential custody). A primitive-typed adapter keeps the seam free of the
	// automations types.
	integrations.SetAutomationTrigger(func(ctx context.Context, org, source, name, dedupeKey string, depth int, payload map[string]any) (int, error) {
		return automations.Deliver(ctx, org, automations.TriggerEvent{
			Source: source, Name: name, DedupeKey: dedupeKey, Depth: depth, Payload: payload,
		})
	})
}
