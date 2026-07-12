// Copyright © 2026 Hanzo AI. MIT License.

package finance

import "sync"

// The process-wide in-process finance client — the ONE money seam a co-resident host
// publishes once at boot and every consumer (the ai prepaid gate's balance/usage
// hooks, the admin credit grant, the edge meter) resolves by the NARROW 3-method
// finance.Client it actually needs. This mirrors commerce's PublishEmbedded /
// currentEmbedded in-proc seam and deliberately AVOIDS threading a wide Deps bag
// through every Mount: a small, focused interface between systems, not a god-object.
var (
	currentMu sync.RWMutex
	current   Client
)

// Publish records the process-wide finance client. Called once at boot; nil clears.
func Publish(c Client) {
	currentMu.Lock()
	current = c
	currentMu.Unlock()
}

// Current returns the published finance client, or nil before Publish (a consumer
// treats nil as "money layer not co-resident" and fails closed).
func Current() Client {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}
