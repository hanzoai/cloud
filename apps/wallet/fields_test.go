package wallet

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals the names an owner chose under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func rawName(t *testing.T, st *store, table, id string) string {
	t.Helper()
	var v string
	if err := st.db.QueryRow(`SELECT name FROM `+table+` WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The names an owner gave its accounts and wallets are ciphertext in the database on both
// engines, and every read gives them back.
func TestNamesAreSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openStore(t.TempDir(), testMaster)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()
		acct := &WalletAccount{ID: "acct_1", Org: "acme", Name: "Ada's savings", CreatedAt: 1}
		if err := st.createAccount(ctx, acct); err != nil {
			t.Fatal(err)
		}
		w := &Wallet{ID: "w_1", Org: "acme", AccountID: acct.ID, Name: "Ada's agent float", Custody: KindKMS, Tier: "hot", Address: "0xabc", KeyRef: "kms://acme/w_1", CreatedAt: 2}
		if err := st.createWallet(ctx, w); err != nil {
			t.Fatal(err)
		}
		for table, id := range map[string]string{"accounts": "acct_1", "wallets": "w_1"} {
			if v := rawName(t, st, table, id); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "Ada") {
				t.Errorf("%s.name is stored as %.30q: not sealed", table, v)
			}
		}
		if v := rawName(t, st, "wallets", "w_1"); v == "" {
			t.Error("no name")
		}
		var addr string
		if err := st.db.QueryRow(`SELECT address FROM wallets WHERE id = 'w_1'`).Scan(&addr); err != nil || addr != "0xabc" {
			t.Errorf("address = %q, %v: not plain", addr, err)
		}
		if got, found, err := st.getAccount(ctx, "acme", "acct_1"); err != nil || !found || got.Name != acct.Name {
			t.Fatalf("getAccount = %+v %v %v", got, found, err)
		}
		if list, err := st.listAccounts(ctx, "acme"); err != nil || len(list) != 1 || list[0].Name != acct.Name {
			t.Fatalf("listAccounts = %+v, %v", list, err)
		}
		if got, found, err := st.getWallet(ctx, "acme", "w_1"); err != nil || !found || got.Name != w.Name {
			t.Fatalf("getWallet = %+v %v %v", got, found, err)
		}
		if list, err := st.listWallets(ctx, "acme"); err != nil || len(list) != 1 || list[0].Name != w.Name {
			t.Fatalf("listWallets = %+v, %v", list, err)
		}
		// A rotation leaves the sealed name alone.
		if err := st.updateWalletKey(ctx, "acme", "w_1", "0xdef", "kms://acme/w_1/2"); err != nil {
			t.Fatal(err)
		}
		if got, _, err := st.getWallet(ctx, "acme", "w_1"); err != nil || got.Name != w.Name || got.Address != "0xdef" {
			t.Fatalf("after a rotation: %+v, %v", got, err)
		}
	})
}

// A name lifted into another wallet or tenant, or planted in the clear, is refused.
func TestANameIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openStore(t.TempDir(), testMaster)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()
		for _, id := range []string{"a", "b"} {
			if err := st.createAccount(ctx, &WalletAccount{ID: "acct_" + id, Org: "acme", Name: "name " + id, CreatedAt: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.db.Exec(`UPDATE accounts SET name = $1 WHERE id = 'acct_b'`, rawName(t, st, "accounts", "acct_a")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.getAccount(ctx, "acme", "acct_b"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("B read A's name: %v", err)
		}
		if _, err := st.db.Exec(`UPDATE accounts SET name = 'planted' WHERE id = 'acct_b'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.listAccounts(ctx, "acme"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
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

// A SQLite file written before the names were sealed holds plaintext in them. It is built
// by hand, so this stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	st, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO accounts (id, org, name, created_at) VALUES ('acct_old', 'acme', 'old account', 1)`,
		`INSERT INTO wallets (id, org, account_id, name, custody, tier, address, key_ref, created_at) VALUES ('w_old', 'acme', 'acct_old', 'old wallet', 'kms', 'hot', '0x1', 'k', 1)`,
	} {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = openStore(dir, testMaster)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if a, found, err := st.getAccount(ctx, "acme", "acct_old"); err != nil || !found || a.Name != "old account" {
		t.Fatalf("a legacy account = %+v %v %v", a, found, err)
	}
	if w, found, err := st.getWallet(ctx, "acme", "w_old"); err != nil || !found || w.Name != "old wallet" {
		t.Fatalf("a legacy wallet = %+v %v %v", w, found, err)
	}
	for table, id := range map[string]string{"accounts": "acct_old", "wallets": "w_old"} {
		if v := rawName(t, st, table, id); !strings.HasPrefix(v, "seal:") {
			t.Errorf("%s.name is still %.20q after the upgrade", table, v)
		}
	}
}
