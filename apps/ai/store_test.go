package ai

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/dbx"
	"github.com/hanzoai/namespace"

	aiobject "github.com/hanzoai/ai/object"
)

// ai's store opens on either engine as the one ai syncs its tables into, twice over
// (a restart opens it again), and the module reads and writes it once handed.
func TestTheStoreOpensOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		ctx := context.Background()
		for range 2 {
			s, err := openStore(ctx, dir)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got, want := dialect.Of(s.sql).Postgres(), locus.DSN() != ""; got != want {
				t.Fatalf("Postgres() = %v, want %v", got, want)
			}
			aiobject.UseDB(s.db)
			taken, _, err := aiobject.TakeLease("store-test", time.Hour, time.Now())
			if err != nil {
				t.Fatalf("the module cannot write the store it was handed: %v", err)
			}
			_ = taken
			if err := aiobject.CountServed("v", "m", "k", time.Now(), true); err != nil {
				t.Fatal(err)
			}
			rows, err := aiobject.ServedSince("v", time.Now().Add(-time.Hour))
			if err != nil || len(rows) != 1 {
				t.Fatalf("served = %+v, %v", rows, err)
			}
			aiobject.UseDB(nil)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
		}
		// What the first open wrote is there for the second: a lease taken is still taken.
		s, err := openStore(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var n int
		if err := s.db.NewQuery("SELECT COUNT(*) FROM lease").Row(&n); err != nil || n != 1 {
			t.Fatalf("leases = %d, %v: the store lost what the module wrote", n, err)
		}
	})
}

// The file ai kept before the host owned its store (cloud.db) is carried into the seam
// and moves to hanzo-sql whole: every row of every model the same, the NULLs an older
// release left repaired, a number wider than 32 bits and a bool kept, the numbered
// rows' ids kept and the next one after them. The legacy file is left as it was.
func TestAIsLegacyFileMovesToHanzoSQL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("dataSourceName", "")
	t.Setenv(locus.Env, "")
	legacy := filepath.Join(dir, "cloud.db")

	// ai's file as it was: a plain SQLite database its own sync made.
	old, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	odb := dbx.NewFromDB(old, "sqlite")
	if err := aiobject.SyncSchema(odb); err != nil {
		t.Fatal(err)
	}
	const wide = int64(5_000_000_000)
	for _, stmt := range []string{
		`INSERT INTO lease (name, time) VALUES ('sync', '2026-11-13T00:00:00.000000000Z')`,
		`INSERT INTO served (vendor, model, account, hour, answered, failed) VALUES ('openrouter', 'm', 'k1', '2026-11-13T10', 5000000000, 2)`,
		`INSERT INTO served (vendor, model, account, hour, answered, failed) VALUES ('openrouter', 'm', 'k2', '2026-11-13T11', 1, 0)`,
		`INSERT INTO routing_version (id, boot) VALUES (7, 'b7')`,
		`INSERT INTO routing_version (id, boot) VALUES (9, 'b9')`,
		// A route written before the priced column existed: every other column NULL.
		`INSERT INTO model_route (owner, model_name) VALUES ('built-in', 'legacy')`,
		`INSERT INTO model_route (owner, model_name, priced) VALUES ('built-in', 'sold', 1)`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	before := sum(t, legacy)

	// The first open after the host owns the store, with no hanzo-sql: the file is carried
	// into the seam and is what serves.
	s, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if dialect.Of(s.sql).Postgres() {
		t.Fatal("no server is set, yet the store is on one")
	}
	var served int
	if err := s.db.NewQuery("SELECT COUNT(*) FROM served").Row(&served); err != nil || served != 2 {
		t.Fatalf("carried served rows = %d, %v", served, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sum(t, legacy) != before {
		t.Fatal("the legacy file changed in the carry: it is the way back")
	}
	if !fileExists(legacy + ".carried") {
		t.Fatal("the carry left no mark, so the file would be carried again")
	}

	// Then the server, with the move on.
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	m, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if !dialect.Of(m.sql).Postgres() {
		t.Fatal("ai's store did not move to hanzo-sql")
	}
	place, err := locus.Where(namespace.System(), storeSubsystem, dir)
	if err != nil || place != locus.OnServer {
		t.Fatalf("place = %v, %v", place, err)
	}

	aiobject.UseDB(m.db)
	t.Cleanup(func() { aiobject.UseDB(nil) })
	rows, err := aiobject.ServedSince("openrouter", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	if err != nil || len(rows) != 2 {
		t.Fatalf("served after the move = %+v, %v", rows, err)
	}
	var big int64
	for _, r := range rows {
		big = max(big, r.Answered)
	}
	if big != wide {
		t.Fatalf("answered = %d, want %d: a number wider than 32 bits was cut", big, wide)
	}

	var routes []aiobject.ModelRoute
	if err := m.db.Select().From("model_route").OrderBy("model_name").All(&routes); err != nil || len(routes) != 2 {
		t.Fatalf("routes after the move = %+v, %v: an old row's NULLs must read", routes, err)
	}
	if routes[0].ModelName != "legacy" || routes[0].Priced || routes[1].ModelName != "sold" || !routes[1].Priced {
		t.Fatalf("routes = %+v: the priced flag changed in the move", routes)
	}

	var versions []aiobject.RoutingVersion
	if err := m.db.Select().From("routing_version").OrderBy("id").All(&versions); err != nil || len(versions) != 2 ||
		versions[0].Id != 7 || versions[1].Id != 9 {
		t.Fatalf("versions after the move = %+v, %v: ids must be kept", versions, err)
	}
	next := &aiobject.RoutingVersion{Boot: "b10"}
	if err := m.db.Model(next).Insert(); err != nil || next.Id <= 9 {
		t.Fatalf("the next version is numbered %d (%v), want one after 9", next.Id, err)
	}

	taken, _, err := aiobject.TakeLease("sync", time.Hour, time.Date(2026, 11, 13, 0, 30, 0, 0, time.UTC))
	if err != nil || taken {
		t.Fatalf("a lease taken half an hour ago was taken again = %v, %v", taken, err)
	}
}

func sum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
