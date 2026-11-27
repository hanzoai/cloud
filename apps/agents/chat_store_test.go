package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
)

// A conversation file the module wrote before (plaintext, at orgs/<org>/agent.db) and
// the share index it kept at shares.db both leave for hanzo-sql, and what was in them
// reads back through the module's own store on the other side.
func TestConversationsMoveFromTheModulesFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	t.Setenv(locus.Env, "")

	// What the module wrote, as the module wrote it.
	if err := os.MkdirAll(filepath.Join(dir, "orgs", "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := func(p string) *ormdb.SQLiteDBConfig {
		return &ormdb.SQLiteDBConfig{Path: p, Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}}
	}
	old, err := orm.OpenSQLite(cfg(filepath.Join(dir, "orgs", "acme", "agent.db")))
	if err != nil {
		t.Fatal(err)
	}
	cv := orm.New[hz.Conversation](old)
	cv.SetId("conv-1")
	cv.Org, cv.User, cv.Title = "acme", "alice", "our plans"
	if err := cv.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	oldShares, err := orm.OpenSQLite(cfg(filepath.Join(dir, "shares.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := oldShares.Close(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	c := newChatStores(cloud.Deps{})
	t.Cleanup(func() { _ = c.Close() })

	raw, err := c.open("acme")
	if err != nil {
		t.Fatal(err)
	}
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	if place, err := locus.Where(ns, chatSubsystem, dir); err != nil || place != locus.OnServer {
		t.Fatalf("the conversations are at %v, %v after their open", place, err)
	}
	db, err := orm.AdaptSQL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := orm.Get[hz.Conversation](db, "conv-1"); err != nil || got.Title != "our plans" || got.User != "alice" {
		t.Fatalf("the conversation after the move: %+v, %v", got, err)
	}

	// The share index is a store of its own; the module carries shares.db into it (agent's carry_test).
	index, err := c.index()
	if err != nil {
		t.Fatal(err)
	}
	if place, err := locus.Where(namespace.System(), sharesSubsystem, dir); err != nil || place != locus.OnServer {
		t.Fatalf("the share index is at %v, %v", place, err)
	}
	if err := index.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

// The sweep opens an org's conversations without asking open first, as nothing
// requested them. The plaintext file the module wrote must already be the encrypted
// store by then: opened raw it read "file is not a database" and the move failed for
// every org nobody had asked about (1,292 in production).
func TestTheSweepMovesConversationsNobodyAskedFor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("CLOUD_DATA_DIR", dir)
	t.Setenv(locus.Env, "")
	if err := os.MkdirAll(filepath.Join(dir, "orgs", "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(dir, "orgs", "acme", "agent.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"}})
	if err != nil {
		t.Fatal(err)
	}
	cv := orm.New[hz.Conversation](old)
	cv.SetId("conv-1")
	cv.Org, cv.User, cv.Title = "acme", "alice", "quiet org"
	if err := cv.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	c := newChatStores(cloud.Deps{})
	t.Cleanup(func() { _ = c.Close() })
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	// The sweep's open, not the module's.
	s, err := c.orgs.For(ns)
	if err != nil {
		t.Fatalf("the sweep's open of an org nobody asked about: %v", err)
	}
	if place, err := locus.Where(ns, chatSubsystem, dir); err != nil || place != locus.OnServer {
		t.Fatalf("the conversations are at %v, %v", place, err)
	}
	db, err := orm.AdaptSQL(s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := orm.Get[hz.Conversation](db, "conv-1"); err != nil || got.Title != "quiet org" {
		t.Fatalf("the conversation after the move: %+v, %v", got, err)
	}
}
