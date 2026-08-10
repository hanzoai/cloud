package cloud

// What a prepaid gate must guarantee.
//
// A gate reads a SETTLED balance and a completion's cost is unknown until it
// finishes, so two facts have to reach the check that once did not: what the
// completion could cost, and what other in-flight calls have already committed.
// These drive the real meteredAI against a wallet whose balance MOVES — a usage
// POST debits it — because a fixed balance body answers every gate identically
// no matter what was spent, which is why neither gap showed up in the existing
// metering tests.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
)

// wallet is a commerce stand-in that keeps a real balance: a usage POST debits
// it, so a later gate sees what an earlier call actually spent. arrive, when
// non-nil, is closed-over by the balance read to hold every concurrent caller at
// the same instant — the simultaneity the TOCTOU needs to be deterministic
// rather than a race the test wins by luck.
type wallet struct {
	peer planeDebits // the money peer the debit crosses to

	mu      sync.Mutex
	micros  int64 // available, micro-USD (1e6 = $1)
	debited int64 // total debited, micro-USD
	debits  int

	arrive func()
}

// server stands the whole money peer up: the balance READ over HTTP, and the DEBIT over
// the plane — the split the metering client makes. The debit applies to the same running
// balance the read serves, so a later gate sees what an earlier call actually spent.
//
// The debit's amount arrives as an EXACT decimal and is rescaled to micro-USD here. Over
// the old HTTP body it arrived as one of two fields (amountMicros, else amount×10000) and
// the reader had to guess which the sender had filled in.
func (w *wallet) server(t *testing.T) *httptest.Server {
	t.Helper()
	w.peer.serveWith(t, func(_ string, in plane.RecordIn) {
		amt := microsOf(in.Amount)
		w.mu.Lock()
		w.micros -= amt
		w.debited += amt
		w.debits++
		w.mu.Unlock()
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(rw http.ResponseWriter, _ *http.Request) {
		if w.arrive != nil {
			w.arrive()
		}
		w.mu.Lock()
		cents := w.micros / 10000 // the wire speaks whole cents.
		w.mu.Unlock()
		_, _ = rw.Write([]byte(`{"available":` + strconv.FormatInt(cents, 10) + `}`))
	})
	// NO /v1/billing/usage route: a debit that still went over HTTP would 404 here
	// rather than quietly moving the balance.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// settled blocks until n debits have posted (they are fire-and-forget) so the
// assertion reads a finished ledger rather than a racing one.
func (w *wallet) settled(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		got := w.debits
		w.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	w.mu.Lock()
	got := w.debits
	w.mu.Unlock()
	t.Fatalf("only %d/%d debits posted", got, n)
}

func (w *wallet) read() (available, debited int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.micros, w.debited
}

// longAI is a provider that would answer at length but HONORS the MaxTokens it
// is sent, as a real one does — which is the whole reason the meter now resolves
// that ceiling onto the request and the transport forwards it. It reports the
// prompt it actually received so the settled debit is the real cost of the call.
type longAI struct{ completion int }

func (a longAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	out := a.completion
	if req.MaxTokens > 0 && out > req.MaxTokens {
		out = req.MaxTokens
	}
	in := EstTokens(req.Prompt)
	return &types.ChatResponse{Content: "…", PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}, nil
}
func (a longAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

func meterOn(t *testing.T, url string, inner types.AIClient) *meteredAI {
	t.Helper()
	return &meteredAI{
		inner: inner,
		meter: NewResourceMeter(Deps{Metering: mustClient(t, url, false)}, AIMeterProvider),
		rate:  defaultAIPriceUUSDPer1kTokens,
	}
}

// A ONE-CENT org cannot buy two dollars of inference.
//
// Before the reservation the gate priced "hi" at 1 token, allowed it against a
// 1-cent balance, and then settled a million-token completion at $2.00 — the
// wallet ended at -$1.99 and the next gate authorized against money already
// spent. The completion is now part of what is reserved, so the request is
// refused BEFORE any token is spent.
func TestGateReservesTheCompletionNotJustThePrompt(t *testing.T) {
	w := &wallet{micros: 10_000} // exactly 1 cent
	m := meterOn(t, w.server(t).URL, longAI{completion: 1_000_000})

	_, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "hi", Org: "acme", MaxTokens: 1_000_000,
	})
	if err == nil {
		available, debited := w.read()
		t.Fatalf("a 1c org was served a 1M-token completion: %s debited, wallet %s", usd(debited), usd(available))
	}
	if !errors.Is(err, metering.ErrInsufficientBalance) {
		t.Fatalf("refused with %v, want ErrInsufficientBalance (402 pay-first, not an outage)", err)
	}
	if _, debited := w.read(); debited != 0 {
		t.Errorf("refused BEFORE the call, yet %s was debited", usd(debited))
	}
	// The refusal must not strand the commitment: an org that was told "no" has
	// to be able to try again after topping up.
	if p := m.inflight.pending("acme"); p != 0 {
		t.Errorf("hold leaked after refusal: %dc still committed — the org is locked out of its own balance", p)
	}
}

// What the same org CAN buy, it is still served — the gate reserves, it does not
// merely refuse. A modest completion fits inside a 1-cent balance, settles at
// its exact cost, and leaves the wallet solvent and the commitment cleared.
func TestReservationStillServesWhatTheBalanceCovers(t *testing.T) {
	w := &wallet{micros: 10_000} // 1 cent
	m := meterOn(t, w.server(t).URL, longAI{completion: 1_000_000})

	resp, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "hi", Org: "acme", MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("a call the balance covers was refused: %v", err)
	}
	if resp.CompletionTokens > 100 {
		t.Errorf("provider returned %d completion tokens against a 100-token ceiling — the reservation is not binding",
			resp.CompletionTokens)
	}
	w.settled(t, 1)

	available, debited := w.read()
	if available < 0 {
		t.Errorf("wallet ended at %s after a call it could afford", usd(available))
	}
	if debited == 0 {
		t.Error("served but never debited — the call was free")
	}
	if p := m.inflight.pending("acme"); p != 0 {
		t.Errorf("hold leaked after settlement: %dc still committed", p)
	}
}

// TWENTY simultaneous calls cannot each spend the WHOLE balance.
//
// Every caller reads the same 5-cent balance before any debit posts, so without
// a reservation every caller is authorized for the same 5 cents and the org
// spends 20x what it has. Committing before weighing makes the second caller
// see the first one's commitment. The barrier holds all callers at the balance
// read so this is deterministic rather than a race won by luck — but the window
// is real at any concurrency, and inference is exactly the long-running work
// that fills it.
func TestConcurrentCallsCannotEachSpendTheWholeBalance(t *testing.T) {
	const callers = 20
	var once sync.Once
	ready := make(chan struct{})
	var n int
	var mu sync.Mutex

	w := &wallet{micros: 50_000} // 5 cents
	w.arrive = func() {
		mu.Lock()
		n++
		all := n >= callers
		mu.Unlock()
		if all {
			once.Do(func() { close(ready) })
		}
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
		}
	}

	// Each call is priced at EXACTLY the balance: 24_000 prompt + 1_000 completion
	// = 25_000 tokens = 5 cents. ONE is affordable; twenty are not. MaxTokens is
	// stated so this measures concurrency alone, not the default ceiling.
	prompt := make([]byte, 96_000)
	for i := range prompt {
		prompt[i] = 'a'
	}
	m := meterOn(t, w.server(t).URL, longAI{completion: 1_000})

	var wg sync.WaitGroup
	var served int
	for range callers {
		wg.Go(func() {
			_, err := m.ChatCompletion(context.Background(), &types.ChatRequest{Model: "x", Prompt: string(prompt), Org: "acme", MaxTokens: 1_000})
			if err == nil {
				mu.Lock()
				served++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	w.settled(t, served)

	available, debited := w.read()
	if served > 1 {
		t.Errorf("TOCTOU: %d/%d simultaneous callers were each authorized for the whole 5c balance; %s debited",
			served, callers, usd(debited))
	}
	if available < 0 {
		t.Errorf("wallet ended at %s — spent %dx the balance", usd(available), debited/50_000)
	}
}

func usd(micros int64) string { return fmt.Sprintf("$%.4f", float64(micros)/1e6) }

// A 1M-CONTEXT MODEL IS NOT CAPPED BY A CONSTANT.
//
// The ceiling a chat is reserved against is a property of the MODEL, resolved
// from the catalog. This is the regression that keeps recurring in this estate:
// ai/model's name-matching table gave deepseek-v4-pro 16384 and 402'd every long
// prompt, and glm-5.2 dead-ended /compact on a stale 16K fallback. A constant
// here would reintroduce it on the billing path — reserving 32k for a model that
// can emit 1M, and under-reserving by ~30x.
func TestCeilingComesFromTheModelNotAConstant(t *testing.T) {
	t.Cleanup(func() { SetCompletionCeiling(nil) })
	SetCompletionCeiling(func(model string) int {
		if model == "glm-5.2" {
			return 1_000_000
		}
		return 0 // undeclared → the floor
	})

	big := atMost(&types.ChatRequest{Model: "glm-5.2", Prompt: "hi"})
	if big < 1_000_000 {
		t.Errorf("glm-5.2 reserved %d tokens, want >= 1M — a 1M model was capped by a constant", big)
	}
	small := atMost(&types.ChatRequest{Model: "unknown", Prompt: "hi"})
	if small >= 1_000_000 {
		t.Errorf("an undeclared model reserved %d tokens; it must take the floor, not another model's ceiling", small)
	}
	if small != EstTokens("hi")+defaultMaxCompletionTokens {
		t.Errorf("undeclared model reserved %d, want prompt+floor(%d)", small, defaultMaxCompletionTokens)
	}
	// The caller's own MaxTokens always wins over the catalog: it is their stated
	// intent, and it is the only value ever sent upstream.
	req := &types.ChatRequest{Model: "glm-5.2", Prompt: "hi", MaxTokens: 500}
	if got := atMost(req); got != EstTokens("hi")+500 {
		t.Errorf("caller MaxTokens=500 reserved %d, want prompt+500", got)
	}
	if req.MaxTokens != 500 {
		t.Errorf("atMost mutated the caller's MaxTokens to %d — a ceiling we synthesize must never reach the wire", req.MaxTokens)
	}
}
