package cloud

// The per-token debit must name the agent run that caused it.
//
// A run's cost is the SUM of its rounds. The flat run fee is one debit apps/agents
// makes itself and can label; the per-token charges are made HERE, one completion
// at a time, and until they carried the run's name there was no key that joined
// those ledger rows back to the run — so an operator could ask what an ORG spent
// and never what a RUN cost. This pins the correlation id onto the debit as it
// crosses the plane, which is the only place the claim is actually true or false.

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
)

func TestMeteredAI_PerTokenDebitNamesItsRun(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 100}}
	m := &meteredAI{
		inner: inner,
		meter: NewMeter(Deps{Metering: mustClient(t, srv.URL, false)}, AIMeterProvider),
		rate:  defaultAIPriceUUSDPer1kTokens,
	}

	const runID = "run_0123456789abcdef"
	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "hello world", Org: "acme", RunID: runID,
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}

	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatal("no usage recorded on the metered AI path")
	}
	debit, ok := fc.debits.last()
	if !ok {
		t.Fatal("no debit crossed the plane")
	}
	if got := debit.In.Usage.RequestID; got != runID {
		t.Fatalf("per-token debit carries correlation id %q, want the run %q — "+
			"this run's cost cannot be summed from the ledger", got, runID)
	}
	// It is the CORRELATION id, never the idempotency key: a tool loop settles once
	// per round under the same run, and pinning Ref to the run would dedup every
	// round after the first into the first one's debit — free inference.
	if debit.In.Usage.Ref == runID {
		t.Fatal("the run id must not be the ledger's idempotency key: " +
			"every round after the first would dedup away")
	}
}

// A completion that belongs to no run (a direct API call) carries no run
// correlation id, rather than an empty one that would read as a run named "".
func TestMeteredAI_DirectCallCarriesNoRunID(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	m := &meteredAI{
		inner: &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 100}},
		meter: NewMeter(Deps{Metering: mustClient(t, srv.URL, false)}, AIMeterProvider),
		rate:  defaultAIPriceUUSDPer1kTokens,
	}
	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "hello world", Org: "acme",
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatal("no usage recorded")
	}
	debit, _ := fc.debits.last()
	if got := debit.In.Usage.RequestID; got != "" {
		t.Fatalf("a runless call carries correlation id %q, want none", got)
	}
}
