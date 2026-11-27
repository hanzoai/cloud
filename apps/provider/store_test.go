package provider

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// testMaster is this package's throwaway data-plane key: 32 bytes, dev only. Grants' codes
// are sealed under it, as they are under the deployment's.
var testMaster = bytes.Repeat([]byte{0x2a}, 32)

// testKMS is the KMS the stores most tests open keep their codes in.
var testKMS = kmstest.New()

// storeOver is a store made over a handle somebody else opened, with the keys a deployment
// with testMaster has: for a test that seeds an older file's shape by hand first.
func storeOver(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	c, err := tenure.NewCipher(testMaster)
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, fields: f, cipher: c}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreOrgScopeIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()

		if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", ExternalID: "TA", AccountLabel: "Acme", Scopes: []string{"chat:write"}}); err != nil {
			t.Fatalf("upsert acme: %v", err)
		}
		if err := s.Upsert(ctx, Connection{Org: "globex", Provider: "slack", ExternalID: "TG", AccountLabel: "Globex"}); err != nil {
			t.Fatalf("upsert globex: %v", err)
		}

		// Each org sees only its own row.
		if list, err := s.List(ctx, "acme", ""); err != nil || len(list) != 1 || list[0].ExternalID != "TA" {
			t.Fatalf("acme list must be exactly its own: %v %+v", err, list)
		}
		if c, found, err := s.Get(ctx, "globex", "", "slack", ""); err != nil || !found || c.AccountLabel != "Globex" {
			t.Fatalf("globex get: %v found=%v %+v", err, found, c)
		}
		// A provider acme never connected is not-found (no cross-row bleed).
		if _, found, err := s.Get(ctx, "acme", "", "github", ""); err != nil || found {
			t.Fatalf("acme github must be not-found, got found=%v err=%v", found, err)
		}
		// Scopes round-trip.
		if c, _, _ := s.Get(ctx, "acme", "", "slack", ""); len(c.Scopes) != 1 || c.Scopes[0] != "chat:write" {
			t.Fatalf("scopes round-trip failed: %+v", c.Scopes)
		}
	})
}

func TestStoreUpsertPreservesConnectedAt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", AccountLabel: "v1"}); err != nil {
			t.Fatal(err)
		}
		first, _, _ := s.Get(ctx, "acme", "", "slack", "")
		time.Sleep(1100 * time.Millisecond) // cross a unix-second boundary
		if err := s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", AccountLabel: "v2"}); err != nil {
			t.Fatal(err)
		}
		second, _, _ := s.Get(ctx, "acme", "", "slack", "")
		if second.AccountLabel != "v2" {
			t.Fatalf("re-connect must update label, got %q", second.AccountLabel)
		}
		if second.ConnectedAt != first.ConnectedAt {
			t.Fatalf("connected_at must be preserved across re-connect: %d != %d", second.ConnectedAt, first.ConnectedAt)
		}
		if second.UpdatedAt < first.UpdatedAt {
			t.Fatalf("updated_at must advance: %d < %d", second.UpdatedAt, first.UpdatedAt)
		}
	})
}

func TestStoreResolveOrgByExternalID(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		_ = s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", ExternalID: "T123"})

		if org, ok, err := s.ResolveOrgByExternalID(ctx, "slack", "T123"); err != nil || !ok || org != "acme" {
			t.Fatalf("resolve T123: org=%q ok=%v err=%v", org, ok, err)
		}
		// Unknown external id → not found.
		if _, ok, _ := s.ResolveOrgByExternalID(ctx, "slack", "T999"); ok {
			t.Fatal("unknown external id must not resolve")
		}
		// Empty external id never matches (so scaffold '' rows don't collide).
		if _, ok, _ := s.ResolveOrgByExternalID(ctx, "slack", ""); ok {
			t.Fatal("empty external id must not resolve")
		}
		// Wrong provider, same id → not found (provider-scoped).
		if _, ok, _ := s.ResolveOrgByExternalID(ctx, "github", "T123"); ok {
			t.Fatal("external id is provider-scoped")
		}
	})
}

func TestStoreNonceSingleUse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.PutNonce(ctx, "n1", "acme", "slack", ""); err != nil {
			t.Fatalf("put nonce: %v", err)
		}
		// First consume with the correct binding succeeds.
		if _, ok, err := s.ConsumeNonce(ctx, "n1", "acme", "slack"); err != nil || !ok {
			t.Fatalf("first consume must succeed: ok=%v err=%v", ok, err)
		}
		// Second consume (replay) deletes zero rows.
		if _, ok, _ := s.ConsumeNonce(ctx, "n1", "acme", "slack"); ok {
			t.Fatal("replay consume must return false (single-use)")
		}

		// A nonce consumed under the WRONG org/provider binding must not fire.
		_ = s.PutNonce(ctx, "n2", "acme", "slack", "")
		if _, ok, _ := s.ConsumeNonce(ctx, "n2", "globex", "slack"); ok {
			t.Fatal("consume with wrong org must return false")
		}
		if _, ok, _ := s.ConsumeNonce(ctx, "n2", "acme", "github"); ok {
			t.Fatal("consume with wrong provider must return false")
		}
		// The correct binding still works afterward (the failed attempts didn't burn it).
		if _, ok, _ := s.ConsumeNonce(ctx, "n2", "acme", "slack"); !ok {
			t.Fatal("correct binding must still consume n2")
		}
	})
}

// TestStoreConsumeNonceConcurrentSingleWinner is the RACE proof for two (here N)
// concurrent OAuth callbacks presenting the SAME valid state: the single atomic
// DELETE ... WHERE nonce AND org AND provider (RowsAffected==1) is the gate, so
// EXACTLY ONE consumer wins and the rest see zero rows — no read-then-delete
// TOCTOU window, no double-exchange. Run under `-race`.
func TestStoreConsumeNonceConcurrentSingleWinner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.PutNonce(ctx, "hot", "acme", "slack", ""); err != nil {
			t.Fatalf("put nonce: %v", err)
		}
		const racers = 32
		var wg sync.WaitGroup
		wins := make(chan bool, racers)
		start := make(chan struct{})
		for range racers {
			wg.Go(func() {
				<-start // release all goroutines together to maximize contention
				_, ok, err := s.ConsumeNonce(ctx, "hot", "acme", "slack")
				if err != nil {
					t.Errorf("consume: %v", err)
					return
				}
				wins <- ok
			})
		}
		close(start)
		wg.Wait()
		close(wins)
		won := 0
		for ok := range wins {
			if ok {
				won++
			}
		}
		if won != 1 {
			t.Fatalf("exactly one concurrent consumer must win, got %d", won)
		}
	})
}

func TestStoreGCNonces(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		_ = s.PutNonce(ctx, "old", "acme", "slack", "")
		// GC everything created before "now+1h" — reaps the just-created nonce.
		n, err := s.GCNonces(ctx, time.Now().Add(time.Hour).Unix())
		if err != nil {
			t.Fatalf("gc: %v", err)
		}
		if n != 1 {
			t.Fatalf("gc want 1 reaped, got %d", n)
		}
		// It is gone — a later consume finds nothing.
		if _, ok, _ := s.ConsumeNonce(ctx, "old", "acme", "slack"); ok {
			t.Fatal("gc'd nonce must not consume")
		}
	})
}

func TestStoreDeleteIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		_ = s.Upsert(ctx, Connection{Org: "acme", Provider: "slack"})
		if gone, err := s.Delete(ctx, "acme", "", "slack", ""); err != nil || !gone {
			t.Fatalf("first delete: gone=%v err=%v", gone, err)
		}
		if _, found, _ := s.Get(ctx, "acme", "", "slack", ""); found {
			t.Fatal("row must be gone after delete")
		}
		if gone, err := s.Delete(ctx, "acme", "", "slack", ""); err != nil || gone {
			t.Fatalf("second delete must report gone=false: gone=%v err=%v", gone, err)
		}
	})
}

// ClaimNonce is the redemption side of a deep-link code: the code carries the org, and
// the one DELETE ... RETURNING is the single-use proof, however many connections the
// claimants hold.
func TestStoreClaimNonceSingleUse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		if err := s.PutNonce(ctx, "code1", "acme", "telegram", ""); err != nil {
			t.Fatalf("put nonce: %v", err)
		}
		// A code for another provider, or none at all, claims nothing and burns nothing.
		if org, ok, err := s.ClaimNonce(ctx, "code1", "slack"); err != nil || ok || org != "" {
			t.Fatalf("a claim under the wrong provider: %q %v %v", org, ok, err)
		}
		if _, ok, err := s.ClaimNonce(ctx, "  ", "telegram"); err != nil || ok {
			t.Fatalf("a blank code claimed: %v %v", ok, err)
		}
		if org, ok, err := s.ClaimNonce(ctx, "code1", "telegram"); err != nil || !ok || org != "acme" {
			t.Fatalf("the first claim must answer the org: %q %v %v", org, ok, err)
		}
		if _, ok, _ := s.ClaimNonce(ctx, "code1", "telegram"); ok {
			t.Fatal("a claimed code claimed twice")
		}

		if err := s.PutNonce(ctx, "hot", "globex", "telegram", ""); err != nil {
			t.Fatalf("put nonce: %v", err)
		}
		const racers = 32
		var wg sync.WaitGroup
		wins := make(chan string, racers)
		start := make(chan struct{})
		for range racers {
			wg.Go(func() {
				<-start
				org, ok, err := s.ClaimNonce(ctx, "hot", "telegram")
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if ok {
					wins <- org
				}
			})
		}
		close(start)
		wg.Wait()
		close(wins)
		var won []string
		for org := range wins {
			won = append(won, org)
		}
		if len(won) != 1 || won[0] != "globex" {
			t.Fatalf("a code claimed by %d racers, want exactly one (globex): %v", len(won), won)
		}
	})
}

// Text PostgreSQL would refuse (a NUL byte, bytes that are not UTF-8) is refused at the
// store's boundary on both engines, so the same input is the same error on both and the
// error never carries the value.
func TestStoreRefusesTextPostgresRefuses(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for name, bad := range map[string]string{"NUL": "a\x00b", "not UTF-8": "a\xffb"} {
			for what, err := range map[string]error{
				"Upsert":     s.Upsert(ctx, Connection{Org: "acme", Provider: "slack", AccountLabel: bad}),
				"Share":      s.Share(ctx, Connection{Org: "acme", Provider: "slack", Label: bad}),
				"PutNonce":   s.PutNonce(ctx, bad, "acme", "slack", ""),
				"PutGrant":   s.PutGrant(ctx, Grant{ID: "g", Org: "acme", User: "u", Provider: "p", Label: "l", Code: bad, UserCode: "x", Interval: 5, CreatedAt: 1, ExpiresAt: 9}),
				"JoinThread": s.JoinThread(ctx, "T", "C", bad),
			} {
				if !errors.Is(err, dialect.ErrText) {
					t.Errorf("%s with %s text: %v, want ErrText", what, name, err)
				}
				if err != nil && strings.Contains(err.Error(), bad) {
					t.Errorf("%s with %s text: the error carries the value", what, name)
				}
			}
			if _, err := s.MarkEvent(ctx, "slack", bad); !errors.Is(err, dialect.ErrText) {
				t.Errorf("MarkEvent with %s text: %v, want ErrText", name, err)
			}
		}
		if list, err := s.List(ctx, "acme", ""); err != nil || len(list) != 0 {
			t.Fatalf("a refused write left a row: %v %+v", err, list)
		}
	})
}

// Collisions names every org that holds an account more than one does, once each, however
// many accounts collide, and says nothing of an account one org holds twice (the org's
// own row and a person's).
func TestCollisionsGroupsEveryOrgOfEveryAccount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for _, c := range []Connection{
			{Org: "a", Provider: "github", ExternalID: "1"},
			{Org: "b", Provider: "github", ExternalID: "1"},
			{Org: "c", Provider: "github", ExternalID: "1"},
			{Org: "b", Provider: "github", Label: "two", ExternalID: "2"},
			{Org: "c", Provider: "github", Label: "two", ExternalID: "2"},
			{Org: "a", Provider: "slack", ExternalID: "1"},
			{Org: "d", Provider: "github", ExternalID: "3"},
			{Org: "d", User: "alice", Provider: "github", ExternalID: "3"},
		} {
			if err := s.Share(ctx, c); err != nil {
				t.Fatalf("share %+v: %v", c, err)
			}
		}
		got, err := s.Collisions(ctx)
		if err != nil {
			t.Fatalf("collisions: %v", err)
		}
		want := []collision{
			{Provider: "github", ExternalID: "1", Orgs: []string{"a", "b", "c"}},
			{Provider: "github", ExternalID: "2", Orgs: []string{"b", "c"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("collisions = %+v, want %+v", got, want)
		}
	})
}

// A database created before this schema gained `user`, `label` and `expires_at`
// must be brought to the declared shape, with its rows intact.
//
// This is the production failure, reproduced. `CREATE TABLE IF NOT EXISTS` is a
// no-op on an existing table, so the new columns reached new databases only, and
// GET /v1/provider answered HTTP 500 `no such column: user` on every real one
// — the Slack workspace read as disconnected to everything that asks this store
// while its install was live. Without alignConnections this test fails on the very
// first query, exactly as production did.
//
// SQLite only, by nature: a file made by an older binary has no counterpart on
// hanzo-sql, where the store is made with the declared table. The old file is built
// by hand and the store is put over its handle, so the engine is the file's whatever
// the environment names.
func TestOldConnectionsTableIsBroughtToTheDeclaredShape(t *testing.T) {
	t.Setenv(locus.Env, "")
	// The shape this schema had BEFORE user/label/expires_at: no such columns, and
	// the older (org,provider,owner) key.
	pre, err := locus.Open(namespace.System(), subsystem, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pre.Close() }()
	if _, err := pre.Exec(`
CREATE TABLE connections (
  org           TEXT NOT NULL,
  provider      TEXT NOT NULL,
  owner         TEXT NOT NULL DEFAULT '',
  external_id   TEXT NOT NULL DEFAULT '',
  account_label TEXT NOT NULL DEFAULT '',
  bot_user_id   TEXT NOT NULL DEFAULT '',
  scopes_csv    TEXT NOT NULL DEFAULT '',
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, provider, owner)
);
INSERT INTO connections (org,provider,owner,external_id,account_label,bot_user_id,scopes_csv,connected_at,updated_at)
  VALUES ('hanzo','slack','','T0231','Hanzo','U0BOT','chat:write,im:write',1700000000,1700000000);`); err != nil {
		t.Fatalf("seed old shape: %v", err)
	}

	// Migrating the old database must align the table.
	s := storeOver(t, pre)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate an old database: %v", err)
	}

	// THE symptom: the query this store makes must prepare.
	got, err := s.ListFor(context.Background(), "hanzo", "slack")
	if err != nil {
		t.Fatalf("ListFor after align: %v — this is the production 500", err)
	}
	if len(got) != 1 {
		t.Fatalf("carried %d rows, want 1 — the migration must not lose connections", len(got))
	}
	if got[0].ExternalID != "T0231" || got[0].AccountLabel != "Hanzo" {
		t.Errorf("row not carried faithfully: %+v", got[0])
	}
	// A column the old table never had takes its declared default, and '' is what
	// an org-held connection means.
	if got[0].User != "" {
		t.Errorf("user = %q, want the empty default", got[0].User)
	}

	// Idempotent: a second migration must be a no-op, not another rebuild.
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	again, err := s.ListFor(context.Background(), "hanzo", "slack")
	if err != nil || len(again) != 1 {
		t.Fatalf("second migrate: %v rows=%d", err, len(again))
	}
}

// A process with no data-plane master writes no code in the clear and reads none: the
// store does not open.
func TestNoMasterOpensNoStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openStore(t.TempDir(), master, testKMS); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

func scalarCount(s *Store, q string) (int, error) {
	var n int
	err := s.db.QueryRow(q).Scan(&n)
	return n, err
}

// A platform's retry of one event may land on any replica, and an agent turn is
// BILLED, so of three replicas handed the same event exactly one may take it. On
// hanzo-sql the replicas are three stores opened on one database, each with its own
// pool, as three pods are; every event is marked by all three at once. A SQLite file
// belongs to one process, so there the three share the file's one handle and the
// claim is its single connection.
//
// MUTATION: dedupe in a map in the Store (the process-local table this replaced) and
// each replica takes every event.
func TestEventsAreTakenByOneReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		var pods []*Store
		for range 3 {
			if len(pods) > 0 && locus.DSN() == "" {
				pods = append(pods, pods[0])
				continue
			}
			s, err := openStore(dir, testMaster, testKMS)
			if err != nil {
				t.Fatalf("openStore: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			pods = append(pods, s)
		}
		const events = 20
		var took [events]atomic.Int64
		var wg sync.WaitGroup
		for _, s := range pods {
			for e := range events {
				wg.Add(1)
				go func() {
					defer wg.Done()
					fresh, err := s.MarkEvent(context.Background(), "slack", fmt.Sprintf("Ev-%d", e))
					if err != nil {
						t.Errorf("mark: %v", err)
						return
					}
					if fresh {
						took[e].Add(1)
					}
				}()
			}
		}
		wg.Wait()
		for e := range events {
			if n := took[e].Load(); n != 1 {
				t.Errorf("event %d was taken by %d replicas, want exactly one", e, n)
			}
		}
	})
}
