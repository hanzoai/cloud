package agents

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestLifecycleFieldsRoundTrip: the four bot-lifecycle columns persist and read
// back through Create/Get/Update.
func TestLifecycleFieldsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := mk("acme", "sweeper")
	a.ExecutionMode = ModeLongRunning
	a.Schedule = "*/5 * * * *"
	a.ComputeRef = "vm-123"
	a.ServiceAccountID = "acme-sweeper"
	if err := s.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Get(ctx, "acme", "sweeper")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExecutionMode != ModeLongRunning || got.Schedule != "*/5 * * * *" ||
		got.ComputeRef != "vm-123" || got.ServiceAccountID != "acme-sweeper" {
		t.Fatalf("lifecycle fields not persisted: %+v", got)
	}

	got.Schedule = "0 9 * * 1"
	got.ComputeRef = "vm-456"
	got.UpdatedAt = time.Now().Unix()
	if err := s.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := s.Get(ctx, "acme", "sweeper")
	if got2.Schedule != "0 9 * * 1" || got2.ComputeRef != "vm-456" {
		t.Fatalf("update did not persist lifecycle edits: %+v", got2)
	}
}

// TestDefaultExecutionMode: a fresh agent created without a mode reads back as
// one-shot (the DEFAULT that the DDL + migration guarantee), never empty.
func TestDefaultExecutionMode(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, mk("acme", "plain")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ := s.Get(ctx, "acme", "plain")
	if got.ExecutionMode != ModeOneShot {
		t.Fatalf("default execution_mode = %q, want %q", got.ExecutionMode, ModeOneShot)
	}
}

// TestListLongRunning: returns only long-running agents WITH a schedule, across
// orgs, and each carries its own org (the scheduler scopes actions per agent).
func TestListLongRunning(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	oneShot := mk("acme", "oneshot") // default one-shot
	lr := mk("acme", "cron")
	lr.ExecutionMode, lr.Schedule = ModeLongRunning, "* * * * *"
	lrNoSched := mk("beta", "cron")
	lrNoSched.ExecutionMode, lrNoSched.Schedule = ModeLongRunning, "" // no schedule -> excluded
	lrOther := mk("beta", "nightly")
	lrOther.ExecutionMode, lrOther.Schedule = ModeLongRunning, "0 0 * * *"

	for _, a := range []Agent{oneShot, lr, lrNoSched, lrOther} {
		if err := s.Create(ctx, a); err != nil {
			t.Fatalf("seed %s/%s: %v", a.Org, a.Name, err)
		}
	}

	got, err := s.ListLongRunning(ctx)
	if err != nil {
		t.Fatalf("list long-running: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 scheduled agents (acme/cron, beta/nightly), got %d: %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, a := range got {
		seen[a.Org+"/"+a.Name] = a.Schedule
	}
	if seen["acme/cron"] != "* * * * *" || seen["beta/nightly"] != "0 0 * * *" {
		t.Fatalf("wrong scheduled set: %v", seen)
	}
	if _, bad := seen["beta/cron"]; bad {
		t.Fatalf("long-running agent WITHOUT a schedule must be excluded")
	}
}

// TestMigrationIdempotentOnLegacyDB: a DB created with the PRE-lifecycle schema
// (no new columns) is migrated forward on open, existing rows survive with the
// column defaults, and re-opening (re-running migrate) is a clean no-op.
func TestMigrationIdempotentOnLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Hand-build the legacy schema + a legacy row, exactly as the pre-lifecycle
	// migrate() would have, then close.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	const legacyDDL = `
CREATE TABLE agents (
  id TEXT PRIMARY KEY, org TEXT NOT NULL, name TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '', instructions TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '', tools TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'ready', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);`
	if _, err := legacy.Exec(legacyDDL); err != nil {
		t.Fatalf("legacy ddl: %v", err)
	}
	now := time.Now().Unix()
	if _, err := legacy.Exec(
		`INSERT INTO agents (id,org,name,model,instructions,description,tools,status,created_at,updated_at)
		 VALUES ('old-id','acme','legacy','m','i','d','[]','ready',?,?)`, now, now); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	_ = legacy.Close()

	// Open through the real store TWICE — the first migrates, the second proves
	// idempotency (no error re-adding existing columns).
	for i := 0; i < 2; i++ {
		st, err := openStore(path)
		if err != nil {
			t.Fatalf("open #%d migrate failed: %v", i, err)
		}
		got, err := st.Get(context.Background(), "acme", "legacy")
		if err != nil {
			t.Fatalf("open #%d: legacy row lost: %v", i, err)
		}
		if got.ExecutionMode != ModeOneShot {
			t.Fatalf("open #%d: migrated row default mode = %q, want %q", i, got.ExecutionMode, ModeOneShot)
		}
		if got.Schedule != "" || got.ComputeRef != "" || got.ServiceAccountID != "" {
			t.Fatalf("open #%d: migrated defaults not empty: %+v", i, got)
		}
		_ = st.Close()
	}
}
