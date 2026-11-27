// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Regression test for Finding 1: Bind buyer orders, order clearing, lease
// settlement, and market export to the host-validated principal rather than
// trusting caller-supplied BuyerID fields.
func TestHTTPPrincipalBindingPreventsCrossOrgSpend(t *testing.T) {
	now := int64(1760000000)
	kpProv, _ := DeriveKeyPair("reg1-prov", 101, 1)
	kpExp, _ := DeriveKeyPair("reg1-exp", 101, 2)

	m := NewMarket(DefaultConfig())
	if err := m.DepositBuyer("org-victim", 5_000_000); err != nil {
		t.Fatal(err)
	}
	if err := m.DepositBuyer("org-attacker", 5_000_000); err != nil {
		t.Fatal(err)
	}
	if err := m.DepositProviderWithKey("prov-1", kpProv.PubKeyHex, 2_000_000); err != nil {
		t.Fatal(err)
	}
	offer := testOffer(t, kpProv, "offer-reg1", "prov-1", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(offer, now); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(m, kpExp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.Now = func() int64 { return now }

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, luxlog.New("test"), srv); err != nil {
		t.Fatal(err)
	}

	do := func(method, path string, body any, user, org string, admin bool) (int, []byte) {
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req.Header.Set("X-User-Id", user)
		}
		if org != "" {
			req.Header.Set("X-Org-Id", org)
		}
		if admin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// 1. Unauthenticated order submission fails closed with 401.
	orderBody := map[string]any{
		"orderId":                "ord-unauth",
		"buyerId":                "org-victim",
		"model":                  "llama-3.3-70b",
		"requestedTokens":        500_000,
		"minVramGb":              80,
		"maxMicroUsdPer1MTokens": 600_000,
		"createdAtUnix":          now,
		"expiresAtUnix":          now + 600,
	}
	if code, _ := do(http.MethodPost, "/v1/spotmarket/orders", orderBody, "", "", false); code != http.StatusUnauthorized {
		t.Fatalf("unauth submit order: want 401, got %d", code)
	}

	// 2. Attacker authenticated as org-attacker cannot spend org-victim's deposit.
	orderBody["orderId"] = "ord-spoof"
	if code, b := do(http.MethodPost, "/v1/spotmarket/orders", orderBody, "user-atk", "org-attacker", false); code != http.StatusForbidden {
		t.Fatalf("cross-org spoofed buyerId: want 403, got %d (%s)", code, string(b))
	}
	if avail, esc := m.BuyerBalance("org-victim"); avail != 5_000_000 || esc != 0 {
		t.Fatalf("victim balance mutated by spoofed order: avail=%d esc=%d", avail, esc)
	}

	// 3. Victim submits its own order (omitting buyerId -> server stamps org-victim).
	victimOrd := map[string]any{
		"orderId":                "ord-victim-1",
		"model":                  "llama-3.3-70b",
		"requestedTokens":        500_000,
		"minVramGb":              80,
		"maxMicroUsdPer1MTokens": 600_000,
		"createdAtUnix":          now,
		"expiresAtUnix":          now + 600,
	}
	code, body := do(http.MethodPost, "/v1/spotmarket/orders", victimOrd, "user-vic", "org-victim", false)
	if code != http.StatusCreated {
		t.Fatalf("victim submit order: want 201, got %d (%s)", code, string(body))
	}
	var created Order
	if err := json.Unmarshal(body, &created); err != nil || created.BuyerID != "org-victim" {
		t.Fatalf("expected server-stamped BuyerID=org-victim, got %+v (err=%v)", created, err)
	}

	// Provider submits valid signed bid.
	bid, err := SignBid(kpProv.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-reg1", OrderID: "ord-victim-1", OfferID: "offer-reg1",
		ProviderID: "prov-1", ProviderPubKeyHex: kpProv.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, b := do(http.MethodPost, "/v1/spotmarket/bids", bid, "", "", false); code != http.StatusCreated {
		t.Fatalf("submit bid: want 201, got %d (%s)", code, string(b))
	}

	// 4. Attacker cannot clear victim's order.
	if code, b := do(http.MethodPost, "/v1/spotmarket/orders/ord-victim-1/clear", nil, "user-atk", "org-attacker", false); code != http.StatusForbidden {
		t.Fatalf("cross-org clear order: want 403, got %d (%s)", code, string(b))
	}

	// Victim clears its own order.
	code, body = do(http.MethodPost, "/v1/spotmarket/orders/ord-victim-1/clear", nil, "user-vic", "org-victim", false)
	if code != http.StatusOK {
		t.Fatalf("victim clear order: want 200, got %d (%s)", code, string(body))
	}
	var cleared clearOrderOut
	if err := json.Unmarshal(body, &cleared); err != nil || len(cleared.Leases) != 1 {
		t.Fatalf("expected 1 lease, got %+v (err=%v)", cleared, err)
	}
	leaseID := cleared.Leases[0].LeaseID

	// 5. Attacker cannot settle victim's lease.
	settleBody := map[string]any{
		"deliveredTokens":   500_000,
		"observedTtftMs":    85.0,
		"observedLatencyMs": computeGenerationLatencyMs(85.0, 500_000, 125.0),
		"observedTokPerSec": 125.0,
		"completed":         true,
	}
	if code, b := do(http.MethodPost, "/v1/spotmarket/leases/"+leaseID+"/settle", settleBody, "user-atk", "org-attacker", false); code != http.StatusForbidden {
		t.Fatalf("cross-org settle lease: want 403, got %d (%s)", code, string(b))
	}

	// 6. Non-admin org cannot export the full multi-org market ledger.
	if code, _ := do(http.MethodGet, "/v1/spotmarket/export", nil, "user-atk", "org-attacker", false); code != http.StatusForbidden {
		t.Fatalf("non-admin export: want 403, got %d", code)
	}
	if code, _ := do(http.MethodGet, "/v1/spotmarket/export", nil, "", "", false); code != http.StatusUnauthorized {
		t.Fatalf("unauth export: want 401, got %d", code)
	}

	// Victim settles its own lease cleanly.
	if code, b := do(http.MethodPost, "/v1/spotmarket/leases/"+leaseID+"/settle", settleBody, "user-vic", "org-victim", false); code != http.StatusOK {
		t.Fatalf("victim settle lease: want 200, got %d (%s)", code, string(b))
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift: %v", err)
	}

	// 7. Standalone net/http Handler also enforces principal binding fail-closed.
	srvStd, err := NewServer(m, kpExp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	srvStd.Now = func() int64 { return now }
	srvStd.Acting = func(_ context.Context) (string, error) { return "org-attacker", nil }
	h := srvStd.Handler()
	rawSpoof, _ := json.Marshal(map[string]any{
		"orderId": "ord-std-spoof", "buyerId": "org-victim", "model": "llama-3.3-70b",
		"requestedTokens": 100_000, "minVramGb": 80, "maxMicroUsdPer1MTokens": 600_000,
		"createdAtUnix": now, "expiresAtUnix": now + 600,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/spotmarket/orders", bytes.NewReader(rawSpoof)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("standalone net/http cross-org spoof: want 403, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// Regression test for Finding 2: Funded provider accounts must be cryptographically
// bound to their authorized signing key so an attacker with a fresh key cannot
// register offers or bond/slash another provider's funds.
func TestProviderKeyBindingPreventsFundedAccountHijack(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	victimKP, _ := DeriveKeyPair("reg2-victim", 202, 1)
	attackerKP, _ := DeriveKeyPair("reg2-attacker", 202, 2)

	// Victim is funded BEFORE any offer is registered and without BindProviderKey yet.
	if err := m.DepositProvider("prov-victim", 2_000_000); err != nil {
		t.Fatal(err)
	}

	// Attacker tries to claim prov-victim with a fresh self-signed offer -> MUST FAIL CLOSED.
	spoofOffer := testOffer(t, attackerKP, "offer-spoof", "prov-victim", "independent", 300_000, now, 1)
	if err := m.RegisterOffer(spoofOffer, now); !errors.Is(err, ErrUnauthorizedProviderKey) {
		t.Fatalf("unbound funded provider hijack: want ErrUnauthorizedProviderKey, got %v", err)
	}

	// Bind prov-victim to victim's real key; attempting to rebind to attacker fails.
	if err := m.BindProviderKey("prov-victim", victimKP.PubKeyHex); err != nil {
		t.Fatalf("BindProviderKey victim: %v", err)
	}
	if err := m.BindProviderKey("prov-victim", attackerKP.PubKeyHex); !errors.Is(err, ErrUnauthorizedProviderKey) {
		t.Fatalf("rebind to attacker: want ErrUnauthorizedProviderKey, got %v", err)
	}

	// Victim's own signed offer succeeds; attacker's offer for prov-victim still fails.
	legitOffer := testOffer(t, victimKP, "offer-legit", "prov-victim", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(legitOffer, now); err != nil {
		t.Fatalf("legit offer rejected: %v", err)
	}
	if err := m.RegisterOffer(spoofOffer, now); !errors.Is(err, ErrUnauthorizedProviderKey) {
		t.Fatalf("bound provider hijack: want ErrUnauthorizedProviderKey, got %v", err)
	}

	// Attacker cannot submit a bid naming prov-victim against legitOffer.
	_ = m.DepositBuyer("buyer-1", 5_000_000)
	if err := m.SubmitOrder(Order{
		OrderID: "ord-reg2", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
	}, now); err != nil {
		t.Fatal(err)
	}
	spoofBid, _ := SignBid(attackerKP.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-spoof", OrderID: "ord-reg2", OfferID: "offer-legit",
		ProviderID: "prov-victim", ProviderPubKeyHex: attackerKP.PubKeyHex,
		BidMicroUSDPer1MTokens: 300_000, OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	if err := m.SubmitBid(spoofBid, now); !errors.Is(err, ErrUnauthorizedProviderKey) {
		t.Fatalf("attacker bid on victim account: want ErrUnauthorizedProviderKey, got %v", err)
	}
	if avail, locked := m.ProviderBalance("prov-victim"); avail != 2_000_000 || locked != 0 {
		t.Fatalf("victim provider balance mutated: avail=%d locked=%d", avail, locked)
	}

	// Permissionless self-certifying key-derived ProviderID works without pre-binding,
	// and rejects any mismatched signer key.
	selfID := ProviderIDFromPubKey(attackerKP.PubKeyHex)
	if err := m.DepositProvider(selfID, 1_000_000); err != nil {
		t.Fatal(err)
	}
	selfOffer := testOffer(t, attackerKP, "offer-self", selfID, "independent", 410_000, now, 2)
	if err := m.RegisterOffer(selfOffer, now); err != nil {
		t.Fatalf("self-certifying key-derived provider offer failed: %v", err)
	}
	impostorOffer := testOffer(t, victimKP, "offer-self-impostor", selfID, "independent", 410_000, now, 3)
	if err := m.RegisterOffer(impostorOffer, now); !errors.Is(err, ErrUnauthorizedProviderKey) {
		t.Fatalf("impostor on key-derived provider ID: want ErrUnauthorizedProviderKey, got %v", err)
	}
}

// Regression test for Finding 3: BundleCommitment must commit EVERY field of
// Lease consumed after import so mutating BuyerID, AgreedMicroUSDPer1MTokens,
// SlashBPSOnBreach, or SLA bounds invalidates VerifyExportBundle.
func TestExportBundleCommitmentCoversCompleteLeaseTerms(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kpProv, _ := DeriveKeyPair("reg3-prov", 303, 1)
	kpExp, _ := DeriveKeyPair("reg3-exp", 303, 2)
	_ = m.DepositBuyer("buyer-a", 5_000_000)
	_ = m.DepositProviderWithKey("prov-1", kpProv.PubKeyHex, 2_000_000)

	off := testOffer(t, kpProv, "offer-reg3", "prov-1", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(off, now); err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitOrder(Order{
		OrderID: "ord-reg3", BuyerID: "buyer-a", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
	}, now); err != nil {
		t.Fatal(err)
	}
	bid, _ := SignBid(kpProv.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-reg3", OrderID: "ord-reg3", OfferID: "offer-reg3",
		ProviderID: "prov-1", ProviderPubKeyHex: kpProv.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	if err := m.SubmitBid(bid, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ClearOrder("ord-reg3", now); err != nil {
		t.Fatal(err)
	}

	bundle, err := m.ExportBundle(kpExp.PrivateKey, now)
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	if err := VerifyExportBundle(bundle); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}

	cloneBundle := func() MarketExportBundle {
		raw, _ := json.Marshal(bundle)
		var cp MarketExportBundle
		_ = json.Unmarshal(raw, &cp)
		return cp
	}

	mutations := []struct {
		name   string
		mutate func(*MarketExportBundle)
	}{
		{"Lease.BuyerID", func(b *MarketExportBundle) { b.Leases[0].BuyerID = "attacker" }},
		{"Lease.AgreedMicroUSDPer1MTokens", func(b *MarketExportBundle) { b.Leases[0].AgreedMicroUSDPer1MTokens = 999_999 }},
		{"Lease.SlashBPSOnBreach", func(b *MarketExportBundle) { b.Leases[0].SlashBPSOnBreach = 1 }},
		{"Lease.TargetTTFTMs", func(b *MarketExportBundle) { b.Leases[0].TargetTTFTMs = 999_999 }},
		{"Lease.MinThroughputTokPerSec", func(b *MarketExportBundle) { b.Leases[0].MinThroughputTokPerSec = 0.01 }},
		{"Lease.BidID", func(b *MarketExportBundle) { b.Leases[0].BidID = "tampered-bid" }},
		{"Lease.OfferID", func(b *MarketExportBundle) { b.Leases[0].OfferID = "tampered-offer" }},
		{"Lease.StartedAtUnix", func(b *MarketExportBundle) { b.Leases[0].StartedAtUnix += 1 }},
	}

	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			tampered := cloneBundle()
			tc.mutate(&tampered)
			if err := VerifyExportBundle(tampered); err == nil {
				t.Fatalf("mutating %s did not fail VerifyExportBundle", tc.name)
			}
		})
	}
}

// Regression test for Finding 4: ExportBundle/ImportBundle must preserve
// pending orders and bids across export/import so forked markets can clear
// pending auctions, expire stale orders to release locked funds, and fail over
// active leases without stranded balances.
func TestExportImportPreservesPendingAuctionsAndFailover(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kp1, _ := DeriveKeyPair("reg4-p1", 404, 1)
	kp2, _ := DeriveKeyPair("reg4-p2", 404, 2)
	kpExp, _ := DeriveKeyPair("reg4-exp", 404, 3)

	_ = m.DepositBuyer("buyer-1", 5_000_000)
	_ = m.DepositProviderWithKey("prov-1", kp1.PubKeyHex, 2_000_000)
	_ = m.DepositProviderWithKey("prov-2", kp2.PubKeyHex, 2_000_000)

	off1 := testOffer(t, kp1, "offer-p1", "prov-1", "independent", 400_000, now, 1)
	off2 := testOffer(t, kp2, "offer-p2", "prov-2", "independent", 450_000, now, 1)
	_ = m.RegisterOffer(off1, now)
	_ = m.RegisterOffer(off2, now)

	// Submit order & 2 bids, then EXPORT BEFORE CLEARING.
	if err := m.SubmitOrder(Order{
		OrderID: "ord-pending", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 300, RankingWeights: DefaultRankingWeights(),
	}, now); err != nil {
		t.Fatal(err)
	}
	bid1, _ := SignBid(kp1.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-p1", OrderID: "ord-pending", OfferID: "offer-p1",
		ProviderID: "prov-1", ProviderPubKeyHex: kp1.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 300, Nonce: 1,
	})
	bid2, _ := SignBid(kp2.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-p2", OrderID: "ord-pending", OfferID: "offer-p2",
		ProviderID: "prov-2", ProviderPubKeyHex: kp2.PubKeyHex, BidMicroUSDPer1MTokens: 450_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 110_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 300, Nonce: 1,
	})
	if err := m.SubmitBid(bid1, now); err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitBid(bid2, now); err != nil {
		t.Fatal(err)
	}

	bundle, err := m.ExportBundle(kpExp.PrivateKey, now)
	if err != nil {
		t.Fatalf("ExportBundle pending auction: %v", err)
	}

	// Fork A: Clear the imported pending order, settle as failed, and fail over!
	forkClear, err := ImportBundle(bundle, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("ImportBundle forkClear: %v", err)
	}
	leases, _, err := forkClear.ClearOrder("ord-pending", now+5)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClearOrder on imported pending auction failed: leases=%d err=%v", len(leases), err)
	}
	// Non-winning provider's bond must be unlocked immediately.
	if _, locked2 := forkClear.ProviderBalance("prov-2"); locked2 != 0 {
		t.Fatalf("non-winning provider bond remained locked after forked clear: %d", locked2)
	}
	// Settle active lease as non-delivery and fail over onto prov-2.
	if _, err := forkClear.SettleLease(leases[0].LeaseID, DeliveryReport{
		LeaseID: leases[0].LeaseID, DeliveredTokens: 0, Completed: false, FailureReason: "crash",
	}, now+10); err != nil {
		t.Fatalf("SettleLease on imported lease: %v", err)
	}
	foLease, err := forkClear.FailoverLease(leases[0].LeaseID, now+12)
	if err != nil || foLease.ProviderID != "prov-2" {
		t.Fatalf("FailoverLease on imported market failed: lease=%+v err=%v", foLease, err)
	}
	if err := forkClear.CheckConservation(); err != nil {
		t.Fatalf("forkClear conservation drift: %v", err)
	}

	// Fork B: ExpireStale releases locked buyer escrow and all pending bid bonds.
	forkExpire, err := ImportBundle(bundle, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("ImportBundle forkExpire: %v", err)
	}
	if n := forkExpire.ExpireStale(now + 400); n == 0 {
		t.Fatalf("expected ExpireStale to release expired pending order")
	}
	if avail, esc := forkExpire.BuyerBalance("buyer-1"); avail != 5_000_000 || esc != 0 {
		t.Fatalf("buyer escrow not released by ExpireStale: avail=%d esc=%d", avail, esc)
	}
	if avail1, locked1 := forkExpire.ProviderBalance("prov-1"); avail1 != 2_000_000 || locked1 != 0 {
		t.Fatalf("prov-1 bond not released by ExpireStale: avail=%d locked=%d", avail1, locked1)
	}
	if avail2, locked2 := forkExpire.ProviderBalance("prov-2"); avail2 != 2_000_000 || locked2 != 0 {
		t.Fatalf("prov-2 bond not released by ExpireStale: avail=%d locked=%d", avail2, locked2)
	}
	if err := forkExpire.CheckConservation(); err != nil {
		t.Fatalf("forkExpire conservation drift: %v", err)
	}

	// Stripping pending orders/bids while leaving locked escrow/bonds MUST fail VerifyExportBundle.
	stripped := bundle
	stripped.Orders = nil
	stripped.PendingBids = nil
	if err := VerifyExportBundle(stripped); err == nil {
		t.Fatal("bundle with stranded locked escrow/bonds and missing pending orders/bids must fail verification")
	}
}

// Regression test for Finding 5: Token-cost and bond calculations must use
// checked 128-bit arithmetic and reject int64 overflows before mutating state.
func TestTokenCostOverflowRejectedBeforeMutation(t *testing.T) {
	if _, err := ComputeTokenCostMicroUSDChecked(2_000_000, math.MaxInt64); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("ComputeTokenCostMicroUSDChecked(2M, MaxInt64): want ErrArithmeticOverflow, got %v", err)
	}
	if got := ComputeTokenCostMicroUSD(2_000_000, math.MaxInt64); got != math.MaxInt64 || got < 0 {
		t.Fatalf("ComputeTokenCostMicroUSD overflow must saturate positive at MaxInt64, got %d", got)
	}
	if got, err := ComputeTokenCostMicroUSDChecked(1_000_000, math.MaxInt64); err != nil || got != math.MaxInt64 {
		t.Fatalf("exact boundary 1M * MaxInt64 / 1M: got (%d, %v), want (MaxInt64, nil)", got, err)
	}
	if _, err := ComputeTokenCostMicroUSDChecked(1_000_001, math.MaxInt64); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("boundary+1 overflow: want ErrArithmeticOverflow, got %v", err)
	}

	// An unfunded buyer MUST NOT be able to manufacture positive balance via overflow.
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	err := m.SubmitOrder(Order{
		OrderID:                "ord-overflow",
		BuyerID:                "unfunded-attacker",
		Model:                  "llama-3.3-70b",
		RequestedTokens:        2_000_000,
		MinVRAMGB:              80,
		MaxMicroUSDPer1MTokens: math.MaxInt64,
		CreatedAtUnix:          now,
		ExpiresAtUnix:          now + 600,
	}, now)
	if !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("overflow SubmitOrder: want ErrArithmeticOverflow, got %v", err)
	}
	if avail, esc := m.BuyerBalance("unfunded-attacker"); avail != 0 || esc != 0 {
		t.Fatalf("unfunded attacker balance mutated on overflow: avail=%d esc=%d", avail, esc)
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift: %v", err)
	}
}

// Regression test for Finding 6: FailoverLease must reject active, completed,
// expired, and already-recovered leases before allocating replacement funds.
func TestFailoverRejectsActiveCompletedAndAlreadyRecoveredLeases(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kp1, _ := DeriveKeyPair("reg6-p1", 606, 1)
	kp2, _ := DeriveKeyPair("reg6-p2", 606, 2)
	kp3, _ := DeriveKeyPair("reg6-p3", 606, 3)

	_ = m.DepositBuyer("buyer-1", 5_000_000)
	_ = m.DepositProviderWithKey("prov-1", kp1.PubKeyHex, 2_000_000)
	_ = m.DepositProviderWithKey("prov-2", kp2.PubKeyHex, 2_000_000)
	_ = m.DepositProviderWithKey("prov-3", kp3.PubKeyHex, 2_000_000)

	_ = m.RegisterOffer(testOffer(t, kp1, "offer-1", "prov-1", "independent", 400_000, now, 1), now)
	_ = m.RegisterOffer(testOffer(t, kp2, "offer-2", "prov-2", "independent", 450_000, now, 1), now)
	_ = m.RegisterOffer(testOffer(t, kp3, "offer-3", "prov-3", "independent", 500_000, now, 1), now)

	_ = m.SubmitOrder(Order{
		OrderID: "ord-reg6", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
	}, now)
	bid1, _ := SignBid(kp1.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-1", OrderID: "ord-reg6", OfferID: "offer-1",
		ProviderID: "prov-1", ProviderPubKeyHex: kp1.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	_ = m.SubmitBid(bid1, now)
	leases, _, err := m.ClearOrder("ord-reg6", now)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClearOrder: %v", err)
	}
	leaseID := leases[0].LeaseID

	// 1. Active lease MUST be rejected by FailoverLease without mutating state.
	if _, err := m.FailoverLease(leaseID, now+1); !errors.Is(err, ErrLeaseNotEligibleForFailover) {
		t.Fatalf("failover on active lease: want ErrLeaseNotEligibleForFailover, got %v", err)
	}
	if l, ok := m.Lease(leaseID); !ok || l.Status != LeaseActive {
		t.Fatalf("active lease status mutated by rejected failover: %+v", l)
	}

	// 2. Settle as non-delivery -> first failover succeeds.
	if _, err := m.SettleLease(leaseID, DeliveryReport{
		LeaseID: leaseID, DeliveredTokens: 0, Completed: false, FailureReason: "crash",
	}, now+5); err != nil {
		t.Fatal(err)
	}
	fo1, err := m.FailoverLease(leaseID, now+6)
	if err != nil || fo1 == nil {
		t.Fatalf("first failover on settled failed lease failed: %v", err)
	}

	// 3. Repeated failover on already-recovered lease MUST be rejected without allocating extra funds.
	availBefore, escBefore := m.BuyerBalance("buyer-1")
	if _, err := m.FailoverLease(leaseID, now+7); !errors.Is(err, ErrLeaseNotEligibleForFailover) {
		t.Fatalf("duplicate failover: want ErrLeaseNotEligibleForFailover, got %v", err)
	}
	availAfter, escAfter := m.BuyerBalance("buyer-1")
	if availBefore != availAfter || escBefore != escAfter {
		t.Fatalf("duplicate failover mutated balances: before=(%d,%d) after=(%d,%d)", availBefore, escBefore, availAfter, escAfter)
	}

	// 4. Complete replacement lease, then verify FailoverLease rejects LeaseCompleted.
	if _, err := m.SettleLease(fo1.LeaseID, DeliveryReport{
		LeaseID: fo1.LeaseID, DeliveredTokens: fo1.AllocatedTokens,
		ObservedTTFTMs: 85, ObservedLatencyMs: computeGenerationLatencyMs(85, fo1.AllocatedTokens, 125),
		ObservedTokPerSec: 125, Completed: true,
	}, now+15); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FailoverLease(fo1.LeaseID, now+16); !errors.Is(err, ErrLeaseNotEligibleForFailover) {
		t.Fatalf("failover on completed lease: want ErrLeaseNotEligibleForFailover, got %v", err)
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift: %v", err)
	}
}

// Regression test for Finding 7: Hardware-attested orders must fail closed
// unless an offer's hardware quote was verified by a trusted AttestationVerifier.
func TestHardwareAttestationRequiresVerifiedEvidence(t *testing.T) {
	now := int64(1760000000)
	kp, _ := DeriveKeyPair("reg7-prov", 707, 1)

	mkHWOffer := func(id, quoteHash string, nonce uint64) SignedOffer {
		so, err := SignOffer(kp.PrivateKey, Offer{
			ProtocolVersion: ProtocolVersion, OfferID: id, ProviderID: "prov-hw", ProviderPubKeyHex: kp.PubKeyHex,
			OperatorClass:   "independent",
			Hardware:        GPUHardwareSpec{GPUModel: "H100-SXM5-80GB", GPUCount: 1, AvailableVRAMGB: 80, TotalVRAMGB: 80},
			SupportedModels: []string{"llama-3.3-70b"},
			Location:        LocationSpec{Region: "eu-central", Jurisdiction: "DE", DatacenterTier: 3},
			Pricing:         PricingSpec{MicroUSDPer1MTokens: 450_000, MicroUSDPerGPUHour: 1_800_000},
			SLO:             ServiceCommitment{TargetTTFTMs: 90, MinThroughputTokPerSec: 120, UptimeBPS: 9950, SlashBPSOnBreach: 5000},
			AvailableSlots:  4, IssuedAtUnix: now, ExpiresAtUnix: now + 3600, Nonce: nonce,
			AttestationClass:     VerificationHardwareAttestation,
			AttestationQuoteHash: quoteHash,
		})
		if err != nil {
			t.Fatal(err)
		}
		return so
	}

	// 1. Without AttestationVerifier configured, self-signed hardware_attestation
	// claims (even with empty or non-empty quote hash) MUST fail closed.
	mUnconfigured := NewMarket(DefaultConfig())
	_ = mUnconfigured.DepositBuyer("buyer-tee", 5_000_000)
	_ = mUnconfigured.DepositProviderWithKey("prov-hw", kp.PubKeyHex, 2_000_000)
	_ = mUnconfigured.RegisterOffer(mkHWOffer("offer-empty-quote", "", 1), now)
	_ = mUnconfigured.RegisterOffer(mkHWOffer("offer-unverified-quote", "sha256:fakequote", 2), now)

	_ = mUnconfigured.SubmitOrder(Order{
		OrderID: "ord-tee-1", BuyerID: "buyer-tee", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
		RequireHardwareAttested: true,
	}, now)
	bidEmpty, _ := SignBid(kp.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-empty-quote", OrderID: "ord-tee-1", OfferID: "offer-empty-quote",
		ProviderID: "prov-hw", ProviderPubKeyHex: kp.PubKeyHex, BidMicroUSDPer1MTokens: 450_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	_ = mUnconfigured.SubmitBid(bidEmpty, now)
	if leases, rej, err := mUnconfigured.ClearOrder("ord-tee-1", now); !errors.Is(err, ErrNoEligibleBids) || len(leases) != 0 {
		t.Fatalf("self-signed empty quote bypassed TEE gate: leases=%d rej=%+v err=%v", len(leases), rej, err)
	}

	// 2. With a trusted AttestationVerifier configured, only verified quotes pass.
	cfgVerified := DefaultConfig()
	cfgVerified.AttestationVerifier = func(o Offer) error {
		if o.AttestationQuoteHash == "tdx-quote-verified-001" {
			return nil
		}
		return ErrUnverifiedHardwareAttestation
	}
	mVerified := NewMarket(cfgVerified)
	_ = mVerified.DepositBuyer("buyer-tee", 5_000_000)
	_ = mVerified.DepositProviderWithKey("prov-hw", kp.PubKeyHex, 2_000_000)
	_ = mVerified.RegisterOffer(mkHWOffer("offer-bad-quote", "tdx-quote-forged", 10), now)
	_ = mVerified.RegisterOffer(mkHWOffer("offer-good-quote", "tdx-quote-verified-001", 11), now)

	_ = mVerified.SubmitOrder(Order{
		OrderID: "ord-tee-bad", BuyerID: "buyer-tee", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
		RequireHardwareAttested: true,
	}, now)
	bidBad, _ := SignBid(kp.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-bad-quote", OrderID: "ord-tee-bad", OfferID: "offer-bad-quote",
		ProviderID: "prov-hw", ProviderPubKeyHex: kp.PubKeyHex, BidMicroUSDPer1MTokens: 450_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 10,
	})
	_ = mVerified.SubmitBid(bidBad, now)
	if _, _, err := mVerified.ClearOrder("ord-tee-bad", now); !errors.Is(err, ErrNoEligibleBids) {
		t.Fatalf("forged TEE quote should be rejected, got err=%v", err)
	}

	_ = mVerified.SubmitOrder(Order{
		OrderID: "ord-tee-good", BuyerID: "buyer-tee", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
		RequireHardwareAttested: true,
	}, now)
	bidGood, _ := SignBid(kp.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-good-quote", OrderID: "ord-tee-good", OfferID: "offer-good-quote",
		ProviderID: "prov-hw", ProviderPubKeyHex: kp.PubKeyHex, BidMicroUSDPer1MTokens: 450_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 11,
	})
	_ = mVerified.SubmitBid(bidGood, now)
	if leases, _, err := mVerified.ClearOrder("ord-tee-good", now); err != nil || len(leases) != 1 {
		t.Fatalf("verified TEE quote should clear cleanly: leases=%d err=%v", len(leases), err)
	}
}

// Regression test for Finding 8: Every ZIP route in Mount must be registered
// as a typed operation so all 9 operations project into /v1/openapi.json, MCP
// tools, CLI commands, and SDKs.
func TestZipMountRegistersTypedOperationsInOpenAPI(t *testing.T) {
	m := NewMarket(DefaultConfig())
	srv, err := NewServer(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, luxlog.New("test"), srv); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal OpenAPISpec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal OpenAPISpec: %v", err)
	}

	got := make(map[string]map[string]any)
	for path, methods := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/spotmarket") {
			continue
		}
		for method, op := range methods {
			got[strings.ToUpper(method)+" "+path] = op
		}
	}

	want := []string{
		"GET /v1/spotmarket/health",
		"POST /v1/spotmarket/offers",
		"GET /v1/spotmarket/offers",
		"POST /v1/spotmarket/orders",
		"POST /v1/spotmarket/bids",
		"POST /v1/spotmarket/orders/{id}/clear",
		"POST /v1/spotmarket/leases/{id}/settle",
		"GET /v1/spotmarket/peers",
		"GET /v1/spotmarket/export",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d typed OpenAPI operations, got %d: %v", len(want), len(got), got)
	}
	for _, opKey := range want {
		op, ok := got[opKey]
		if !ok {
			t.Errorf("missing typed OpenAPI operation %q", opKey)
			continue
		}
		if desc, _ := op["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("typed OpenAPI operation %q has empty description (run go generate ./apps/spotmarket)", opKey)
		}
	}
}

// Regression test for Finding 9: SettleLease must enforce snapshotted latency
// and throughput SLA commitments even when Completed=true and all tokens are
// delivered.
func TestSettleLeaseEnforcesSLABreachSlashing(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kp, _ := DeriveKeyPair("reg9-prov", 909, 1)
	_ = m.DepositBuyer("buyer-1", 5_000_000)
	_ = m.DepositProviderWithKey("prov-sla", kp.PubKeyHex, 2_000_000)

	// Offer promises TargetTTFTMs=90, MinThroughputTokPerSec=120, SlashBPSOnBreach=5000.
	off := testOffer(t, kp, "offer-sla", "prov-sla", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(off, now); err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitOrder(Order{
		OrderID: "ord-sla", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
	}, now); err != nil {
		t.Fatal(err)
	}
	bid, _ := SignBid(kp.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-sla", OrderID: "ord-sla", OfferID: "offer-sla",
		ProviderID: "prov-sla", ProviderPubKeyHex: kp.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000,
		IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	if err := m.SubmitBid(bid, now); err != nil {
		t.Fatal(err)
	}
	leases, _, err := m.ClearOrder("ord-sla", now)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClearOrder: %v", err)
	}
	if leases[0].TargetTTFTMs != 90 || leases[0].MinThroughputTokPerSec != 120 {
		t.Fatalf("SLA commitments not snapshotted onto lease: %+v", leases[0])
	}

	// Provider delivers all tokens with Completed=true, but grossly violates TTFT & throughput!
	rcpt, err := m.SettleLease(leases[0].LeaseID, DeliveryReport{
		LeaseID:           leases[0].LeaseID,
		DeliveredTokens:   leases[0].AllocatedTokens,
		ObservedTTFTMs:    60_000,
		ObservedLatencyMs: computeGenerationLatencyMs(60_000, leases[0].AllocatedTokens, 1.0),
		ObservedTokPerSec: 1.0,
		Completed:         true,
	}, now+100)
	if err != nil {
		t.Fatalf("SettleLease: %v", err)
	}
	if rcpt.Status != LeaseSlashedSLABreach {
		t.Fatalf("want LeaseSlashedSLABreach, got %s", rcpt.Status)
	}
	if rcpt.SlashedBondMicroUSD != 50_000 || rcpt.BondReturnedMicroUSD != 50_000 {
		t.Fatalf("expected 50%% SLA breach bond slash (50000/50000), got slashed=%d returned=%d", rcpt.SlashedBondMicroUSD, rcpt.BondReturnedMicroUSD)
	}
	tel, ok := m.Telemetry("prov-sla")
	if !ok || tel.CompletedJobs != 0 || tel.FailedJobs != 1 || tel.SlashedJobs != 1 || tel.ReliabilityBPS >= 9000 {
		t.Fatalf("SLA breach should demote reliability and not increment CompletedJobs: %+v", tel)
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift: %v", err)
	}
}

// Regression test for Findings 10, 11, and 12:
// - CompletionRatePct is calculated strictly from deliveries, not auction clears.
// - Failover replacement executions apply the same provider failure model.
// - Simulated generation durations are converted to milliseconds consistently.
func TestSimLatencyUnitsCompletionAndFailoverFailureModel(t *testing.T) {
	// Finding 12: 1,000,000 tokens at 125 tok/s with 8-way parallelism = 1,000 s = 1,000,000 ms (+ 85 ms TTFT).
	gotMs := computeGenerationLatencyMs(85.0, 1_000_000, 125.0)
	if math.Abs(gotMs-1_000_085.0) > 1e-6 {
		t.Fatalf("computeGenerationLatencyMs(85, 1M, 125) = %.3f ms, want 1000085.000 ms", gotMs)
	}

	// Finding 10 & 11 surgical check: Primary provider crashes (crashProb=1.0),
	// and the ONLY failover replacement provider is adversarial (bait_and_switch).
	// Replacement MUST fail & be slashed (NOT unconditionally succeed!), yielding
	// FailoversTriggered==1, FailoversRecovered==0, OrdersCleared==1, OrdersCompleted==0.
	kpCrash, _ := DeriveKeyPair("reg11-crash", 1111, 1)
	kpAdv, _ := DeriveKeyPair("reg11-adv", 1111, 2)
	hw := GPUHardwareSpec{GPUModel: "H100-SXM5-80GB", GPUCount: 1, AvailableVRAMGB: 80, TotalVRAMGB: 80, Interconnect: "NVLink", FP8TFLOPS: 1979}
	loc := LocationSpec{Region: "eu-central", Jurisdiction: "DE", DatacenterTier: 3}
	providers := []*simProvider{
		{
			id: "prov-crash", operatorClass: "independent", kp: kpCrash, hardware: hw, location: loc,
			models: []string{"llama-3.3-70b"}, basePrices: map[string]int64{"llama-3.3-70b": 200_000},
			baseTTFTMs: 80, baseTokPerSec: 120, slots: 2, crashProb: 1.0,
		},
		{
			id: "prov-adv-replacement", operatorClass: "independent", kp: kpAdv, hardware: hw, location: loc,
			models: []string{"llama-3.3-70b"}, basePrices: map[string]int64{"llama-3.3-70b": 500_000},
			baseTTFTMs: 95, baseTokPerSec: 110, slots: 2, adversarial: true, advRole: "bait_and_switch",
		},
	}
	orders := []simOrderSpec{
		{
			epoch: 0, indexInEpoch: 0, model: "llama-3.3-70b", requestedTokens: 500_000,
			minVRAMGB: 80, maxPriceMicroUSD: 800_000, fixedBasePrice: 1_200_000, burstMultiplier: 1.0,
		},
	}
	simCfg := SimConfig{Seed: 1111, NumProviders: 2, Epochs: 1, BaseOrdersPerStep: 1, BaseTimeUnix: 1760000000}
	m, _, err := simulateSpotRegimeWithProviders(simCfg, providers, orders, regimeOpts{
		name: "adversarial_failover_check", verifySignatures: true, enforceSlashing: true,
		hanzoKillSwitch: true, demandScale: 1.0, offerTTLSeconds: 3600, bidTTLSeconds: 600,
	}, 1.20)
	if err != nil {
		t.Fatalf("simulateSpotRegimeWithProviders: %v", err)
	}
	if m.OrdersCleared != 1 || m.OrdersCompleted != 0 || m.ClearRatePct != 100.0 || m.CompletionRatePct != 0.0 {
		t.Fatalf("expected 1 cleared / 0 completed order, got cleared=%d completed=%d clearPct=%.2f compPct=%.2f",
			m.OrdersCleared, m.OrdersCompleted, m.ClearRatePct, m.CompletionRatePct)
	}
	if m.FailoversTriggered != 1 || m.FailoversRecovered != 0 {
		t.Fatalf("adversarial failover replacement must NOT succeed unconditionally: triggered=%d recovered=%d",
			m.FailoversTriggered, m.FailoversRecovered)
	}
	if m.UsefulTokens >= m.RequestedTokens {
		t.Fatalf("adversarial failover replacement must not deliver remaining tokens: useful=%d requested=%d",
			m.UsefulTokens, m.RequestedTokens)
	}
	if m.LedgerDriftMicroUSD != 0 {
		t.Fatalf("ledger drift=%d, want 0", m.LedgerDriftMicroUSD)
	}

	// Compact multi-regime check for unverified delivery-based completion & ms latencies.
	cfg := SimConfig{Seed: 1350, NumProviders: 40, Epochs: 6, BaseOrdersPerStep: 8, BaseTimeUnix: 1760000000}
	rep, err := RunBenchmarkSuite(cfg)
	if err != nil {
		t.Fatalf("RunBenchmarkSuite: %v", err)
	}
	unverified := rep.NegativeRegimes[0]
	if unverified.OrdersCompleted >= unverified.OrdersCleared {
		t.Fatalf("unverified negative control should have OrdersCompleted (%d) < OrdersCleared (%d) when adversarial leases deliver zero tokens",
			unverified.OrdersCompleted, unverified.OrdersCleared)
	}
	if unverified.CompletionRatePct >= unverified.ClearRatePct {
		t.Fatalf("unverified CompletionRatePct (%.2f%%) must be < ClearRatePct (%.2f%%)",
			unverified.CompletionRatePct, unverified.ClearRatePct)
	}
	if rep.FixedBaseline.LatencyP50Ms < 100_000 || rep.PrimarySpot.LatencyP50Ms < 100_000 {
		t.Fatalf("reported latency p50 not converted to milliseconds: baseline=%.2fms spot=%.2fms",
			rep.FixedBaseline.LatencyP50Ms, rep.PrimarySpot.LatencyP50Ms)
	}
}

func TestHTTPRejectsBuyerUnilateralSlashing(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kpProv, err := DeriveKeyPair("r2-slash-prov", 201, 1)
	if err != nil {
		t.Fatal(err)
	}
	kpExp, err := DeriveKeyPair("r2-slash-exp", 201, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DepositBuyer("org-buyer", 50_000_000); err != nil {
		t.Fatal(err)
	}
	if err := m.DepositProviderWithKey("prov-honest", kpProv.PubKeyHex, 50_000_000); err != nil {
		t.Fatal(err)
	}

	so := testOffer(t, kpProv, "off-slash-guard", "prov-honest", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(so, now); err != nil {
		t.Fatal(err)
	}

	makeActiveLease := func(orderID, bidID string, nonce uint64) string {
		t.Helper()
		ord := Order{
			OrderID:                orderID,
			BuyerID:                "org-buyer",
			Model:                  "llama-3.3-70b",
			RequestedTokens:        500_000,
			MinVRAMGB:              80,
			MaxMicroUSDPer1MTokens: 600_000,
			CreatedAtUnix:          now,
			ExpiresAtUnix:          now + 600,
		}
		if err := m.SubmitOrder(ord, now); err != nil {
			t.Fatal(err)
		}
		sb, err := SignBid(kpProv.PrivateKey, Bid{
			ProtocolVersion:        ProtocolVersion,
			BidID:                  bidID,
			OrderID:                orderID,
			OfferID:                so.Offer.OfferID,
			ProviderID:             "prov-honest",
			ProviderPubKeyHex:      kpProv.PubKeyHex,
			BidMicroUSDPer1MTokens: 400_000,
			OfferedTokens:          500_000,
			CommittedVRAMGB:        80,
			BondMicroUSD:           200_000,
			IssuedAtUnix:           now,
			ExpiresAtUnix:          now + 600,
			Nonce:                  nonce,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.SubmitBid(sb, now); err != nil {
			t.Fatal(err)
		}
		leases, _, err := m.ClearOrder(orderID, now)
		if err != nil || len(leases) != 1 {
			t.Fatalf("ClearOrder(%s): leases=%d err=%v", orderID, len(leases), err)
		}
		return leases[0].LeaseID
	}

	leaseZip := makeActiveLease("ord-slash-zip", "bid-slash-zip", 101)
	leaseStd := makeActiveLease("ord-slash-std", "bid-slash-std", 102)

	srv, err := NewServer(m, kpExp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.Now = func() int64 { return now }

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, luxlog.New("test"), srv); err != nil {
		t.Fatal(err)
	}

	doZip := func(path string, body map[string]any, user, org string, admin bool) (int, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req.Header.Set("X-User-Id", user)
		}
		if org != "" {
			req.Header.Set("X-Org-Id", org)
		}
		if admin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("app.Test %s: %v", path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	// Buyer cannot unilaterally slash via BaitAndSwitchPriceAttempted, non-delivery,
	// partial delivery, inflated reported rate, or SLA breach on ZIP surface.
	hostileReports := []map[string]any{
		{"deliveredTokens": 500_000, "observedTtftMs": 80.0, "observedTokPerSec": 120.0, "completed": true, "baitAndSwitchPriceAttempted": true},
		{"deliveredTokens": 500_000, "observedTtftMs": 80.0, "observedTokPerSec": 120.0, "completed": true, "reportedMicroUsdPer1MTokens": 999_999},
		{"deliveredTokens": 0, "completed": false},
		{"deliveredTokens": 250_000, "observedTtftMs": 80.0, "observedTokPerSec": 120.0, "completed": true},
		{"deliveredTokens": 500_000, "observedTtftMs": 5000.0, "observedTokPerSec": 120.0, "completed": true},
		{"deliveredTokens": 500_000, "observedTtftMs": 80.0, "observedTokPerSec": 1.0, "completed": true},
	}
	for i, rep := range hostileReports {
		if code, b := doZip("/v1/spotmarket/leases/"+leaseZip+"/settle", rep, "buyer-user", "org-buyer", false); code != http.StatusForbidden {
			t.Fatalf("case %d: buyer unilateral slashing on zip: want 403, got %d (%s)", i, code, string(b))
		}
	}

	// Standalone net/http surface also blocks buyer unilateral slashing.
	srvStd, err := NewServer(m, kpExp.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	srvStd.Now = func() int64 { return now }
	srvStd.Acting = func(_ context.Context) (string, error) { return "org-buyer", nil }
	h := srvStd.Handler()
	rawBait, _ := json.Marshal(hostileReports[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/spotmarket/leases/"+leaseStd+"/settle", bytes.NewReader(rawBait)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("buyer unilateral slashing on net/http: want 403, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Independently authorized settlement reporter (admin) CAN apply verified slashing.
	if code, b := doZip("/v1/spotmarket/leases/"+leaseZip+"/settle", hostileReports[0], "settlement-verifier", "verifier-org", true); code != http.StatusOK {
		t.Fatalf("independent settlement reporter slashing: want 200, got %d (%s)", code, string(b))
	}

	// Buyer CAN still confirm honest full delivery on leaseStd (zero slashing).
	honestRaw, _ := json.Marshal(map[string]any{
		"deliveredTokens":   500_000,
		"observedTtftMs":    85.0,
		"observedLatencyMs": computeGenerationLatencyMs(85.0, 500_000, 125.0),
		"observedTokPerSec": 125.0,
		"completed":         true,
	})
	recHonest := httptest.NewRecorder()
	h.ServeHTTP(recHonest, httptest.NewRequest(http.MethodPost, "/v1/spotmarket/leases/"+leaseStd+"/settle", bytes.NewReader(honestRaw)))
	if recHonest.Code != http.StatusOK {
		t.Fatalf("buyer honest full-delivery settle: want 200, got %d (%s)", recHonest.Code, recHonest.Body.String())
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift: %v", err)
	}
}

func TestExportBundleRejectsRekeyedTelemetry(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kpHonest, err := DeriveKeyPair("r2-tel-honest", 202, 1)
	if err != nil {
		t.Fatal(err)
	}
	kpAdv, err := DeriveKeyPair("r2-tel-adv", 202, 2)
	if err != nil {
		t.Fatal(err)
	}
	kpExp, err := DeriveKeyPair("r2-tel-exp", 202, 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.DepositProviderWithKey("prov-a-honest", kpHonest.PubKeyHex, 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := m.DepositProviderWithKey("prov-b-adv", kpAdv.PubKeyHex, 1_000_000); err != nil {
		t.Fatal(err)
	}

	soHonest := testOffer(t, kpHonest, "off-honest", "prov-a-honest", "independent", 400_000, now, 1)
	soAdv := testOffer(t, kpAdv, "off-adv", "prov-b-adv", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(soHonest, now); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterOffer(soAdv, now); err != nil {
		t.Fatal(err)
	}

	bundle, err := m.ExportBundle(kpExp.PrivateKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyExportBundle(bundle); err != nil {
		t.Fatalf("clean bundle failed verify: %v", err)
	}

	// Re-key telemetry map while preserving sorted key order (a < b):
	// "prov-a-honest" -> "prov-a-rekeyed", "prov-b-adv" -> "prov-z-rekeyed"
	// leaves value order identical, testing that gossipStateRoot commits map keys!
	rekeyed := bundle
	rekeyed.Gossip.Telemetry = map[string]ProviderTelemetry{
		"prov-a-rekeyed": bundle.Gossip.Telemetry["prov-a-honest"],
		"prov-z-rekeyed": bundle.Gossip.Telemetry["prov-b-adv"],
	}
	if err := VerifyExportBundle(rekeyed); !errors.Is(err, ErrInvalidExportBundle) {
		t.Fatalf("expected VerifyExportBundle to reject re-keyed telemetry, got %v", err)
	}
	if _, err := ImportBundle(rekeyed, DefaultConfig(), now); !errors.Is(err, ErrInvalidExportBundle) {
		t.Fatalf("expected ImportBundle to reject re-keyed telemetry, got %v", err)
	}
}

func TestClearOrderRejectsExpiredOrderAndReleasesFunds(t *testing.T) {
	m := NewMarket(DefaultConfig())
	kp, err := DeriveKeyPair("r2-exp-prov", 203, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DepositBuyer("buyer-exp", 10_000_000); err != nil {
		t.Fatal(err)
	}
	if err := m.DepositProviderWithKey("prov-exp", kp.PubKeyHex, 10_000_000); err != nil {
		t.Fatal(err)
	}

	so := testOffer(t, kp, "off-exp", "prov-exp", "independent", 400_000, 100, 1)
	if err := m.RegisterOffer(so, 100); err != nil {
		t.Fatal(err)
	}

	ord := Order{
		OrderID:                "ord-exp-1",
		BuyerID:                "buyer-exp",
		Model:                  "llama-3.3-70b",
		RequestedTokens:        1_000_000,
		MinVRAMGB:              80,
		MaxMicroUSDPer1MTokens: 600_000,
		CreatedAtUnix:          100,
		ExpiresAtUnix:          110,
	}
	if err := m.SubmitOrder(ord, 100); err != nil {
		t.Fatal(err)
	}
	sb, err := SignBid(kp.PrivateKey, Bid{
		ProtocolVersion:        ProtocolVersion,
		BidID:                  "bid-exp-1",
		OrderID:                ord.OrderID,
		OfferID:                so.Offer.OfferID,
		ProviderID:             "prov-exp",
		ProviderPubKeyHex:      kp.PubKeyHex,
		BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens:          1_000_000,
		CommittedVRAMGB:        80,
		BondMicroUSD:           200_000,
		IssuedAtUnix:           100,
		ExpiresAtUnix:          1000,
		Nonce:                  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitBid(sb, 100); err != nil {
		t.Fatal(err)
	}

	// ClearOrder at t=111 (after order expiry at 110, while offer/bid expire at 1000)
	// WITHOUT calling ExpireStale must reject the order and refund escrow + unlock bond.
	leases, _, err := m.ClearOrder(ord.OrderID, 111)
	if !errors.Is(err, ErrExpiredOrder) {
		t.Fatalf("expected ErrExpiredOrder on expired order clear, got leases=%d err=%v", len(leases), err)
	}
	if len(leases) != 0 {
		t.Fatalf("expected 0 leases awarded for expired order, got %d", len(leases))
	}
	snap := m.LedgerSnapshot()
	if snap.BuyerEscrow["buyer-exp"] != 0 || snap.BuyerBalances["buyer-exp"] != 10_000_000 {
		t.Fatalf("buyer escrow not refunded cleanly: escrow=%d bal=%d", snap.BuyerEscrow["buyer-exp"], snap.BuyerBalances["buyer-exp"])
	}
	if snap.ProviderLockedBonds["prov-exp"] != 0 || snap.ProviderBalances["prov-exp"] != 10_000_000 {
		t.Fatalf("provider bond not unlocked cleanly: locked=%d bal=%d", snap.ProviderLockedBonds["prov-exp"], snap.ProviderBalances["prov-exp"])
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift after expired order clear: %v", err)
	}
	// Calling ExpireStale afterward must not double-refund.
	if n := m.ExpireStale(112); n != 0 {
		t.Fatalf("expected 0 additional expiries, got %d", n)
	}
	if err := m.CheckConservation(); err != nil {
		t.Fatalf("conservation drift after follow-up ExpireStale: %v", err)
	}
}

