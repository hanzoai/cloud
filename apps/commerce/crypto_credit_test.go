// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// crypto_credit_test.go — A CONFIRMED ON-CHAIN DEPOSIT IS CASH THE GATE CAN SPEND.
//
// The crypto rail mints a custody address (planeCryptoMint), and commerce's
// deposit watcher (billing/depositledger) credits a transfer to it once the chain
// has buried it deep enough. In this binary the balance every gate reads is the
// finance ledger commerce reaches through creditledger — the same read
// GET /v1/billing/balance serves (finance.Funds) — so that ledger is what is
// asserted, never commerce's own transaction store.
//
// Everything is real but the two things that would leave the box: the custody
// signer answers one fixed address, and the chain is a value the test advances.
// The watcher runs on commerce's own production intent store.

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/depositledger"
	"github.com/hanzoai/commerce/billing/depositwatch"
	"github.com/hanzoai/commerce/models/types/currency"
	"github.com/hanzoai/commerce/payment/processor"
	"github.com/hanzoai/commerce/payment/routing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

// depositAddr is the address the custody signer mints. Chain logs carry it
// lowercased, so the fake chain reports it that way.
const depositAddr = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"

// baseUSDC is the watch table production arms: USDC on Base.
var baseUSDC = []routing.Account{{
	Processor: string(processor.MPC), ID: "base_usdc", Network: "base", Asset: "usdc",
	Token: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", Endpoint: "http://rpc.invalid", On: true,
}}

// custody is the MPC signer as the mint path asks it: one address for the payer.
type custody struct{ *processor.BaseProcessor }

func newCustody() custody {
	b := processor.NewBaseProcessor(processor.MPC, nil)
	b.SetConfigured(true)
	return custody{b}
}

var errCustody = errors.New("custody fake: not a charge processor")

func (custody) GenerateAddress(context.Context, string, string) (processor.Wallet, error) {
	return processor.Wallet{Address: depositAddr, ID: "wallet-1"}, nil
}
func (custody) SupportedChains() []string { return []string{"base"} }
func (custody) GetBalance(context.Context, string, string) (*processor.Balance, error) {
	return nil, errCustody
}
func (custody) EstimateFee(context.Context, processor.PaymentRequest) (currency.Cents, error) {
	return 0, errCustody
}
func (custody) Charge(context.Context, processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, errCustody
}
func (custody) Refund(context.Context, processor.RefundRequest) (*processor.RefundResult, error) {
	return nil, errCustody
}
func (custody) GetTransaction(context.Context, string) (*processor.Transaction, error) {
	return nil, errCustody
}
func (custody) ValidateWebhook(context.Context, []byte, string) (*processor.WebhookEvent, error) {
	return nil, errCustody
}

// chain is Base as the watcher reads it: a head the test moves and the USDC
// transfers that have landed.
type chain struct {
	mu        sync.Mutex
	head      uint64
	transfers []depositwatch.Transfer
}

func (c *chain) BlockNumber(context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head, nil
}
func (*chain) Decimals(context.Context) (int, error)  { return 6, nil }
func (*chain) Symbol(context.Context) (string, error) { return "USDC", nil }
func (c *chain) TransfersTo(_ context.Context, addrs []string, from, to uint64) ([]depositwatch.Transfer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := map[string]bool{}
	for _, a := range addrs {
		want[strings.ToLower(a)] = true
	}
	var out []depositwatch.Transfer
	for _, t := range c.transfers {
		if t.Block >= from && t.Block <= to && want[strings.ToLower(t.To)] {
			out = append(out, t)
		}
	}
	return out, nil
}

func (c *chain) land(cents int64, block uint64, tx string, event uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transfers = append(c.transfers, depositwatch.Transfer{
		To:         strings.ToLower(depositAddr),
		Units:      new(big.Int).Mul(big.NewInt(cents), big.NewInt(10_000)), // 6 decimals: 1 cent = 10^4 units
		TxHash:     tx,
		EventIndex: event,
		Block:      block,
	})
}

func (c *chain) advance(to uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.head = to
}

// watcher is commerce's deposit watcher over the fake chain and the production
// intent store, installed as the process default the mint gate reads.
func watcher(t *testing.T, ch *chain) *depositledger.Service {
	t.Helper()
	svc, err := depositledger.New(baseUSDC,
		depositledger.WithReader(func(depositwatch.Asset) (depositwatch.Reader, error) { return ch, nil }),
		depositledger.WithInterval(time.Hour))
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}
	return svc
}

// TestConfirmedCryptoDepositIsSpendableCash is the whole rail, minted to credited:
// a payer asks for an address, USDC lands on it, the chain buries it, and the
// money is in the payer's CASH bucket of the ledger the AI gate reads — once,
// however many times the watcher looks again.
func TestConfirmedCryptoDepositIsSpendableCash(t *testing.T) {
	ctx := context.Background()
	emb, err := commercemod.Embed(ctx, commercemod.EmbedConfig{
		DataDir: t.TempDir(), Dev: true,
		// The production wiring (use.go): the host ledger, and the network's books.
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
		t.Fatal("the finance ledger cannot say where a wallet's money came from")
	}

	// USDC is custody's only where the policy's crypto rails say so: by default it is Stripe's.
	// An admin who runs the custody rail names it, and switches the processor on.
	pol, err := commercebilling.ReadRouting(ctx, "")
	if err != nil {
		t.Fatalf("read the routing policy: %v", err)
	}
	pol.CryptoRails = []routing.CryptoRoute{{Asset: "usdc", Processor: string(processor.MPC)}}
	for i := range pol.Processors {
		if pol.Processors[i].Processor == string(processor.MPC) {
			pol.Processors[i].On = true
		}
	}
	if _, fault := commercebilling.WriteRouting(ctx, "", pol, "test", "custody takes usdc"); fault != nil {
		t.Fatalf("name the custody rail: %v", fault)
	}

	prior, priorErr := processor.Global().Registered(processor.MPC)
	processor.Register(newCustody())
	t.Cleanup(func() {
		if priorErr == nil {
			processor.Register(prior)
		} else {
			processor.Global().Unregister(processor.MPC)
		}
	})

	ch := &chain{head: 5000}
	svc := watcher(t, ch)
	prev := depositledger.Default()
	depositledger.SetDefault(svc)
	t.Cleanup(func() { svc.Stop(); depositledger.SetDefault(prev) })
	// Running is what the mint gate asks: an address is handed out only while
	// something is reading the chain for it.
	svc.Start()

	// A wallet-created account: its own org, paying from the org's pool account.
	const org = "wallet-evm-5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"
	const payer = org
	in := cloud.For(ctx, org)

	minted, err := planeCryptoMint(in, &client.CryptoMintIn{Payer: payer, Chain: "base", Token: "usdc", AmountCents: 2500})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if minted.Status != "pending" || minted.DepositAddress != depositAddr || minted.Chain != "base" || minted.Token != "usdc" {
		t.Fatalf("minted %+v; want a pending base/usdc intent at the custody address", minted)
	}
	// What an agent needs to pay it, on the answer itself.
	if minted.AmountCents != 2500 || minted.MinimumCents != 1 || minted.FeeCents != 0 ||
		minted.RequiredConfirmations != 20 || minted.Confirmations != 0 || minted.CreditedCents != 0 {
		t.Fatalf("minted terms %+v; want amount 2500, minimum 1, no fee, 20 confirmations required, none seen, nothing credited", minted)
	}
	if want := "Send only USDC on base to " + depositAddr + "; deposits under $0.01 credit nothing. " +
		"It is credited to your balance as cash once the transfer is 20 blocks deep, and status moves pending → confirming → succeeded."; minted.Instructions != want {
		t.Fatalf("instructions = %q\nwant %q", minted.Instructions, want)
	}
	// From here every pass is the test's own, so each assertion names one pass.
	svc.Stop()

	books := network.SandboxOnly()
	funds := func(subject string, test bool) finance.Funds {
		t.Helper()
		f, err := funder.Funds(ctx, org, subject, test)
		if err != nil {
			t.Fatalf("funds %s/%s (test=%t): %v", org, subject, test, err)
		}
		return f
	}
	pass := func(label string) {
		t.Helper()
		if _, err := svc.SyncOnce(ctx); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	read := func() *client.CryptoDeposit {
		t.Helper()
		d, err := planeCryptoDeposit(in, &client.CryptoDepositIn{Payer: payer, ID: minted.ID})
		if err != nil {
			t.Fatalf("read the deposit back: %v", err)
		}
		return d
	}

	// $25.00 lands one block below the head: seen, not yet deep enough.
	ch.land(2500, 4999, "0xfeedface", 3)
	pass("a shallow sighting")
	if got := read(); got.Status != "confirming" || got.Confirmations != 2 {
		t.Fatalf("after a shallow sighting: status %q, %d confirmations; want confirming, 2", got.Status, got.Confirmations)
	}
	if f := funds(payer, books); f.Total().Sign() != 0 {
		t.Fatalf("a deposit two blocks deep is already spendable: %+v", f)
	}

	// The chain buries it: twenty blocks is Base's depth.
	ch.advance(5018)
	pass("the confirming pass")
	f := funds(payer, books)
	if c := f.Cash.Cents(); c != 2500 {
		t.Fatalf("cash after a confirmed $25.00 deposit = %d cents; want 2500 — the confirmed deposit never reached the ledger the gate reads", c)
	}
	if k := f.Credit.Cents(); k != 0 {
		t.Fatalf("a paid deposit landed as %d cents of minted credit; want 0 — money the customer paid is cash", k)
	}
	if got := read(); got.Status != "succeeded" || got.CreditedCents != 2500 || got.Confirmations != 20 {
		t.Fatalf("after the confirming pass: status %q, credited %d, %d confirmations; want succeeded, 2500, 20",
			got.Status, got.CreditedCents, got.Confirmations)
	}

	// Repeats: the reorg window is re-scanned every pass, a restart re-reads it, and
	// a second replica reads the same blocks. None of them credits again.
	for i := 0; i < 3; i++ {
		pass("a repeat pass")
	}
	if _, err := watcher(t, ch).SyncOnce(ctx); err != nil {
		t.Fatalf("a second replica: %v", err)
	}
	if c := funds(payer, books).Cash.Cents(); c != 2500 {
		t.Fatalf("cash after repeat passes = %d cents; want 2500 — one deposit credited more than once", c)
	}

	// The key is the EVENT: a second transfer in the same transaction is its own
	// money and credits once too.
	ch.land(1000, 4999, "0xfeedface", 4)
	pass("a second event in the same transaction")
	pass("its repeat")
	if c := funds(payer, books).Cash.Cents(); c != 3500 {
		t.Fatalf("cash after a second $10.00 event = %d cents; want 3500", c)
	}

	// The payer the intent named and nobody else: not the other books, not a
	// stranger in the same org, not another org.
	if f := funds(payer, !books); f.Total().Sign() != 0 {
		t.Fatalf("the other books hold %+v; a deposit lands in the books its org transacts in", f)
	}
	if f := funds(org+"/stranger", books); f.Total().Sign() != 0 {
		t.Fatalf("a stranger in the payer's org holds %+v", f)
	}
	other, err := funder.Funds(ctx, "acme", "acme", books)
	if err != nil || other.Total().Sign() != 0 {
		t.Fatalf("another org holds %+v (%v)", other, err)
	}
}
