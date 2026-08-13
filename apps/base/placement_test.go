// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import "testing"

// A Base is embedded SQLite, and that is what every Base here is today. What
// these pin is that the question now HAS somewhere to be answered: the host can
// name a server per tenant, and the answer reaches the config Base is opened
// with. Without this the capability is present in the library and unreachable
// from the one process that pools a Base per tenant.

// TestPlacement_EmbeddedUnlessAHostSaysOtherwise: no placement is the answer
// every Base gets today, and it must stay the embedded one — a DSN that appears
// on its own would move a tenant's data off its file.
func TestPlacement_EmbeddedUnlessAHostSaysOtherwise(t *testing.T) {
	defer SetPlacement(nil)
	SetPlacement(nil)

	cfg := appConfig("/tmp/x", "acme")
	if cfg.DataDSN != "" || cfg.AuxDSN != "" {
		t.Fatalf("no placement must leave a Base embedded, got data=%q aux=%q", cfg.DataDSN, cfg.AuxDSN)
	}
	if cfg.DefaultDataDir != "/tmp/x" {
		t.Fatalf("data dir = %q, want /tmp/x", cfg.DefaultDataDir)
	}
	if !cfg.HideStartBanner {
		t.Fatal("HideStartBanner must stay set: a pooled Base prints no banner")
	}
}

// TestPlacement_AnsweredPerTenant: the host is asked with the org, so two orgs
// can be placed differently and one of them can still be embedded. This is the
// whole point of asking per tenant rather than per process.
func TestPlacement_AnsweredPerTenant(t *testing.T) {
	defer SetPlacement(nil)

	var asked []string
	SetPlacement(func(org string) (string, string) {
		asked = append(asked, org)
		if org == "acme" {
			return "postgres://data/acme", "postgres://aux/acme"
		}
		return "", ""
	})

	if cfg := appConfig("/tmp/a", "acme"); cfg.DataDSN != "postgres://data/acme" || cfg.AuxDSN != "postgres://aux/acme" {
		t.Fatalf("placed org: data=%q aux=%q", cfg.DataDSN, cfg.AuxDSN)
	}
	if cfg := appConfig("/tmp/b", "other"); cfg.DataDSN != "" || cfg.AuxDSN != "" {
		t.Fatalf("unplaced org must stay embedded: data=%q aux=%q", cfg.DataDSN, cfg.AuxDSN)
	}
	if len(asked) != 2 || asked[0] != "acme" || asked[1] != "other" {
		t.Fatalf("the host is asked once per Base, with the org: %v", asked)
	}
}

// TestPlacement_ThePlatformBaseIsNoTenant: the platform's own Base asks with the
// empty org. A host that places only tenants must be able to tell it apart, and
// the empty org is already refused wherever a tenant is expected.
func TestPlacement_ThePlatformBaseIsNoTenant(t *testing.T) {
	defer SetPlacement(nil)

	var asked []string
	SetPlacement(func(org string) (string, string) {
		asked = append(asked, org)
		if org == "" {
			return "postgres://data/platform", ""
		}
		return "postgres://data/tenant", ""
	})

	if cfg := appConfig("/tmp/p", ""); cfg.DataDSN != "postgres://data/platform" {
		t.Fatalf("platform Base data DSN = %q", cfg.DataDSN)
	}
	if len(asked) != 1 || asked[0] != "" {
		t.Fatalf("platform Base must ask with the empty org: %v", asked)
	}
}
