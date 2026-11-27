// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestDevBootstrapRefusedWithoutOptIn(t *testing.T) {
	t.Setenv(DevBootstrapEnv, "")
	if devBootstrapAllowed() {
		t.Fatal("dev bootstrap must be disabled unless IAM_DEV_BOOTSTRAP=1")
	}
	if _, _, err := openStore(t.TempDir()); err == nil {
		t.Fatal("openStore must refuse an absent store when IAM_DEV_BOOTSTRAP is unset")
	}
}

func TestDevBootstrapRefusedOnMainnet(t *testing.T) {
	t.Setenv(DevBootstrapEnv, "1")
	t.Setenv("CLOUD_ENV", "mainnet")
	if devBootstrapAllowed() {
		t.Fatal("dev bootstrap must be refused on CLOUD_ENV=mainnet even when IAM_DEV_BOOTSTRAP=1")
	}
	if _, _, err := openStore(t.TempDir()); err == nil {
		t.Fatal("openStore must refuse an absent store on CLOUD_ENV=mainnet")
	}
}

func TestDevBootstrapStandsUpWorkingEncryptedIAM(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DevBootstrapEnv, "1")
	t.Setenv("CLOUD_ENV", "local")
	t.Setenv("CLOUD_DATA_DIR", dir)
	t.Setenv("IAM_DEV_HOST_RELATIVE", "1")
	t.Setenv("IAM_ISSUER", "")
	t.Setenv("IAM_ISSUER_MAP", "")
	t.Setenv("IAM_SIGNING_KEYS", "")
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))
	t.Cleanup(func() { _ = Shutdown() })

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use with IAM_DEV_BOOTSTRAP=1: %v", err)
	}
	if DB() == nil {
		t.Fatal("DB() must be non-nil after dev bootstrap mount (fixes admin iam_directory 500)")
	}
	if _, err := os.Stat(filepath.Join(devKeysDir(dir), "cert-local")); err != nil {
		t.Fatalf("expected generated dev signing key: %v", err)
	}

	// Re-open on a second boot (persistent volume restart) must keep working cleanly.
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	t.Setenv("IAM_SIGNING_KEYS", "")
	if err := Use(zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true}), cloud.Deps{}); err != nil {
		t.Fatalf("second Use after restart: %v", err)
	}
	if DB() == nil {
		t.Fatal("DB() must stay open across restarts when IAM_DEV_BOOTSTRAP=1")
	}
	_ = context.Background()
}
