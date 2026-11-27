package tel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// rawColumn reads a column as the database holds it, past the store.
func rawColumn(t *testing.T, s *Store, table, col, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT `+col+` FROM `+table+` WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read %s.%s: %v", table, col, err)
	}
	return v
}

func notPlain(t *testing.T, where, stored, plain string) {
	t.Helper()
	if !strings.HasPrefix(stored, "seal:") || strings.Contains(stored, plain) || strings.Contains(stored, plain[len(plain)-4:]) {
		t.Errorf("%s is stored as %.30q: not sealed", where, stored)
	}
}

// The numbers a person called from or texted, and what they said, are ciphertext in the
// database on both engines, and the store gives them back whole.
func TestPersonalDataIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		n := Number{Org: "acme", ID: "n1", E164: "+14155550100", Country: "US", Monthly: 150, Currency: "usd"}
		c := Call{Org: "acme", ID: "c1", From: "+14155550123", To: "+14155550100", Status: "queued", Agent: "agent_1"}
		m := SMS{Org: "acme", ID: "m1", From: "+14155550123", To: "+14155550100", Text: "my account number is 99-4471-23", Status: "sent"}
		if err := s.PutNumber(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := s.PutCall(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := s.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}

		notPlain(t, "tel_numbers.e164", rawColumn(t, s, "tel_numbers", "e164", "n1"), n.E164)
		notPlain(t, "tel_calls.from_no", rawColumn(t, s, "tel_calls", "from_no", "c1"), c.From)
		notPlain(t, "tel_calls.to_no", rawColumn(t, s, "tel_calls", "to_no", "c1"), c.To)
		notPlain(t, "tel_messages.from_no", rawColumn(t, s, "tel_messages", "from_no", "m1"), m.From)
		notPlain(t, "tel_messages.to_no", rawColumn(t, s, "tel_messages", "to_no", "m1"), m.To)
		notPlain(t, "tel_messages.body", rawColumn(t, s, "tel_messages", "body", "m1"), m.Text)
		if b := rawColumn(t, s, "tel_numbers", "e164_blind", "n1"); strings.Contains(b, "4155550100") || len(b) != 64 {
			t.Errorf("e164_blind is %q: not a blind index", b)
		}
		// What a predicate reads stays readable.
		if rawColumn(t, s, "tel_calls", "status", "c1") != "queued" || rawColumn(t, s, "tel_numbers", "country", "n1") != "US" {
			t.Error("status or country is not plain")
		}

		if got, err := s.Number(ctx, "acme", "n1"); err != nil || got.E164 != n.E164 || got.Monthly != 150 {
			t.Fatalf("Number = %+v, %v", got, err)
		}
		if got, err := s.NumberByE164(ctx, "acme", " "+n.E164+" "); err != nil || got.ID != "n1" || got.E164 != n.E164 {
			t.Fatalf("NumberByE164 = %+v, %v", got, err)
		}
		if list, err := s.Numbers(ctx, "acme"); err != nil || len(list) != 1 || list[0].E164 != n.E164 {
			t.Fatalf("Numbers = %+v, %v", list, err)
		}
		if got, err := s.Call(ctx, "acme", "c1"); err != nil || got != c {
			t.Fatalf("Call = %+v, %v; want %+v", got, err, c)
		}
		if list, err := s.Calls(ctx, "acme"); err != nil || len(list) != 1 || list[0] != c {
			t.Fatalf("Calls = %+v, %v", list, err)
		}
		if list, err := s.Messages(ctx, "acme"); err != nil || len(list) != 1 || list[0] != m {
			t.Fatalf("Messages = %+v, %v", list, err)
		}
		// A status report does not touch what is sealed.
		c.Status = "completed"
		if err := s.PutCall(ctx, c); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Call(ctx, "acme", "c1"); err != nil || got.From != c.From || got.Status != "completed" {
			t.Fatalf("after a status report: %+v, %v", got, err)
		}
		// Another org sealing the same number: the lookup is the blind index, so a
		// foreign org still finds nothing, and the unique index still holds one holder.
		if err := s.PutNumber(ctx, Number{Org: "globex", ID: "n2", E164: n.E164}); !dialect.Unique(err) {
			t.Fatalf("a second org recording the same number: %v", err)
		}
	})
}

// A number or text lifted into another row or tenant, or planted in the clear, is
// refused instead of read as the store's own.
func TestASealedColumnIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		for _, m := range []SMS{{Org: "acme", ID: "a", From: "+1415", To: "+1416", Text: "one"}, {Org: "acme", ID: "b", From: "+1417", To: "+1418", Text: "two"}} {
			if err := s.PutMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE tel_messages SET body = $1 WHERE id = 'b'`, rawColumn(t, s, "tel_messages", "body", "a")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Messages(ctx, "acme"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("B read A's text: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE tel_messages SET body = 'planted' WHERE id = 'b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Messages(ctx, "acme"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE tel_messages SET org = 'evil' WHERE id = 'a'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Messages(ctx, "evil"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a message moved to another tenant opened: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openStore(t.TempDir(), master); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before the columns were sealed holds plaintext in them, has no
// blind index, and keeps one holder per number with a unique index over the number
// itself. It is built by hand (the rows are plaintext because the code that wrote them
// was), so this stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`DROP INDEX tel_numbers_blind`,
		`ALTER TABLE tel_numbers DROP COLUMN e164_blind`,
		`CREATE UNIQUE INDEX tel_numbers_e164 ON tel_numbers (e164)`,
		`INSERT INTO tel_numbers (org, id, e164, country, bought) VALUES ('acme', 'n_old', '+14155550100', 'US', 1)`,
		`INSERT INTO tel_calls (org, id, from_no, to_no, status, started) VALUES ('acme', 'c_old', '+14155550123', '+14155550100', 'completed', 1)`,
		`INSERT INTO tel_messages (org, id, from_no, to_no, body, status, sent) VALUES ('acme', 'm_old', '+14155550123', '+14155550100', 'old text', 'sent', 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		s, err = openStore(dir, testMaster)
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		if got, err := s.NumberByE164(ctx, "acme", "+14155550100"); err != nil || got.ID != "n_old" || got.E164 != "+14155550100" {
			t.Fatalf("pass %d: a legacy number by value: %+v, %v", pass, got, err)
		}
		if err := s.PutNumber(ctx, Number{Org: "globex", ID: "n_dup", E164: "+14155550100"}); !dialect.Unique(err) {
			t.Fatalf("pass %d: a legacy number is not held by one org: %v", pass, err)
		}
		if got, err := s.Call(ctx, "acme", "c_old"); err != nil || got.From != "+14155550123" || got.To != "+14155550100" {
			t.Fatalf("pass %d: a legacy call: %+v, %v", pass, got, err)
		}
		if list, err := s.Messages(ctx, "acme"); err != nil || len(list) != 1 || list[0].Text != "old text" {
			t.Fatalf("pass %d: a legacy message: %+v, %v", pass, list, err)
		}
		for _, c := range []struct{ table, col, id string }{
			{"tel_numbers", "e164", "n_old"}, {"tel_calls", "from_no", "c_old"}, {"tel_calls", "to_no", "c_old"},
			{"tel_messages", "from_no", "m_old"}, {"tel_messages", "to_no", "m_old"}, {"tel_messages", "body", "m_old"},
		} {
			if v := rawColumn(t, s, c.table, c.col, c.id); !strings.HasPrefix(v, "seal:") {
				t.Errorf("pass %d: %s.%s is still %.20q after the upgrade", pass, c.table, c.col, v)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
