package studio

// Studio runs on every replica. These tests stand a second replica beside the first:
// on hanzo-sql a second pool on the same store, which is what another pod is; on
// SQLite the same state, because a file belongs to one process.

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/orm/pgtest"
	"github.com/hanzoai/orm/tenant"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
)

// wallet is the money one payer has, which the gate weighs a reservation against.
type wallet struct {
	cents int64
	asked atomic.Int32
	meet  chan struct{} // closed when the first two askers have both asked
	mu    sync.Mutex
	n     int
}

// afford is the meter's Authorize against the wallet's balance. The first two askers
// wait for each other before they answer, so both have read what is held before either
// has made its reservation: the moment two replicas could each see the other's as absent.
func (w *wallet) afford(_ context.Context, _ cloud.Payer, cents int64) error {
	w.mu.Lock()
	w.n++
	first := w.n <= 2
	if w.n == 2 {
		close(w.meet)
	}
	w.mu.Unlock()
	w.asked.Add(1)
	if first {
		select {
		case <-w.meet:
		case <-time.After(300 * time.Millisecond): // alone: one process's lock held the other out
		}
	}
	if cents > w.cents {
		return meter.ErrInsufficientBalance
	}
	return nil
}

// replica is another process's state over the store dir names.
func replica(t *testing.T, dir string, w *wallet) *state {
	t.Helper()
	db, closeDB, err := openDB(context.Background(), dir)
	if err != nil {
		t.Fatalf("open a replica's store: %v", err)
	}
	t.Cleanup(func() { _ = closeDB() })
	return &state{db: db, closeDB: closeDB, afford: w.afford}
}

// reserveRun reserves cents for a run of org, the way a create does: the reservation
// is the run's own row, committed with the weighing.
func reserveRun(s *state, org, id string, cents int64) error {
	ctx := scoped(context.Background(), org)
	return s.reserve(ctx, cloud.Payer{Wallet: account.Org(org)}, cents, func(tx *tenant.DB) error {
		return tx.Insert(ctx, "runs", runRow{
			ID: id, Creator: "ann", Kind: "image", Title: id, Status: queued, Graph: "{}", Input: "{}", Tokens: "{}",
			Root: id, Estimate: cents, Payer: "{}", Created: now(),
		})
	})
}

func engines(t *testing.T, body func(t *testing.T, replicas func(w *wallet) (a, b *state))) {
	t.Run("sqlite", func(t *testing.T) {
		body(t, func(w *wallet) (*state, *state) {
			s := replica(t, t.TempDir(), w)
			return s, s
		})
	})
	t.Run("postgres", func(t *testing.T) {
		if pg == nil {
			t.Skip("no PostgreSQL on this machine: " + why)
		}
		dir := strings.TrimPrefix(strings.Fields(pg.DSN("", ""))[0], "host=")
		t.Setenv("CLOUD_SQL_ADMIN_DSN", "postgres://"+pgtest.Super+"@localhost:5432/postgres?sslmode=disable&host="+url.QueryEscape(dir))
		body(t, func(w *wallet) (*state, *state) {
			data := t.TempDir()
			return replica(t, data, w), replica(t, data, w)
		})
	})
}

// A wallet that covers one run is reserved by one of two creates made together on two
// replicas, and the other is refused: neither weighs the wallet before the other's
// reservation is there to be weighed.
func TestTwoReplicasReserveOneWalletOnce(t *testing.T) {
	engines(t, func(t *testing.T, replicas func(*wallet) (*state, *state)) {
		w := &wallet{cents: 100, meet: make(chan struct{})}
		a, b := replicas(w)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, s := range []*state{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = reserveRun(s, "once", []string{"run_a", "run_b"}[i], 60)
			}()
		}
		wg.Wait()
		var won int
		for _, err := range errs {
			if err == nil {
				won++
			}
		}
		if won != 1 {
			t.Fatalf("%d of two creates reserved a wallet that covers one: %v", won, errs)
		}
		held, err := held(scoped(context.Background(), "once"), a.db)
		if err != nil || held != 60 {
			t.Fatalf("the org holds %d cents (%v), want 60", held, err)
		}
	})
}

// Creates that fit together are all reserved, whichever replica takes them, and a
// create for another org does not wait on this org's wallet.
func TestTwoReplicasReserveWhatFits(t *testing.T) {
	engines(t, func(t *testing.T, replicas func(*wallet) (*state, *state)) {
		w := &wallet{cents: 100, meet: make(chan struct{})}
		a, b := replicas(w)
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i, c := range []struct {
			s       *state
			org, id string
		}{{a, "fits-one", "r1"}, {b, "fits-one", "r2"}, {a, "fits-two", "r3"}, {b, "fits-two", "r4"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = reserveRun(c.s, c.org, c.id, 40)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("create %d: %v", i, err)
			}
		}
		for _, org := range []string{"fits-one", "fits-two"} {
			if held, err := held(scoped(context.Background(), org), a.db); err != nil || held != 80 {
				t.Errorf("%s holds %d cents (%v), want 80", org, held, err)
			}
		}
	})
}
