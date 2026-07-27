package esign

import (
	"os"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
)

func quietLog() luxlog.Logger { return luxlog.NewNoOpLogger() }

// TestMigrateDataDirCarriesTenantDocumentsOver is the reason the rename is safe.
// Per-tenant stores live at {DataDir}/{subsystem}/{tenant}.db, so renaming the
// subsystem without moving the directory would leave every signed document
// unreachable — the store would open empty and look like data loss.
func TestMigrateDataDirCarriesTenantDocumentsOver(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "sign")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A tenant document store and the development signer's key material, the two
	// things that live under the subsystem's directory.
	for name, body := range map[string]string{
		"mfrw2zi.db": "tenant documents",
		"signer.key": "development key",
	} {
		if err := os.WriteFile(filepath.Join(old, name), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	if err := migrateDataDir(dir, quietLog()); err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}

	cur := filepath.Join(dir, "esign")
	for name, want := range map[string]string{
		"mfrw2zi.db": "tenant documents",
		"signer.key": "development key",
	} {
		got, err := os.ReadFile(filepath.Join(cur, name))
		if err != nil {
			t.Errorf("%s did not survive the rename: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q after the rename, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the previous directory still exists; documents would be served from neither or both")
	}
}

// TestMigrateDataDirIsIdempotent proves a second boot is a no-op, so the
// migration can sit in Mount permanently instead of needing to be removed.
func TestMigrateDataDirIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sign"), 0o700); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sign", "t.db"), []byte("documents"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := range 3 {
		if err := migrateDataDir(dir, quietLog()); err != nil {
			t.Fatalf("migrateDataDir call %d: %v", i+1, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "esign", "t.db"))
	if err != nil || string(got) != "documents" {
		t.Errorf("repeated migration damaged the data: %q err=%v", got, err)
	}
}

// TestMigrateDataDirLeavesBothDirectoriesAlone proves an ambiguous state is not
// resolved by guessing. If a deployment has written under BOTH names, merging
// them would silently pick one tenant's documents over another's.
func TestMigrateDataDirLeavesBothDirectoriesAlone(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"sign": "previous", "esign": "current"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "t.db"), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := migrateDataDir(dir, quietLog()); err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}
	for name, want := range map[string]string{"sign": "previous", "esign": "current"} {
		got, err := os.ReadFile(filepath.Join(dir, name, "t.db"))
		if err != nil || string(got) != want {
			t.Errorf("%s/t.db = %q err=%v, want %q left untouched", name, got, err, want)
		}
	}
}

// TestMigrateDataDirOnFreshDeployment proves a deployment that never ran under
// the previous name boots without creating anything.
func TestMigrateDataDirOnFreshDeployment(t *testing.T) {
	dir := t.TempDir()
	if err := migrateDataDir(dir, quietLog()); err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "esign")); !os.IsNotExist(err) {
		t.Error("migration created a directory on a fresh deployment; it should only carry an existing one over")
	}
}
