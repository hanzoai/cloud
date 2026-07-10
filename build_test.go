package cloud_test

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients"

	// Blank import registers the kms subsystem's client factory (init) into cloud,
	// so BuildDeps can build the in-process deps.KMS below. cloud itself never
	// imports clients/kms (no cloud⇄kms cycle); this external test can.
	_ "github.com/hanzoai/cloud/clients/kms"
)

// TestBuildDeps_EnabledLeavesNil verifies that BuildDeps leaves an enabled
// Mount-fills-it subsystem's Client field nil — the subsystem Mount() installs
// it. KMS is the exception (see TestBuildDeps_KMSEnabledIsInProcess): it is
// constructed eagerly in BuildDeps because its store must exist before any
// dependent subsystem mounts.
func TestBuildDeps_EnabledLeavesNil(t *testing.T) {
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
		Enable:  []string{"iam", "base", "commerce", "ai", "o11y", "vfs", "mq"},
	}
	deps := cloud.BuildDeps(cfg)

	if deps.IAM != nil {
		t.Errorf("deps.IAM: enabled subsystem must leave Client nil, got %T", deps.IAM)
	}
}

// TestBuildDeps_KMSEnabledIsInProcess verifies the HIP-0106 "embed KMS in cloud"
// contract: when the kms subsystem is enabled, deps.KMS is a live
// in-process client (never nil, never a disabled stub) so other subsystems get a
// working KMS via direct Go dispatch with no RPC. Absent a master key it still
// resolves (health-only, fail-closed) — the point is that deps.KMS is populated.
func TestBuildDeps_KMSEnabledIsInProcess(t *testing.T) {
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
	// It must NOT be the fail-closed disabled stub — that stub returns IsDisabled
	// errors; an in-process client (no master key) returns a master-key error.
	_, err := deps.KMS.GetSecret(context.Background(), "any")
	if err == nil {
		t.Fatal("GetSecret with no master key must fail closed")
	}
	if clients.IsDisabled(err) {
		t.Errorf("deps.KMS resolved to the DISABLED stub, want the in-process client: %v", err)
	}
}

// TestBuildDeps_DisabledNoEndpointReturnsDisabled verifies that a
// disabled subsystem with no RPC endpoint resolves to the disabled
// fail-closed stub.
func TestBuildDeps_DisabledNoEndpointReturnsDisabled(t *testing.T) {
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: "/tmp",
		Enable:  []string{"gateway"}, // intentionally none of the others
	}
	deps := cloud.BuildDeps(cfg)

	if deps.IAM == nil {
		t.Fatal("deps.IAM: disabled + no endpoint must give a disabled stub, got nil")
	}
	_, err := deps.IAM.VerifyJWT(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected disabledErr from VerifyJWT")
	}
	if !clients.IsDisabled(err) {
		t.Errorf("expected IsDisabled, got %v", err)
	}
}

// TestBuildDeps_DisabledWithEndpointReturnsRPC verifies that a
// disabled subsystem with a configured ZAP endpoint resolves to the
// RPC stub.
func TestBuildDeps_DisabledWithEndpointReturnsRPC(t *testing.T) {
	cfg := &cloud.Config{
		Brand:      "hanzo",
		Domain:     "api.hanzo.ai",
		DataDir:    "/tmp",
		Enable:     []string{"gateway"},
		IAMZAPAddr: "iam.hanzo.svc:9653",
	}
	deps := cloud.BuildDeps(cfg)

	if deps.IAM == nil {
		t.Fatal("deps.IAM: expected RPC stub")
	}
	_, err := deps.IAM.VerifyJWT(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error from RPC stub (transport pending)")
	}
	if clients.IsDisabled(err) {
		t.Errorf("expected NOT-disabled, got disabled: %v", err)
	}
}

// TestBuildDeps_PaymentsAndVault_AlwaysRPC verifies that payments and
// vault always resolve to a non-nil client even though neither is in
// the enabled list — they are not co-resident per HIP-0106.
func TestBuildDeps_PaymentsAndVault_AlwaysRPC(t *testing.T) {
	cfg := &cloud.Config{
		Brand:           "hanzo",
		Domain:          "api.hanzo.ai",
		DataDir:         "/tmp",
		Enable:          []string{"commerce"},
		PaymentsZAPAddr: "payments.hanzo.svc:9653",
		VaultZAPAddr:    "vault.hanzo.svc:9653",
	}
	deps := cloud.BuildDeps(cfg)

	if deps.Payments == nil {
		t.Fatal("deps.Payments must be non-nil even when not enabled")
	}
	if deps.Vault == nil {
		t.Fatal("deps.Vault must be non-nil even when not enabled")
	}
	// Call them to confirm typed dispatch — they'll return "transport
	// pending" errors but not nil deref.
	if _, err := deps.Payments.CreateIntent(context.Background(), &cloud.IntentRequest{Token: "tok-1", Currency: "USD", AmountCents: 100}); err == nil {
		t.Fatal("expected RPC stub error")
	}
	if _, err := deps.Vault.Charge(context.Background(), &cloud.VaultChargeRequest{Token: "tok-1", AmountCents: 100}); err == nil {
		t.Fatal("expected RPC stub error")
	}
}
