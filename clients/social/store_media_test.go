// Copyright © 2026 Hanzo AI. MIT License.

package social

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// TestMigrateAddsMediaColumnToOldPosts is the regression for a social_posts table
// created BEFORE the media column landed: it must gain the column on the next store
// open, so a post write carrying media no longer 500s ("table social_posts has no
// column named media"). CREATE TABLE IF NOT EXISTS never alters an existing table, so
// an old prod social_posts is frozen at its original schema without the additive
// ALTER. The store is an encrypted single-file SQLite only the binary can open, so
// migrate-on-open is the ONLY upgrade path (no hand-patch). Mirrors marketing's
// store_migrate_test.go for the scheduled_at column.
func TestMigrateAddsMediaColumnToOldPosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "social.db")

	// Seed a prod-shaped OLD DB: social_posts WITH the publish-state columns but
	// WITHOUT media, plus an existing row — exactly what a pre-media prod deployment
	// holds. Written through cek so the on-disk format matches what openStore reads.
	raw, err := cek.Open(path)
	if err != nil {
		t.Fatalf("cek.Open (seed old db): %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE social_posts (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  content      TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL DEFAULT 'x',
  status       TEXT NOT NULL DEFAULT 'draft',
  schedule_at  INTEGER NOT NULL DEFAULT 0,
  account_id   TEXT NOT NULL DEFAULT '',
  external_id  TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);`); err != nil {
		t.Fatalf("create old-schema posts: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO social_posts
  (id,org,content,channel,status,schedule_at,account_id,external_id,error,created_at,updated_at)
  VALUES ('old1','karma','legacy post','x','draft',0,'','','',1,2)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	_ = raw.Close()

	// Open through the real store: migrate() must ADD the media column to the existing
	// table (idempotent — swallows "duplicate column name" on a fresh DB).
	s, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore (migrate must upgrade old table): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()

	// The exact prod repro: a create that writes media must now SUCCEED, not 500 with
	// "no column named media".
	if _, err := s.CreatePost(ctx, Post{
		ID: "new1", Org: "karma", Content: "with media", Channel: "x", Status: "draft",
		Media: []string{"https://s3.hanzo.ai/a.png", "https://s3.hanzo.ai/b.png"}, CreatedAt: 10, UpdatedAt: 10,
	}); err != nil {
		t.Fatalf("CreatePost after migrate must not 500 on media: %v", err)
	}
	got, err := s.GetPost(ctx, "karma", "new1")
	if err != nil {
		t.Fatalf("GetPost new: %v", err)
	}
	if len(got.Media) != 2 || got.Media[0] != "https://s3.hanzo.ai/a.png" {
		t.Fatalf("media not round-tripped after migrate: %+v", got.Media)
	}

	// The legacy row survived the upgrade and reads media as [] (non-nil), never null.
	old, err := s.GetPost(ctx, "karma", "old1")
	if err != nil {
		t.Fatalf("GetPost legacy: %v", err)
	}
	if old.Content != "legacy post" || old.Media == nil || len(old.Media) != 0 {
		t.Fatalf("legacy row corrupted after migrate: %+v", old)
	}
}
