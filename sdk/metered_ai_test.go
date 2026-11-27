package cloud

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client/commerce"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// nanosPerToken is what the stub transports below price a token at, prompt and
// completion alike: $2 a million, as the model API tells it on the call's Bill.
const nanosPerToken = 2_000

// tellTokens prices tokens on ctx's Bill as the plane does for the model API.
func tellTokens(ctx context.Context, tokens int) {
	infer.BillOf(ctx).Tell(int64(tokens) * nanosPerToken)
}

// tokensOf is every token a response reports.
func tokensOf(r *infer.ChatResponse) int {
	if r == nil {
		return 0
	}
	return max(r.TotalTokens, r.PromptTokens+r.CompletionTokens)
}

// recordingAI is a transport stub that records the request it received (so a test
// can assert the billing scope was forwarded), returns a canned response, and tells
// the call's price as the plane does.
type recordingAI struct {
	chatReq   *infer.ChatRequest
	embedReq  *infer.EmbedRequest
	rerankReq *infer.RerankRequest
	resp      *infer.ChatResponse
}

func (r *recordingAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	r.chatReq = req
	tellTokens(ctx, tokensOf(r.resp))
	return r.resp, nil
}

func (r *recordingAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	r.embedReq = req
	tellTokens(ctx, EstTokens(req.Inputs...))
	return [][]float32{{1, 2, 3}}, nil
}

func (r *recordingAI) Rerank(ctx context.Context, req *infer.RerankRequest) ([]float64, error) {
	r.rerankReq = req
	tellTokens(ctx, EstTokens(append([]string{req.Query}, req.Documents...)...))
	return []float64{0.5, 0.9}, nil
}

// flat is a price list that lists every model at perMillion dollars a million
// tokens, prompt and completion alike.
func flat(perMillion string) *priceList {
	return &priceList{ask: func(context.Context, string) (ModelPrice, bool, error) {
		return ModelPrice{Input: perMillion, Output: perMillion}, true, nil
	}}
}

// listerAI ALSO implements types.ModelLister — the capability that must survive the
// metering wrap so agents' model-catalog validation keeps working.
type listerAI struct{}

func (listerAI) ChatCompletion(context.Context, *infer.ChatRequest) (*infer.ChatResponse, error) {
	return &infer.ChatResponse{}, nil
}

func (listerAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }

func (listerAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

func (listerAI) Models(context.Context) ([]string, error) { return []string{"m"}, nil }

// passthroughMetered builds a meteredAI whose meter is not Enabled (no commerce
// URL), so the wrapper is a transparent pass-through — the dev/un-provisioned
// posture that must never block a call.
func passthroughMetered(inner infer.Client) *meteredAI {
	return &meteredAI{inner: inner, meter: &Meter{}}
}

func TestEstTokens(t *testing.T) {
	cases := map[string]int{"": 0, "abcd": 1, "a": 1, "aaaaaaaa": 2}
	for in, want := range cases {
		if got := EstTokens(in); got != want {
			t.Fatalf("EstTokens(%q) = %d, want %d", in, got, want)
		}
	}
	if got := EstTokens(); got != 0 {
		t.Fatalf("EstTokens() = %d, want 0", got)
	}
	if got := EstTokens("aaaa", "aaaa"); got != 2 {
		t.Fatalf("EstTokens(2x4chars) = %d, want 2", got)
	}
}

// A hold is the most the call can cost at the model's list price — the row the
// model API bills it from — rounded up to whole cents, and never under a cent, so a
// free or unlisted model still meets the gate. The list is asked once a minute, not
// once a call.
func TestAHoldIsTheModelsListPrice(t *testing.T) {
	asked := 0
	p := &priceList{ask: func(_ context.Context, model string) (ModelPrice, bool, error) {
		asked++
		if model == "enso-ultra" {
			return ModelPrice{Input: "5.50", Output: "22"}, true, nil
		}
		return ModelPrice{}, false, nil
	}}
	ctx := context.Background()
	if got := p.hold(ctx, "enso-ultra", 1_000_000, 0); got != 550 {
		t.Fatalf("a million prompt tokens of enso-ultra held %d cents, want 550", got)
	}
	if got := p.hold(ctx, "enso-ultra", 0, 1_000); got != 3 {
		t.Fatalf("a thousand completion tokens at $22 a million held %d cents, want 3 (2.2 up)", got)
	}
	if got := p.hold(ctx, "nobody-lists-me", 1_000_000, 1_000_000); got != holdFloorCents {
		t.Fatalf("an unlisted model held %d cents, want the floor %d", got, holdFloorCents)
	}
	if asked != 2 {
		t.Fatalf("the list was asked %d times for two models, want 2: a price is held a minute", asked)
	}
}

// The wrapper forwards the call to the transport unchanged and returns its
// response, and the billing scope on the request reaches the transport (so the
// gen_ai span + any downstream carry it).
func TestMeteredAI_PassThroughForwardsScope(t *testing.T) {
	serveCommerceOK(t)
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 42}}
	m := passthroughMetered(inner)

	resp, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{Model: "x", Prompt: "yo", Org: "acme", Payer: account.Org("acme"), Project: "p1"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content = %q, want hi", resp.Content)
	}
	if inner.chatReq == nil || inner.chatReq.Org != "acme" || inner.chatReq.Project != "p1" {
		t.Fatalf("billing scope not forwarded to transport: %+v", inner.chatReq)
	}

	vecs, err := m.Embed(context.Background(), &infer.EmbedRequest{Model: "bge-m3", Inputs: []string{"a"}, Org: "acme", Payer: account.Org("acme")})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("embed: err=%v vecs=%d", err, len(vecs))
	}
	if inner.embedReq == nil || inner.embedReq.Org != "acme" {
		t.Fatalf("embed scope not forwarded: %+v", inner.embedReq)
	}
}

// TestMeteredAI_AdminMasqueradeBillsHomeOrg is the AI-meter half of the billing
// split (mirrors the edge-gate proof): a SuperAdmin (Payer=the admin account)
// running inference while acting in a victim org (Org=victim) must debit the ADMIN ledger,
// while the inner AI call still receives the EFFECTIVE org (victim) for its data
// scope (BYO keys / RAG). Driven end-to-end through a real metering client → fake
// commerce, so the recorded debit's billing key is observed on the wire.
func TestMeteredAI_AdminMasqueradeBillsHomeOrg(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 100}}
	m := &meteredAI{
		inner: inner,
		meter: NewMeter(Deps{Metering: mustClient(t, srv.URL)}, AIMeterProvider),
	}

	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "x", Prompt: "hello world", Org: "victim", Payer: account.Org("admin"), Project: "p",
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}

	// DATA scope: the inner AI call receives the EFFECTIVE org (victim), NOT home —
	// so BYO keys / RAG resolve against the acted-on org, never the admin org.
	if got := inner.chatReq.Org; got != "victim" {
		t.Fatalf("inner AI data scope = %q, want victim (effective, never rescoped to home)", got)
	}

	// DEBIT: recorded against the HOME org (admin). Record is async (fire-and-forget).
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("no usage recorded (Record must fire on the metered AI path)")
	}
	debit, ok := fc.debits.last()
	if !ok {
		t.Fatal("no debit crossed the plane")
	}
	if debit.In.Subject != "admin" {
		t.Fatalf("debit must land on the HOME org (admin), got subject %q", debit.In.Subject)
	}
	if debit.Org != "admin" {
		t.Fatalf("debit must be written to the HOME org's books (admin), got org %q", debit.Org)
	}
}

// A system call (no billing org) is executed but never gated or debited — there is
// no customer to bill — and must not error.
func TestMeteredAI_SystemCallNotMetered(t *testing.T) {
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "ok"}}
	m := passthroughMetered(inner)
	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{Model: "x", Prompt: "sys", Org: ""}); err != nil {
		t.Fatalf("system call errored: %v", err)
	}
}

// The optional ModelLister capability must survive the metering wrap, or agents'
// model-catalog validation silently breaks.
func TestMeteredAI_ModelListerPreserved(t *testing.T) {
	wrapped := MeteredAIClient(listerAI{}, Deps{})
	if _, ok := wrapped.(infer.ModelLister); !ok {
		t.Fatalf("ModelLister capability lost through the metering wrap")
	}
	// A plain transport (no Models) must NOT gain a ModelLister.
	plain := MeteredAIClient(&recordingAI{}, Deps{})
	if _, ok := plain.(infer.ModelLister); ok {
		t.Fatalf("metering wrap fabricated a ModelLister on a non-lister transport")
	}
}

// streamAI is the full-capability transport shape (what httpAI is): it lists
// models AND streams.
type streamAI struct{ listerAI }

func (s *streamAI) ChatStream(context.Context, *infer.ChatRequest, func(string) error) (*infer.ChatResponse, error) {
	return &infer.ChatResponse{Content: "hello"}, nil
}

// The meter streams, and keeps ModelLister beside it, so a caller that wants deltas
// buys them through the same one metered path (metered_ai_price_test.go pins the
// charge).
func TestAMeteredClientStreams(t *testing.T) {
	wrapped := MeteredAIClient(&streamAI{}, Deps{})
	if _, ok := wrapped.(infer.StreamCompleter); !ok {
		t.Fatal("the meter offered no stream")
	}
	if _, ok := wrapped.(infer.ModelLister); !ok {
		t.Fatal("ModelLister lost when the transport also streams")
	}
}

// A BYO call (Workers AI on the org's own token) is charged one fee a call, never a
// share of an inference price: the org's provider already billed the compute, and
// the model API lists no price for a model it does not serve. The fee is never
// zero by default, so the gate always runs and every call leaves a row.
func TestTheBYOFeeIsOnePerCall(t *testing.T) {
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "")
	if got := BYOFloorMicros(); got != defaultBYOFloorMicros || got <= 0 {
		t.Fatalf("default BYO fee %d, want %d and above zero", got, defaultBYOFloorMicros)
	}
	if MicrosToGateCents(BYOFloorMicros()) < 1 {
		t.Fatal("the BYO fee must reserve a cent so the gate never short-circuits")
	}
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "250")
	if got := BYOFloorMicros(); got != 250 {
		t.Fatalf("override 250 read as %d", got)
	}
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "-3")
	if got := BYOFloorMicros(); got != defaultBYOFloorMicros {
		t.Fatalf("an invalid override read as %d, want the default %d", got, defaultBYOFloorMicros)
	}
}

// MicrosToGateCents rounds UP with a 1-cent floor — a pre-call gate must never
// under-reserve.
func TestMicrosToGateCents(t *testing.T) {
	for _, tc := range []struct{ micros, cents int64 }{
		{0, 0}, {1, 1}, {9999, 1}, {10000, 1}, {10001, 2}, {25000, 3},
	} {
		if got := MicrosToGateCents(tc.micros); got != tc.cents {
			t.Fatalf("MicrosToGateCents(%d) = %d, want %d", tc.micros, got, tc.cents)
		}
	}
}

// serveCommerceOK stands up the ledger these tests assume is there. The AI path
// gates through the ONE Meter, and since the ledger moved to its own
// binary a meter with no local URL asks commerce over the socket. Without a
// biller to answer, these would be asserting "the gate fails closed" — which is
// true, and is TestMeter_UnconfiguredIsNoop's job, not theirs.
func serveCommerceOK(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := zip.New(zip.Config{AppName: commerce.App})
	app.Post("/finance/authorize",
		func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
			return &client.Verdict{OK: true}, nil
		}, zip.WithOperationID(client.FinanceAuthorize))

	// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE.
	//
	// zip.SocketPath reads ZIP_RUNTIME_DIR on every call and every test points it at its
	// own temp dir, so a listener that resolved its own address resolved it whenever the
	// scheduler got to it — which can be after this test has ended and the NEXT one has
	// repointed the directory. It then bound at the next test's address and registered
	// as the app serving there (zip keys that registry by ADDRESS, here.go), so the next
	// test's calls were answered by THIS stand-in. It only serves finance_authorize, so
	// a debit came back "unknown op: finance_record" and read as "the meter never fired".
	//
	// Measured: 1 root-suite run in 4, with the shadow's op list naming this helper.
	// It was harmless while the debit was an HTTP POST to a captured URL; it stopped
	// being harmless when the debit moved onto the plane, where a peer is found by NAME.
	addr := zip.SocketPath(commerce.App)
	go func() { _ = app.Listen(addr) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	// Dial the SOCKET, not Peer: zip dials lazily, so Peer answers without a listener
	// behind it and this loop would return before the stand-in was up.
	for range 400 {
		if c, err := net.Dial("unix", addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("commerce stand-in never began listening at %s", addr)
}

// TestRerankCarriesScopeThroughTheMeter: the wrapper forwards the request it was
// given — model, query, documents and billing scope — and hands back the
// transport's scores unchanged, in the pass-through posture.
func TestRerankCarriesScopeThroughTheMeter(t *testing.T) {
	inner := &recordingAI{}
	m := passthroughMetered(inner)
	scores, err := m.Rerank(context.Background(), &infer.RerankRequest{
		Model: "zen-rerank", Query: "q", Documents: []string{"a", "b"}, Org: "acme", Payer: account.Org("home"), Project: "p",
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[1] != 0.9 {
		t.Fatalf("scores: got %v", scores)
	}
	r := inner.rerankReq
	if r == nil || r.Org != "acme" || r.Payer != account.Org("home") || r.Project != "p" || r.Model != "zen-rerank" || len(r.Documents) != 2 {
		t.Fatalf("request forwarded: %+v", r)
	}
}
