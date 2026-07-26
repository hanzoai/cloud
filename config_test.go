package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// TestEnabled_StagedSubsystemsExcludedFromMountAll locks the HIP-0106 staged-
// rollout contract in code: with an EMPTY Enable list ("mount everything"), a
// STAGED subsystem is NOT enabled — it mounts only when named in CLOUD_ENABLE
// explicitly — while every non-staged subsystem does mount.
//
// The iam/ingress pair is the load-bearing case, so each is pinned BY NAME rather
// than left to a list. ingress is staged: its Mount returns errors, and an error
// there aborts cloud's startup. iam is NOT staged: its Mount fails closed with a
// 503 and cannot take the process down, so the mount-all default serves identity
// from the in-process embed (clients/iam). Production runs exactly this default
// (CLOUD_ENABLE unset), so drift in either direction silently changes which
// identity plane answers /v1/iam/* and whether ingress can block a boot.
func TestEnabled_StagedSubsystemsExcludedFromMountAll(t *testing.T) {
	// Empty Enable = mount-all default.
	c := &cloud.Config{}

	if !c.Enabled("iam") {
		t.Error("iam is not staged: it must be enabled under the empty (mount-all) default")
	}
	if c.Enabled("ingress") {
		t.Error("ingress is staged: it must NOT be enabled under the empty (mount-all) default — it requires explicit CLOUD_ENABLE")
	}
	// Every non-staged subsystem mounts under the empty default.
	for _, name := range []string{"commerce", "ai", "kms", "treasury", "admin", "base", "o11y"} {
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

// TestEnabled_StagedActivatesAdditively proves the additive lever: EnableStaged
// turns a staged subsystem on WITHOUT collapsing the non-staged default into an
// allowlist. With an empty Enable and EnableStaged=["iam"], iam is on AND every
// non-staged subsystem stays on (the faithful "prod default + iam" shape) — while
// a sibling staged subsystem the deployment did NOT name stays off.
func TestEnabled_StagedActivatesAdditively(t *testing.T) {
	c := &cloud.Config{EnableStaged: []string{"iam"}}

	if !c.Enabled("iam") {
		t.Error("iam must be enabled via the additive EnableStaged lever")
	}
	// The non-staged default is undisturbed: everything else is still on.
	for _, name := range []string{"ai", "commerce", "billing", "console", "kms"} {
		if !c.Enabled(name) {
			t.Errorf("%s must stay enabled (EnableStaged must not collapse the default to an allowlist)", name)
		}
	}
	// A staged subsystem NOT named stays off — additive activation never widens
	// beyond what was asked.
	if c.Enabled("ingress") {
		t.Error("ingress was not named in EnableStaged; it must stay disabled")
	}
}
