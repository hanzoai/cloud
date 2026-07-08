package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// TestEnabled_StagedSubsystemsExcludedFromMountAll locks the HIP-0106 staged-
// rollout contract in code: with an EMPTY Enable list ("mount everything"), a
// STAGED subsystem (iam) is NOT enabled — it mounts only when named in
// CLOUD_ENABLE explicitly, while every non-staged subsystem still mounts.
//
// This is the guard that keeps the IAM embed (iamserver.InitEmbed, which boots
// process-global Beego config the `ai` subsystem shares) from booting under the
// mount-all default and crashing the binary at `ai` bootstrap — the boot-smoke
// failure that pinned the fleet to a pre-embed image since #142.
func TestEnabled_StagedSubsystemsExcludedFromMountAll(t *testing.T) {
	// Empty Enable = mount-all default.
	c := &cloud.Config{}

	if c.Enabled("iam") {
		t.Error("iam must NOT be enabled under the empty (mount-all) default — it is staged and requires explicit CLOUD_ENABLE")
	}
	// Non-staged subsystems still mount under the empty default.
	for _, name := range []string{"ai", "kms", "commerce", "treasury", "admin", "base", "o11y"} {
		if !c.Enabled(name) {
			t.Errorf("non-staged subsystem %q must be enabled under the empty (mount-all) default", name)
		}
	}
}

// TestEnabled_StagedSubsystemActivatesWhenNamed proves the ONE activation path:
// naming a staged subsystem explicitly in Enable turns it on (and an explicit
// list still gates everything else the same way).
func TestEnabled_StagedSubsystemActivatesWhenNamed(t *testing.T) {
	c := &cloud.Config{Enable: []string{"iam", "ai"}}

	if !c.Enabled("iam") {
		t.Error("iam must be enabled once named explicitly in Enable")
	}
	if !c.Enabled("ai") {
		t.Error("ai must be enabled when named explicitly in Enable")
	}
	// An explicit list is an allowlist: an unnamed subsystem is disabled,
	// staged or not.
	if c.Enabled("commerce") {
		t.Error("commerce is not in the explicit Enable list; it must be disabled")
	}
}
