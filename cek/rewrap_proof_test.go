package cek

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRewrapCarriesTheSameDEK is the proof the migration is safe: after Rewrap the
// sidecar opens under the OWNER and yields the IDENTICAL DEK it held before. If the
// key changed, every page in the database would be unreadable — so this equality is
// the whole safety argument, asserted rather than assumed.
func TestRewrapCarriesTheSameDEK(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	// resetMaster, not SetMasterKey: the master is resolved through a sync.Once, so a
	// plain Set is a no-op once a sibling test has already resolved it. This test
	// passed alone and failed in the package for exactly that reason — it minted its
	// sidecar under its own key while Rewrap read whichever key won the race to the
	// Once. resetMaster clears the Once first, which is what every other test here does.
	resetMaster(master)

	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")

	// A store as the OLD world wrote it: wrapped under Global.
	dek, sidecar, err := mintSidecar(Global, master)
	if err != nil {
		t.Fatalf("mint legacy sidecar: %v", err)
	}
	before := append([]byte(nil), dek...)
	if err := os.WriteFile(db+dekSuffix, sidecar, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	owner := Org("hanzo")
	// Precondition: the new derivation cannot open it. This is the prod symptom.
	if _, err := unwrapSidecar(owner, master, sidecar); err == nil {
		t.Fatal("legacy sidecar unexpectedly opened under the owner — no bug to migrate")
	}

	res := Rewrap(owner, db)
	if res.Err != nil {
		t.Fatalf("rewrap: %v", res.Err)
	}
	if !res.Rewrapped {
		t.Fatal("expected a migration, got none")
	}

	next, err := os.ReadFile(db + dekSuffix)
	if err != nil {
		t.Fatalf("read migrated sidecar: %v", err)
	}
	after, err := unwrapSidecar(owner, master, next)
	if err != nil {
		t.Fatalf("migrated sidecar does not open under owner: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("DEK CHANGED — every page in the database would be unreadable")
	}

	// Idempotent: running it again is a no-op, not a second rewrap.
	if again := Rewrap(owner, db); again.Err != nil || !again.Already || again.Rewrapped {
		t.Fatalf("second run should be a no-op, got %+v", again)
	}
}

// TestRewrapRefusesAnUnknownSidecar proves a sidecar that opens under NEITHER
// identity is reported, never "repaired" with a fresh DEK.
func TestRewrapRefusesAnUnknownSidecar(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 9)
	}
	// resetMaster, not SetMasterKey: the master is resolved through a sync.Once, so a
	// plain Set is a no-op once a sibling test has already resolved it. This test
	// passed alone and failed in the package for exactly that reason — it minted its
	// sidecar under its own key while Rewrap read whichever key won the race to the
	// Once. resetMaster clears the Once first, which is what every other test here does.
	resetMaster(master)
	dir := t.TempDir()
	db := filepath.Join(dir, "x.db")
	junk := make([]byte, fileIDLen+40)
	if err := os.WriteFile(db+dekSuffix, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	res := Rewrap(Org("acme"), db)
	if res.Err == nil {
		t.Fatal("expected an error for an unopenable sidecar")
	}
	if res.Rewrapped {
		t.Fatal("must not rewrite a sidecar it could not open")
	}
}
