package zzmeasure

// Measures the production arrangement of every app process that is NOT flags:
// apps/flags is LINKED (imported) but flags.Mount was never called, exactly as in
// plugin/entitlement, plugin/allowance, plugin/ai, plugin/affiliate,
// plugin/admission and plugin/admin.

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/flags"
)

func TestUnmountedFlagsAnswer(t *testing.T) {
	flags.Register(flags.Def{Key: "zz_probe", Category: "Launch", Type: flags.TypeBool, Default: "false"})

	// 1. the platform-switch READ
	t.Logf("flags.Bool(paywall_enforced) = %v", flags.Bool(cloud.SwitchPaywallEnforced))
	t.Logf("flags.Bool(paywall_strict)   = %v", flags.Bool(cloud.SwitchPaywallStrict))
	t.Logf("flags.Bool(zz_probe)         = %v", flags.Bool("zz_probe"))

	// 2. the platform-switch WRITE (admin cockpit's one write path)
	err := flags.SetPlatformSwitch("zz_probe", json.RawMessage(`{"key":"zz_probe","active":true}`), "z@hanzo.ai")
	t.Logf("flags.SetPlatformSwitch      = %v", err)

	// 3. the board the cockpit renders
	b := flags.Board()
	t.Logf("flags.Board().Configured     = %v  switches=%d", b.Configured, len(b.Switches))
	for _, s := range b.Switches {
		if s.Key == cloud.SwitchPaywallEnforced || s.Key == "zz_probe" {
			t.Logf("   switch %-20s value=%-6s source=%s", s.Key, s.Value, s.Source)
		}
	}

	// 4. the edge client serve.go's SpendGate reads
	t.Logf("cloud.Switch(paywall_enforced) = %v", cloud.Switch(cloud.SwitchPaywallEnforced))

	// 5. the experiments legs
	_, err = flags.Assign("acme", "", "zz_probe", "acme", nil)
	t.Logf("flags.Assign                 = %v", err)
	err = flags.PutDef("acme", "", "zz_probe", json.RawMessage(`{}`), "z@hanzo.ai")
	t.Logf("flags.PutDef                 = %v", err)
}
