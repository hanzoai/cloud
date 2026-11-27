package social

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// rawToken reads the token column as the database holds it, past the store.
func rawToken(t *testing.T, s *Store, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT token FROM social_accounts WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read token: %v", err)
	}
	return v
}

// The provider access token is a KMS value: the row holds its path and no byte of it, a
// read asks KMS, and a delete asks KMS to forget it; an account with no token keeps the
// column empty and puts nothing in KMS.
func TestTheAccessTokenLivesInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := testStore(t)
		const secret = "EAAB-live-access-token-0f3a9c"
		a, err := s.CreateAccount(ctx, socialAccount{ID: "acct_1", Org: "acme", Provider: "x", Handle: "@acme", Status: "connected", Token: secret, CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateAccount(ctx, socialAccount{ID: "acct_2", Org: "acme", Provider: "x", Handle: "@bare", Status: "connected", CreatedAt: 2, UpdatedAt: 2}); err != nil {
			t.Fatal(err)
		}

		const ref = "orgs/acme/social/acct_1/token@default"
		if v := rawToken(t, s, "acct_1"); v != "kms:"+ref || strings.Contains(v, secret[:6]) {
			t.Fatalf("token column holds %q, want its KMS path", v)
		}
		if v := rawToken(t, s, "acct_2"); v != "" {
			t.Fatalf("an absent token became %q", v)
		}
		if held := testKMS.Held(); len(held) != 1 || held[ref] != secret {
			t.Fatalf("KMS holds %v, want the token at its path and nothing else", held)
		}
		got, err := s.GetAccount(ctx, "acme", "acct_1")
		if err != nil || got.Token != "" || got.tokenRef != "kms:"+ref {
			t.Fatalf("GetAccount = %+v, %v; a read names the token and does not carry it", got, err)
		}
		if tok, err := s.Token(ctx, got); err != nil || tok != secret {
			t.Fatalf("Token = %q, %v, want %q", tok, err, secret)
		}
		// An update that does not name the token leaves it where it is.
		a.Handle = "@acme2"
		a.UpdatedAt = 5
		if _, err := s.UpdateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetAccount(ctx, "acme", "acct_1"); err != nil || got.Handle != "@acme2" {
			t.Fatalf("after an update: %+v, %v", got, err)
		} else if tok, _ := s.Token(ctx, got); tok != secret {
			t.Fatalf("after an update the token is %q", tok)
		}

		if ok, err := s.DeleteAccount(ctx, "acme", "acct_1"); err != nil || !ok {
			t.Fatalf("DeleteAccount = %v, %v", ok, err)
		}
		if refs := testKMS.Refs(); len(refs) != 0 {
			t.Fatalf("a deleted account left %v in KMS", refs)
		}
	})
}

// A reference names its own account's token: a column pointed at another org's path is
// refused, and one that names nothing KMS holds is a dangling reference.
func TestTheTokenReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := testStore(t)
		for _, a := range []socialAccount{
			{ID: "acct_a", Org: "acme", Provider: "x", Status: "connected", Token: "token-of-a", CreatedAt: 1, UpdatedAt: 1},
			{ID: "acct_g", Org: "globex", Provider: "x", Status: "connected", Token: "token-of-g", CreatedAt: 1, UpdatedAt: 1},
		} {
			if _, err := s.CreateAccount(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE social_accounts SET token = $1 WHERE id = 'acct_a'`, rawToken(t, s, "acct_g")); err != nil {
			t.Fatal(err)
		}
		a, err := s.GetAccount(ctx, "acme", "acct_a")
		if err != nil {
			t.Fatal(err)
		}
		if tok, err := s.Token(ctx, a); err == nil || tok != "" {
			t.Fatalf("acme's account read globex's token: %q, %v", tok, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE social_accounts SET token = 'kms:orgs/acme/social/acct_a/nothing@default' WHERE id = 'acct_a'`); err != nil {
			t.Fatal(err)
		}
		a, _ = s.GetAccount(ctx, "acme", "acct_a")
		if _, err := s.Token(ctx, a); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}
	})
}

// With no KMS a token is neither kept nor read, and the account is not written.
func TestNoKMSKeepsNoToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		_, err = s.CreateAccount(context.Background(), socialAccount{ID: "acct_1", Org: "acme", Provider: "x", Status: "connected", Token: "tok", CreatedAt: 1, UpdatedAt: 1})
		if !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("CreateAccount with no KMS = %v, want ErrNoKMS", err)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM social_accounts`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a refused create left %d rows (%v)", n, err)
		}
	})
}

// A file written before KMS holds the token in plaintext or sealed as a field. The open
// writes each to KMS and replaces the column with its path, once. Built by hand on both
// engines: the rows are the old shapes in the current table.
func TestALegacyTokenMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		dir, k := t.TempDir(), kmstest.New()
		s, err := openStore(dir, testMaster, k)
		if err != nil {
			t.Fatal(err)
		}
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.Fields(namespace.System(), subsystem)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Row("social_accounts", "acme", "acct_field")
		sealed := r.Seal("token", "sealed-token-qrs")
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		for _, q := range []struct{ id, token string }{{"acct_old", "legacy-token-xyz"}, {"acct_field", sealed}, {"acct_bare", ""}} {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO social_accounts (id, org, provider, handle, status, token, created_at, updated_at) VALUES ($1, 'acme', 'x', '@h', 'connected', $2, 1, 1)`, q.id, q.token); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{"acct_old": "legacy-token-xyz", "acct_field": "sealed-token-qrs"} {
			v := rawToken(t, s, id)
			if v != "kms:orgs/acme/social/"+id+"/token@default" || strings.Contains(v, "legacy") || strings.Contains(v, "qrs") {
				t.Fatalf("%s: the column holds %q, want its KMS path", id, v)
			}
			a, err := s.GetAccount(ctx, "acme", id)
			if tok, terr := s.Token(ctx, a); err != nil || terr != nil || tok != want {
				t.Fatalf("%s after the move: token %q (%v, %v), want %q", id, tok, err, terr, want)
			}
		}
		if v := rawToken(t, s, "acct_bare"); v != "" {
			t.Errorf("an empty token became %q", v)
		}
		before, reads := k.Held(), k.Reads()
		cols := []string{rawToken(t, s, "acct_old"), rawToken(t, s, "acct_field")}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = openStore(dir, testMaster, k)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		after := k.Held()
		if len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second open touched KMS: %d values, then %d; reads %d then %d", len(before), len(after), reads, k.Reads())
		}
		if rawToken(t, s, "acct_old") != cols[0] || rawToken(t, s, "acct_field") != cols[1] {
			t.Fatal("a second open rewrote a column")
		}
	})
}

// SQLite file remnant: the legacy path through locus on a real file still opens.
func TestAFileOpensWithoutAMasterWhenNothingIsOld(t *testing.T) {
	t.Setenv(locus.Env, "")
	s, err := openStore(t.TempDir(), nil, kmstest.New())
	if err != nil {
		t.Fatalf("a store with nothing to open opens without a master: %v", err)
	}
	_ = s.Close()
}
