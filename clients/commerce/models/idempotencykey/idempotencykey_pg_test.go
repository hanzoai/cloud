package idempotencykey_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/idempotencykey"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// openReproPostgres returns a real Postgres-backed db.DB on a FRESH, uniquely
// named database (dropped on cleanup), or SKIPS if no local Postgres is
// reachable.
//
// Why this test needs the real Postgres backend: production commerce runs on
// Postgres — commerce.go wires db.NewPostgresDB when SQL_URL is set and installs
// it via datastore.SetDefaultDB, and the billing usage path calls datastore.New
// directly against it. The in-repo unit tests, by contrast, run on SQLite
// (util/test/ae). The idempotency round-trip defect is INVISIBLE on SQLite —
// SQLiteDB.Get has a kind-less fallback (db/sqlite.go) — and only bites on
// Postgres, whose Get requires an exact kind match (db/postgres.go). A faithful
// reproduction of the production double-charge therefore MUST exercise Postgres.
func openReproPostgres(t *testing.T) (backend db.DB, cleanup func()) {
	t.Helper()

	base := os.Getenv("COMMERCE_TEST_PG_DSN")
	if base == "" {
		u := "postgres"
		if cu, err := user.Current(); err == nil && cu.Username != "" {
			u = cu.Username
		}
		base = fmt.Sprintf("postgres://%s@localhost:5432/postgres?sslmode=disable", u)
	}

	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Skipf("no local postgres (open: %v)", err)
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Skipf("local postgres not reachable (ping: %v)", err)
	}

	name := fmt.Sprintf("commerce_idem_repro_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Skipf("cannot create test database: %v", err)
	}

	childURL, err := url.Parse(base)
	if err != nil {
		admin.Exec("DROP DATABASE " + name)
		admin.Close()
		t.Fatalf("parse base DSN: %v", err)
	}
	childURL.Path = "/" + name

	pg, err := db.NewPostgresDB(&db.PostgresDBConfig{DSN: childURL.String(), TenantID: "system"})
	if err != nil {
		admin.Exec("DROP DATABASE " + name)
		admin.Close()
		t.Fatalf("NewPostgresDB: %v", err)
	}

	return pg, func() {
		pg.Close()
		admin.Exec("DROP DATABASE " + name)
		admin.Close()
	}
}

// TestBegin_Idempotent_NoDoubleDebit_Postgres reproduces the LIVE double-charge
// on the production (Postgres) backend. It mirrors the money path of
// billing.RecordUsage — Begin the guard, and only when it is NOT a replay
// perform a withdraw + Complete — and asserts that two submits of the SAME
// (scope, key) perform AT MOST ONE withdraw.
//
// Each debit builds its OWN datastore (datastore.NewWithDB) exactly as each HTTP
// request does in production (datastore.New(org.Namespaced(c))), both sharing the
// one Postgres store. On the unfixed code the second Begin cannot find the guard
// its own first write created (the read decodes the deterministic id into a
// KIND-LESS key, which Postgres's kind-qualified Get never matches), so it
// returns replay=false and a SECOND withdraw is issued: two rows → DOUBLE CHARGE.
// After the fix the second Begin replays and no second withdraw is written.
//
// The sibling TestRecordUsage_Idempotent_NoDoubleDebit (SQLite, via ae) PASSES on
// the unfixed code — SQLite's kind-less Get fallback hides the bug — which is
// exactly why this Postgres-backed reproduction is required.
func TestBegin_Idempotent_NoDoubleDebit_Postgres(t *testing.T) {
	pg, cleanup := openReproPostgres(t)
	defer cleanup()

	const ns = "usage-idem-pg-org"
	ctx := nscontext.WithNamespace(context.Background(), ns)

	const subject = ns + "/alice@example.com"
	const scope = "billing-usage"
	const key = "req-pg-abc-123" // the requestId chat sends per spend

	moves := 0
	debitOnce := func() {
		d := datastore.NewWithDB(ctx, pg)
		rec, replay, err := idempotencykey.Begin(d, scope, key)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if replay {
			if rec.Status != idempotencykey.StatusCompleted {
				t.Fatalf("replay but status=%q, want completed", rec.Status)
			}
			return
		}
		moves++
		trans := transaction.New(d)
		trans.Type = transaction.Withdraw
		trans.SourceId = subject
		trans.SourceKind = "iam-user"
		trans.Currency = currency.USD
		trans.Amount = currency.Cents(100) // $1.00 debit
		trans.Tags = "api-usage"
		if err := trans.Create(); err != nil {
			t.Fatalf("withdraw Create: %v", err)
		}
		if err := idempotencykey.Complete(rec, `{"type":"withdraw","amount":100}`); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	debitOnce() // first spend
	debitOnce() // retry with the SAME key — MUST replay, never re-debit

	if moves != 1 {
		t.Fatalf("DOUBLE-CHARGE: performed %d debits for one (scope,key); want 1 — "+
			"Begin failed to find the prior guard on the Postgres backend.", moves)
	}

	// Independent evidence straight from Postgres: exactly ONE withdraw row.
	verify := datastore.NewWithDB(ctx, pg)
	var withdraws []*transaction.Transaction
	if _, err := transaction.Query(verify).
		Filter("SourceId=", subject).
		Filter("Type=", string(transaction.Withdraw)).
		GetAll(&withdraws); err != nil {
		t.Fatalf("count withdraws: %v", err)
	}
	if len(withdraws) != 1 {
		t.Fatalf("Postgres holds %d withdraw rows for %s; want 1 (double charge persisted)",
			len(withdraws), subject)
	}
}
