package payment

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
)

// The env fallback (cloud orgs with no per-org Square creds) must surface the
// PRODUCTION app id + location when SQUARE_ENVIRONMENT=production — the values
// the browser tokenizes against and commerce charges with.
func TestSquarePublicConfig_EnvProduction(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	t.Setenv("SQUARE_APPLICATION_ID", "sq0idp-PROD")
	t.Setenv("SQUARE_LOCATION_ID", "PRODLOC")

	got := SquarePublicConfig(&organization.Organization{})
	if got.ApplicationID != "sq0idp-PROD" || got.LocationID != "PRODLOC" {
		t.Errorf("app/location = %q/%q, want sq0idp-PROD/PRODLOC", got.ApplicationID, got.LocationID)
	}
	if got.Environment != "production" {
		t.Errorf("environment = %q, want production", got.Environment)
	}
}

// The SAME test-mode authority (SQUARE_ENVIRONMENT=sandbox) must select the
// SANDBOX public vars so the app id never straddles environments.
func TestSquarePublicConfig_EnvSandbox(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "sandbox")
	t.Setenv("SQUARE_APPLICATION_ID", "sq0idp-PROD")
	t.Setenv("SQUARE_LOCATION_ID", "PRODLOC")
	t.Setenv("SQUARE_SANDBOX_APPLICATION_ID", "sandbox-sq0idb-SBX")
	t.Setenv("SQUARE_SANDBOX_LOCATION_ID", "SBXLOC")

	got := SquarePublicConfig(&organization.Organization{})
	if got.ApplicationID != "sandbox-sq0idb-SBX" || got.LocationID != "SBXLOC" {
		t.Errorf("app/location = %q/%q, want sandbox pair", got.ApplicationID, got.LocationID)
	}
	if got.Environment != "sandbox" {
		t.Errorf("environment = %q, want sandbox", got.Environment)
	}
}

// SQUARE_ENVIRONMENT is fail-closed: any set-but-unrecognized value selects
// sandbox, so a misconfigured deploy can never silently charge live cards.
func TestSquarePublicConfig_FailClosedEnv(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "typo-not-production")
	got := SquarePublicConfig(&organization.Organization{})
	if got.Environment != "sandbox" {
		t.Errorf("environment = %q, want sandbox (fail-closed)", got.Environment)
	}
}
