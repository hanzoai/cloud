package reward

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/namespace"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// The rewards store leaves its file for hanzo-sql with the orm's entities in it, and the
// entities read back through the orm on the other side.
func TestRewardsMoveFromTheirFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv(locus.Env, "")
	t.Setenv("IAM_STORE_BACKEND", "")
	db, closeDB, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ready(db); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		m := orm.New[Sent](db)
		m.SetId(fmt.Sprintf("sent:ev%d", i))
		m.CreatedAt, m.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		if created, err := db.CreateIfAbsent(ctx, m.Key(), m); err != nil || !created {
			t.Fatalf("mark %d: %v %v", i, created, err)
		}
	}
	c := orm.New[Cursor](db)
	c.SetId("cursor:github")
	c.Day = "2026-11-09"
	c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if _, err := db.Put(ctx, c.Key(), c); err != nil {
		t.Fatal(err)
	}
	if err := closeDB(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	db, closeDB, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	place, err := locus.Where(namespace.System(), "reward", dir)
	if err != nil || place != locus.OnServer {
		t.Fatalf("the rewards store is at %v, %v after its open", place, err)
	}
	if err := ready(db); err != nil {
		t.Fatal(err)
	}
	// The orm keeps a moment, a flag and a document in types of its own on each engine, so
	// the rows are read through the orm and not compared as cells.
	for i := range 5 {
		if mark := orm.New[Sent](db); db.Get(ctx, db.NewKey(orm.Kind[Sent](), fmt.Sprintf("sent:ev%d", i), 0, nil), mark) != nil {
			t.Fatalf("mark %d did not arrive", i)
		}
	}
	raw, err := locus.Open(namespace.System(), "reward", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM _entities WHERE deleted = false`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("%d entities on the server, %v; want 6", n, err)
	}
	back := orm.New[Cursor](db)
	if err := db.Get(ctx, db.NewKey(orm.Kind[Cursor](), "cursor:github", 0, nil), back); err != nil || back.Day != "2026-11-09" {
		t.Fatalf("the cursor after the move: %+v, %v", back, err)
	}
}
