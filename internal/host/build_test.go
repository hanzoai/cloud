package host_test

import (
	"context"
	"errors"
	"testing"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"

	// Blank import registers the kms subsystem's client factory (init) into cloud,
	// so BuildDeps can build the in-process deps.KMS below. cloud itself never
	// imports clients/kms (no cloud⇄kms cycle); this external test can.
	_ "github.com/hanzoai/cloud/apps/kms"
)

// TestBuildDeps_KMSEnabledIsInProcess verifies the HIP-0106 "embed KMS in cloud"
// contract: when the kms subsystem is enabled, deps.KMS is a live
// in-process client (never nil, never a disabled stub) so other subsystems get a
// working KMS via direct Go dispatch with no RPC. Absent a master key it still
// resolves (health-only, fail-closed) — the point is that deps.KMS is populated.
func TestBuildDeps_KMSEnabledIsInProcess(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
		Enable:  []string{"kms"},
	}
	deps := cloud.BuildDeps(cfg)

	if deps.KMS == nil {
		t.Fatal("deps.KMS: enabled kms must give an in-process client, got nil")
	}
	// It must NOT be the fail-closed disabled stub — that stub returns ErrDisabled
	// errors; an in-process client (no master key) returns a master-key error.
	_, err := deps.KMS.GetSecret(context.Background(), "any")
	if err == nil {
		t.Fatal("GetSecret with no master key must fail closed")
	}
	if errors.Is(err, secret.ErrDisabled) {
		t.Errorf("deps.KMS resolved to the DISABLED stub, want the in-process client: %v", err)
	}
}
