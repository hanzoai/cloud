// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func testOffer(t *testing.T, kp *KeyPair, offerID, provID, opClass string, priceMicro int64, now int64, nonce uint64) SignedOffer {
	t.Helper()
	so, err := SignOffer(kp.PrivateKey, Offer{
		ProtocolVersion:   ProtocolVersion,
		OfferID:           offerID,
		ProviderID:        provID,
		ProviderPubKeyHex: kp.PubKeyHex,
		OperatorClass:     opClass,
		Hardware: GPUHardwareSpec{
			GPUModel:        "H100-SXM5-80GB",
			GPUCount:        1,
			AvailableVRAMGB: 80,
			TotalVRAMGB:     80,
			Interconnect:    "NVLink",
			FP8TFLOPS:       1979,
		},
		SupportedModels: []string{"llama-3.3-70b", "qwen-2.5-32b"},
		Location: LocationSpec{
			Region:         "eu-central",
			Jurisdiction:   "DE",
			DatacenterTier: 3,
		},
		Pricing: PricingSpec{
			MicroUSDPer1MTokens: priceMicro,
			MicroUSDPerGPUHour:  priceMicro * 4,
			MinBillableTokens:   1000,
		},
		SLO: ServiceCommitment{
			TargetTTFTMs:           90,
			MinThroughputTokPerSec: 120,
			UptimeBPS:              9950,
			CollateralMicroUSD:     500_000,
			SlashBPSOnBreach:       5000,
		},
		AvailableSlots:   4,
		IssuedAtUnix:     now,
		ExpiresAtUnix:    now + 3600,
		Nonce:            nonce,
		AttestationClass: VerificationCryptoSignature,
	})
	if err != nil {
		t.Fatalf("SignOffer: %v", err)
	}
	return so
}

func TestCryptoLowSAndMalleationRejection(t *testing.T) {
	kp, err := DeriveKeyPair("test-crypto", 42, 1)
	if err != nil {
		t.Fatalf("DeriveKeyPair: %v", err)
	}
	now := int64(1760000000)
	so := testOffer(t, kp, "offer-1", "prov-1", "independent", 450_000, now, 1)

	if err := VerifySignedOffer(so); err != nil {
		t.Fatalf("valid signed offer rejected: %v", err)
	}

	// Tampered pricing must fail commitment check
	tampered := so
	tampered.Offer.Pricing.MicroUSDPer1MTokens = 100
	if err := VerifySignedOffer(tampered); !errors.Is(err, ErrCommitmentMismatch) {
		t.Fatalf("tampered offer: want ErrCommitmentMismatch, got %v", err)
	}

	// Forged signature (signed with different key over same digest) must fail closed
	attackerKP, err := DeriveKeyPair("attacker", 42, 2)
	if err != nil {
		t.Fatalf("DeriveKeyPair attacker: %v", err)
	}
	forged := so
	forgedSig, err := SignDigestLowS(attackerKP.PrivateKey, OfferCommitment(so.Offer))
	if err != nil {
		t.Fatalf("SignDigestLowS: %v", err)
	}
	forged.SignatureHex = bytesToHex(forgedSig[:])
	if err := VerifySignedOffer(forged); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("forged offer: want ErrInvalidSignature, got %v", err)
	}

	// EIP-2 high-S malleated twin of a genuine valid signature MUST be rejected fail-closed
	highS, err := MalleateSignatureHighS(so.SignatureHex)
	if err != nil {
		t.Fatalf("MalleateSignatureHighS: %v", err)
	}
	malleated := so
	malleated.SignatureHex = highS
	if err := VerifySignedOffer(malleated); !errors.Is(err, ErrHighSMalleableSignature) {
		t.Fatalf("malleated high-S offer: want ErrHighSMalleableSignature, got %v", err)
	}
}

func TestEnsoZeroHanzoPreferenceParity(t *testing.T) {
	if !verifyZeroHanzoParity(1350) {
		t.Fatal("Enso zero-Hanzo-preference parity check failed")
	}

	// Also test asymmetric case where independent provider is 1 microdollar cheaper:
	// independent MUST strictly win over hanzo regardless of ordering.
	kpH, _ := DeriveKeyPair("parity-h", 77, 1)
	kpI, _ := DeriveKeyPair("parity-i", 77, 2)
	now := int64(1760000000)

	offH := testOffer(t, kpH, "offer-hanzo", "prov-hanzo", "hanzo", 500_000, now, 1)
	offI := testOffer(t, kpI, "offer-indep", "prov-indep", "independent", 499_999, now, 1)

	bidH, _ := SignBid(kpH.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-a-hanzo", OrderID: "ord-1", OfferID: offH.Offer.OfferID,
		ProviderID: "prov-hanzo", ProviderPubKeyHex: kpH.PubKeyHex, BidMicroUSDPer1MTokens: 500_000,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	bidI, _ := SignBid(kpI.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-z-indep", OrderID: "ord-1", OfferID: offI.Offer.OfferID,
		ProviderID: "prov-indep", ProviderPubKeyHex: kpI.PubKeyHex, BidMicroUSDPer1MTokens: 499_999,
		OfferedTokens: 500_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})

	ord := Order{
		OrderID: "ord-1", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 500_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 800_000,
		CreatedAtUnix: now, ExpiresAtUnix: now + 600, RankingWeights: DefaultRankingWeights(),
	}
	tel := ProviderTelemetry{MeasuredTTFTMs: 95, ReliabilityBPS: 9800, ObservedSampleCount: 20}
	ranked, rej := RankCandidates(ord, []Candidate{
		{SignedOffer: offH, SignedBid: bidH, Telemetry: tel, FreeSlots: 4},
		{SignedOffer: offI, SignedBid: bidI, Telemetry: tel, FreeSlots: 4},
	}, now, true)
	if len(rej) != 0 || len(ranked) != 2 {
		t.Fatalf("unexpected ranking result: ranked=%d rej=%v", len(ranked), rej)
	}
	if ranked[0].Candidate.SignedBid.Bid.ProviderID != "prov-indep" {
		t.Fatalf("expected cheaper independent provider to win over hanzo, got %s", ranked[0].Candidate.SignedBid.Bid.ProviderID)
	}
}

func TestReverseAuctionPartialFillSlashingFailoverAndConservation(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	if err := m.DepositBuyer("buyer-1", 10_000_000); err != nil {
		t.Fatal(err)
	}

	kp1, _ := DeriveKeyPair("p", 99, 1)
	kp2, _ := DeriveKeyPair("p", 99, 2)
	kp3, _ := DeriveKeyPair("p", 99, 3)
	for _, entry := range []struct {
		id string
		kp *KeyPair
	}{
		{"prov-1", kp1},
		{"prov-2", kp2},
		{"prov-3", kp3},
	} {
		if err := m.DepositProviderWithKey(entry.id, entry.kp.PubKeyHex, 2_000_000); err != nil {
			t.Fatal(err)
		}
	}

	off1 := testOffer(t, kp1, "offer-1", "prov-1", "independent", 400_000, now, 1)
	off2 := testOffer(t, kp2, "offer-2", "prov-2", "community", 450_000, now, 1)
	off3 := testOffer(t, kp3, "offer-3", "prov-3", "independent", 500_000, now, 1)
	for _, o := range []SignedOffer{off1, off2, off3} {
		if err := m.RegisterOffer(o, now); err != nil {
			t.Fatalf("RegisterOffer %s: %v", o.Offer.OfferID, err)
		}
	}

	// Order requests 1,000,000 tokens with partial fill enabled
	ord := Order{
		OrderID: "ord-partial", BuyerID: "buyer-1", Model: "llama-3.3-70b",
		RequestedTokens: 1_000_000, MinVRAMGB: 80, MaxMicroUSDPer1MTokens: 700_000,
		AllowedJurisdictions: []string{"DE"}, AllowPartialFill: true,
		EscrowMicroUSD: 700_000, CreatedAtUnix: now, ExpiresAtUnix: now + 600,
		RankingWeights: DefaultRankingWeights(),
	}
	if err := m.SubmitOrder(ord, now); err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}

	// prov-1 bids 600k tokens @ 400k, prov-2 bids 600k tokens @ 450k -> Enso splits 600k + 400k!
	bid1, _ := SignBid(kp1.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-1", OrderID: ord.OrderID, OfferID: "offer-1",
		ProviderID: "prov-1", ProviderPubKeyHex: kp1.PubKeyHex, BidMicroUSDPer1MTokens: 400_000,
		OfferedTokens: 600_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	bid2, _ := SignBid(kp2.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-2", OrderID: ord.OrderID, OfferID: "offer-2",
		ProviderID: "prov-2", ProviderPubKeyHex: kp2.PubKeyHex, BidMicroUSDPer1MTokens: 450_000,
		OfferedTokens: 600_000, CommittedVRAMGB: 80, BondMicroUSD: 120_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 1,
	})
	if err := m.SubmitBid(bid1, now); err != nil {
		t.Fatalf("SubmitBid 1: %v", err)
	}
	if err := m.SubmitBid(bid2, now); err != nil {
		t.Fatalf("SubmitBid 2: %v", err)
	}

	// Replay nonce and Sybil duplicate bid must both fail closed
	if err := m.SubmitBid(bid1, now); err == nil {
		t.Fatal("duplicate bid1 should fail")
	}
	sybilBid, _ := SignBid(kp1.PrivateKey, Bid{
		ProtocolVersion: ProtocolVersion, BidID: "bid-1-sybil", OrderID: ord.OrderID, OfferID: "offer-1",
		ProviderID: "prov-1", ProviderPubKeyHex: kp1.PubKeyHex, BidMicroUSDPer1MTokens: 300_000,
		OfferedTokens: 600_000, CommittedVRAMGB: 80, BondMicroUSD: 100_000, IssuedAtUnix: now, ExpiresAtUnix: now + 600, Nonce: 2,
	})
	if err := m.SubmitBid(sybilBid, now); !errors.Is(err, ErrSybilBidLimit) {
		t.Fatalf("Sybil bid: want ErrSybilBidLimit, got %v", err)
	}

	leases, _, err := m.ClearOrder(ord.OrderID, now)
	if err != nil {
		t.Fatalf("ClearOrder: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("expected 2 partial-fill leases, got %d", len(leases))
	}
	if leases[0].AllocatedTokens+leases[1].AllocatedTokens != 1_000_000 {
		t.Fatalf("partial fill sum mismatch: %d + %d", leases[0].AllocatedTokens, leases[1].AllocatedTokens)
	}

	// Lease 0 completes honestly within promised SLA
	rcpt0, err := m.SettleLease(leases[0].LeaseID, DeliveryReport{
		LeaseID: leases[0].LeaseID, DeliveredTokens: leases[0].AllocatedTokens,
		ObservedTTFTMs: 85, ObservedLatencyMs: computeGenerationLatencyMs(85, leases[0].AllocatedTokens, 122), ObservedTokPerSec: 122, Completed: true,
	}, now+10)
	if err != nil {
		t.Fatalf("SettleLease 0: %v", err)
	}
	if rcpt0.Status != LeaseCompleted || rcpt0.SlashedBondMicroUSD != 0 {
		t.Fatalf("unexpected honest receipt: %+v", rcpt0)
	}

	// Lease 1 attempts bait-and-switch price inflation -> 0 payout, full refund, 100% bond slashed!
	rcpt1, err := m.SettleLease(leases[1].LeaseID, DeliveryReport{
		LeaseID: leases[1].LeaseID, DeliveredTokens: 0,
		ReportedMicroUSDPer1MTokens: 1_500_000, BaitAndSwitchPriceAttempted: true,
	}, now+12)
	if err != nil {
		t.Fatalf("SettleLease 1: %v", err)
	}
	if rcpt1.Status != LeaseSlashedBaitSwitch || rcpt1.ProviderPayoutMicroUSD != 0 || rcpt1.SlashedBondMicroUSD != leases[1].ProviderBondMicroUSD {
		t.Fatalf("expected full bait-and-switch slash, got %+v", rcpt1)
	}

	// Failover lease 1 onto cheapest proven-healthy alternative (prov-1, whose slot freed after honest completion)
	foLease, err := m.FailoverLease(leases[1].LeaseID, now+13)
	if err != nil {
		t.Fatalf("FailoverLease: %v", err)
	}
	if foLease.ProviderID == leases[1].ProviderID || foLease.ProviderID != "prov-1" || foLease.AllocatedTokens != leases[1].AllocatedTokens {
		t.Fatalf("unexpected failover lease: %+v", foLease)
	}
	foRcpt, err := m.SettleLease(foLease.LeaseID, DeliveryReport{
		LeaseID: foLease.LeaseID, DeliveredTokens: foLease.AllocatedTokens,
		ObservedTTFTMs: 88, ObservedLatencyMs: computeGenerationLatencyMs(88, foLease.AllocatedTokens, 121), ObservedTokPerSec: 121, Completed: true,
	}, now+20)
	if err != nil {
		t.Fatalf("SettleLease failover: %v", err)
	}
	if foRcpt.Status != LeaseCompleted || foRcpt.SlashedBondMicroUSD != 0 {
		t.Fatalf("unexpected failover receipt: %+v", foRcpt)
	}

	if err := m.CheckConservation(); err != nil {
		t.Fatalf("ledger conservation failed: %v", err)
	}
	if m.HanzoCalls() != 0 {
		t.Fatalf("expected 0 Hanzo calls, got %d", m.HanzoCalls())
	}
}

func TestSovereigntyExportVerifyForkAndTamperDetection(t *testing.T) {
	now := int64(1760000000)
	m := NewMarket(DefaultConfig())
	kpProv, _ := DeriveKeyPair("sov-prov", 55, 1)
	kpExp, _ := DeriveKeyPair("sov-exp", 55, 2)
	_ = m.DepositBuyer("buyer-sov", 5_000_000)
	_ = m.DepositProviderWithKey("prov-sov", kpProv.PubKeyHex, 2_000_000)

	_ = m.RegisterPeer(PeerNode{PeerID: "peer-1", Endpoint: "https://peer1.example", PubKeyHex: kpProv.PubKeyHex, Regions: []string{"eu-central"}, LastSeenUnix: now, OperatorClass: "independent"})
	off := testOffer(t, kpProv, "offer-sov", "prov-sov", "independent", 400_000, now, 1)
	if err := m.RegisterOffer(off, now); err != nil {
		t.Fatal(err)
	}

	bundle, err := m.ExportBundle(kpExp.PrivateKey, now)
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	if err := VerifyExportBundle(bundle); err != nil {
		t.Fatalf("VerifyExportBundle: %v", err)
	}

	// Tampering with ledger balance must fail verification closed
	tampered := bundle
	tampered.Ledger.BuyerBalances = copyInt64Map(bundle.Ledger.BuyerBalances)
	tampered.Ledger.BuyerBalances["buyer-sov"] += 1
	if err := VerifyExportBundle(tampered); err == nil {
		t.Fatal("tampered export bundle must fail verification")
	}

	// Cold-start fork into fresh isolated Market with Hanzo offline
	forked, err := ImportBundle(bundle, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if err := forked.CheckConservation(); err != nil {
		t.Fatalf("forked conservation failed: %v", err)
	}
	if forked.HanzoCalls() != 0 {
		t.Fatalf("forked market made Hanzo calls: %d", forked.HanzoCalls())
	}
	if len(forked.ListOffers(now)) != 1 {
		t.Fatalf("expected 1 imported offer, got %d", len(forked.ListOffers(now)))
	}
}

func TestDualSurfaceHTTPZipAndStdNetHTTP(t *testing.T) {
	now := int64(1760000000)
	kpProv, _ := DeriveKeyPair("http-prov", 88, 1)
	kpExp, _ := DeriveKeyPair("http-exp", 88, 2)

	setupSrv := func() *Server {
		m := NewMarket(DefaultConfig())
		_ = m.DepositBuyer("buyer-http", 5_000_000)
		_ = m.DepositProviderWithKey("prov-http", kpProv.PubKeyHex, 2_000_000)
		srv, err := NewServer(m, kpExp.PrivateKey)
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		srv.Now = func() int64 { return now }
		return srv
	}

	offer := testOffer(t, kpProv, "offer-http-1", "prov-http", "independent", 420_000, now, 1)
	offerJSON, _ := json.Marshal(offer)

	// 1. Test zip.App surface (typed ops + zip.CallerOf principal headers)
	srvZip := setupSrv()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, luxlog.New("test"), srvZip); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	doZip := func(method, path string, body []byte, user, org string, admin bool) (int, []byte) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
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
			t.Fatalf("zip %s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, _ := doZip(http.MethodGet, "/v1/spotmarket/health", nil, "", "", false); code != http.StatusOK {
		t.Fatalf("zip health want 200, got %d", code)
	}
	if code, b := doZip(http.MethodPost, "/v1/spotmarket/offers", offerJSON, "", "", false); code != http.StatusCreated {
		t.Fatalf("zip post offer want 201, got %d: %s", code, string(b))
	}
	if code, b := doZip(http.MethodGet, "/v1/spotmarket/offers?model=llama-3.3-70b&jurisdiction=DE", nil, "", "", false); code != http.StatusOK {
		t.Fatalf("zip get offers want 200, got %d: %s", code, string(b))
	} else {
		var out listOffersOut
		if err := json.Unmarshal(b, &out); err != nil || len(out.Data) != 1 {
			t.Fatalf("zip get offers unexpected payload: %s (err=%v)", string(b), err)
		}
	}
	if code, b := doZip(http.MethodGet, "/v1/spotmarket/export", nil, "admin-user", "admin", true); code != http.StatusOK {
		t.Fatalf("zip export (admin) want 200, got %d: %s", code, string(b))
	} else {
		var bundle MarketExportBundle
		if err := json.Unmarshal(b, &bundle); err != nil || VerifyExportBundle(bundle) != nil {
			t.Fatalf("zip export bundle invalid: %v", err)
		}
	}

	// 2. Test standalone standard-library net/http surface (zero zip dependency)
	srvStd := setupSrv()
	srvStd.Acting = func(_ context.Context) (string, error) { return "buyer-http", nil }
	srvStd.AuthorizeExport = func(_ context.Context) error { return nil }
	h := srvStd.Handler()
	doStd := func(method, path string, body []byte) (int, []byte) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	if code, _ := doStd(http.MethodGet, "/v1/spotmarket/health", nil); code != http.StatusOK {
		t.Fatalf("net/http health want 200, got %d", code)
	}
	if code, b := doStd(http.MethodPost, "/v1/spotmarket/offers", offerJSON); code != http.StatusCreated {
		t.Fatalf("net/http post offer want 201, got %d: %s", code, string(b))
	}
	if code, b := doStd(http.MethodGet, "/v1/spotmarket/offers?model=llama-3.3-70b&jurisdiction=DE", nil); code != http.StatusOK {
		t.Fatalf("net/http get offers want 200, got %d: %s", code, string(b))
	}
	if code, b := doStd(http.MethodGet, "/v1/spotmarket/peers", nil); code != http.StatusOK {
		t.Fatalf("net/http peers want 200, got %d: %s", code, string(b))
	}
	if code, b := doStd(http.MethodGet, "/v1/spotmarket/export", nil); code != http.StatusOK {
		t.Fatalf("net/http export want 200, got %d: %s", code, string(b))
	} else {
		var bundle MarketExportBundle
		if err := json.Unmarshal(b, &bundle); err != nil || VerifyExportBundle(bundle) != nil {
			t.Fatalf("net/http export bundle invalid: %v", err)
		}
	}
}

func Test100ProviderSimulationReproducibilityAndInvariants(t *testing.T) {
	cfg := DefaultSimConfig()
	rep1, err := RunBenchmarkSuite(cfg)
	if err != nil {
		t.Fatalf("RunBenchmarkSuite run 1: %v", err)
	}
	rep2, err := RunBenchmarkSuite(cfg)
	if err != nil {
		t.Fatalf("RunBenchmarkSuite run 2: %v", err)
	}

	// Deterministic parity across repeated runs with identical seed
	if rep1.PrimarySpot.RealizedCostPer1MUsefulUSD != rep2.PrimarySpot.RealizedCostPer1MUsefulUSD ||
		rep1.PrimarySpot.UsefulTokens != rep2.PrimarySpot.UsefulTokens ||
		rep1.PrimarySpot.AdversarialTotalDetected != rep2.PrimarySpot.AdversarialTotalDetected {
		t.Fatalf("simulation is non-deterministic across runs")
	}

	// Acceptance criteria assertions
	p := rep1.PrimarySpot
	if p.ProvidersTotal != 100 {
		t.Fatalf("want 100 providers, got %d", p.ProvidersTotal)
	}
	if p.LedgerDriftMicroUSD != 0 {
		t.Fatalf("primary spot ledger drift = %d, want 0", p.LedgerDriftMicroUSD)
	}
	if p.HanzoCalls != 0 || p.CompletionWithoutHanzoPct != 100.0 {
		t.Fatalf("decentralization failure: hanzoCalls=%d pct=%.2f", p.HanzoCalls, p.CompletionWithoutHanzoPct)
	}
	if !p.ZeroHanzoPreferenceVerified {
		t.Fatal("zero Hanzo preference parity not verified")
	}
	if p.RealizedCostPer1MUsefulUSD >= rep1.FixedBaseline.RealizedCostPer1MUsefulUSD {
		t.Fatalf("expected spot cost (%.4f) < fixed baseline (%.4f)", p.RealizedCostPer1MUsefulUSD, rep1.FixedBaseline.RealizedCostPer1MUsefulUSD)
	}
	if p.AdversarialTotalInjected == 0 || p.AdversarialDetectionPct < 99.9 {
		t.Fatalf("expected ~100%% adversarial detection, got %d/%d (%.2f%%)", p.AdversarialTotalDetected, p.AdversarialTotalInjected, p.AdversarialDetectionPct)
	}
	if p.HardwareAttestationClaimed {
		t.Fatal("must never falsely claim hardware attestation when none is verified")
	}
	if len(rep1.NegativeRegimes) != 3 {
		t.Fatalf("expected 3 honest negative/boundary regimes, got %d", len(rep1.NegativeRegimes))
	}
	for _, neg := range rep1.NegativeRegimes {
		if neg.LedgerDriftMicroUSD != 0 {
			t.Fatalf("negative regime %s ledger drift = %d, want 0", neg.Name, neg.LedgerDriftMicroUSD)
		}
	}
	// Verify negative regimes honestly surface real boundary degradation:
	// 1. Unverified control loses useful tokens, drops delivery-based completion, & fails adversarial detection
	if rep1.NegativeRegimes[0].AdversarialDetectionPct >= 25.0 ||
		rep1.NegativeRegimes[0].TokenFulfillRatePct >= p.TokenFulfillRatePct ||
		rep1.NegativeRegimes[0].CompletionRatePct >= rep1.NegativeRegimes[0].ClearRatePct {
		t.Fatalf("unverified negative control did not degrade as expected: det=%.2f%% fulfill=%.2f%% comp=%.2f%% clear=%.2f%%",
			rep1.NegativeRegimes[0].AdversarialDetectionPct, rep1.NegativeRegimes[0].TokenFulfillRatePct,
			rep1.NegativeRegimes[0].CompletionRatePct, rep1.NegativeRegimes[0].ClearRatePct)
	}
	// 2. Fleet saturation shows price surge and graceful admission rejection
	if rep1.NegativeRegimes[1].RealizedCostPer1MUsefulUSD <= p.RealizedCostPer1MUsefulUSD || rep1.NegativeRegimes[1].AdmissionRejectPct <= 0 {
		t.Fatalf("saturation boundary did not show price surge / admission reject: cost=%.4f reject=%.2f%%",
			rep1.NegativeRegimes[1].RealizedCostPer1MUsefulUSD, rep1.NegativeRegimes[1].AdmissionRejectPct)
	}
	// 3. Ultra-short TTL churn increases signature verification overhead
	if rep1.NegativeRegimes[2].VerificationOverheadMicroUS <= p.VerificationOverheadMicroUS {
		t.Fatalf("TTL churn boundary did not show verification overhead rise: churn=%d primary=%d",
			rep1.NegativeRegimes[2].VerificationOverheadMicroUS, p.VerificationOverheadMicroUS)
	}
	// 4. Latency quantiles must reflect realistic millisecond generation durations (not 1000x too small)
	if rep1.FixedBaseline.LatencyP50Ms < 100_000 || p.LatencyP50Ms < 100_000 {
		t.Fatalf("latency quantiles appear to be in seconds instead of milliseconds: baseline=%.2fms spot=%.2fms",
			rep1.FixedBaseline.LatencyP50Ms, p.LatencyP50Ms)
	}
	if !rep1.Sovereignty.SignatureVerified || !rep1.Sovereignty.PostForkClearOK || rep1.Sovereignty.ForkLedgerDriftMicro != 0 || rep1.Sovereignty.PostForkHanzoCalls != 0 {
		t.Fatalf("sovereignty check failed: %+v", rep1.Sovereignty)
	}
}

func bytesToHex(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}
