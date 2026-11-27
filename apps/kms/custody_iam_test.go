package kms

import "testing"

// An organization's signing keys, which IAM seals under the org (apps/iam vault.go),
// are custodied by iam: the org's own people and admins never read or replace one.
func TestTheIAMSigningSubtreeIsCustodied(t *testing.T) {
	h, ok := custodyOfRef("orgs/acme/iam/signing/acme-signing")
	if !ok || h.app != "iam" {
		t.Fatalf("an org's signing key is not custodied by iam: %+v, %v", h, ok)
	}
	if h.sync {
		t.Fatal("an org's signing key is read by the org's own deployments")
	}
}
