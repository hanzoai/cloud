// Package seed — one-shot seed helpers for local dev.
//
// Nothing in this package runs automatically; callers must opt in by (a)
// importing it and (b) setting COMMERCE_SEED_DEV=1. Production
// deployments MUST NOT run seeds — tenant rows in prod are created through
// the admin API at deploy time, with full audit trail.
package seed

import (
	"errors"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/clients/commerce/store"
)

// SeedDev creates a demo tenant row with devnet hostnames iff the gate env
// var is set. It is idempotent: a second call when the tenant already
// exists returns nil.
//
// Usage (from commerced main, only after store.New succeeds):
//
//	if err := seed.SeedDev(s); err != nil {
//	    log.Fatal(err)
//	}
func SeedDev(s *store.Store) error {
	if os.Getenv("COMMERCE_SEED_DEV") != "1" {
		return nil // gate closed — no-op
	}
	if s == nil || s.Tenants == nil {
		return errors.New("seed: nil store")
	}

	// Check for an existing demo tenant and short-circuit if found.
	tenants, err := s.Tenants.List(500, 0)
	if err != nil {
		return fmt.Errorf("seed: list tenants: %w", err)
	}
	for _, t := range tenants {
		if t.Name == "demo" {
			return nil // already seeded
		}
	}

	t := &store.Tenant{
		Name: "demo",
		Hostnames: []string{
			"pay.dev.example.com",
			"pay.test.example.com",
		},
		Brand: store.BrandConfig{
			DisplayName:  "Demo Tenant",
			LogoURL:      "https://cdn.example.com/demo.png",
			PrimaryColor: "#0ea5e9",
		},
		IAM: store.IAMConfig{
			Issuer:   "https://hanzo.id",
			ClientID: "demo-exchange-client-id",
		},
		IDV: store.IDVConfig{
			Provider: "persona",
			Endpoint: "https://withpersona.com/verify",
		},
		// No credentials in this row — providers[].kms_path references
		// are set by the credential-upload admin flow.
		Providers: []store.Provider{
			{Name: "square", Enabled: true, KMSPath: "commerce/demo/square"},
			{Name: "stripe", Enabled: false, KMSPath: "commerce/demo/stripe"},
		},
		BDEndpoint: "https://bd.dev.example.com",
		ReturnURLAllowlist: []string{
			"https://dev.example.com",
			"https://test.example.com",
		},
	}

	if err := s.Tenants.Create(t); err != nil {
		return fmt.Errorf("seed: create demo: %w", err)
	}
	return nil
}
