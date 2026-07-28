package cloud

import (
	"testing"
)

// index installs a boot snapshot for one test and restores the previous one after,
// so these tests can share the package-level index without ordering coupling.
func index(t *testing.T, cfg *Config, specs ...MountSpec) {
	t.Helper()
	prev := subsystems.Load()
	t.Cleanup(func() { subsystems.Store(prev) })
	indexSubsystems(specs, cfg)
}

// TestMountPrefixesIsTheOneRule proves the /v1/<name> fallback and that a declared
// set is taken verbatim. scope's middleware gate and the span index share this, so a
// regression here mislabels spans AND misplaces middleware.
func TestMountPrefixesIsTheOneRule(t *testing.T) {
	got := MountPrefixes("kms", nil)
	if len(got) != 1 || got[0] != "/v1/kms" {
		t.Fatalf("undeclared prefixes = %v, want [/v1/kms]", got)
	}
	declared := []string{"/v1/chat", "/v1/messages"}
	got = MountPrefixes("ai", declared)
	if len(got) != 2 || got[0] != "/v1/chat" || got[1] != "/v1/messages" {
		t.Fatalf("declared prefixes = %v, want %v", got, declared)
	}
}

// TestSubsystemOfPrefersTheLongestPrefix is the correctness property the whole board
// rests on: a nested surface must not be attributed to its parent.
func TestSubsystemOfPrefersTheLongestPrefix(t *testing.T) {
	index(t, &Config{},
		MountSpec{Name: "admin"},
		MountSpec{Name: "infra", Prefixes: []string{"/v1/admin/infra"}},
		MountSpec{Name: "kms"},
	)

	for _, tc := range []struct{ path, want string }{
		{"/v1/admin", "admin"},
		{"/v1/admin/o11y", "admin"},
		{"/v1/admin/infra", "infra"},
		{"/v1/admin/infra/droplets", "infra"},
		{"/v1/kms/secrets", "kms"},
		{"/v1/kmsx", ""},      // sibling, not a child — must not match /v1/kms
		{"/v1/unmounted", ""}, // nothing owns it
		{"/healthz", ""},      // non-/v1
	} {
		if got := SubsystemOf(tc.path); got != tc.want {
			t.Errorf("SubsystemOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestDisabledSubsystemIsListedButOwnsNoPath proves the two halves of the enablement
// contract: the board still SEES a switched-off subsystem (that is the answer to "why
// is this empty"), but it claims no prefix, so its path is not attributed to it.
func TestDisabledSubsystemIsListedButOwnsNoPath(t *testing.T) {
	// Enable is an explicit allowlist, so naming only "kms" disables "ads".
	index(t, &Config{Enable: []string{"kms"}},
		MountSpec{Name: "kms"},
		MountSpec{Name: "ads"},
	)

	all := Subsystems()
	if len(all) != 2 {
		t.Fatalf("inventory = %d subsystems, want 2 (a disabled one must still be listed)", len(all))
	}
	// Sorted by name: ads, kms.
	if all[0].Name != "ads" || all[0].Enabled {
		t.Errorf("ads = %+v, want listed and disabled", all[0])
	}
	if all[1].Name != "kms" || !all[1].Enabled {
		t.Errorf("kms = %+v, want listed and enabled", all[1])
	}
	if got := SubsystemOf("/v1/ads/x"); got != "" {
		t.Errorf("SubsystemOf(/v1/ads/x) = %q, want \"\" — a disabled subsystem serves nothing", got)
	}
	if got := SubsystemOf("/v1/kms/x"); got != "kms" {
		t.Errorf("SubsystemOf(/v1/kms/x) = %q, want kms", got)
	}
}

// TestSubsystemsSnapshotIsIsolated proves a caller cannot disturb the index that every
// request reads.
func TestSubsystemsSnapshotIsIsolated(t *testing.T) {
	index(t, &Config{}, MountSpec{Name: "kms"})
	got := Subsystems()
	got[0].Name = "mutated"
	if again := Subsystems(); again[0].Name != "kms" {
		t.Fatalf("mutating the returned slice changed the index: %q", again[0].Name)
	}
}

// TestSubsystemOfWithoutIndex proves the un-mounted case (a test or entrypoint that
// never calls MountAll) is label-absent rather than a panic or a wrong guess.
func TestSubsystemOfWithoutIndex(t *testing.T) {
	prev := subsystems.Load()
	t.Cleanup(func() { subsystems.Store(prev) })
	subsystems.Store(nil)

	if got := SubsystemOf("/v1/kms"); got != "" {
		t.Fatalf("SubsystemOf on an unbuilt index = %q, want \"\"", got)
	}
	if got := Subsystems(); len(got) != 0 {
		t.Fatalf("Subsystems on an unbuilt index = %v, want empty", got)
	}
}
