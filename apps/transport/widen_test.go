package transport

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/sdk/locus"
)

// A store written before the members policy keeps its rows and accepts members
// once it is opened again.
func TestAStoreFromBeforeMembersIsWidened(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := locus.System("channels", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE channel_policy (
  org          TEXT NOT NULL,
  channel      TEXT NOT NULL,
  dm_policy    TEXT NOT NULL DEFAULT 'pairing' CHECK (dm_policy IN ('pairing','allowlist','open')),
  group_policy TEXT NOT NULL DEFAULT 'open' CHECK (group_policy IN ('open','allowlist','disabled')),
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (org, channel)
);
INSERT INTO channel_policy VALUES ('acme', 'telegram', 'allowlist', 'disabled', 7);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore over the old table: %v", err)
	}
	if p, err := policyFor(ctx, st, "acme", "telegram"); err != nil || p.DM != DMAllowlist || p.Group != GroupDisabled {
		t.Fatalf("kept row = %+v (%v), want allowlist/disabled", p, err)
	}
	if err := setPolicy(ctx, st, "acme", "slack", policyRow{DM: DMMembers, Group: GroupOpen}, 8); err != nil {
		t.Fatalf("members after widening: %v", err)
	}
	_ = st.Close()
	again, err := openStore(dir)
	if err != nil {
		t.Fatalf("a second open: %v", err)
	}
	_ = again.Close()
}
