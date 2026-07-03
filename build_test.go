package cloud_test

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients"
)

// TestBuildDeps_EnabledLeavesNil verifies that BuildDeps leaves an enabled
// Mount-fills-it subsystem's Client field nil — the subsystem Mount() installs
// it. KMS is now constructed by the composition root (Serve, via kms.New) and
// injected as deps.KMS — NOT by BuildDeps. See
// TestBuildDeps_KMSConstructedByCompositionRoot.
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

// TestBuildDeps_KMSConstructedByCompositionRoot verifies the HIP-0106 explicit-
// wiring contract: BuildDeps NO LONGER constructs the embedded KMS store — the
// composition root (Serve) does, via kms.New, and injects it as deps.KMS. So
// BuildDeps with kmssvc enabled but no ZAP endpoint yields the fail-closed
// DISABLED stub (non-nil), which Serve overwrites with the live store. The real
// in-process store + its /v1/kms/* wiring are proven directly in
// clients/kms/kms_test.go (kms.New + kms.Mount) and in the boot-parity smoke in
// cmd/cloud/main_test.go.
func TestBuildDeps_KMSConstructedByCompositionRoot(t *testing.T) {
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
		Enable:  []string{"kmssvc"},
	}
	deps := cloud.BuildDeps(cfg)

	if deps.KMS == nil {
		t.Fatal("deps.KMS must be non-nil (the disabled stub), never nil")
	}
	// With KMS construction moved to Serve, BuildDeps returns the fail-closed
	// disabled stub here (no ZAP endpoint configured). Serve overrides it.
	_, err := deps.KMS.GetSecret(context.Background(), "any")
	if err == nil {
		t.Fatal("GetSecret on the disabled stub must fail closed")
	}
	if !clients.IsDisabled(err) {
		t.Errorf("BuildDeps must leave deps.KMS as the disabled stub (Serve fills the real store); got %v", err)
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
