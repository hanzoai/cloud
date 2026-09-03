package cloud_test

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients"

	// Blank import registers the kms subsystem's client factory (init) into cloud,
	// so BuildDeps can build the in-process deps.KMS below. cloud itself never
	// imports clients/kms (no cloud⇄kms cycle); this external test can.
	_ "github.com/hanzoai/cloud/apps/kms"
)

// TestBuildDeps_EnabledLeavesNil verifies that BuildDeps leaves an enabled
// Mount-fills-it subsystem's Client field nil — the subsystem Use() installs
// it. KMS is the exception (see TestBuildDeps_KMSEnabledIsInProcess): it is
// constructed eagerly in BuildDeps because its store must exist before any
// dependent subsystem mounts.
func TestBuildDeps_EnabledLeavesNil(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
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
	t.Setenv("CLOUD_DATA_DIR", "/tmp")
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

// A subsystem that is not in this process is DISABLED, and there is no address
// that can say otherwise.
//
// This test used to assert the opposite: that CLOUD_IAM_ZAP_ADDR resolved to an
// RPC client which was NOT disabled. It passed for the wrong reason. That client
// answered every method with "not yet wired (zapc-gen pending)", so what the
// assertion actually pinned was that a configured address produces a client that
// fails differently from an unconfigured one — not that it reaches anything. Boot
// logged "deps.IAM → ZAP RPC" and every VerifyJWT failed, which is the worst of
// both: an operator reading the log saw the transport up, and the calls broke
// somewhere they had no reason to look.
//
// The peer plane is the transport (plane.Ask, over the peer's own socket). A
// subsystem reachable that way is reached by NAME with no address to configure;
// one that is not reachable is honestly disabled. Neither state has room for an
// endpoint string, so there is nothing left to set.
func TestBuildDeps_AnAddressCannotUndisableASubsystem(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", "/tmp")
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: "/tmp",
		Enable:  []string{"gateway"},
	}
	deps := cloud.BuildDeps(cfg)

	if deps.IAM == nil {
		t.Fatal("deps.IAM: not enabled here must give a disabled stub, got nil")
	}
	_, err := deps.IAM.VerifyJWT(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected an error from the disabled stub")
	}
	// The ONE failure mode. Anything else means a second transport grew back.
	if !clients.IsDisabled(err) {
		t.Errorf("a subsystem that is not here must report itself DISABLED, not "+
			"fail in some transport-specific way; got %v", err)
	}
}
