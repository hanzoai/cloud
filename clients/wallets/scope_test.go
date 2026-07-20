package wallets

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luxfi/crypto"
)

// ── scope derivation (the extension of the org-only keyRef) ───────────────────

func TestScopeKeyRefDerivation(t *testing.T) {
	const id = "wal_abc"

	// Org-only scope derives the EXACT legacy ref — an org's default-scope wallet
	// keeps its un-suffixed key, so scoping is additive, never a migration.
	if got := (Scope{Org: "acme"}).keyRef(id); got != "wallets/acme/"+id {
		t.Fatalf("org-only ref = %q, want wallets/acme/%s", got, id)
	}

	// Each present narrowing adds ONE labeled segment, in a fixed order.
	full := Scope{Org: "acme", Project: "proj1", Agent: "bot7", AccountID: "acct9"}
	if got, want := full.keyRef(id), "wallets/acme/p/proj1/a/bot7/c/acct9/"+id; got != want {
		t.Fatalf("full-scope ref = %q, want %q", got, want)
	}

	// Distinct scopes MUST derive distinct refs (no two scopes address one key).
	refs := map[string]Scope{}
	for _, sc := range []Scope{
		{Org: "acme"},
		{Org: "acme", Project: "p1"},
		{Org: "acme", Agent: "p1"},     // same value, different DIMENSION → distinct ref
		{Org: "acme", AccountID: "p1"}, //     "                              "
		{Org: "acme", Project: "p1", Agent: "a1"},
		{Org: "other"}, // different tenant boundary
	} {
		ref := sc.keyRef(id)
		if prev, dup := refs[ref]; dup {
			t.Fatalf("scope %+v and %+v collide on ref %q", sc, prev, ref)
		}
		refs[ref] = sc
	}

	// A tenant can never reach across the org boundary: two orgs' refs differ even
	// with identical narrowings.
	a := Scope{Org: "acme", Project: "p"}.keyRef(id)
	b := Scope{Org: "evil", Project: "p"}.keyRef(id)
	if a == b {
		t.Fatal("cross-org refs must differ")
	}
}

func TestValidNarrowingRejectsInjection(t *testing.T) {
	for _, ok := range []string{"", "proj1", "bot-7", "acct_9", "a.b.c", "A1"} {
		if !validNarrowing(ok) {
			t.Fatalf("validNarrowing(%q) = false, want true", ok)
		}
	}
	// A '/' would let a crafted value cross into another wallet's ref — reject it,
	// along with the other ref-breaking shapes.
	for _, bad := range []string{"a/b", "../evil", "p/c/other", " sp ace", "has space", "-leadingdash"} {
		if validNarrowing(bad) {
			t.Fatalf("validNarrowing(%q) = true, want false (ref injection)", bad)
		}
	}
}

// ── scoped provisioning end-to-end (KMS-custodied at the scoped ref) ──────────

func TestScopedWalletSealsAtScopedRef(t *testing.T) {
	k, _ := testKMS(t)
	_, app := newService(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)

	acct := mkAccount(t, app, "acme")

	// Create an AGENT-scoped wallet (agent is a create-body narrowing).
	code, body := req(t, app, http.MethodPost, "/v1/wallets", "acme", map[string]any{
		"accountId": acct, "name": "w", "custody": "kms", "tier": "hot", "agent": "bot7",
	})
	if code != http.StatusOK {
		t.Fatalf("create scoped wallet = %d (%s)", code, body)
	}
	var w Wallet
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	if w.Agent != "bot7" || w.AccountID != acct {
		t.Fatalf("scope not persisted: agent=%q account=%q", w.Agent, w.AccountID)
	}

	// The key material is sealed at the SCOPED ref, not the org-only one.
	scopedRef := (Scope{Org: "acme", Agent: "bot7", AccountID: acct}).keyRef(w.ID)
	stored, err := k.GetSecret(context.Background(), scopedRef)
	if err != nil {
		t.Fatalf("secret not at scoped ref %q: %v", scopedRef, err)
	}
	if len(stored) == 0 {
		t.Fatal("scoped ref holds no key material")
	}
	// It is NOT at the org-only ref (proof the scope actually moved the ref).
	if _, err := k.GetSecret(context.Background(), "wallets/acme/"+w.ID); err == nil {
		t.Fatal("key must NOT be reachable at the org-only ref for a scoped wallet")
	}

	// And it signs: the signature recovers to the wallet address.
	digest := crypto.Keccak256([]byte("scoped sign"))
	sig := signOnce(t, app, "acme", w.ID, digest)
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("SigToPub: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub).Hex(); got != w.Address {
		t.Fatalf("scoped signature recovered to %s, want %s", got, w.Address)
	}
}

// ── the ONE scope lookup path narrows within, never across, the org ───────────

func TestScopeLookupPath(t *testing.T) {
	k, _ := testKMS(t)
	s, app := newService(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)
	ctx := context.Background()

	// Provision four wallets in org "acme" spanning distinct scopes, plus one in a
	// second org to prove isolation. Build them through the store so the test pins
	// exactly the scope of each (project needs a header on the HTTP path).
	acct := mkAccount(t, app, "acme")
	mk := func(org string, sc Scope) *Wallet {
		w := &Wallet{ID: newID("wal"), Scope: sc, Name: "w", Custody: KindKMS, Tier: TierHot, CreatedAt: 1}
		addr, err := (kmsCustody{kms: k}).Provision(ctx, w)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		w.Address = addr
		if err := s.State.store.createWallet(ctx, w); err != nil {
			t.Fatalf("create: %v", err)
		}
		return w
	}
	wOrg := mk("acme", Scope{Org: "acme", AccountID: acct})
	wProj := mk("acme", Scope{Org: "acme", Project: "p1", AccountID: acct})
	wAgent := mk("acme", Scope{Org: "acme", Project: "p1", Agent: "bot7", AccountID: acct})
	_ = mk("evil", Scope{Org: "evil", Project: "p1", Agent: "bot7", AccountID: acct})

	ids := func(ws []Wallet) map[string]bool {
		m := map[string]bool{}
		for _, w := range ws {
			m[w.ID] = true
		}
		return m
	}

	mustList := func(sc Scope) map[string]bool {
		ws, err := s.State.store.listWalletsByScope(ctx, sc)
		if err != nil {
			t.Fatalf("listWalletsByScope: %v", err)
		}
		return ids(ws)
	}

	// Whole org: all three acme wallets, none from evil.
	got := mustList(Scope{Org: "acme"})
	if !got[wOrg.ID] || !got[wProj.ID] || !got[wAgent.ID] || len(got) != 3 {
		t.Fatalf("org listing = %v, want the 3 acme wallets only", got)
	}

	// Project narrowing: only the two under p1.
	got = mustList(Scope{Org: "acme", Project: "p1"})
	if got[wOrg.ID] || !got[wProj.ID] || !got[wAgent.ID] || len(got) != 2 {
		t.Fatalf("project listing = %v, want the 2 p1 wallets", got)
	}

	// Agent narrowing: only the agent wallet.
	got = mustList(Scope{Org: "acme", Project: "p1", Agent: "bot7"})
	if len(got) != 1 || !got[wAgent.ID] {
		t.Fatalf("agent listing = %v, want just the bot7 wallet", got)
	}

	// Isolation: the SAME narrowings under another org never reach acme's wallets.
	got = mustList(Scope{Org: "evil", Project: "p1", Agent: "bot7"})
	if got[wAgent.ID] {
		t.Fatal("cross-org scope lookup leaked an acme wallet")
	}
}
