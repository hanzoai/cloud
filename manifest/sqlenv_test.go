package manifest_test

import (
	"testing"

	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// The manifest cannot import the driver (the driver reads the manifest's list of
// ported stores), so it spells the variables itself; they must be the same two.
func TestTheVariablesAreTheDrivers(t *testing.T) {
	if manifest.SQLDSNEnv != locus.Env || manifest.SQLConnsEnv != postgres.ConnsEnv {
		t.Fatalf("manifest says %s and %s; the driver says %s and %s", manifest.SQLDSNEnv, manifest.SQLConnsEnv, locus.Env, postgres.ConnsEnv)
	}
}
