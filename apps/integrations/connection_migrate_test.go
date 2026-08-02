package integrations

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hanzoai/cloud/basedb"
	"github.com/hanzoai/namespace"
)

// oldSchema is the connections table exactly as it was before the owner joined
// the key. The migration has to lift a live database off this, so the test
// builds one rather than asserting against a fixture that could drift.
const oldSchema = `
CREATE TABLE connections (
  org           TEXT NOT NULL,
  provider      TEXT NOT NULL,
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, provider)
);`

// seedOldStore writes a pre-migration database through the SAME opener openStore
// uses, and returns the DIRECTORY it lives in — so the reopen below lands on
// exactly this file.
func seedOldStore(t *testing.T, rows [][]any) string {
	t.Helper()
	dir := t.TempDir()
	db, err := basedb.Open(namespace.System(), "integrations", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO connections (org,provider,external_id,account_label,bot_user_id,scopes_csv,connected_at,updated_at)
			 VALUES (?,?,?,?,?,?,?,?)`, r...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dir
}

// The owner is RECOVERED from account_label, which already held the GitHub org
// login. Defaulting it to "" instead would leave the row unaddressable the moment
// a second account connected.
func TestMigrationRecoversTheGithubOwner(t *testing.T) {
	dir := seedOldStore(t, [][]any{
		{"hanzo", "github", "62000701", "hanzoai", "", "", 100, 100},
		{"acme", "slack", "T123", "Acme Inc", "U1", "chat:write", 200, 200},
	})
	s, err := openStore(dir) // migrate() runs here
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	c, ok, err := s.Get(ctx, "hanzo", "github", "hanzoai")
	if err != nil || !ok {
		t.Fatalf("github row lost its identity after migration: ok=%v err=%v", ok, err)
	}
	if c.ExternalID != "62000701" {
		t.Errorf("installation id changed: %s", c.ExternalID)
	}

	// A single-account provider takes owner="", which is where its callers look.
	sl, ok, err := s.Get(ctx, "acme", "slack", "")
	if err != nil || !ok {
		t.Fatalf("slack row lost: ok=%v err=%v", ok, err)
	}
	if sl.BotUserID != "U1" || sl.ExternalID != "T123" {
		t.Errorf("slack row mangled: %+v", sl)
	}
}

// connected_at is "connected since" and must survive a schema change; a
// migration that reset it would silently rewrite history.
func TestMigrationPreservesEveryColumn(t *testing.T) {
	dir := seedOldStore(t, [][]any{
		{"hanzo", "github", "62000701", "hanzoai", "B1", "repo,read:org", 1234, 5678},
	})
	s, err := openStore(dir)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer func() { _ = s.Close() }()

	c, ok, err := s.Get(context.Background(), "hanzo", "github", "hanzoai")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if c.ConnectedAt != 1234 || c.UpdatedAt != 5678 {
		t.Errorf("timestamps changed: connected=%d updated=%d", c.ConnectedAt, c.UpdatedAt)
	}
	if c.BotUserID != "B1" || c.AccountLabel != "hanzoai" {
		t.Errorf("fields changed: %+v", c)
	}
	if len(c.Scopes) != 2 {
		t.Errorf("scopes changed: %v", c.Scopes)
	}
}

// migrate() runs on every open, so the rebuild must be a no-op once done —
// otherwise the second boot would drop a table it had already replaced.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := seedOldStore(t, [][]any{
		{"hanzo", "github", "62000701", "hanzoai", "", "", 100, 100},
	})
	for i := range 3 {
		s, err := openStore(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		conns, err := s.ListFor(context.Background(), "hanzo", "github")
		if err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
		if len(conns) != 1 || conns[0].Owner != "hanzoai" {
			t.Fatalf("open %d: rows=%d %+v", i, len(conns), conns)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// After migrating, the org can connect a SECOND GitHub account — the whole point.
func TestMigratedStoreAcceptsASecondAccount(t *testing.T) {
	dir := seedOldStore(t, [][]any{
		{"hanzo", "github", "62000701", "hanzoai", "", "", 100, 100},
	})
	s, err := openStore(dir)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.Upsert(ctx, Connection{
		Org: "hanzo", Provider: "github", Owner: "hanzo-apps",
		ExternalID: "143008414", AccountLabel: "hanzo-apps",
	}); err != nil {
		t.Fatalf("connect second account: %v", err)
	}
	conns, err := s.ListFor(ctx, "hanzo", "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 {
		t.Fatalf("want 2 accounts after migration, got %d", len(conns))
	}
}

// A fresh database takes the wide key directly and never runs the rebuild.
func TestFreshStoreNeedsNoRebuild(t *testing.T) {
	s := openTestStore(t)
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('connections') WHERE name='owner'`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("inspect: %v", err)
	}
	if n != 1 {
		t.Fatalf("fresh store has no owner column")
	}
}
