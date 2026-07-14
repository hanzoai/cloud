// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package affiliates

import (
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// A store whose affiliate_referrals table predates the referrer_org column must
// migrate cleanly: the referrer_org index is created AFTER ADD COLUMN, so it no
// longer fails with "no such column: referrer_org" (the v1.800.1 boot crash).
func TestMigrateFromPreReferrerOrgSchema(t *testing.T) {
	path := t.TempDir() + "/old.db"

	// 1) Stand up the OLD schema: affiliate_referrals WITHOUT referrer_org, and an
	// affiliates row so the backfill has something to resolve.
	db, err := cek.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	old := `
CREATE TABLE affiliates (
  id TEXT PRIMARY KEY, org TEXT NOT NULL UNIQUE, code TEXT NOT NULL DEFAULT '',
  requested_code TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, rate_bps INTEGER NOT NULL,
  accrued_cents INTEGER NOT NULL DEFAULT 0, paid_cents INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, approved_at INTEGER NOT NULL DEFAULT 0, suspended_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE affiliate_referrals (
  id TEXT PRIMARY KEY, affiliate_id TEXT NOT NULL, referred_org TEXT NOT NULL UNIQUE,
  code TEXT NOT NULL, created_at INTEGER NOT NULL
);
INSERT INTO affiliates (id,org,status,rate_bps,created_at) VALUES ('aff1','acme','approved',1000,1);
INSERT INTO affiliate_referrals (id,affiliate_id,referred_org,code,created_at) VALUES ('r1','aff1','beta','acme',2);
`
	if _, err := db.Exec(old); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	_ = db.Close()

	// 2) openStore runs migrate() on the existing old DB — this crashed v1.800.1.
	s, err := openStore(path)
	if err != nil {
		t.Fatalf("migrate from old schema: %v", err)
	}
	defer s.Close()

	// 3) The column exists and the backfill ran: the pre-migration edge now carries
	// its affiliate's org as referrer_org.
	var referrer string
	if err := s.db.QueryRow(`SELECT referrer_org FROM affiliate_referrals WHERE referred_org='beta'`).Scan(&referrer); err != nil {
		t.Fatalf("read referrer_org: %v", err)
	}
	if referrer != "acme" {
		t.Fatalf("referrer_org = %q, want acme (backfilled from the affiliate's org)", referrer)
	}

	// 4) Re-running migrate() is idempotent (a restart must not fail).
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
