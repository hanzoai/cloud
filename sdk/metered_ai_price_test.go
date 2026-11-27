package cloud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// pricedAI answers like the plane does: the completion, and the price the model API
// told for it on the call's Bill.
type pricedAI struct {
	recordingAI
	nanos int64
	told  bool
}

func (p *pricedAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	p.chatReq = req
	if p.told {
		infer.BillOf(ctx).Tell(p.nanos)
	}
	return p.resp, nil
}

func (p *pricedAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	p.embedReq = req
	if p.told {
		infer.BillOf(ctx).Tell(p.nanos)
	}
	return [][]float32{{1, 2, 3}}, nil
}

// An in-process call is charged what the model API priced it at — the price the
// same answer costs any caller of api.hanzo.ai — and never a rate of the meter's
// own. A million prompt tokens of enso-ultra list at $5.50; the meter's flat
// $2-a-million charged $2.00 for them, below what the call cost to serve.
func TestAnInProcessCallIsChargedTheModelAPIsPrice(t *testing.T) {
	inner := &pricedAI{
		recordingAI: recordingAI{resp: &infer.ChatResponse{Content: "hi", PromptTokens: 1_000_000, TotalTokens: 1_000_000}},
		nanos:       5_500_000_000, told: true,
	}
	m, fc := meteredOver(t, inner)
	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-ultra", Prompt: "hello", Org: "acme", Payer: account.Org("acme"),
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if got := microsOf(d.In.Amount); got != 5_500_000 {
		t.Fatalf("debited %d micro-USD, want 5500000 — the model API's price for the call", got)
	}
}

// Embeddings are charged the same way: the price told for them, whatever the
// meter's own estimate of the input was.
func TestAnInProcessEmbeddingIsChargedTheModelAPIsPrice(t *testing.T) {
	inner := &pricedAI{nanos: 1_300, told: true}
	m, fc := meteredOver(t, inner)
	if _, err := m.Embed(context.Background(), &infer.EmbedRequest{
		Model: "zen-embedding", Inputs: []string{"a passage of text to embed"}, Org: "acme", Payer: account.Org("acme"),
	}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if got := d.In.Amount.Decimal; got != "0.0000013" {
		t.Fatalf("debited %q, want 0.0000013 — 1300 nano-USD, exactly what was told", got)
	}
}

// A call the transport priced at nothing — a free model — moves no money, and a
// call nobody priced is not charged a guess: there is no second price to fall to.
func TestAnUnpricedOrFreeCallIsChargedNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		told bool
	}{{"told zero", true}, {"told nothing", false}} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &pricedAI{recordingAI: recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 500}}, told: tc.told}
			m, fc := meteredOver(t, inner)
			if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
				Model: "enso-free", Prompt: "hello", Org: "acme", Payer: account.Org("acme"),
			}); err != nil {
				t.Fatalf("chat: %v", err)
			}
			if waitFor(func() bool { return fc.usages() > 0 }, 200*time.Millisecond) {
				d, _ := fc.debits.last()
				if microsOf(d.In.Amount) != 0 {
					t.Fatalf("debited %d micro-USD for a call priced at nothing", microsOf(d.In.Amount))
				}
			}
		})
	}
}

// erringAI tells the call's price and then fails, as a transport does when the
// model API answered and priced a call this process could not read.
type erringAI struct{ pricedAI }

func (e *erringAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	_, _ = e.pricedAI.ChatCompletion(ctx, req)
	return nil, errors.New("cloud: the answer carried no choices")
}

// A call the model API priced is charged even when this process could not use the
// answer: the vendor was paid for it, and the same call costs any other caller the
// same. Left uncharged, a wallet holding one hold's worth buys answers it cannot
// read for ever, a retry each time.
func TestAPricedCallThatFailsHereIsStillCharged(t *testing.T) {
	inner := &erringAI{pricedAI{nanos: 3_000_000, told: true}}
	m, fc := meteredOver(t, inner)
	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-flash", Prompt: "hello", Org: "acme", Payer: account.Org("acme"),
	}); err == nil {
		t.Fatal("the transport's failure was swallowed")
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("a priced call that failed here made %d debits, want 1", fc.usages())
	}
	d, _ := fc.debits.last()
	if got := microsOf(d.In.Amount); got != 3_000 {
		t.Fatalf("debited %d micro-USD, want 3000: what the model API priced it at", got)
	}
}

// A support session is the operator's, not the person's: the gate still weighs the
// person's wallet, as it does at the edge, and nothing is debited from it.
func TestASupportSessionsCallChargesNobody(t *testing.T) {
	inner := &pricedAI{recordingAI: recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 10}}, nanos: 3_000_000, told: true}
	m, fc := meteredOver(t, inner)
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "ann", ActedBy: "operator-sub"})
	if _, err := m.ChatCompletion(ctx, &infer.ChatRequest{
		Model: "enso-flash", Prompt: "hello", Org: "acme", Payer: account.Org("acme"),
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if inner.chatReq == nil {
		t.Fatal("a support session's call never reached the model")
	}
	if waitFor(func() bool { return fc.usages() > 0 }, 200*time.Millisecond) {
		d, _ := fc.debits.last()
		t.Fatalf("a support session debited %q", d.In.Subject)
	}
}

// A list price that could not be read is asked again on the next call rather than
// held as "unpriced" for a minute: a hold of a cent against a call that costs a
// dollar is how a wallet is driven below zero.
func TestAnUnreadablePriceIsAskedAgain(t *testing.T) {
	asked := 0
	p := &priceList{ask: func(context.Context, string) (ModelPrice, bool, error) {
		asked++
		if asked == 1 {
			return ModelPrice{}, false, errors.New("ai_rates: no peer answered")
		}
		return ModelPrice{Input: "5.50", Output: "22"}, true, nil
	}}
	ctx := context.Background()
	if got := p.hold(ctx, "enso-ultra", 1_000_000, 0); got != holdFloorCents {
		t.Fatalf("an unreadable price held %d cents, want the floor", got)
	}
	if got := p.hold(ctx, "enso-ultra", 1_000_000, 0); got != 550 {
		t.Fatalf("the price read on the next call held %d cents, want 550", got)
	}
}

// streamedAI answers like the plane does a streamed completion: deltas as they
// come, and the price the model API told, on the call's Bill, once.
type streamedAI struct {
	pricedAI
	deltas []string
}

func (s *streamedAI) ChatStream(ctx context.Context, req *infer.ChatRequest, emit func(string) error) (*infer.ChatResponse, error) {
	s.chatReq = req
	text := ""
	for _, d := range s.deltas {
		text += d
		if emit(d) != nil {
			break
		}
	}
	if s.told {
		infer.BillOf(ctx).Tell(s.nanos)
	}
	return &infer.ChatResponse{Content: text, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, nil
}

// A streamed call is charged as a buffered one is: once, at the model API's price,
// its deltas reaching the caller as they came. A stream nobody priced is charged
// nothing, never a guess.
func TestAStreamedCallIsChargedTheModelAPIsPriceOnce(t *testing.T) {
	inner := &streamedAI{pricedAI: pricedAI{nanos: 5_500_000_000, told: true}, deltas: []string{"po", "ng"}}
	m, fc := meteredOver(t, inner)
	var said []string
	resp, err := m.ChatStream(context.Background(), &infer.ChatRequest{
		Model: "enso-ultra", Prompt: "ping", Org: "acme", Payer: account.Org("acme"),
	}, func(d string) error { said = append(said, d); return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Content != "pong" || len(said) != 2 {
		t.Fatalf("content %q in %v, want pong in two deltas", resp.Content, said)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	time.Sleep(50 * time.Millisecond)
	if fc.usages() != 1 {
		t.Fatalf("a streamed call made %d debits, want 1", fc.usages())
	}
	d, _ := fc.debits.last()
	if got := microsOf(d.In.Amount); got != 5_500_000 {
		t.Fatalf("debited %d micro-USD, want 5500000 — the model API's price for the call", got)
	}

	unpriced := &streamedAI{deltas: []string{"x"}}
	m2, fc2 := meteredOver(t, unpriced)
	if _, err := m2.ChatStream(context.Background(), &infer.ChatRequest{
		Model: "enso-ultra", Prompt: "ping", Org: "acme", Payer: account.Org("acme"),
	}, func(string) error { return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if fc2.usages() != 0 {
		t.Fatalf("a stream nobody priced made %d debits, want none", fc2.usages())
	}
}

// Over a client that cannot stream, ChatStream is the metered ChatCompletion with
// the whole answer as its one delta: no stream ever goes around the meter.
func TestAStreamOverAClientThatCannotIsTheMeteredCompletion(t *testing.T) {
	inner := &pricedAI{recordingAI: recordingAI{resp: &infer.ChatResponse{Content: "whole", TotalTokens: 3}}, nanos: 700, told: true}
	m, fc := meteredOver(t, inner)
	var said []string
	if _, err := m.ChatStream(context.Background(), &infer.ChatRequest{
		Model: "enso", Prompt: "hi", Org: "acme", Payer: account.Org("acme"),
	}, func(d string) error { said = append(said, d); return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(said) != 1 || said[0] != "whole" {
		t.Fatalf("deltas %v, want the whole answer once", said)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
}
