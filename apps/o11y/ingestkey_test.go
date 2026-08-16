// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"testing"
)

// stub replaces both key spaces for one test and restores them after.
func stub(t *testing.T, projects, iam func(context.Context, string) (string, bool)) {
	t.Helper()
	p, i := projectKeys, iamKeys
	projectKeys, iamKeys = projects, iam
	t.Cleanup(func() { projectKeys, iamKeys = p, i })
}

func never(t *testing.T, who string) func(context.Context, string) (string, bool) {
	return func(context.Context, string) (string, bool) {
		t.Helper()
		t.Fatalf("%s was asked; it should not have been", who)
		return "", false
	}
}

func answers(org string) func(context.Context, string) (string, bool) {
	return func(context.Context, string) (string, bool) { return org, org != "" }
}

// THE DEFECT THIS CLOSES. A pk- key is minted by apps/projects, from crypto/rand,
// onto the project row — IAM has never seen it. Asking IAM about one is not a lookup
// that misses, it is a question about a different set, so the error endpoint refused
// every key that works everywhere else and accepted no event at all.
func TestAProjectsKeyResolvesThroughProjects(t *testing.T) {
	stub(t, answers("acme"), never(t, "IAM"))

	org, ok := ingestKeyOrg(context.Background(), "pk-live-whatever")
	if !ok || org != "acme" {
		t.Fatalf("ingestKeyOrg = (%q, %v), want (acme, true) from the app that minted the key", org, ok)
	}
}

// The fallback is a genuine org-scoped key, which IAM does own — so a key the
// projects space does not hold must still reach it.
func TestAnOrgKeyStillReachesIAM(t *testing.T) {
	stub(t, answers(""), answers("globex"))

	org, ok := ingestKeyOrg(context.Background(), "pk-org-key")
	if !ok || org != "globex" {
		t.Fatalf("ingestKeyOrg = (%q, %v), want (globex, true) via the IAM fallback", org, ok)
	}
}

// A key neither space holds is refused, and refused is a CLEAN answer — the caller
// answers 401 rather than attributing an event to a tenant nobody named.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	stub(t, answers(""), answers(""))

	if org, ok := ingestKeyOrg(context.Background(), "pk-nobody"); ok || org != "" {
		t.Fatalf("ingestKeyOrg = (%q, %v), want (\"\", false)", org, ok)
	}
}

// A projects outage reads as "not this space" and falls through, so an unreachable
// peer can make attribution ABSENT and never WRONG. It must not attribute the event
// to whatever IAM would say about a key that is not IAM's.
func TestAProjectsOutageDoesNotMisattribute(t *testing.T) {
	// The real projectKeyOrg answers ("", false) on any transport failure.
	asked := false
	stub(t, answers(""), func(_ context.Context, _ string) (string, bool) {
		asked = true
		return "", false
	})

	if _, ok := ingestKeyOrg(context.Background(), "pk-live-whatever"); ok {
		t.Fatal("an unreachable projects peer produced an attribution")
	}
	if !asked {
		t.Fatal("the fallback was not consulted, so a genuine org key would break during a projects outage")
	}
}

// With no plane peer running at all — the case every unit test and every
// single-app binary is in — the projects lookup answers cleanly rather than
// panicking or blocking the ingest path.
func TestProjectsLookupWithNoPeerIsClean(t *testing.T) {
	if org, ok := projectKeyOrg(context.Background(), "pk-live-whatever"); ok || org != "" {
		t.Fatalf("projectKeyOrg with no peer = (%q, %v), want (\"\", false)", org, ok)
	}
}
