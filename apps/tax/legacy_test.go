package tax

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// A file written before the columns were fields holds its documents in plaintext and its
// TINs concealed under the org's key (org.Cipher.Conceal, bound to the namespace and "tax.tin"
// and to no row). It is built by hand (the rows are what the code that wrote them left), so
// this stays on SQLite: hanzo-sql has no such file. Opening it seals every column in place
// with the triggers that keep a final record final lifted for that, and put back.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	dir := t.TempDir()
	ns := principal.MustOrgNamespace("acme", "")
	c, err := tenure.NewCipher(master)
	if err != nil {
		t.Fatal(err)
	}
	conceal := func(plain string) []byte {
		t.Helper()
		b, err := c.Conceal(ns, legacyTINSubsystem, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	openAt := func() *store {
		t.Helper()
		db, err := cloud.OrgDB(dir, ns, subsystem)
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(master)(ns, db)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return st
	}

	st := openAt()
	ctx := context.Background()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO profile (id, version, data, tin, ftin, updated_at) VALUES (1, 3, '{"name":"Acme Legacy"}', $1, $2, 1)`, []any{conceal("ein:123456789"), conceal("ftin:GB 123 4567")}},
		{`INSERT INTO payees (payee, share, category, data, created_at, updated_at) VALUES ('globex', 'w9_1', 'services', '{"note":"legacy payee"}', 1, 1)`, nil},
		{`INSERT INTO shares (id, payer, status, data, created_at, updated_at) VALUES ('w9_1', 'umbrella', 'granted', '{"note":"legacy share"}', 1, 1)`, nil},
		{`INSERT INTO forms (id, payee, year, kind, status, content, payer_tin, payee_tin, created_at, updated_at) VALUES ('f_old', 'globex', 2025, 'NEC', 'furnished', '{"legacy":"form"}', $1, $2, 1, 1)`, []any{conceal("ein:123456789"), conceal("ssn:123456789")}},
		{`INSERT INTO received (id, payer, year, kind, content, payer_tin, furnished_at) VALUES ('r_old', 'umbrella', 2025, 'NEC', '{"legacy":"statement"}', $1, 1)`, []any{conceal("ein:987654321")}},
		{`INSERT INTO filings (id, year, kind, status, data, created_at, updated_at) VALUES ('fil_old', 2025, 'NEC', 'exported', '{"legacy":"filing"}', 1, 1)`, nil},
	} {
		if _, err := st.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		st = openAt()
		p, err := st.profile(ctx)
		if err != nil || p.data != `{"name":"Acme Legacy"}` || p.tin.full() != "12-3456789" || p.ftin.full() != "GB 123 4567" {
			t.Fatalf("pass %d: the legacy profile = %+v, %v", pass, p, err)
		}
		if r, err := st.payee(ctx, "globex"); err != nil || r.data != `{"note":"legacy payee"}` {
			t.Fatalf("pass %d: payee = %+v, %v", pass, r, err)
		}
		if r, err := st.share(ctx, "w9_1"); err != nil || r.data != `{"note":"legacy share"}` {
			t.Fatalf("pass %d: share = %+v, %v", pass, r, err)
		}
		f, err := st.form(ctx, "f_old")
		if err != nil || f.content != `{"legacy":"form"}` || f.payerTIN.full() != "12-3456789" || f.payeeTIN.full() != "123-45-6789" {
			t.Fatalf("pass %d: form = %+v, %v", pass, f, err)
		}
		if r, err := st.statement(ctx, "r_old"); err != nil || r.content != `{"legacy":"statement"}` || r.payerTIN.full() != "98-7654321" {
			t.Fatalf("pass %d: statement = %+v, %v", pass, r, err)
		}
		if r, err := st.filing(ctx, "fil_old"); err != nil || r.data != `{"legacy":"filing"}` {
			t.Fatalf("pass %d: filing = %+v, %v", pass, r, err)
		}
		for _, col := range []struct{ table, col, id string }{
			{"profile", "data", "1"}, {"profile", "tin", "1"}, {"profile", "ftin", "1"}, {"payees", "data", "globex"}, {"shares", "data", "w9_1"},
			{"forms", "content", "f_old"}, {"forms", "payer_tin", "f_old"}, {"forms", "payee_tin", "f_old"},
			{"received", "content", "r_old"}, {"received", "payer_tin", "r_old"}, {"filings", "data", "fil_old"},
		} {
			key := "id"
			if col.table == "payees" {
				key = "payee"
			}
			var raw []byte
			if err := st.db.QueryRow(`SELECT `+col.col+` FROM `+col.table+` WHERE `+key+` = $1`, col.id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(raw), "seal:") {
				t.Errorf("pass %d: %s.%s is still %.20q after the upgrade", pass, col.table, col.col, raw)
			}
		}
		// The triggers that keep a furnished form and a received statement final are back.
		f.content = `{"legacy":"changed"}`
		if err := st.updateForm(ctx, *f, Furnished); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("pass %d: the store rewrote a furnished form: %v", pass, err)
		}
		if _, err := st.db.Exec(`UPDATE received SET content = 'x' WHERE id = 'r_old'`); err == nil {
			t.Fatalf("pass %d: the store rewrote a furnished statement", pass)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
