// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/depositledger"
	"github.com/hanzoai/commerce/billing/depositwatch"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/cryptopaymentintent"
	"github.com/hanzoai/commerce/models/organization"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/subscription"
	"github.com/hanzoai/commerce/models/types/currency"
	"github.com/hanzoai/commerce/payment/processor"
	"github.com/hanzoai/commerce/payment/routing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

type multiCustody struct {
	*processor.BaseProcessor
	seq atomic.Int64
}

func newMultiCustody() *multiCustody {
	b := processor.NewBaseProcessor(processor.MPC, nil)
	b.SetConfigured(true)
	return &multiCustody{BaseProcessor: b}
}

func (m *multiCustody) GenerateAddress(_ context.Context, _ string, _ string) (processor.Wallet, error) {
	n := m.seq.Add(1)
	addr := fmt.Sprintf("0x100000000000000000000000000000000000%04x", n)
	return processor.Wallet{Address: addr, ID: fmt.Sprintf("wallet-%d", n)}, nil
}
func (*multiCustody) SupportedChains() []string { return []string{"base", "ethereum"} }
func (*multiCustody) GetBalance(context.Context, string, string) (*processor.Balance, error) {
	return nil, errCustody
}
func (*multiCustody) EstimateFee(context.Context, processor.PaymentRequest) (currency.Cents, error) {
	return 0, errCustody
}
func (*multiCustody) Charge(context.Context, processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, errCustody
}
func (*multiCustody) Refund(context.Context, processor.RefundRequest) (*processor.RefundResult, error) {
	return nil, errCustody
}
func (*multiCustody) GetTransaction(context.Context, string) (*processor.Transaction, error) {
	return nil, errCustody
}
func (*multiCustody) ValidateWebhook(context.Context, []byte, string) (*processor.WebhookEvent, error) {
	return nil, errCustody
}

func setupHardenedCryptoEnv(t *testing.T, accounts []routing.Account) (finance.Funder, *chain, *depositledger.Service) {
	t.Helper()
	ctx := context.Background()
	emb, err := commercemod.Embed(ctx, commercemod.EmbedConfig{
		DataDir: t.TempDir(), Dev: true,
		Ledger: ledger{}, Born: born(), Ceiling: ceiling(),
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(stop)
	})

	fin := funded(t)
	funder, ok := fin.(finance.Funder)
	if !ok {
		t.Fatal("finance ledger does not implement finance.Funder")
	}

	pol, err := commercebilling.ReadRouting(ctx, "")
	if err != nil {
		t.Fatalf("ReadRouting: %v", err)
	}
	pol.CryptoRails = []routing.CryptoRoute{{Asset: "usdc", Processor: string(processor.MPC)}}
	for i := range pol.Processors {
		if pol.Processors[i].Processor == string(processor.MPC) {
			pol.Processors[i].On = true
		}
	}
	if _, fault := commercebilling.WriteRouting(ctx, "", pol, "test", "native custody"); fault != nil {
		t.Fatalf("WriteRouting: %v", fault)
	}

	prior, priorErr := processor.Global().Registered(processor.MPC)
	processor.Register(newMultiCustody())
	t.Cleanup(func() {
		if priorErr == nil {
			processor.Register(prior)
		} else {
			processor.Global().Unregister(processor.MPC)
		}
	})

	ch := &chain{head: 10000}
	svc, err := depositledger.New(accounts,
		depositledger.WithReader(func(depositwatch.Asset) (depositwatch.Reader, error) { return ch, nil }),
		depositledger.WithInterval(time.Hour))
	if err != nil {
		t.Fatalf("depositledger.New: %v", err)
	}
	prev := depositledger.Default()
	depositledger.SetDefault(svc)
	t.Cleanup(func() { svc.Stop(); depositledger.SetDefault(prev) })
	svc.Start()
	return funder, ch, svc
}

func TestNativeCryptoNeverConflatesStripeAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	funder, _, svc := setupHardenedCryptoEnv(t, []routing.Account{{
		Processor: string(processor.MPC), ID: "base_usdc_fee", Network: "base", Asset: "usdc",
		Token: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Endpoint: "http://rpc.invalid",
		FeeCents: 50, Confirmations: 20, On: true,
	}})
	_ = funder

	// 1. Live capability discovery returns exact contract, decimals, minimumCents, minAmount, fee, confirmations.
	opts, err := planeCryptoOptions(ctx, &cloud.Unit{})
	if err != nil {
		t.Fatalf("planeCryptoOptions: %v", err)
	}
	if len(opts.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %+v", opts.Assets)
	}
	pair := opts.Assets[0]
	if pair.Chain != "base" || pair.Token != "usdc" ||
		pair.Contract != "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913" ||
		pair.Decimals != 6 || pair.FeeCents != 50 || pair.MinimumCents != 51 ||
		pair.MinAmount != "510000" || pair.Confirmations != 20 || pair.AddressTagRequired {
		t.Fatalf("unexpected capability metadata: %+v", pair)
	}

	// 2. Stopping the watcher fails closed (503) even if routing policy names Stripe for crypto!
	svc.Stop()
	pol, _ := commercebilling.ReadRouting(ctx, "")
	pol.CryptoRails = []routing.CryptoRoute{{Asset: "usdc", Processor: string(processor.Stripe)}}
	_, _ = commercebilling.WriteRouting(ctx, "", pol, "test", "stripe crypto enabled")

	if _, err := planeCryptoOptions(ctx, &cloud.Unit{}); err == nil {
		t.Fatal("expected 503 when native watcher is stopped, even when Stripe crypto is in routing policy")
	}
	inCtx := cloud.For(ctx, "wallet-evm-aa11bb22cc33dd44")
	if _, err := planeCryptoMint(inCtx, &client.CryptoMintIn{Payer: "wallet-evm-aa11bb22cc33dd44", Chain: "base", Token: "usdc", AmountCents: 500}); err == nil {
		t.Fatal("expected 503 mint refusal when native watcher is stopped")
	}
}

func TestDepositSettlementIdempotencyReplayAndValidation(t *testing.T) {
	ctx := context.Background()
	funder, _, _ := setupHardenedCryptoEnv(t, []routing.Account{{
		Processor: string(processor.MPC), ID: "base_usdc", Network: "base", Asset: "usdc",
		Token: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Endpoint: "http://rpc.invalid",
		FeeCents: 50, Confirmations: 20, On: true,
	}})

	const orgName = "wallet-evm-1122334455667788"
	inCtx := cloud.For(ctx, orgName)
	books := network.SandboxOnly()

	// Sub-minimum ($0.25 < $0.51 minimum) and negative amounts are rejected 400.
	if _, err := planeCryptoMint(inCtx, &client.CryptoMintIn{Payer: orgName, Chain: "base", Token: "usdc", AmountCents: 25}); err == nil {
		t.Fatal("expected sub-minimum mint rejection")
	}
	if _, err := planeCryptoMint(inCtx, &client.CryptoMintIn{Payer: orgName, Chain: "base", Token: "usdc", AmountCents: -500}); err == nil {
		t.Fatal("expected negative amountCents rejection")
	}

	// Mint $5.00 floor (500 cents) and verify repeat call reuses the same address.
	dep5, err := planeCryptoMint(inCtx, &client.CryptoMintIn{Payer: orgName, Chain: "base", Token: "usdc", AmountCents: 500})
	if err != nil {
		t.Fatalf("mint $5.00: %v", err)
	}
	depReuse, err := planeCryptoMint(inCtx, &client.CryptoMintIn{Payer: orgName, Chain: "base", Token: "usdc", AmountCents: 5000})
	if err != nil {
		t.Fatalf("mint $50.00 reuse: %v", err)
	}
	if depReuse.ID != dep5.ID || depReuse.DepositAddress != dep5.DepositAddress || depReuse.AmountCents != 5000 {
		t.Fatalf("expected open intent reuse on repeat mint: first=%+v second=%+v", dep5, depReuse)
	}
	if depReuse.SettlementState != "pending" {
		t.Fatalf("expected initial settlementState=pending, got %q", depReuse.SettlementState)
	}

	// Foreign payer cannot read this deposit intent (404).
	if _, err := planeCryptoDeposit(inCtx, &client.CryptoDepositIn{Payer: orgName + "/intruder", ID: dep5.ID}); err == nil {
		t.Fatal("foreign payer read must fail 404")
	}

	// Wrong chain, wrong token, wrong contract, and sub-minimum on-chain units are rejected cleanly.
	units50 := big.NewInt(50_000_000) // $50.00 USDC (6 decimals)
	if _, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "polygon", Token: "usdc", Contract: dep5.Contract,
		TxHash: "0xbad1", LogIndex: 0, Block: 10001, Confirmations: 25, Units: units50, Decimals: 6,
	}); !errors.Is(err, ErrDepositAssetMismatch) {
		t.Fatalf("wrong chain: got %v, want ErrDepositAssetMismatch", err)
	}
	if _, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "base", Token: "usdt", Contract: dep5.Contract,
		TxHash: "0xbad2", LogIndex: 0, Block: 10001, Confirmations: 25, Units: units50, Decimals: 6,
	}); !errors.Is(err, ErrDepositAssetMismatch) {
		t.Fatalf("wrong token: got %v, want ErrDepositAssetMismatch", err)
	}
	if _, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "base", Token: "usdc", Contract: "0xdeadbeef00000000000000000000000000000000",
		TxHash: "0xbad3", LogIndex: 0, Block: 10001, Confirmations: 25, Units: units50, Decimals: 6,
	}); !errors.Is(err, ErrDepositAssetMismatch) {
		t.Fatalf("wrong contract: got %v, want ErrDepositAssetMismatch", err)
	}
	if _, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "base", Token: "usdc", Contract: dep5.Contract,
		TxHash: "0xsubmin", LogIndex: 0, Block: 10001, Confirmations: 25, Units: big.NewInt(400_000), Decimals: 6, // $0.40 < $0.50 fee
	}); !errors.Is(err, ErrDepositBelowMinimum) {
		t.Fatalf("sub-minimum units: got %v, want ErrDepositBelowMinimum", err)
	}

	// Shallow confirmation -> pending moves to confirming, 0 cash credited.
	shallow, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "base", Token: "usdc", Contract: dep5.Contract,
		TxHash: "0xAbCdEf0123456789", LogIndex: 2, Block: 9995, Confirmations: 5, Units: units50, Decimals: 6,
	})
	if err != nil || shallow.SettlementState != "confirming" {
		t.Fatalf("shallow sighting: %+v (%v)", shallow, err)
	}
	readConfirming, err := planeCryptoDeposit(inCtx, &client.CryptoDepositIn{Payer: orgName, ID: dep5.ID})
	if err != nil || readConfirming.Status != "confirming" || readConfirming.SettlementState != "confirming" || readConfirming.LogIndex != 2 {
		t.Fatalf("expected confirming state: %+v (%v)", readConfirming, err)
	}

	// Deep confirmation (20 blocks) -> credits $50.00 - $0.50 fee = $49.50 (4950 cents) ONCE.
	settled, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: orgName, Payer: orgName, DepositID: dep5.ID,
		Chain: "base", Token: "usdc", Contract: dep5.Contract,
		TxHash: "0xAbCdEf0123456789", LogIndex: 2, Block: 9995, Confirmations: 20, Units: units50, Decimals: 6,
	})
	if err != nil || settled.CreditedCents != 4950 || settled.SettlementState != "credited" {
		t.Fatalf("settled: %+v (%v); want 4950 cents credited", settled, err)
	}

	// Concurrent/repeated replay of the same (tx_hash, log_index) — including mixed-case hex — MUST reject with ErrDuplicateDepositTx and credit 0 more!
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = RecordConfirmedDeposit(ctx, DepositReceiptIn{
				Org: orgName, Payer: orgName, DepositID: dep5.ID,
				Chain: "base", Token: "usdc", Contract: dep5.Contract,
				TxHash: "0xABCDEF0123456789", LogIndex: 2, Block: 9995, Confirmations: 25, Units: units50, Decimals: 6,
			})
		}(i)
	}
	wg.Wait()
	for i, rerr := range errs {
		if !errors.Is(rerr, ErrDuplicateDepositTx) {
			t.Fatalf("replay[%d] err = %v; want ErrDuplicateDepositTx", i, rerr)
		}
	}

	f, err := funder.Funds(ctx, orgName, orgName, books)
	if err != nil || f.Cash.Cents() != 4950 {
		t.Fatalf("ledger cash after 5 replays = %d (%v); want exact 4950", f.Cash.Cents(), err)
	}

	// Sweep progression: credited -> swept.
	if err := MarkDepositSwept(ctx, orgName, orgName, dep5.ID, "0xsweep999"); err != nil {
		t.Fatalf("MarkDepositSwept: %v", err)
	}
	readSwept, err := planeCryptoDeposit(inCtx, &client.CryptoDepositIn{Payer: orgName, ID: dep5.ID})
	if err != nil || readSwept.Status != "succeeded" || readSwept.SettlementState != "swept" || readSwept.SweptTxHash != "0xsweep999" {
		t.Fatalf("expected swept state: %+v (%v)", readSwept, err)
	}
}

func TestFeeInclusivePlanQuoteAndExpiredIntentAndCollisionHealing(t *testing.T) {
	ctx := context.Background()
	funder, ch, svc := setupHardenedCryptoEnv(t, []routing.Account{{
		Processor: string(processor.MPC), ID: "base_usdc", Network: "base", Asset: "usdc",
		Token: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Endpoint: "http://rpc.invalid",
		FeeCents: 50, Confirmations: 20, On: true,
	}})
	_ = ch

	// Seed a $20/mo plan in the plan authority.
	pl := commerceplan.New(commerceplan.AuthorityDB(ctx))
	pl.Slug = "crypto-pro"
	pl.Name = "Crypto Pro"
	pl.Category = "personal"
	pl.Price = 2000
	pl.Currency = "usd"
	pl.Interval = "month"
	pl.Status = "active"
	if err := pl.Create(); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	const buyerOrg = "wallet-evm-9988776655443322"
	inCtx := cloud.For(ctx, buyerOrg)
	books := network.SandboxOnly()

	// 1. Plan-bound deposit quote MUST include the $0.50 fee (2000 + 50 = 2050 cents)
	// so sending the quoted AmountCents activates the $20.00 plan cleanly!
	planDep, err := planeCryptoMint(inCtx, &client.CryptoMintIn{
		Payer: buyerOrg, Chain: "base", Token: "usdc", PlanID: "crypto-pro",
	})
	if err != nil {
		t.Fatalf("mint plan deposit: %v", err)
	}
	if planDep.AmountCents != 2050 || planDep.FeeCents != 50 || planDep.PlanID != "crypto-pro" {
		t.Fatalf("expected fee-inclusive plan quote 2050 cents (2000 + 50 fee), got %+v", planDep)
	}

	// Settle exact quoted 2050 cents (20,500,000 raw USDC units) -> net 2000 cents opens crypto-pro, 0 excess cash.
	rcpt, err := RecordConfirmedDeposit(ctx, DepositReceiptIn{
		Org: buyerOrg, Payer: buyerOrg, DepositID: planDep.ID,
		Chain: "base", Token: "usdc", Contract: planDep.Contract,
		TxHash: "0xplanpaid01", LogIndex: 0, Block: 9950, Confirmations: 25,
		Units: big.NewInt(20_500_000), Decimals: 6,
	})
	if err != nil || rcpt.SubscriptionID == "" || rcpt.WalletCents != 0 {
		t.Fatalf("plan deposit settlement failed: %+v (%v)", rcpt, err)
	}
	orgObj, _ := orgOf(inCtx, "test")
	db := commercedatastore.New(orgObj.Namespaced(ctx))
	sub := subscription.New(db)
	if err := sub.GetById(rcpt.SubscriptionID); err != nil || sub.Status != subscription.Active || sub.PlanId != "crypto-pro" {
		t.Fatalf("expected active crypto-pro subscription, got %+v (%v)", sub, err)
	}
	f, _ := funder.Funds(ctx, buyerOrg, buyerOrg, books)
	if f.Cash.Cents() != 0 {
		t.Fatalf("expected 0 leftover cash when exact plan+fee paid, got %d", f.Cash.Cents())
	}

	// 2. Unfunded intent past ExpiresAt transitions cleanly to status="expired", settlementState="expired".
	expOrg := "wallet-evm-expired00000001"
	expCtx := cloud.For(ctx, expOrg)
	expDep, err := planeCryptoMint(expCtx, &client.CryptoMintIn{Payer: expOrg, Chain: "base", Token: "usdc", AmountCents: 500})
	if err != nil {
		t.Fatalf("mint expDep: %v", err)
	}
	expOrgObj, _ := orgOf(expCtx, "test")
	expDB := commercedatastore.New(expOrgObj.Namespaced(ctx))
	rawIntent := cryptopaymentintent.New(expDB)
	_ = rawIntent.GetById(expDep.ID)
	rawIntent.ExpiresAt = time.Now().Add(-1 * time.Hour)
	_ = rawIntent.Update()

	readExp, err := planeCryptoDeposit(expCtx, &client.CryptoDepositIn{Payer: expOrg, ID: expDep.ID})
	if err != nil || readExp.Status != "expired" || readExp.SettlementState != "expired" {
		t.Fatalf("expected expired deposit status, got %+v (%v)", readExp, err)
	}

	// 3. Reproduce the exact Nov 26 production Kraken shared-address watcher wedge:
	// two orgs holding kraken: shared address 0x545475d8941197628186ed9355402930a25a3d5c.
	const sharedKrakenAddr = "0x545475d8941197628186ed9355402930a25a3d5c"
	for idx, name := range []string{"wallet-evm-kraken1", "wallet-evm-kraken2"} {
		kCtx := cloud.For(ctx, name)
		kOrg, err := orgOf(kCtx, "test")
		if err != nil {
			t.Fatal(err)
		}
		// Ensure organization row exists in default namespace for depositledger.Watched.
		_ = organization.Query(commercedatastore.New(ctx))
		kDB := commercedatastore.New(kOrg.Namespaced(ctx))
		ki := cryptopaymentintent.New(kDB)
		ki.Amount = 500
		ki.Currency = "usd"
		ki.Chain = cryptopaymentintent.Base
		ki.Token = "usdc"
		ki.DepositAddress = sharedKrakenAddr
		ki.WalletID = "kraken:USDC:Kraken (Polygon - Unified)"
		ki.CustomerRef = name
		ki.Status = cryptopaymentintent.Pending
		ki.ExpiresAt = time.Now().Add(time.Duration(idx-2) * time.Hour)
		ki.Defaults()
		if err := ki.Create(); err != nil {
			t.Fatal(err)
		}
	}
	// Before healing, depositledger SyncOnce wedges on duplicate deposit identity!
	svc.Stop()
	if _, err := svc.SyncOnce(ctx); err == nil || !strings.Contains(err.Error(), "claimed by more than one intent") {
		t.Fatalf("expected unhealed duplicate Kraken address to wedge SyncOnce, got %v", err)
	}
	// Healing retires the unfunded duplicate while keeping the primary intent watched -> SyncOnce succeeds!
	if retired := healExpiredSharedAddresses(ctx, "base", "usdc"); retired != 1 {
		t.Fatalf("expected 1 duplicate Kraken shared-address intent retired, got %d", retired)
	}
	if _, err := svc.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after healExpiredSharedAddresses failed: %v", err)
	}
}

func TestStalledWatcherDetectionAndProcessorCancellationClassification(t *testing.T) {
	ctx := context.Background()
	_, _, _ = setupHardenedCryptoEnv(t, baseUSDC)

	// Classify all Nov 26 Stripe incident classes & failure modes.
	cases := []struct {
		status, cancelReason, errCode, currency, method, want string
	}{
		{"canceled", "abandoned", "", "usd", "crypto", "checkout_abandoned"},
		{"canceled", "", "", "jpy", "card", "checkout_abandoned"},
		{"canceled", "requested_by_customer", "", "usd", "card", "customer_canceled"},
		{"canceled", "fraudulent", "", "usd", "card", "fraud_blocked"},
		{"requires_action", "", "", "usd", "crypto", "awaiting_crypto_checkout"},
		{"requires_payment_method", "", "", "inr", "crypto", "currency_method_unsupported"},
		{"requires_payment_method", "", "authentication_required", "eur", "card", "3ds_authentication_failed"},
		{"requires_payment_method", "", "card_declined", "usd", "card", "card_declined"},
		{"succeeded", "", "", "usd", "card", "succeeded"},
	}
	for _, tc := range cases {
		if got := ClassifyProcessorOutcome(tc.status, tc.cancelReason, tc.errCode, tc.currency, tc.method); got != tc.want {
			t.Errorf("ClassifyProcessorOutcome(%q,%q,%q,%q,%q) = %q; want %q",
				tc.status, tc.cancelReason, tc.errCode, tc.currency, tc.method, got, tc.want)
		}
	}

	// Create a confirming deposit stuck for 2 hours (exceeding 30m SLA) + cursor lagging head by 10,000 blocks.
	stallOrg := "wallet-evm-stalled0001"
	sCtx := cloud.For(ctx, stallOrg)
	dep, err := planeCryptoMint(sCtx, &client.CryptoMintIn{Payer: stallOrg, Chain: "base", Token: "usdc", AmountCents: 500})
	if err != nil {
		t.Fatal(err)
	}
	orgObj, _ := orgOf(sCtx, "test")
	db := commercedatastore.New(orgObj.Namespaced(ctx))
	in := cryptopaymentintent.New(db)
	_ = in.GetById(dep.ID)
	_ = in.MarkConfirming("0xstucktx", 5000)
	_ = in.Update()

	now := time.Now().UTC().Add(2 * time.Hour)
	beforeSnap := RailMetricsSnapshot()
	rep := CheckDepositWatcherHealth(ctx, WatcherHealthConfig{
		Now:            now,
		ConfirmingSLA:  30 * time.Minute,
		MaxLagBlocks:   500,
		HeadBlocks:     map[string]uint64{"base:usdc": 25_000},
		AlertWindowKey: fmt.Sprintf("test-window-%d", time.Now().UnixNano()),
	})
	if rep.Healthy {
		t.Fatalf("expected unhealthy watcher report on cursor lag + SLA-breached confirming deposit: %+v", rep)
	}
	if len(rep.StalledDeposits) == 0 || rep.Assets[0].LagBlocks < 10_000 || !rep.Assets[0].Stalled {
		t.Fatalf("expected stalled deposit & cursor lag reported: %+v", rep)
	}
	afterSnap := RailMetricsSnapshot()
	if afterSnap.StalledAlerts <= beforeSnap.StalledAlerts {
		t.Fatalf("expected stalled watcher alert counter to increment: before=%d after=%d", beforeSnap.StalledAlerts, afterSnap.StalledAlerts)
	}
}
