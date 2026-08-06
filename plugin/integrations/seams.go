package main

import (
	"context"

	"github.com/hanzoai/cloud/apps/automations"
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
	// The coding orchestrator is NOT wired here any more. It was, because coding
	// could not import git (git imports integrations, integrations called coding)
	// and so its clone-url/verify-ref seams had to arrive from a root that imports
	// both. Those seams are peer calls now — git is another PROCESS, and the
	// in-process functions this root passed answered ""/false there, which the
	// dispatcher reads as "git is not available". With no cycle left to dodge,
	// integrations builds its own Dispatcher at the trigger (slack_coding.go).

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
