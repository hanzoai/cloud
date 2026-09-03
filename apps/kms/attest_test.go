// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"errors"
	"testing"

	"github.com/zap-proto/zip"
)

func forbidden(t *testing.T, err error, why string) {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("%s: want 403, got %v", why, err)
	}
}

// The attestation decides; a stated org may narrow it and never widen it.
func TestAdmitScopesEveryReadByWhoTheKernelSaysIsCalling(t *testing.T) {
	acme := identity{Org: "acme", Account: "acme-api"}
	platform := identity{Org: "hanzo", Platform: true, Account: "cloud"}

	if err := admit("orgs/acme/db", "", acme, true); err != nil {
		t.Fatalf("a tenant reads its own: %v", err)
	}
	if err := admit("orgs/acme/db", "acme", acme, true); err != nil {
		t.Fatalf("a tenant stating itself: %v", err)
	}
	forbidden(t, admit("orgs/zeta/db", "", acme, true), "a tenant reading another tenant")
	forbidden(t, admit("orgs/zeta/db", "zeta", acme, true), "a tenant STATING another tenant")
	forbidden(t, admit("ingress/acme-seal", "", acme, true), "a tenant reading the deployment's")

	if err := admit("ingress/acme-seal", "", platform, true); err != nil {
		t.Fatalf("the platform reads the deployment's: %v", err)
	}
	if err := admit("orgs/hanzo/kms", "", platform, true); err != nil {
		t.Fatalf("the platform reads its own org: %v", err)
	}
	forbidden(t, admit("orgs/acme/db", "", platform, true), "the platform reading a tenant's")
	forbidden(t, admit("orgs/acme/db", "acme", platform, true), "the platform stating a tenant")

	forbidden(t, admit("orgs/acme/db", "acme", identity{}, false), "an unattested caller, whatever it states")
	forbidden(t, admit("ingress/acme-seal", "", identity{}, false), "an unattested caller on a deployment ref")

	if err := admit("", "", platform, true); err == nil {
		t.Fatal("an empty ref is a bad request")
	}
}

func TestFromPodReadsTenancyOffTheNamespace(t *testing.T) {
	if got := fromPod("tenant-acme", "acme-api"); got.Org != "acme" || got.Platform || got.Account != "acme-api" {
		t.Fatalf("tenant pod: %+v", got)
	}
	got := fromPod("hanzo", "cloud")
	if !got.Platform || got.Account != "cloud" || got.Org == "" {
		t.Fatalf("platform pod: %+v", got)
	}
	if got := fromPod("tenant-", "x"); !got.Platform {
		t.Fatalf("an empty tenant is not a tenant: %+v", got)
	}
}
