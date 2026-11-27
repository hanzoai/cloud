package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// A device grant's code is a KMS value: the row holds its path and no byte of it, a read
// asks KMS, taking the grant forgets the code, a delete and the sweep of expired grants
// forget it, and a path carried to another org is refused.
func TestAGrantCodeLivesInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := kmstest.New()
		s, err := openStore(t.TempDir(), testMaster, k)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		exp := time.Now().Add(time.Minute).Unix()
		for _, g := range []Grant{
			{ID: "ga", Org: "acme", User: "alice", Provider: "github", Label: "x", Code: "gho_code-of-ga", UserCode: "U", Interval: 5, CreatedAt: 1, ExpiresAt: exp},
			{ID: "gb", Org: "acme", User: "alice", Provider: "github", Label: "x", Code: "gho_code-of-gb", UserCode: "U", Interval: 5, CreatedAt: 1, ExpiresAt: exp},
			{ID: "gc", Org: "acme", User: "alice", Provider: "github", Label: "x", Code: "gho_code-of-gc", UserCode: "U", Interval: 5, CreatedAt: 1, ExpiresAt: 2},
			{ID: "gg", Org: "globex", User: "bob", Provider: "github", Label: "x", Code: "gho_code-of-gg", UserCode: "U", Interval: 5, CreatedAt: 1, ExpiresAt: exp},
		} {
			if err := s.PutGrant(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
		raw := func(id string) string {
			var v string
			if err := s.db.QueryRow(`SELECT code FROM grants WHERE id = $1`, id).Scan(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		if v := raw("ga"); v != "kms:orgs/acme/provider/ga/code@default" || strings.Contains(v, "gho_") {
			t.Fatalf("the code column holds %q, want its KMS path", v)
		}
		if held := k.Held(); len(held) != 4 || held["orgs/acme/provider/ga/code@default"] != "gho_code-of-ga" {
			t.Fatalf("KMS holds %v, want each code at its path", held)
		}
		if got, ok, err := s.GetGrant(ctx, "ga", "acme", "alice"); err != nil || !ok || got.Code != "gho_code-of-ga" {
			t.Fatalf("GetGrant = %+v %v %v", got, ok, err)
		}
		if len(k.Held()) != 4 {
			t.Fatal("reading a grant forgot its code")
		}

		// A path carried to another org is refused by KMS, and a dangling path is not an empty code.
		if _, err := s.db.Exec(`UPDATE grants SET code = $1 WHERE id = 'gb'`, raw("gg")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.GetGrant(ctx, "gb", "acme", "alice"); err == nil {
			t.Fatal("acme's grant read globex's code")
		}
		if _, err := s.db.Exec(`UPDATE grants SET code = 'kms:orgs/acme/provider/gb/nothing@default' WHERE id = 'gb'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.GetGrant(ctx, "gb", "acme", "alice"); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}

		// Taking a grant reads its code once and forgets it.
		if got, ok, err := s.TakeGrant(ctx, "ga", "acme", "alice"); err != nil || !ok || got.Code != "gho_code-of-ga" {
			t.Fatalf("TakeGrant = %+v %v %v", got, ok, err)
		}
		if _, ok := k.Held()["orgs/acme/provider/ga/code@default"]; ok {
			t.Fatal("a taken grant left its code in KMS")
		}
		// A delete and the sweep of the expired forget theirs.
		if err := s.DeleteGrant(ctx, "gg"); err != nil {
			t.Fatal(err)
		}
		if n, err := s.GCGrants(ctx, 10); err != nil || n != 1 {
			t.Fatalf("GCGrants = %d, %v, want the one expired grant", n, err)
		}
		if refs := k.Refs(); len(refs) != 1 || refs[0] != "orgs/acme/provider/gb/code@default" {
			t.Fatalf("KMS still holds %v, want only the live grant's code", refs)
		}
	})
}

// With no KMS a code is neither kept nor read, and the grant is not written.
func TestNoKMSKeepsNoCode(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		err = s.PutGrant(context.Background(), Grant{ID: "ga", Org: "acme", User: "alice", Provider: "github", Label: "x", Code: "gho_c", UserCode: "U", Interval: 5, CreatedAt: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()})
		if !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("PutGrant with no KMS = %v, want ErrNoKMS", err)
		}
		if n, err := scalarCount(s, `SELECT COUNT(*) FROM grants`); err != nil || n != 0 {
			t.Fatalf("a refused grant left %d rows (%v)", n, err)
		}
	})
}

// A file written before KMS holds a grant's code plain, concealed under org.Cipher.Conceal,
// or sealed as a field. The open writes each to KMS and replaces the column with its path,
// once. Built by hand on both engines: the rows are the old shapes in the current table.
func TestALegacyGrantCodeMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := kmstest.New()
		s, err := openStore(t.TempDir(), testMaster, k)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		ns, err := principal.OrgNamespace("acme", "")
		if err != nil {
			t.Fatal(err)
		}
		blob, err := c.Conceal(ns, legacyCodeSubsystem, []byte("gho_concealed_77"))
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.Fields(namespace.System(), subsystem)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Row("grants", "acme", "g_field")
		sealed := r.Seal("code", "gho_field_99")
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		exp := time.Now().Add(time.Minute).Unix()
		for id, code := range map[string]string{"g_plain": "gho_plain_55", "g_conc": legacyCodePrefix + base64.RawURLEncoding.EncodeToString(blob), "g_field": sealed} {
			if _, err := s.db.Exec(`INSERT INTO grants (id, org, "user", provider, label, code, user_code, interval, last_poll_at, created_at, expires_at)
				VALUES ($1,'acme','alice','github','x',$2,'U',5,0,1,$3)`, id, code, exp); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatalf("move: %v", err)
		}
		cols := ""
		for id, want := range map[string]string{"g_plain": "gho_plain_55", "g_conc": "gho_concealed_77", "g_field": "gho_field_99"} {
			var raw string
			if err := s.db.QueryRow(`SELECT code FROM grants WHERE id = $1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw != "kms:orgs/acme/provider/"+id+"/code@default" || strings.Contains(raw, "gho_") {
				t.Fatalf("%s: the code column holds %q, want its KMS path", id, raw)
			}
			cols += raw
			if got, ok, err := s.GetGrant(ctx, id, "acme", "alice"); err != nil || !ok || got.Code != want {
				t.Fatalf("%s = %q %v %v, want %q", id, got.Code, ok, err, want)
			}
		}
		before, reads := k.Held(), k.Reads()
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
		if after := k.Held(); len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second open touched KMS: %d values, then %d; reads %d then %d", len(before), len(after), reads, k.Reads())
		}
		again := ""
		for _, id := range []string{"g_plain", "g_conc", "g_field"} {
			var raw string
			_ = s.db.QueryRow(`SELECT code FROM grants WHERE id = $1`, id).Scan(&raw)
			again += raw
		}
		if len(cols) != len(again) {
			t.Fatal("a second open rewrote a column")
		}
	})
}

// A connect nonce is looked up by its value and never read back, so the table holds its
// blind index: a reader of it holds nothing that completes a flow, or a Telegram link.
func TestANonceIsStoredAsItsBlind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		const nonce = "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
		if err := s.PutNonce(ctx, nonce, "acme", "slack", "https://app.acme.test/back"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutNonce(ctx, "tg-code-4471", "acme", "telegram", ""); err != nil {
			t.Fatal(err)
		}
		rows, err := s.db.Query(`SELECT blind FROM oauth_nonces`)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			n++
			if len(k) != 64 || strings.Contains(k, "9f8e7d") || strings.Contains(k, "4471") {
				t.Errorf("the table is keyed by %q: not a blind index", k)
			}
		}
		_ = rows.Close()
		if n != 2 {
			t.Fatalf("%d rows", n)
		}
		// A stored key is not a nonce.
		if _, ok, err := s.ConsumeNonce(ctx, s.nonce(nonce), "acme", "slack"); err != nil || ok {
			t.Fatalf("the stored key consumed as a nonce: %v %v", ok, err)
		}
		if back, ok, err := s.ConsumeNonce(ctx, nonce, "acme", "slack"); err != nil || !ok || back != "https://app.acme.test/back" {
			t.Fatalf("consume: %q %v %v", back, ok, err)
		}
		if org, ok, err := s.ClaimNonce(ctx, "tg-code-4471", "telegram"); err != nil || !ok || org != "acme" {
			t.Fatalf("claim: %q %v %v", org, ok, err)
		}
		if _, ok, _ := s.ClaimNonce(ctx, "tg-code-4471", "telegram"); ok {
			t.Fatal("a claimed code claimed twice")
		}
	})
}

// A file from before: grants hold the code plain or concealed under org.Cipher.Conceal,
// and nonces are keyed by the nonce itself. All of it is read back as it was; the codes move
// to KMS and the nonces are blinded in place when the store opens. SQLite only, by nature: the old file
// is one an older binary made.
func TestALegacyFileIsMovedAndBlindedInPlace(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	pre, err := locus.Open(namespace.System(), subsystem, dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tenure.NewCipher(testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.Conceal(ns, legacyCodeSubsystem, []byte("gho_concealed_77"))
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Minute).Unix()
	if _, err := pre.Exec(`
CREATE TABLE grants (id TEXT PRIMARY KEY, org TEXT NOT NULL, "user" TEXT NOT NULL, provider TEXT NOT NULL, label TEXT NOT NULL,
  code TEXT NOT NULL, user_code TEXT NOT NULL, interval INTEGER NOT NULL, last_poll_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE oauth_nonces (nonce TEXT PRIMARY KEY, org TEXT NOT NULL, provider TEXT NOT NULL, created_at INTEGER NOT NULL, back TEXT NOT NULL DEFAULT '');
INSERT INTO oauth_nonces VALUES ('legacy-nonce-1','acme','slack',1700000000,'https://app.acme.test/back');`); err != nil {
		t.Fatal(err)
	}
	for id, code := range map[string]string{"g_plain": "gho_plain_55", "g_conc": legacyCodePrefix + base64.RawURLEncoding.EncodeToString(blob)} {
		if _, err := pre.Exec(`INSERT INTO grants VALUES ($1,'acme','alice','github','x',$2,'U',5,0,1,$3)`, id, code, exp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pre.Close(); err != nil {
		t.Fatal(err)
	}

	k := kmstest.New()
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		s, err := openStore(dir, testMaster, k)
		if err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		ctx := context.Background()
		for id, want := range map[string]string{"g_plain": "gho_plain_55", "g_conc": "gho_concealed_77"} {
			var raw string
			if err := s.db.QueryRow(`SELECT code FROM grants WHERE id = $1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw != "kms:orgs/acme/provider/"+id+"/code@default" {
				t.Errorf("pass %d: %s holds %.60q, want its KMS path", pass, id, raw)
			}
			if got, ok, err := s.GetGrant(ctx, id, "acme", "alice"); err != nil || !ok || got.Code != want {
				t.Fatalf("pass %d: %s = %q %v %v, want %q", pass, id, got.Code, ok, err, want)
			}
		}
		if pass == 1 {
			if back, ok, err := s.ConsumeNonce(ctx, "legacy-nonce-1", "acme", "slack"); err != nil || !ok || back != "https://app.acme.test/back" {
				t.Fatalf("an in-flight nonce from before: %q %v %v", back, ok, err)
			}
		}
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'oauth_nonces_plain'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("pass %d: the plaintext nonce table is still there: %d %v", pass, n, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
