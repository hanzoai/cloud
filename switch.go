package cloud

import "sync/atomic"

// Platform switches, read from the edge.
//
// clients/flags owns evaluation and imports THIS package (cloud.Deps, Handle,
// OrgStore, …). serve.go mounts the edge middleware and therefore imports
// routers. So the root package can never import flags — and without a seam, no
// filter mounted from serve.go can read a switch at all. That is not a
// theoretical gap: the `paywall_enforced` switch was registered in the cockpit
// and governed only clients/entitlements.RequireProduct, a leaf free to import
// flags, while the paywall serve.go actually mounts stayed env-gated. Flipping
// the switch in admin.hanzo.ai changed nothing.
//
// The seam inverts at the direction the dependency already runs: flags calls
// SetSwitchReader on mount, the root package calls Switch. One function in, one
// function out, no new import in either direction.

// SwitchPaywallEnforced gates the subscription paywall. The key lives here so the
// registry entry (clients/entitlements), the evaluator (clients/flags) and the
// edge (serve.go) all name the same string — a switch whose readers disagree
// about its key is worse than no switch.
const SwitchPaywallEnforced = "paywall_enforced"

// switchReader is the flag engine's Bool, installed by clients/flags.Mount.
// atomic because Mount runs during boot while requests may already be served.
var switchReader atomic.Pointer[func(string) bool]

// SetSwitchReader installs the platform-switch evaluator. clients/flags calls it
// once on mount; passing nil detaches it (used by tests).
func SetSwitchReader(f func(string) bool) {
	if f == nil {
		switchReader.Store(nil)
		return
	}
	switchReader.Store(&f)
}

// Switch reports a platform switch's live value. It returns false before the flag
// engine mounts and when no engine is present (!cgo), which is the same dark
// default the engine itself falls back to — so an unmounted switch never enforces.
func Switch(key string) bool {
	if p := switchReader.Load(); p != nil {
		return (*p)(key)
	}
	return false
}
