package wallet

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestTheStoreKeepsEveryReadInsideItsOrg runs the store itself, on both engines,
// without the custody a wallet is normally provisioned through: an account and
// wallets are written as rows, and every lookup is held to its org and, within the
// org, to the scope it names. The wallet rows carry synthetic addresses and key
// references, because what is under test is the SQL and not the key.
func TestTheStoreKeepsEveryReadInsideItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openStore(t.TempDir(), testMaster)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()

		acct := &WalletAccount{ID: "acct_1", Org: "acme", Name: "main", CreatedAt: 5}
		if err := st.createAccount(ctx, acct); err != nil {
			t.Fatalf("create account: %v", err)
		}
		if _, found, err := st.getAccount(ctx, "evil", acct.ID); err != nil || found {
			t.Fatalf("another org read acme's account: found=%v err=%v", found, err)
		}
		if got, found, err := st.getAccount(ctx, "acme", acct.ID); err != nil || !found || got.Name != "main" || got.CreatedAt != 5 {
			t.Fatalf("account = %+v found=%v err=%v", got, found, err)
		}

		mk := func(id string, sc Scope, finance string) *Wallet {
			t.Helper()
			w := &Wallet{ID: id, Scope: sc, Name: "w", Custody: KindKMS, Tier: TierHot, Address: "0x" + id,
				KeyRef: "wallets/" + id, FinanceAccount: finance, CreatedAt: 1}
			if err := st.createWallet(ctx, w); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
			return w
		}
		wOrg := mk("wal_org", Scope{Org: "acme", AccountID: acct.ID}, "")
		wProj := mk("wal_proj", Scope{Org: "acme", Project: "p1", AccountID: acct.ID}, "")
		wAgent := mk("wal_agent", Scope{Org: "acme", Project: "p1", Agent: "bot7", AccountID: acct.ID}, "fin_1")
		mk("wal_evil", Scope{Org: "evil", Project: "p1", Agent: "bot7", AccountID: acct.ID}, "")

		ids := func(sc Scope) map[string]bool {
			t.Helper()
			ws, err := st.listWalletsByScope(ctx, sc)
			if err != nil {
				t.Fatalf("listWalletsByScope: %v", err)
			}
			m := map[string]bool{}
			for _, w := range ws {
				m[w.ID] = true
			}
			return m
		}
		if got := ids(Scope{Org: "acme"}); len(got) != 3 || !got[wOrg.ID] || !got[wProj.ID] || !got[wAgent.ID] {
			t.Fatalf("org listing = %v, want the 3 acme wallets only", got)
		}
		if got := ids(Scope{Org: "acme", Project: "p1"}); len(got) != 2 || got[wOrg.ID] {
			t.Fatalf("project listing = %v, want the 2 p1 wallets", got)
		}
		if got := ids(Scope{Org: "acme", Project: "p1", Agent: "bot7"}); len(got) != 1 || !got[wAgent.ID] {
			t.Fatalf("agent listing = %v, want just the bot7 wallet", got)
		}
		if got := ids(Scope{Org: "acme", Project: "p1", Agent: "bot7", AccountID: "acct_other"}); len(got) != 0 {
			t.Fatalf("a scope that names another account matched %v", got)
		}
		if got := ids(Scope{Org: "evil", Project: "p1", Agent: "bot7"}); got[wAgent.ID] || len(got) != 1 {
			t.Fatalf("the same narrowing under another org reached %v", got)
		}

		// The finance account is NULL when none was stated, and read back as empty.
		if w, found, err := st.getWallet(ctx, "acme", wOrg.ID); err != nil || !found || w.FinanceAccount != "" {
			t.Fatalf("a wallet with no finance account read back as %+v found=%v err=%v", w, found, err)
		}
		if w, found, err := st.getWallet(ctx, "acme", wAgent.ID); err != nil || !found || w.FinanceAccount != "fin_1" || w.Agent != "bot7" {
			t.Fatalf("a wallet read back as %+v found=%v err=%v", w, found, err)
		}
		if _, found, err := st.getWallet(ctx, "evil", wAgent.ID); err != nil || found {
			t.Fatalf("another org read acme's wallet: found=%v err=%v", found, err)
		}

		// A rotated key lands on the org's own wallet and on nobody else's.
		if err := st.updateWalletKey(ctx, "evil", wAgent.ID, "0xrotated", "wallets/rotated"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("another org rotated acme's key: %v", err)
		}
		if err := st.updateWalletKey(ctx, "acme", wAgent.ID, "0xrotated", "wallets/rotated"); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if w, _, _ := st.getWallet(ctx, "acme", wAgent.ID); w == nil || w.Address != "0xrotated" || w.KeyRef != "wallets/rotated" {
			t.Fatalf("the rotated key did not persist: %+v", w)
		}
	})
}
