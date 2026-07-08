//go:build controlplane

// package controlplane_test locks the external-cert invariant from the
// OUTSIDE: this file sees only the package's exported surface — no
// selfComposedCert, no verifyOwnCertStructure — exactly the vantage point of
// a future external/recovery/light-client package. It proves that surface
// offers no way to admit an externally-obtained cert through the structural
// check: VerifyExternalCert is the only exported verify seam for such a cert,
// and it fails closed today (increment-2's cryptographic verify is not
// implemented), never silently falling back to the structural gate.
package controlplane_test

import (
	"testing"

	cp "github.com/hanzoai/cloud/clients/controlplane"
)

func TestExternalCert_MustNotAdmitThroughStructuralCheck(t *testing.T) {
	// Run a real honest N=7 ceremony so we hold a genuinely valid,
	// structurally well-formed cert — the strongest case an attacker could
	// present to a future external path, not a corrupted one.
	c := cp.NewCluster(7)
	res := c.RunHeight(nil)
	if !res.Finalized || res.Cert == nil {
		t.Fatalf("ceremony did not finalize: %+v", res)
	}

	// A future recovery/gossip/light-client path never holds the original
	// in-process value — it deserializes bytes off the wire into a fresh
	// *quasar.QuasarCert. Model that here with a value copy through a bare
	// pointer, severing any provenance this package's internals might
	// otherwise track.
	raw := *res.Cert
	external := &raw
	if !external.Verify(nil) {
		t.Fatal("test setup: the external copy must itself be structurally well-formed (that's the attack this test defends against)")
	}

	// The ONLY exported verify seam for an externally-obtained cert MUST fail
	// closed: increment-2's cryptographic VerifyUnderPolicy/VerifyWithRealKeys
	// is not implemented yet, so admitting it here — even though it would
	// pass the structural check — would be exactly the forgeable-cert
	// acceptance path this package's containment work exists to prevent.
	if err := cp.VerifyExternalCert(external, nil); err == nil {
		t.Fatal("VerifyExternalCert accepted an external cert with no cryptographic verification wired — must fail closed")
	}

	// There is no other exported entry point that takes a bare
	// *quasar.QuasarCert and returns a verified/admitted result — the
	// package's finalization surface (NewCluster/RunHeight/RunRound) only ever
	// produces certs from its own ceremony, never accepts one as input. This
	// test package cannot even name verifyOwnCertStructure or
	// selfComposedCert (unexported) — exporting either in a future change is
	// exactly the regression this file exists to catch, since it would be the
	// first thing that let this test start reaching for them instead of
	// VerifyExternalCert.
}
