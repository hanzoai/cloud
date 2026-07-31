package cloud_test

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// A non-empty Enable is an allowlist: it names exactly what mounts, and every
// subsystem is governed by the same rule — there is no set that needs a second
// lever to reach.
func TestEnabled_ExplicitListIsAnAllowlist(t *testing.T) {
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

// An empty Enable list mounts everything, with no subsystem held back. There used
// to be a staged set that an empty list skipped, on the rule that a subsystem was
// staged while its Mount could abort startup — and apps are their own processes
// now, so a Mount that fails takes down its own child and nothing else. The risk
// the exception existed for is structural, and the exception went with it.
func TestEnabled_EmptyListMountsEverything(t *testing.T) {
	c := &cloud.Config{}
	for _, name := range []string{"ai", "commerce", "billing", "kms", "iam", "ingress"} {
		if !c.Enabled(name) {
			t.Errorf("%s must be enabled when Enable is empty — there is no held-back set", name)
		}
	}
}
