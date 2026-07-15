package apps

import (
	"github.com/hanzoai/cloud/clients/coding"
	"github.com/hanzoai/cloud/clients/git"
	"github.com/hanzoai/cloud/clients/integrations"
)

// wire_seams.go wires cross-subsystem in-process seams that cannot be a MountSpec
// because they compose functions ACROSS packages that must not import each other.
//
// The coding orchestrator (clients/coding) needs git's CloneURL + VerifyRef, but
// clients/git imports clients/integrations (Slack-notify), and integrations calls
// coding — so coding -> git would cycle (integrations -> coding -> git ->
// integrations). The composition root is the ONE place that imports all three, so
// it assembles the coding Dispatcher here (git seams + agents/tracker/bot adapters)
// and injects it into the Slack trigger surface. init() runs once at load, before
// cloud.Serve; the git functions are plain reads that resolve their state at call
// time, so no mount ordering is required. The mirror-failure logger is nil (those
// failures are non-fatal and dropped).
func init() {
	integrations.SetCodingDispatcher(coding.NewDispatcher(git.CloneURL, git.VerifyRef, nil))
}
