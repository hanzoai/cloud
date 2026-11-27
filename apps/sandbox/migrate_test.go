package sandbox

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// legacySandboxDDL is the sandbox table BEFORE any of the columns that were added
// to it later: runtime, cluster, actor, connected_at, payer, metered_at and
// disk_metered_at.
const legacySandboxDDL = `
CREATE TABLE sandbox (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'sandbox',
  class        TEXT NOT NULL DEFAULT 'exec',
  project      TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'pending',
  image        TEXT NOT NULL DEFAULT '',
  pod          TEXT NOT NULL DEFAULT '',
  volume       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0
);
INSERT INTO sandbox (id, org, class, status, pod, created_at, last_used_at, expires_at)
VALUES ('m_old', 'acme', 'dev', 'running', 'm-old', 1, 2, 3);`

// TestAnOldRegistryGainsItsColumns: a database that predates the later columns gains
// every one on the next open, on either engine, a row written before them reads
// back with their defaults (no payer, no watermark, no lessee), and opening again
// changes nothing.
func TestAnOldRegistryGainsItsColumns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns := mustNS(t, "acme")
		raw := open(ns, "sandbox")
		if _, err := raw.Exec(legacySandboxDDL); err != nil {
			t.Fatalf("seed the old table: %v", err)
		}
		_ = raw.Close()

		ctx := context.Background()
		for pass := range 2 {
			st, err := openStore(open(ns, "sandbox"))
			if err != nil {
				t.Fatalf("open pass %d: %v", pass, err)
			}
			old, err := st.Get(ctx, "acme", "m_old")
			if err != nil {
				t.Fatalf("read the old row on pass %d: %v", pass, err)
			}
			if old.Class != "dev" || old.Pod != "m-old" || old.ExpiresAt != 3 ||
				old.Payer != "" || old.MeteredAt != 0 || old.DiskMeteredAt != 0 || old.ConnectedAt != 0 || old.Actor != "" || old.Runtime != "" || old.Cluster != "" {
				t.Fatalf("the old row read back as %+v", old)
			}
			if err := st.Put(ctx, Sandbox{ID: "m_new" + string(rune('a'+pass)), Org: "acme", Kind: KindSandbox, Class: "dev", Status: "running",
				Runtime: "runc", Cluster: "lab", Actor: "alice", Payer: "acme", MeteredAt: 5, DiskMeteredAt: 6, ConnectedAt: 7,
				CreatedAt: 1, LastUsedAt: 1, ExpiresAt: 10}); err != nil {
				t.Fatalf("write through the new columns on pass %d: %v", pass, err)
			}
			_ = st.Close()
		}
	})
}
