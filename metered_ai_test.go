package cloud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
)

// recordingAI is a transport stub that records the request it received (so a test
// can assert the billing scope was forwarded) and returns a canned response.
type recordingAI struct {
	chatReq  *types.ChatRequest
	embedReq *types.EmbedRequest
	resp     *types.ChatResponse
}

func (r *recordingAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	r.chatReq = req
	return r.resp, nil
}

func (r *recordingAI) Embed(_ context.Context, req *types.EmbedRequest) ([][]float32, error) {
	r.embedReq = req
	return [][]float32{{1, 2, 3}}, nil
}

// listerAI ALSO implements types.ModelLister — the capability that must survive the
// metering wrap so agents' model-catalog validation keeps working.
type listerAI struct{}

func (listerAI) ChatCompletion(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}
func (listerAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }
func (listerAI) Models(context.Context) ([]string, error)                        { return []string{"m"}, nil }

// passthroughMetered builds a meteredAI whose meter is not Enabled (no commerce
// URL), so the wrapper is a transparent pass-through — the dev/un-provisioned
// posture that must never block a call.
func passthroughMetered(inner types.AIClient) *meteredAI {
	return &meteredAI{inner: inner, meter: &ResourceMeter{}, rate: defaultAIPriceUUSDPer1kTokens}
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

func TestMeteredAI_Pricing(t *testing.T) {
	m := &meteredAI{rate: 2000} // 2000 micro-USD per 1000 tokens ($2 / 1M tokens).
	if got := m.micros(1000); got != 2000 {
		t.Fatalf("micros(1000) = %d, want 2000", got)
	}
	// 2000 micro-USD is 0.2 cents → the conservative gate rounds UP to 1 cent.
	if got := m.cents(1000); got != 1 {
		t.Fatalf("cents(1000) = %d, want 1 (round up)", got)
	}
	// 10_000 tokens → 20_000 micro-USD = 2 cents exactly.
	if got := m.cents(10000); got != 2 {
		t.Fatalf("cents(10000) = %d, want 2", got)
	}
	// rate 0 = free/un-metered.
	free := &meteredAI{rate: 0}
	if free.micros(1000) != 0 || free.cents(1000) != 0 {
		t.Fatalf("rate 0 must price to zero (free)")
	}
}

// The wrapper forwards the call to the transport unchanged and returns its
// response, and the billing scope on the request reaches the transport (so the
// gen_ai span + any downstream carry it).
func TestMeteredAI_PassThroughForwardsScope(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 42}}
	m := passthroughMetered(inner)

	resp, err := m.ChatCompletion(context.Background(), &types.ChatRequest{Model: "x", Prompt: "yo", Org: "acme", Project: "p1"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content = %q, want hi", resp.Content)
	}
	if inner.chatReq == nil || inner.chatReq.Org != "acme" || inner.chatReq.Project != "p1" {
		t.Fatalf("billing scope not forwarded to transport: %+v", inner.chatReq)
	}

	vecs, err := m.Embed(context.Background(), &types.EmbedRequest{Model: "bge-m3", Inputs: []string{"a"}, Org: "acme"})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("embed: err=%v vecs=%d", err, len(vecs))
	}
	if inner.embedReq == nil || inner.embedReq.Org != "acme" {
		t.Fatalf("embed scope not forwarded: %+v", inner.embedReq)
	}
}

// billedOrg is the metered_ai billing-key resolver: HOME (BillingOrg) when set,
// else the EFFECTIVE Org. Whitespace-only billing falls back too.
func TestBilledOrg(t *testing.T) {
	cases := []struct{ billing, effective, want string }{
		{"admin", "victim", "admin"}, // masquerade: home pays
		{"", "acme", "acme"},         // normal caller: not split → effective
		{"  ", "acme", "acme"},       // whitespace billing → effective
		{"admin", "", "admin"},       // home present, no effective
	}
	for _, tc := range cases {
		if got := billedOrg(tc.billing, tc.effective); got != tc.want {
			t.Fatalf("billedOrg(%q,%q)=%q want %q", tc.billing, tc.effective, got, tc.want)
		}
	}
}

// TestMeteredAI_AdminMasqueradeBillsHomeOrg is the AI-meter half of the billing
// split (mirrors the edge-gate proof): a SuperAdmin (BillingOrg=admin) running
// inference while acting in a victim org (Org=victim) must debit the ADMIN ledger,
// while the inner AI call still receives the EFFECTIVE org (victim) for its data
// scope (BYO keys / RAG). Driven end-to-end through a real metering client → fake
// commerce, so the recorded debit's billing key is observed on the wire.
func TestMeteredAI_AdminMasqueradeBillsHomeOrg(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	inner := &recordingAI{resp: &types.ChatResponse{Content: "hi", TotalTokens: 100}}
	m := &meteredAI{
		inner: inner,
		meter: NewResourceMeter(Deps{Metering: mustClient(t, srv.URL, false)}, AIMeterProvider),
		rate:  defaultAIPriceUUSDPer1kTokens,
	}

	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{
		Model: "x", Prompt: "hello world", Org: "victim", BillingOrg: "admin", Project: "p",
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
	fc.mu.Lock()
	body := string(fc.usageBody)
	fc.mu.Unlock()
	if !strings.Contains(body, `"user":"admin"`) {
		t.Fatalf("debit must land on the HOME org (admin), got usage body: %s", body)
	}
	if strings.Contains(body, `"user":"victim"`) {
		t.Fatalf("debit leaked to the acted-on org (victim): %s", body)
	}
}

// A system call (no billing org) is executed but never gated or debited — there is
// no customer to bill — and must not error.
func TestMeteredAI_SystemCallNotMetered(t *testing.T) {
	inner := &recordingAI{resp: &types.ChatResponse{Content: "ok"}}
	m := passthroughMetered(inner)
	if _, err := m.ChatCompletion(context.Background(), &types.ChatRequest{Model: "x", Prompt: "sys", Org: ""}); err != nil {
		t.Fatalf("system call errored: %v", err)
	}
}

// The optional ModelLister capability must survive the metering wrap, or agents'
// model-catalog validation silently breaks.
func TestMeteredAI_ModelListerPreserved(t *testing.T) {
	wrapped := meteredAIClient(listerAI{}, Deps{})
	if _, ok := wrapped.(types.ModelLister); !ok {
		t.Fatalf("ModelLister capability lost through the metering wrap")
	}
	// A plain transport (no Models) must NOT gain a ModelLister.
	plain := meteredAIClient(&recordingAI{}, Deps{})
	if _, ok := plain.(types.ModelLister); ok {
		t.Fatalf("metering wrap fabricated a ModelLister on a non-lister transport")
	}
}

// BYOInferenceFeeMicros is the shared BYO fee model any BYO inference path (Workers
// AI) prices with: BYOFeeBps of the equivalent metered price, FLOORED per call. The
// token-proportional part is isolated by disabling the floor; the floor has its own
// assertions below.
func TestBYOInferenceFee(t *testing.T) {
	t.Setenv("CLOUD_AI_PRICE_UUSD_PER_1K", "2000") // equiv price = tokens*2 micro-USD
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "0")       // isolate the token-proportional part

	// Default 100 bps (1%): 1000 tokens → equiv 2000 micros → fee 20 micros.
	if got := BYOInferenceFeeMicros(1000); got != 20 {
		t.Fatalf("BYOInferenceFeeMicros(1000) = %d, want 20", got)
	}
	if got := BYOInferenceFeeMicros(0); got != 0 {
		t.Fatalf("zero tokens with the floor disabled must be free, got %d", got)
	}

	// Configurable bps.
	t.Setenv("CLOUD_AI_BYO_FEE_BPS", "500") // 5%
	if got := BYOInferenceFeeMicros(1000); got != 100 {
		t.Fatalf("BYOInferenceFeeMicros(1000) @5%% = %d, want 100", got)
	}

	// Zero bps disables the token-proportional part (floor still disabled here).
	t.Setenv("CLOUD_AI_BYO_FEE_BPS", "0")
	if got := BYOInferenceFeeMicros(1000); got != 0 {
		t.Fatalf("zero bps + zero floor must be free, got %d", got)
	}

	// A negative/invalid bps override falls through to the default — never silently zero.
	t.Setenv("CLOUD_AI_BYO_FEE_BPS", "-3")
	if got := BYOFeeBps(); got != defaultBYOFeeBps {
		t.Fatalf("invalid bps = %d, want default %d", got, defaultBYOFeeBps)
	}
}

// The per-call FLOOR is the F-1 fix: a call the token estimate cannot price (a non-text
// modality: 0 tokens) is still charged ≥ the floor, so it cannot slip through free and
// un-gated. The floor also dominates a cheaper token fee.
func TestBYOInferenceFloor(t *testing.T) {
	t.Setenv("CLOUD_AI_PRICE_UUSD_PER_1K", "2000")
	t.Setenv("CLOUD_AI_BYO_FEE_BPS", "100")
	t.Setenv("CLOUD_AI_BYO_FLOOR_UUSD", "100")

	// 0 tokens (non-text) → the floor, never 0.
	if got := BYOInferenceFeeMicros(0); got != 100 {
		t.Fatalf("BYOInferenceFeeMicros(0) = %d, want the floor 100", got)
	}
	// Small token fee (1000 tokens → 20) is dominated by the floor.
	if got := BYOInferenceFeeMicros(1000); got != 100 {
		t.Fatalf("BYOInferenceFeeMicros(1000) = %d, want max(floor 100, fee 20) = 100", got)
	}
	// A large token fee exceeds the floor and wins (100000 → 2000 > 100).
	if got := BYOInferenceFeeMicros(100000); got != 2000 {
		t.Fatalf("BYOInferenceFeeMicros(100000) = %d, want the token fee 2000", got)
	}
	// The default floor is non-zero, so out of the box no call is free/un-gated.
	if BYOFloorMicros() != defaultBYOFloorMicros || defaultBYOFloorMicros <= 0 {
		t.Fatalf("default floor must be > 0; got %d", defaultBYOFloorMicros)
	}
	// Any positive floor forces the gate reservation to ≥ 1 cent (gate always runs).
	if MicrosToGateCents(BYOInferenceFeeMicros(0)) < 1 {
		t.Fatal("floored fee must reserve ≥ 1 cent so the gate never short-circuits")
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
