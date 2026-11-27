package compliance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/idv"
	"github.com/hanzoai/cloud/internal/sqltest"
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

func fixture(t *testing.T, s *Store) (Subject, Check, Accreditation) {
	t.Helper()
	ctx := context.Background()
	sub := Subject{ID: "sub_1", Org: "acme", Kind: idv.KindIndividual, Ref: "emp-4471", Email: "ada@acme.example", Name: "Ada Lovelace", CreatedAt: 10, UpdatedAt: 10}
	chk := Check{ID: "chk_1", Org: "acme", SubjectID: "sub_1", Kind: idv.KindIndividual, Provider: "persona", ProviderRef: "inq_abc123",
		VerifyURL: "https://verify.example/flow?session=s3cr3t-session-token", Status: idv.StatusPending, CreatedAt: 11, UpdatedAt: 11}
	acc := Accreditation{ID: "acc_1", Org: "acme", SubjectID: "sub_1", Method: MethodSelfAttested, Basis: BasisIncome,
		Status: AccAsserted, Note: "spoke with Ada on the phone about her filing", CreatedAt: 12, UpdatedAt: 12}
	for name, err := range map[string]error{
		"subject": s.CreateSubject(ctx, sub), "check": s.CreateCheck(ctx, chk), "accreditation": s.CreateAccreditation(ctx, acc),
	} {
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	return sub, chk, acc
}

// The fields the SQLCipher file protected are ciphertext in the database, on both
// engines, and the store gives them back whole.
func TestPersonalDataIsSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		sub, chk, acc := fixture(t, s)

		for _, c := range []struct{ table, col, id, plain string }{
			{"compliance_subject", "email", sub.ID, sub.Email},
			{"compliance_subject", "name", sub.ID, sub.Name},
			{"compliance_subject", "ref", sub.ID, sub.Ref},
			{"compliance_check", "verify_url", chk.ID, chk.VerifyURL},
			{"compliance_accreditation", "note", acc.ID, acc.Note},
		} {
			v := rawColumn(t, s, c.table, c.col, c.id)
			if !strings.HasPrefix(v, "seal:") || strings.Contains(v, c.plain) || strings.Contains(v, c.plain[:6]) {
				t.Errorf("%s.%s is stored as %.30q: not sealed", c.table, c.col, v)
			}
		}
		// What a predicate or a join reads stays readable.
		if rawColumn(t, s, "compliance_check", "provider_ref", chk.ID) != chk.ProviderRef {
			t.Error("provider_ref, which the webhook looks a check up by, is not plain")
		}

		ctx := context.Background()
		if got, err := s.GetSubject(ctx, "acme", sub.ID); err != nil || got != sub {
			t.Fatalf("GetSubject = %+v, %v; want %+v", got, err, sub)
		}
		if list, err := s.ListSubjects(ctx, "acme", 10); err != nil || len(list) != 1 || list[0] != sub {
			t.Fatalf("ListSubjects = %+v, %v", list, err)
		}
		if got, err := s.GetCheck(ctx, "acme", chk.ID); err != nil || got != chk {
			t.Fatalf("GetCheck = %+v, %v; want %+v", got, err, chk)
		}
		if got, err := s.GetCheckByProviderRef(ctx, chk.ProviderRef); err != nil || got != chk {
			t.Fatalf("GetCheckByProviderRef = %+v, %v", got, err)
		}
		if list, err := s.ListChecks(ctx, "acme", 10); err != nil || len(list) != 1 || list[0] != chk {
			t.Fatalf("ListChecks = %+v, %v", list, err)
		}
		if got, err := s.GetAccreditation(ctx, "acme", acc.ID); err != nil || got != acc {
			t.Fatalf("GetAccreditation = %+v, %v", got, err)
		}
		if list, err := s.ListAccreditation(ctx, "acme", 10); err != nil || len(list) != 1 || list[0] != acc {
			t.Fatalf("ListAccreditation = %+v, %v", list, err)
		}
		// A status move does not touch what is sealed.
		if err := s.UpdateCheckStatus(ctx, "acme", chk.ID, idv.StatusPending, "persona", 99, 0); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetCheck(ctx, "acme", chk.ID); err != nil || got.VerifyURL != chk.VerifyURL {
			t.Fatalf("after a status move: %+v, %v", got, err)
		}
	})
}

// A party with no email or ref keeps them empty (the schema's "has an email" is a fact),
// and a ciphertext lifted into another row or another tenant is refused.
func TestASealedColumnIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		a := Subject{ID: "sub_a", Org: "acme", Kind: idv.KindIndividual, Email: "a@acme.example", Name: "A", CreatedAt: 1, UpdatedAt: 1}
		b := Subject{ID: "sub_b", Org: "acme", Kind: idv.KindIndividual, Name: "B", CreatedAt: 2, UpdatedAt: 2}
		for _, sub := range []Subject{a, b} {
			if err := s.CreateSubject(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := s.GetSubject(ctx, "acme", "sub_b"); err != nil || got.Email != "" || got.Ref != "" {
			t.Fatalf("an absent email: %+v, %v", got, err)
		}

		// A role with UPDATE copies A's sealed email over B's.
		if _, err := s.db.ExecContext(ctx, `UPDATE compliance_subject SET email = $1 WHERE id = $2`,
			rawColumn(t, s, "compliance_subject", "email", "sub_a"), "sub_b"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetSubject(ctx, "acme", "sub_b"); !errors.Is(err, tenure.ErrFieldAuth) || got.Email != "" {
			t.Fatalf("B read A's email: %+v, %v", got, err)
		}
		// A plaintext written around the store is refused, not read as the store's own.
		if _, err := s.db.ExecContext(ctx, `UPDATE compliance_subject SET email = $1 WHERE id = $2`, "attacker@evil.example", "sub_b"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSubject(ctx, "acme", "sub_b"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
		// A subject moved to another tenant with its ciphertext.
		if _, err := s.db.ExecContext(ctx, `UPDATE compliance_subject SET org = $1 WHERE id = $2`, "evil", "sub_a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSubject(ctx, "evil", "sub_a"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a subject moved to another tenant opened: %v", err)
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

// A SQLite file written before the columns were sealed holds plaintext in them. It is
// built by hand (the rows are plaintext because the code that wrote them was), so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO compliance_subject (id, org, kind, ref, email, name, created_at, updated_at) VALUES ('sub_old', 'acme', 'individual', 'emp-1', 'old@acme.example', 'Old Name', 1, 1)`,
		`INSERT INTO compliance_subject (id, org, kind, created_at, updated_at) VALUES ('sub_bare', 'acme', 'business', 2, 2)`,
		`INSERT INTO compliance_check (id, org, subject_id, kind, provider, provider_ref, verify_url, status, created_at, updated_at) VALUES ('chk_old', 'acme', 'sub_old', 'individual', 'persona', 'inq_1', 'https://verify.example/old?t=tok', 'pending', 1, 1)`,
		`INSERT INTO compliance_accreditation (id, org, subject_id, method, basis, status, note, created_at, updated_at) VALUES ('acc_old', 'acme', 'sub_old', 'self_attested', 'income', 'asserted', 'old note', 1, 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = openStore(dir, testMaster)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.GetSubject(ctx, "acme", "sub_old")
	if err != nil || got.Email != "old@acme.example" || got.Name != "Old Name" || got.Ref != "emp-1" {
		t.Fatalf("a legacy subject: %+v, %v", got, err)
	}
	if bare, err := s.GetSubject(ctx, "acme", "sub_bare"); err != nil || bare.Email != "" {
		t.Fatalf("a legacy subject with nothing to seal: %+v, %v", bare, err)
	}
	if chk, err := s.GetCheck(ctx, "acme", "chk_old"); err != nil || chk.VerifyURL != "https://verify.example/old?t=tok" {
		t.Fatalf("a legacy check: %+v, %v", chk, err)
	}
	if acc, err := s.GetAccreditation(ctx, "acme", "acc_old"); err != nil || acc.Note != "old note" {
		t.Fatalf("a legacy accreditation: %+v, %v", acc, err)
	}
	// And what is on disk now is sealed, so a copy to hanzo-sql carries no plaintext.
	for _, c := range []struct{ table, col, id string }{
		{"compliance_subject", "email", "sub_old"}, {"compliance_subject", "name", "sub_old"}, {"compliance_subject", "ref", "sub_old"},
		{"compliance_check", "verify_url", "chk_old"}, {"compliance_accreditation", "note", "acc_old"},
	} {
		if v := rawColumn(t, s, c.table, c.col, c.id); !strings.HasPrefix(v, "seal:") {
			t.Errorf("%s.%s is still %.20q after the upgrade", c.table, c.col, v)
		}
	}
	if v := rawColumn(t, s, "compliance_subject", "email", "sub_bare"); v != "" {
		t.Errorf("an empty email became %q", v)
	}
}
