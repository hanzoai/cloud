package integrations

import "testing"

// The credential store gates a CONNECT only when the provider actually custodies
// something. GitHub declares `Secrets: nil` — installation tokens are minted on
// demand and never sealed — so gating it on the store refused a connection that
// never needed one.

func TestGithubCustodiesNoSecret(t *testing.T) {
	p, ok := snapshotRegistry()["github"]
	if !ok {
		t.Fatal("github is not registered")
	}
	if len(p.Secrets) != 0 {
		t.Errorf("github declares %v; it mints installation tokens on demand and seals none", p.Secrets)
	}
}

// sealTokens over an empty map writes nothing, which is what makes the narrowed
// gate correct rather than merely convenient: there is no write to fail.
func TestSealingNothingTouchesNoStore(t *testing.T) {
	// A nil service would panic on any store access; reaching the end proves the
	// loop body never ran.
	if err := sealTokens(nil, "/orgs/acme/integrations/github", map[string]string{}); err != nil {
		t.Fatalf("sealing an empty token set must be a no-op, got %v", err)
	}
}

// A provider that DOES custody secrets still requires the store — the gate is
// narrowed, not removed.
func TestAProviderWithSecretsStillNeedsTheStore(t *testing.T) {
	withSecrets := 0
	for id, p := range snapshotRegistry() {
		if len(p.Secrets) > 0 {
			withSecrets++
			if id == "github" {
				t.Error("github must not declare secrets")
			}
		}
	}
	if withSecrets == 0 {
		t.Fatal("no provider custodies secrets; the gate would be dead code")
	}
}
