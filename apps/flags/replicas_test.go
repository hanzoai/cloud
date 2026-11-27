package flags

// Flags runs on every replica. These tests stand a second replica beside the first: on
// hanzo-sql a second client with pools of its own over the same stores, which is what
// another pod is; on SQLite the same client, because a file belongs to one process.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// replicaOf is another replica's client beside c.
func replicaOf(t *testing.T, c *Client) *Client {
	t.Helper()
	if !dialect.Of(platformDB(t, c)).Postgres() {
		return c
	}
	other := &Client{
		stores:     cloud.NewOrgStore[*Store](cloud.Base{DataDir: t.TempDir()}, "flags", openStore),
		distinctID: "test",
		ttl:        time.Minute,
	}
	t.Cleanup(func() { _ = other.stores.CloseAll() })
	return other
}

func platformDB(t *testing.T, c *Client) *sql.DB {
	t.Helper()
	st, err := c.platform()
	if err != nil {
		t.Fatal(err)
	}
	return st.db
}

// parked waits until a caller is waiting for a lock on db's store, which is the one
// observation that tells a request waiting for its key from one that is merely slow:
// on hanzo-sql a backend asking for an advisory lock it has not been granted, on SQLite
// a caller waiting for the file's one connection.
func parked(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if dialect.Of(db).Postgres() {
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				return
			}
		} else if db.Stats().WaitCount > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing ever waited for the lock")
}

// A grant on one replica waits for an edit of the same switch on another, and takes the
// definition that edit stored; a grant of another switch does not wait.
func TestAGrantWaitsForAnotherReplicasEditOfTheSwitch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		first := newTestClient(t)
		second := replicaOf(t, first)
		ctx := context.Background()

		holding, release := make(chan struct{}), make(chan struct{})
		editing := make(chan error, 1)
		st, err := first.platform()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			editing <- st.Edit(ctx, Beta, "ann", func(json.RawMessage) (json.RawMessage, error) {
				close(holding)
				<-release
				return []byte(`{"active":true,"filters":{"groups":[{"properties":[{"key":"user","type":"person","operator":"regex","value":"^(?:u_held)$"}],"rollout_percentage":100}]}}`), nil
			})
		}()
		<-holding

		granted := make(chan error, 1)
		go func() { granted <- second.grant(superCtx, Beta, "u_late", true, "bob") }()
		parked(t, platformDB(t, second))
		select {
		case err := <-granted:
			t.Fatalf("a grant went through while another replica held the switch: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		if dialect.Of(platformDB(t, second)).Postgres() {
			// A file has one connection and every edit waits for it; the keys of a
			// shared store are independent.
			if err := second.grant(superCtx, BetaKey("koma"), "u_other", true, "bob"); err != nil {
				t.Fatalf("a grant of another switch waited for this one: %v", err)
			}
		}

		close(release)
		if err := <-editing; err != nil {
			t.Fatal(err)
		}
		if err := <-granted; err != nil {
			t.Fatal(err)
		}
		users, _ := named(mustStored(t, Beta))
		if len(users) != 2 || !(users[0] == "u_held" && users[1] == "u_late" || users[1] == "u_held" && users[0] == "u_late") {
			t.Fatalf("the grant did not take the definition the edit stored: %v", users)
		}
	})
}

// Grants made together on several replicas all land: none is a write over another's read.
func TestGrantsMadeTogetherOnTwoReplicasBothLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		first := newTestClient(t)
		replicas := []*Client{first, replicaOf(t, first), replicaOf(t, first)}
		var wg sync.WaitGroup
		var people []string
		for i := range 24 {
			u := fmt.Sprintf("u%02d", i)
			people = append(people, u)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := replicas[i%len(replicas)].grant(superCtx, Beta, u, true, "z"); err != nil {
					t.Errorf("grant %s: %v", u, err)
				}
			}()
		}
		wg.Wait()
		users, _ := named(mustStored(t, Beta))
		have := map[string]bool{}
		for _, u := range users {
			have[u] = true
		}
		for _, u := range people {
			if !have[u] {
				t.Errorf("%s's grant was lost", u)
			}
		}
	})
}
