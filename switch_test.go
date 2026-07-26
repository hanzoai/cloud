package cloud

import "testing"

// Before an engine mounts — and on a !cgo build where none exists — a switch must
// read false. An unmounted switch that reported true would enforce a paywall
// nobody turned on.
func TestSwitch_DarkUntilAnEngineMounts(t *testing.T) {
	SetSwitchReader(nil)
	if Switch(SwitchPaywallEnforced) {
		t.Fatal("Switch = true with no reader installed; want false")
	}
}

// The seam exists so the EDGE can read a switch the engine owns: flags.Mount calls
// SetSwitchReader, and from then on the value the engine reports is the value the
// edge sees — including flipping back off, which is the kill switch.
func TestSwitch_ReadsThroughTheInstalledReader(t *testing.T) {
	t.Cleanup(func() { SetSwitchReader(nil) })

	on := false
	var asked string
	SetSwitchReader(func(key string) bool { asked = key; return on })

	if Switch(SwitchPaywallEnforced) {
		t.Fatal("want false while the engine reports false")
	}
	if asked != SwitchPaywallEnforced {
		t.Fatalf("reader asked for %q, want %q", asked, SwitchPaywallEnforced)
	}

	on = true
	if !Switch(SwitchPaywallEnforced) {
		t.Fatal("want true once the engine reports true — the flip did not reach the edge")
	}

	on = false
	if Switch(SwitchPaywallEnforced) {
		t.Fatal("want false again — turning the switch back off must restore access")
	}

	SetSwitchReader(nil)
	if Switch(SwitchPaywallEnforced) {
		t.Fatal("want false after detach")
	}
}
