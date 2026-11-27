package source

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	// devmaster keys this test binary: cek opens nothing without a master.
	_ "github.com/hanzoai/cloud/sdk/kit/master"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	luxlog "github.com/luxfi/log"
)

// sqlite runs a test body on both engines (SQLite and PostgreSQL): "sources" is
// in manifest.SQLStores, so production runs it on hanzo-sql and local dev on SQLite.
func sqlite(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) { f(t) })
}

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func testStore(t *testing.T) *Store {
	t.Helper()
	b := cloud.Base{Log: luxlog.New("test"), DataDir: t.TempDir()}
	stores := cloud.NewOrgStore[*Store](b, "sources", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	s, err := stores.For(namespaceOf("acme"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return s
}

func key(n int) string { return fmt.Sprintf("key-%d", n) }

func link(t *testing.T, s *Store, subject string, n int, at time.Time) (row, bool, error) {
	t.Helper()
	return s.Link(context.Background(), "acme", row{Subject: subject, Kind: "desktop", Name: "mac", OS: "darwin", Arch: "arm64", Version: "1", Key: key(n)},
		fmt.Sprintf("src_%s_%d", subject, n), at)
}

func TestStoreLink(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		s := testStore(t)

		a, created, err := link(t, s, "alice", 1, t0)
		if err != nil || !created {
			t.Fatalf("first link: created=%v err=%v", created, err)
		}
		// Same subject and key: same id, not created, fields refreshed.
		again, created, err := s.Link(context.Background(), "acme", row{Subject: "alice", Kind: "desktop", Name: "renamed", OS: "darwin", Arch: "arm64", Version: "2", Key: key(1)}, "src_other", t0.Add(time.Minute))
		if err != nil || created || again.ID != a.ID || again.Name != "renamed" {
			t.Fatalf("relink: %+v created=%v err=%v", again, created, err)
		}
		// Same key, different subject: a different source.
		b, created, err := link(t, s, "bob", 1, t0)
		if err != nil || !created || b.ID == a.ID {
			t.Fatalf("bob same key: %+v created=%v err=%v", b, created, err)
		}
		// A revoked source's key links anew.
		if _, err := s.Revoke(context.Background(), "alice", a.ID, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		c, created, err := s.Link(context.Background(), "acme", row{Subject: "alice", Kind: "desktop", Name: "mac", OS: "darwin", Arch: "arm64", Version: "1", Key: key(1)}, "src_new", t0.Add(2*time.Minute))
		if err != nil || !created || c.ID != "src_new" {
			t.Fatalf("relink after revoke: %+v created=%v err=%v", c, created, err)
		}
	})
}

func TestStoreCapAndRate(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		s := testStore(t)
		for n := 0; n < maxActive; n++ {
			if _, _, err := link(t, s, "alice", n, t0.Add(time.Duration(n)*7*time.Minute)); err != nil {
				t.Fatalf("link %d: %v", n, err)
			}
		}
		late := t0.Add(maxActive * 7 * time.Minute)
		if _, _, err := link(t, s, "alice", 99, late); !errors.Is(err, errCap) {
			t.Fatalf("26th link: want errCap, got %v", err)
		}
		// The cap is per person.
		if _, _, err := link(t, s, "bob", 99, late); err != nil {
			t.Fatalf("bob under his own cap: %v", err)
		}
		// Relinking an existing install is not a new link and not capped.
		if _, created, err := link(t, s, "alice", 3, late); err != nil || created {
			t.Fatalf("relink at cap: created=%v err=%v", created, err)
		}
	})
	sqlite(t, func(t *testing.T) {
		s := testStore(t)
		for n := 0; n < maxLinksPerHour; n++ {
			if _, _, err := link(t, s, "alice", n, t0.Add(time.Duration(n)*time.Second)); err != nil {
				t.Fatalf("link %d: %v", n, err)
			}
		}
		if _, _, err := link(t, s, "alice", 50, t0.Add(time.Minute)); !errors.Is(err, errLinkRate) {
			t.Fatalf("11th link in an hour: want errLinkRate, got %v", err)
		}
		if _, _, err := link(t, s, "alice", 50, t0.Add(2*time.Hour)); err != nil {
			t.Fatalf("link after the hour: %v", err)
		}
	})
}

func TestStoreIsolationAndRevoke(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		ctx := context.Background()
		s := testStore(t)
		a, _, _ := link(t, s, "alice", 1, t0)

		if _, err := s.Get(ctx, "bob", a.ID); !errors.Is(err, errNotFound) {
			t.Errorf("bob Get alice's: want errNotFound, got %v", err)
		}
		if _, err := s.Revoke(ctx, "bob", a.ID, t0); !errors.Is(err, errNotFound) {
			t.Errorf("bob Revoke alice's: want errNotFound, got %v", err)
		}
		if err := s.Beat(ctx, "bob", a.ID, beat{At: 1}, t0); !errors.Is(err, errNotFound) {
			t.Errorf("bob Beat alice's: want errNotFound, got %v", err)
		}
		if l, err := s.List(ctx, "bob"); err != nil || len(l) != 0 {
			t.Errorf("bob List: %v %v", l, err)
		}
		if g, err := s.Get(ctx, "alice", a.ID); err != nil || g.RevokedAt != 0 {
			t.Fatalf("alice Get: %+v %v", g, err)
		}

		if err := s.Beat(ctx, "alice", a.ID, beat{At: 100, Version: "1", Providers: []prov{{"claude", "claude-1", "login", "ready", "max", false}}}, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Revoke(ctx, "alice", a.ID, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		first, _ := s.Get(ctx, "alice", a.ID)
		if first.RevokedAt != t0.Add(time.Hour).Unix() || len(first.Providers) != 0 {
			t.Errorf("revoked row: %+v", first)
		}
		// Revoking again keeps the first revoked_at.
		if _, err := s.Revoke(ctx, "alice", a.ID, t0.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if second, _ := s.Get(ctx, "alice", a.ID); second.RevokedAt != first.RevokedAt {
			t.Errorf("second revoke moved revoked_at")
		}
		if err := s.Beat(ctx, "alice", a.ID, beat{At: 200}, t0.Add(3*time.Hour)); !errors.Is(err, errRevoked) {
			t.Errorf("beat after revoke: want errRevoked, got %v", err)
		}
		if l, _ := s.List(ctx, "alice"); len(l) != 0 {
			t.Errorf("a revoked source is listed: %v", l)
		}
	})
}

func TestStoreBeat(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		ctx := context.Background()
		s := testStore(t)
		a, _, _ := link(t, s, "alice", 1, t0)
		one := []prov{{"claude", "claude-1", "login", "ready", "max", false}, {"openai", "key-1", "key", "limited", "", false}}
		cases := []struct {
			name string
			b    beat
			at   time.Time
			want error
			n    int
		}{
			{"first", beat{At: 100, Version: "2", Providers: one}, t0, nil, 2},
			{"too soon", beat{At: 101, Version: "2", Providers: one}, t0.Add(5 * time.Second), errTooSoon, 2},
			{"stale", beat{At: 100, Version: "2", Providers: one}, t0.Add(time.Minute), errStale, 2},
			{"replaces providers", beat{At: 102, Version: "3", Providers: one[:1]}, t0.Add(time.Minute), nil, 1},
			{"clears providers", beat{At: 103, Version: "3"}, t0.Add(2 * time.Minute), nil, 0},
		}
		for _, c := range cases {
			if err := s.Beat(ctx, "alice", a.ID, c.b, c.at); !errors.Is(err, c.want) {
				t.Fatalf("%s: want %v, got %v", c.name, c.want, err)
			}
			g, _ := s.Get(ctx, "alice", a.ID)
			if len(g.Providers) != c.n {
				t.Fatalf("%s: providers %d, want %d", c.name, len(g.Providers), c.n)
			}
		}
	})
}

// A store opened before shared logins has source_providers without shared; opening
// it adds the column, so a read of a source does not fail on it.
func TestAStoreFromBeforeSharedLoginsIsMigrated(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		s := testStore(t)
		if _, err := s.db.Exec(`ALTER TABLE source_providers DROP COLUMN shared`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		s, err := openStore(s.db)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		r, _, err := link(t, s, "alice", 1, t0)
		if err != nil {
			t.Fatalf("link: %v", err)
		}
		if _, err := s.Get(context.Background(), "alice", r.ID); err != nil {
			t.Fatalf("get after migration: %v", err)
		}
	})
}

// An existing SQLite sources.db file moves cleanly to hanzo-sql when CLOUD_SQL_MOVE
// is set, preserving linked sources and providers on the server schema.
func TestSourcesMoveToSQL(t *testing.T) {
	dir := t.TempDir()
	ns := namespaceOf("acme")

	// 1. Write rows to a local SQLite sources.db (no DSN).
	t.Setenv(locus.Env, "")
	b := cloud.Base{Log: luxlog.New("test"), DataDir: dir}
	fileStores := cloud.NewOrgStore[*Store](b, "sources", openStore)
	fs, err := fileStores.For(ns)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	linked, _, err := link(t, fs, "alice", 1, t0)
	if err != nil {
		t.Fatalf("link on file: %v", err)
	}
	if err := fs.Beat(context.Background(), "alice", linked.ID, beat{
		At: 100, Version: "1.2.3",
		Providers: []prov{{Provider: "anthropic", Instance: "default", Kind: "cli", State: "ready", Shared: true}},
	}, t0); err != nil {
		t.Fatalf("beat on file: %v", err)
	}
	if err := fileStores.CloseAll(); err != nil {
		t.Fatalf("close file store: %v", err)
	}

	// 2. Point at PostgreSQL with CLOUD_SQL_MOVE=lazy and reopen through OrgStore.
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	pgStores := cloud.NewOrgStore[*Store](b, "sources", openStore)
	t.Cleanup(func() { _ = pgStores.CloseAll() })
	ps, err := pgStores.For(ns)
	if err != nil {
		t.Fatalf("open moved store: %v", err)
	}
	place, err := locus.Where(ns, "sources", dir)
	if err != nil || place != locus.OnServer {
		t.Fatalf("place after move = %v (%v), want OnServer", place, err)
	}
	got, err := ps.Get(context.Background(), "alice", linked.ID)
	if err != nil {
		t.Fatalf("get after move: %v", err)
	}
	if got.Version != "1.2.3" || len(got.Providers) != 1 || !got.Providers[0].Shared {
		t.Fatalf("moved row = %+v, want version=1.2.3 with 1 shared provider", got)
	}
}
