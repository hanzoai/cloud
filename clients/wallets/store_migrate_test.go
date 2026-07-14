package wallets

import (
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// legacyWalletsDDL is the wallets table as it existed BEFORE scoping columns
// (project/agent) and the finance/chain columns were introduced — the shape a
// production wallets.db carries today. A migrate() that indexes a forward-added
// column before ALTER-adding it fails here with "no such column", which fails
// mount and crashloops the pod. This fixes the schema in place so the test proves
// the ordering, not the driver.
const legacyWalletsDDL = `
CREATE TABLE wallets (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  account_id TEXT NOT NULL,
  name       TEXT NOT NULL,
  custody    TEXT NOT NULL,
  tier       TEXT NOT NULL,
  address    TEXT NOT NULL,
  key_ref    TEXT NOT NULL,
  created_at INTEGER NOT NULL
);`

// TestMigrateOverLegacyWalletsTable reproduces the deploy crashloop: opening a
// wallets.db whose table predates the scope/finance columns must migrate cleanly,
// not error on "CREATE INDEX ... no such column: project".
func TestMigrateOverLegacyWalletsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallets.db")

	// Stand up the legacy schema exactly as a pre-scoping prod DB has it.
	raw, err := cek.Open(path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := raw.Exec(legacyWalletsDDL); err != nil {
		t.Fatalf("create legacy wallets table: %v", err)
	}
	_ = raw.Close()

	// openStore runs migrate(): this is the exact path that failed at mount.
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("migrate over legacy wallets table: %v", err)
	}
	defer st.Close()

	// The forward-added columns must now exist and be usable — insert a fully
	// scoped row (the write that ix_wallets_scope indexes) to prove it.
	if _, err := st.db.Exec(
		`INSERT INTO wallets
		   (id, org, project, agent, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"wal_1", "acme", "proj1", "bot7", "acct9", "w", "kms", "std", "", "0xabc", "wallets/acme/wal_1", nil, 1,
	); err != nil {
		t.Fatalf("insert scoped wallet after migrate: %v", err)
	}

	// Re-open the SAME db: migrate() must be idempotent (ALTERs no-op via
	// duplicate-column, indexes IF NOT EXISTS) with no error on a warm restart.
	st2, err := openStore(path)
	if err != nil {
		t.Fatalf("re-migrate (idempotency): %v", err)
	}
	_ = st2.Close()
}
