// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// TestMigrateUpgradesOldCampaignsTable is the regression for the prod 500
// "table marketing_campaigns has no column named scheduled_at": a marketing_campaigns
// table created BEFORE scheduled_at was added to the DDL must gain the column on the
// next store open. CREATE TABLE IF NOT EXISTS never alters an existing table, so
// without the additive ALTER an old prod table is frozen at its original schema and
// every campaign write 500s. The store is an encrypted single-file SQLite only the
// binary can open, so migrate-on-open is the ONLY upgrade path (no hand-patch).
func TestMigrateUpgradesOldCampaignsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marketing.db")

	// Seed a prod-shaped OLD DB: marketing_campaigns WITHOUT scheduled_at, plus an
	// existing row — exactly what a pre-scheduling prod deployment holds. Written
	// through cek so the on-disk format matches what openStore reads back.
	raw, err := cek.Open(path)
	if err != nil {
		t.Fatalf("cek.Open (seed old db): %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE marketing_campaigns (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  name       TEXT NOT NULL,
  channel    TEXT NOT NULL DEFAULT 'email',
  status     TEXT NOT NULL DEFAULT 'draft',
  objective  TEXT NOT NULL DEFAULT '',
  budget     INTEGER NOT NULL DEFAULT 0,
  spend      INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);`); err != nil {
		t.Fatalf("create old-schema table: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO marketing_campaigns
  (id,org,name,channel,status,objective,budget,spend,created_at,updated_at)
  VALUES ('old1','karma','Legacy','email','active','',0,0,1,2)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	_ = raw.Close()

	// Open through the real store: migrate() must ADD the scheduled_at column to the
	// existing table (idempotent — swallows "duplicate column name" on a fresh DB).
	s, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore (migrate must upgrade old table): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	// The exact prod repro: a create that writes scheduled_at must now SUCCEED, not
	// 500 with "no column named scheduled_at".
	if _, err := s.CreateCampaign(ctx, Campaign{
		ID: "new1", Org: "karma", Name: "Spring", Channel: "email",
		Status: "draft", ScheduledAt: 123, CreatedAt: 10, UpdatedAt: 10,
	}); err != nil {
		t.Fatalf("CreateCampaign after migrate must not 500 on scheduled_at: %v", err)
	}
	got, err := s.GetCampaign(ctx, "karma", "new1")
	if err != nil {
		t.Fatalf("GetCampaign new: %v", err)
	}
	if got.ScheduledAt != 123 {
		t.Fatalf("scheduled_at = %d, want 123", got.ScheduledAt)
	}

	// The legacy row survived the upgrade and defaults scheduled_at to 0.
	old, err := s.GetCampaign(ctx, "karma", "old1")
	if err != nil {
		t.Fatalf("GetCampaign legacy: %v", err)
	}
	if old.ScheduledAt != 0 || old.Name != "Legacy" {
		t.Fatalf("legacy row corrupted after migrate: %+v", old)
	}
}

// TestMigrateOnFreshDBIsIdempotent proves the additive ALTER is a no-op on a fresh
// DB (the column already exists via the CREATE) and that re-opening never errors.
func TestMigrateOnFreshDBIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marketing.db")

	s, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore fresh: %v", err)
	}
	_ = s.Close()

	// Re-open: migrate runs again; the ALTER must swallow "duplicate column name".
	s2, err := openStore(path)
	if err != nil {
		t.Fatalf("re-open must be idempotent: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	if _, err := s2.CreateCampaign(context.Background(), Campaign{
		ID: "c1", Org: "karma", Name: "x", Channel: "email", Status: "draft",
		ScheduledAt: 7, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("CreateCampaign on fresh+reopened db: %v", err)
	}
}
