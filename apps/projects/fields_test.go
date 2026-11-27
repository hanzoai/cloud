package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/tenure"
	tenant "github.com/hanzoai/namespace"
)

// rawToken reads a host's token column as the database holds it, past the store.
func rawToken(t *testing.T, s *Store, host string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT token FROM site_hosts WHERE host = $1`, host).Scan(&v); err != nil {
		t.Fatalf("read token: %v", err)
	}
	return v
}

// freshStore is a store over its own KMS, which a test reads what the store keeps from.
func freshStore(t *testing.T) (*Store, *kmstest.Mem) {
	t.Helper()
	k := kmstest.New()
	s, err := openStore(t.TempDir(), testMaster, k)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, k
}

// The DNS challenge of a pending custom host is a KMS value: the row holds its path and no
// byte of it; the store gives it back whole, keeps the one it first issued, and forgets it
// when the host is verified or released, and a verified host has none.
func TestTheChallengeLivesInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, k := freshStore(t)
		const tok = "hanzo-verify=8c1f0b6a44d2e975"
		const ref = "orgs/acme/projects/shop.acme.example/token@default"
		if err := s.ClaimHost(ctx, "shop.acme.example", "acme", "acme", tok, 100); err != nil {
			t.Fatal(err)
		}
		if v := rawToken(t, s, "shop.acme.example"); v != "kms:"+ref {
			t.Fatalf("token column holds %q, want its KMS path", v)
		}
		if held := k.Held(); len(held) != 1 || held[ref] != tok {
			t.Fatalf("KMS holds %v, want the challenge at its path and nothing else", held)
		}
		if got, err := s.HostClaimFor(ctx, "shop.acme.example", "acme", "acme"); err != nil || got.Token != tok || got.Status != HostPending {
			t.Fatalf("HostClaimFor = %+v, %v", got, err)
		}
		// A repeat claim keeps the challenge the customer already published.
		if err := s.ClaimHost(ctx, "shop.acme.example", "acme", "acme", "hanzo-verify=another", 110); err != nil {
			t.Fatal(err)
		}
		if list, err := s.ListHostClaims(ctx, "acme", "acme"); err != nil || len(list) != 1 || list[0].Token != tok {
			t.Fatalf("ListHostClaims = %+v, %v", list, err)
		}
		if k.Held()[ref] != tok {
			t.Fatalf("a repeat claim replaced the challenge in KMS with %q", k.Held()[ref])
		}
		// Verifying clears it, and a verified host has no token to read.
		if err := s.VerifyHost(ctx, "shop.acme.example", "acme", "acme", 120); err != nil {
			t.Fatal(err)
		}
		if v := rawToken(t, s, "shop.acme.example"); v != "" {
			t.Fatalf("a verified host keeps a token: %.30q", v)
		}
		if refs := k.Refs(); len(refs) != 0 {
			t.Fatalf("a verified host left %v in KMS", refs)
		}
		if got, err := s.HostClaimFor(ctx, "shop.acme.example", "acme", "acme"); err != nil || got.Token != "" || got.Status != HostVerified {
			t.Fatalf("after verifying: %+v, %v", got, err)
		}

		// A released claim forgets its challenge.
		if err := s.ClaimHost(ctx, "pending.acme.example", "acme", "acme", "tok-p", 130); err != nil {
			t.Fatal(err)
		}
		if len(k.Refs()) != 1 {
			t.Fatalf("a pending claim holds %v in KMS", k.Refs())
		}
		if err := s.UnbindHost(ctx, "pending.acme.example", "acme", "acme"); err != nil {
			t.Fatal(err)
		}
		if refs := k.Refs(); len(refs) != 0 {
			t.Fatalf("a released claim left %v in KMS", refs)
		}
	})
}

// A reference names its own host's challenge: a column pointed at another org's path is
// refused by KMS, and one that names nothing KMS holds is a dangling reference.
func TestAChallengeReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, _ := freshStore(t)
		if err := s.ClaimHost(ctx, "a.acme.example", "acme", "acme", "tok-a", 1); err != nil {
			t.Fatal(err)
		}
		if err := s.ClaimHost(ctx, "g.globex.example", "globex", "globex", "tok-g", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE site_hosts SET token = $1 WHERE host = 'a.acme.example'`, rawToken(t, s, "g.globex.example")); err != nil {
			t.Fatal(err)
		}
		if got, err := s.HostClaimFor(ctx, "a.acme.example", "acme", "acme"); err == nil || got.Token != "" {
			t.Fatalf("acme's host read globex's challenge: %+v, %v", got, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE site_hosts SET token = 'kms:orgs/acme/projects/a.acme.example/nothing@default' WHERE host = 'a.acme.example'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.HostClaimFor(ctx, "a.acme.example", "acme", "acme"); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}
	})
}

// With no KMS a challenge is neither kept nor read, and the claim is not written.
func TestNoKMSKeepsNoChallenge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.ClaimHost(context.Background(), "shop.acme.example", "acme", "acme", "tok", 1); !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("ClaimHost with no KMS = %v, want ErrNoKMS", err)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM site_hosts WHERE host = 'shop.acme.example'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a refused claim left %d rows (%v)", n, err)
		}
	})
}

// A file written before KMS holds the challenge in plaintext or sealed as a field. The open
// writes each to KMS and replaces the column with its path, once. Built by hand on both
// engines: the rows are the old shapes in the current table.
func TestALegacyChallengeMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, k := freshStore(t)
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.Fields(tenant.System(), subsystem)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Row("site_hosts", "acme", "sealed.acme.example")
		sealed := r.Seal("token", "sealed-challenge")
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		for _, h := range []struct{ host, token string }{{"old.acme.example", "legacy-challenge"}, {"sealed.acme.example", sealed}} {
			if _, err := s.db.ExecContext(ctx,
				`INSERT INTO site_hosts (host, org, slug, status, token, verified_at, created_at, updated_at) VALUES ($1, 'acme', 'acme', 'pending', $2, 0, 1, 1)`, h.host, h.token); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatalf("move: %v", err)
		}
		for host, want := range map[string]string{"old.acme.example": "legacy-challenge", "sealed.acme.example": "sealed-challenge"} {
			if v := rawToken(t, s, host); v != "kms:orgs/acme/projects/"+host+"/token@default" || strings.Contains(v, "challenge") {
				t.Fatalf("%s: the token column holds %q, want its KMS path", host, v)
			}
			if got, err := s.HostClaimFor(ctx, host, "acme", "acme"); err != nil || got.Token != want {
				t.Fatalf("%s after the move: %+v, %v, want %q", host, got, err, want)
			}
		}
		before, reads := k.Held(), k.Reads()
		cols := rawToken(t, s, "old.acme.example") + rawToken(t, s, "sealed.acme.example")
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
		if after := k.Held(); len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second open touched KMS: %d values, then %d; reads %d then %d", len(before), len(after), reads, k.Reads())
		}
		if cols != rawToken(t, s, "old.acme.example")+rawToken(t, s, "sealed.acme.example") {
			t.Fatal("a second open rewrote a column")
		}
	})
}
