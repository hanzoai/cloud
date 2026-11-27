package principals

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// fieldsMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey).
var fieldsMaster = bytes.Repeat([]byte{0x5a}, 32)

func openFieldsStore(t *testing.T, open sqltest.Open) *store {
	t.Helper()
	st, err := openStore(open(namespace.System(), subsystem), fieldsMaster)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func rawOf(t *testing.T, st *store, q string, args ...any) string {
	t.Helper()
	var v string
	if err := st.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// The names and addresses of an org that screened close to a designation, why, and a
// reviewer's note, and a payer's clearance of a payee, are ciphertext in the database on
// both engines; every read gives them back, and a decision reseals the note.
func TestMatchesAndClearancesAreSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		st := openFieldsStore(t, open)
		ctx := context.Background()
		m := matchRow{id: "m1", org: "acme", list: "ofac", ref: "SDN-1", entry: "ACME TRADING LLC", reason: "name resembles ACME TRADING LLC", status: matchOpen,
			hits: []iam.Hit{{Kind: hitName, Role: "entity", Subject: "Acme Trading Ltd of 12 Rue Cler, Paris"}}, score: 0.93, found: 10, seen: 10}
		if err := st.within(ctx, func(tx *sql.Tx) error {
			if err := st.putMatch(ctx, tx, m); err != nil {
				return err
			}
			return st.putClearance(ctx, tx, "cl1", "acme", "globex", true, true, `{"payee":"globex","note":"Ada Lovelace signed off"}`, 11)
		}); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ q, plain string }{
			{`SELECT hits FROM matches WHERE id = 'm1'`, "12 Rue Cler"},
			{`SELECT reason FROM matches WHERE id = 'm1'`, "resembles"},
			{`SELECT body FROM clearances WHERE id = 'cl1'`, "Lovelace"},
		} {
			if v := rawOf(t, st, c.q); !strings.HasPrefix(v, "seal:") || strings.Contains(v, c.plain) {
				t.Errorf("%s holds %.30q: not sealed", c.q, v)
			}
		}
		if rawOf(t, st, `SELECT entry FROM matches WHERE id = 'm1'`) != "ACME TRADING LLC" || rawOf(t, st, `SELECT status FROM matches WHERE id = 'm1'`) != matchOpen {
			t.Error("entry or status is not plain")
		}

		got, err := st.match(ctx, st.db, "m1")
		if err != nil || got.reason != m.reason || len(got.hits) != 1 || got.hits[0].Subject != m.hits[0].Subject {
			t.Fatalf("match = %+v, %v", got, err)
		}
		if list, err := st.ofOrg(ctx, st.db, "acme"); err != nil || len(list) != 1 || list[0].hits[0].Subject != m.hits[0].Subject {
			t.Fatalf("ofOrg = %+v, %v", list, err)
		}
		if q, err := st.queue(ctx, matchOpen, 10); err != nil || len(q) != 1 {
			t.Fatalf("queue = %+v, %v", q, err)
		}
		if err := st.within(ctx, func(tx *sql.Tx) error {
			return st.decide(ctx, tx, "m1", matchOpen, matchCleared, "reviewer_1", "different company, Ada checked the register", 20)
		}); err != nil {
			t.Fatal(err)
		}
		if v := rawOf(t, st, `SELECT note FROM matches WHERE id = 'm1'`); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "register") {
			t.Errorf("note holds %.30q: not sealed", v)
		}
		if got, err := st.match(ctx, st.db, "m1"); err != nil || got.note != "different company, Ada checked the register" || got.status != matchCleared {
			t.Fatalf("after a decision: %+v, %v", got, err)
		}

		if body, err := st.clearance(ctx, "acme", "cl1"); err != nil || !strings.Contains(body, "Lovelace") {
			t.Fatalf("clearance = %q, %v", body, err)
		}
		if list, err := st.clearances(ctx, "acme", "", 10); err != nil || len(list) != 1 || !strings.Contains(list[0], "Lovelace") {
			t.Fatalf("clearances = %v, %v", list, err)
		}
		// Still a record: nothing rewrites or deletes it.
		if _, err := st.db.Exec(`UPDATE clearances SET payer_ok = 0 WHERE id = 'cl1'`); err == nil {
			t.Error("a clearance was rewritten")
		}
	})
}

// A body lifted into another clearance, or planted in the clear, is refused.
func TestASealedColumnIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		st := openFieldsStore(t, open)
		ctx := context.Background()
		m := func(id string) matchRow {
			return matchRow{id: id, org: "acme", list: "ofac", ref: id, entry: "E", reason: "r " + id, status: matchOpen, hits: []iam.Hit{{Kind: hitName, Role: "entity", Subject: id}}, found: 1, seen: 1}
		}
		if err := st.within(ctx, func(tx *sql.Tx) error {
			for _, id := range []string{"a", "b"} {
				if err := st.putMatch(ctx, tx, m(id)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE matches SET reason = $1 WHERE id = 'b'`, rawOf(t, st, `SELECT reason FROM matches WHERE id = 'a'`)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.match(ctx, st.db, "b"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("B read A's reason: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE matches SET reason = 'planted' WHERE id = 'b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ofOrg(ctx, st.db, "acme"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		for _, master := range [][]byte{nil, fieldsMaster[:31]} {
			if st, err := openStore(open(namespace.System(), subsystem), master); err == nil {
				_ = st.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before the columns were sealed holds plaintext in them, and its
// clearances are guarded by a trigger that refuses an UPDATE. It is built by hand, so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := locus.Open(namespace.System(), subsystem, dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(db, fieldsMaster)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TRIGGER clearances_final`,
		`INSERT INTO matches (id, org, list, ref, entry, hits, score, reason, status, note, found_at, seen_at) VALUES ('m_old', 'acme', 'ofac', 'r', 'E', '[{"subject":"Old Name","role":"entity","kind":"name"}]', 0.9, 'old reason', 'open', 'old note', 1, 1)`,
		`INSERT INTO clearances (id, payer, payee, allowed, payer_ok, body, created_at) VALUES ('cl_old', 'acme', 'globex', 1, 1, '{"old":"body"}', 1)`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ {
		db, err := locus.Open(namespace.System(), subsystem, dir)
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(db, fieldsMaster)
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		got, err := st.match(ctx, st.db, "m_old")
		if err != nil || got.reason != "old reason" || got.note != "old note" || len(got.hits) != 1 {
			t.Fatalf("pass %d: a legacy match = %+v, %v", pass, got, err)
		}
		if body, err := st.clearance(ctx, "acme", "cl_old"); err != nil || body != `{"old":"body"}` {
			t.Fatalf("pass %d: a legacy clearance = %q, %v", pass, body, err)
		}
		for _, q := range []string{`SELECT hits FROM matches`, `SELECT reason FROM matches`, `SELECT note FROM matches`, `SELECT body FROM clearances`} {
			if v := rawOf(t, st, q); !strings.HasPrefix(v, "seal:") {
				t.Errorf("pass %d: %s is still %.20q after the upgrade", pass, q, v)
			}
		}
		// The trigger is back.
		if _, err := st.db.Exec(`UPDATE clearances SET payer_ok = 0 WHERE id = 'cl_old'`); err == nil {
			t.Errorf("pass %d: a clearance was rewritten", pass)
		}
		_ = st.Close()
	}
}
