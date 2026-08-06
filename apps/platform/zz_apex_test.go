package platform

import (
	"testing"

	"github.com/hanzoai/cloud/brand"
)

// The defect this fixes: selfGitHost was deps.Domain verbatim ("api.hanzo.ai"),
// and the forge is a SIBLING ("git.hanzo.ai"), so hostAllowed never matched and
// every native build was refused.
func TestSelfForgeIsAllowedFromTheApex(t *testing.T) {
	saved := selfGitHost
	defer func() { selfGitHost = saved }()
	selfGitHost = brand.Apex("api.hanzo.ai")
	if selfGitHost != "hanzo.ai" {
		t.Fatalf("brand.Apex(api.hanzo.ai) = %q, want hanzo.ai", selfGitHost)
	}
	for _, h := range []string{"git.hanzo.ai", "ci.hanzo.ai", "cd.hanzo.ai", "hanzo.ai"} {
		if !hostAllowed(h) {
			t.Errorf("hostAllowed(%q) = false — a deployment's own sibling must be a trusted build source", h)
		}
	}
	if hostAllowed("git.evil.com") {
		t.Error("hostAllowed(git.evil.com) = true — the apex must not widen trust beyond the deployment's own domain")
	}
}

func TestApexOfMultiLabelSuffix(t *testing.T) {
	if got := brand.Apex("api.example.co.uk"); got != "example.co.uk" {
		t.Errorf("brand.Apex = %q, want example.co.uk (never the bare suffix)", got)
	}
}

// RED proof: the OLD assignment (deps.Domain verbatim) refuses the forge.
func TestOldDomainVerbatimRefusedTheForge(t *testing.T) {
	saved := selfGitHost
	defer func() { selfGitHost = saved }()
	selfGitHost = "api.hanzo.ai" // what the code did before the apex reduction
	if hostAllowed("git.hanzo.ai") {
		t.Fatal("expected the old behaviour to REFUSE git.hanzo.ai — if this passes, the bug never existed")
	}
}
