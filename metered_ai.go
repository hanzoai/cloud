package cloud

// The ONE inference gate+meter. meteredAI wraps the transport AIClient (deps.AI)
// so EVERY chat + embed call is authorized against the caller's org
// balance/budget/freeze BEFORE the call and debited its billing account AFTER —
// the single DRY chokepoint through which no inference runs unattributed. The
// billing scope (org, project) rides the request value (ChatRequest/EmbedRequest),
// so it is impossible to call AI without declaring who pays; there is no exempt
// path, no bypass, no side-channel key.
//
// It reuses the SAME commerce path (ResourceMeter over Deps.Metering) and per-org
// invariants as every other Hanzo surface — the debit is forced onto the caller's
// org, never a default or another org. When billing is unconfigured the wrapper
// is a transparent pass-through (ResourceMeter.Enabled()==false → Gate allows,
// Meter no-ops), so a dev/un-provisioned deployment is never blocked.

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
)

// AIMeterProvider is the commerce provider/service label every inference debit
// carries, so LLM spend is attributable and the per-scope + account caps sum over
// (project, "ai"). It is exported so any OTHER inference path that does not run
// through this wrapper — a BYO model invoked with the org's own provider token
// (e.g. Cloudflare Workers AI) — debits the SAME product axis and shares the same
// per-scope caps, rather than inventing a parallel usage label.
const AIMeterProvider = "ai"

// defaultAIPriceUUSDPer1kTokens is the fallback inference price in micro-USD per
// 1000 tokens ($2 / 1M tokens) when no operator override is set. A clearly-named,
// configurable POLICY default — never a fabricated market price; ops sets the real
// number per deployment via CLOUD_AI_PRICE_UUSD_PER_1K. Applied to total tokens
// (chat) or estimated input tokens (embed). 0 makes inference free (and thus
// un-metered), mirroring the edge gate's price==0 pass-through.
const defaultAIPriceUUSDPer1kTokens int64 = 2000

type meteredAI struct {
	inner types.AIClient
	meter *ResourceMeter
	log   luxlog.Logger
	rate  int64 // micro-USD per 1000 tokens.
}

// meteredAIClient wraps inner so every inference call meters through the ONE
// commerce path. It preserves the optional ModelLister capability when inner
// implements it (agents' model-catalog validation must survive the wrap). deps
// must already carry Metering/Env/Logger.
func meteredAIClient(inner types.AIClient, deps Deps) types.AIClient {
	m := &meteredAI{
		inner: inner,
		meter: NewResourceMeter(deps, AIMeterProvider),
		log:   deps.Logger,
		rate:  aiPriceUUSDPer1kTokens(),
	}
	if _, ok := inner.(types.ModelLister); ok {
		return &meteredAIModels{m}
	}
	return m
}

// meteredAIModels re-exposes the ModelLister capability, delegating to the wrapped
// client so callers that type-assert types.ModelLister (agents) still see it.
type meteredAIModels struct{ *meteredAI }

func (m *meteredAIModels) Models(ctx context.Context) ([]string, error) {
	return m.inner.(types.ModelLister).Models(ctx)
}

// ChatCompletion pre-authorizes on a prompt-derived estimate (an out-of-funds /
// over-cap / frozen org is refused BEFORE upstream tokens are spent), runs the
// call, then debits the EXACT token cost (completion tokens are only known after).
func (m *meteredAI) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	// The balance gate + the debit key on the HOME org that PAYS (BillingOrg, the
	// caller's X-User-Owner) — so a SuperAdmin acting in another org spends from the
	// admin ledger. The inner AI call still receives req (req.Org, the EFFECTIVE org)
	// for its data scope (BYO keys, RAG). billedOrg falls back to req.Org when the
	// caller did not split them (home==effective for a normal caller).
	payer := billedOrg(req.BillingOrg, req.Org)
	if err := m.gate(ctx, payer, req.Project, EstTokens(req.Prompt)); err != nil {
		return nil, err
	}
	resp, err := m.inner.ChatCompletion(ctx, req)
	if err != nil {
		return resp, err
	}
	if resp != nil {
		total := resp.TotalTokens
		if total <= 0 {
			total = resp.PromptTokens + resp.CompletionTokens
		}
		if total <= 0 {
			total = EstTokens(req.Prompt) // gateway omitted usage → fall back to the estimate.
		}
		m.record(payer, req.Project, req.Model, metering.Usage{
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
			TotalTokens:      total,
		}, total)
	}
	return resp, nil
}

// Embed pre-authorizes + debits on the deterministic input-token estimate (the
// embeddings API returns no usage), through the same one path as chat.
func (m *meteredAI) Embed(ctx context.Context, req *types.EmbedRequest) ([][]float32, error) {
	if req == nil || len(req.Inputs) == 0 {
		return m.inner.Embed(ctx, req) // nothing to bill; let the transport no-op.
	}
	toks := EstTokens(req.Inputs...)
	payer := billedOrg(req.BillingOrg, req.Org) // HOME org pays; req.Org stays the data scope.
	if err := m.gate(ctx, payer, req.Project, toks); err != nil {
		return nil, err
	}
	vecs, err := m.inner.Embed(ctx, req)
	if err != nil {
		return vecs, err
	}
	m.record(payer, req.Project, req.Model, metering.Usage{TotalTokens: toks}, toks)
	return vecs, nil
}

// billedOrg resolves the org whose ledger PAYS for an inference: the caller's HOME
// org (billing, the X-User-Owner threaded onto the request) when present, else the
// EFFECTIVE org (the data scope). For a normal caller the two are equal; only a
// platform SuperAdmin acting in another org differs, and then the debit must land on
// the admin (home) org, never the acted-on org. Empty billing ⟹ effective, so a
// caller that has not split them keeps today's behavior (no regression).
func billedOrg(billing, effective string) string {
	if b := strings.TrimSpace(billing); b != "" {
		return b
	}
	return effective
}

// gate is the pre-call balance/budget/freeze check. tokens is the pre-call cost
// estimate; a system call (org=="") is not gated (no customer to bill).
func (m *meteredAI) gate(ctx context.Context, org, project string, tokens int) error {
	if org == "" {
		return nil
	}
	// project rides the ChatRequest/EmbedRequest value (internal S2S), not a
	// server-minted identity claim, so it is unvalidated here → a project-scoped
	// cap stays soft. The request-edge BillingGate already hardens the validated
	// project axis for the inbound LLM path.
	return m.meter.Gate(ctx, org, project, false, AIMeterProvider, m.cents(tokens))
}

// record debits the EXACT micro-USD cost to the org's billing account, attributed
// to the project scope + model. A system call (org=="") is recorded nowhere but
// logged, so any unattributed inference is detectable rather than silent.
func (m *meteredAI) record(org, project, model string, u metering.Usage, tokens int) {
	if org == "" {
		if m.log != nil {
			m.log.Warn("AI call with no billing org — inference not attributed", "model", model, "tokens", tokens)
		}
		return
	}
	u.AmountMicros = m.micros(tokens)
	u.Project = project
	u.Model = model
	u.Service = AIMeterProvider
	m.meter.MeterUsage(org, AIMeterProvider, u)
}

// micros converts a token count to the debit in micro-USD at the configured rate.
func (m *meteredAI) micros(tokens int) int64 {
	if tokens <= 0 || m.rate <= 0 {
		return 0
	}
	return int64(tokens) * m.rate / 1000
}

// cents converts a token count to whole cents (round UP, min 1 when there is any
// cost) for the conservative pre-call gate — the gate must never under-reserve.
func (m *meteredAI) cents(tokens int) int64 {
	return MicrosToGateCents(m.micros(tokens))
}

// MicrosToGateCents converts a micro-USD amount to the whole cents a pre-call
// balance gate must RESERVE: round UP (a gate must never under-reserve), with a
// 1-cent floor for any positive amount. 1 cent = 10_000 micro-USD. Shared by the
// LLM meter and every other inference path (Workers AI) so one rule prices the
// gate everywhere.
func MicrosToGateCents(micros int64) int64 {
	if micros <= 0 {
		return 0
	}
	if c := (micros + 9999) / 10000; c >= 1 { // ceil.
		return c
	}
	return 1
}

// EstTokens is a deterministic, provider-agnostic token estimate (~4 chars/token)
// used to price embeddings (no usage returned) and to pre-gate chat before the
// completion tokens are known.
func EstTokens(texts ...string) int {
	var chars int
	for _, t := range texts {
		chars += len(t)
	}
	if chars == 0 {
		return 0
	}
	if t := chars / 4; t > 0 {
		return t
	}
	return 1
}

// aiPriceUUSDPer1kTokens resolves the inference price (micro-USD per 1000 tokens)
// from CLOUD_AI_PRICE_UUSD_PER_1K, else the policy default. 0 makes inference
// free/un-metered; a negative/invalid value falls through to the default so a typo
// can never silently zero out billing.
func aiPriceUUSDPer1kTokens() int64 {
	s := strings.TrimSpace(os.Getenv("CLOUD_AI_PRICE_UUSD_PER_1K"))
	if s == "" {
		return defaultAIPriceUUSDPer1kTokens
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return defaultAIPriceUUSDPer1kTokens
	}
	return n
}

// defaultBYOFeeBps is the platform routing fee on a BYO inference call, in basis
// points of the EQUIVALENT metered price (100 bps = 1%). A BYO call runs on the
// org's OWN provider token — the provider already billed the org for the compute —
// so Hanzo charges only this thin fee for gating/metering/observing the call, never
// the full inference cost (that would double-bill). A clearly-named policy default,
// overridable per deployment; 0 disables the token-proportional part — but a call is
// still floored (BYOFloorMicros), so 0 bps alone does NOT make a call free/un-gated.
const defaultBYOFeeBps int64 = 100

// BYOFeeBps resolves the BYO inference platform fee (basis points) from
// CLOUD_AI_BYO_FEE_BPS, else the policy default. A negative/invalid value falls
// through to the default so a typo cannot silently zero out the fee; 0 is honored
// (free BYO). This is the ONE BYO fee knob for the whole binary.
func BYOFeeBps() int64 {
	s := strings.TrimSpace(os.Getenv("CLOUD_AI_BYO_FEE_BPS"))
	if s == "" {
		return defaultBYOFeeBps
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return defaultBYOFeeBps
	}
	return n
}

// defaultBYOFloorMicros is the minimum per-CALL BYO inference fee, in micro-USD
// ($0.0001). It exists because the token-denominated fee CANNOT price a non-text
// modality (audio for ASR, image bytes for vision/classification) — those yield no
// token estimate, so without a floor the pre-call gate would reserve 0, short-circuit
// BEFORE the balance/freeze/cap check, and proxy an unbilled, ungated inference. The
// floor forces every /ai/run to reserve ≥ 1 cent (so a frozen / broke / over-cap org
// is refused) and to leave a debit row ≥ the floor. Overridable; 0 opts out (and
// re-opens the modality gap — an explicit ops choice, like a 0 price).
const defaultBYOFloorMicros int64 = 100

// BYOFloorMicros resolves the per-call BYO floor (micro-USD) from CLOUD_AI_BYO_FLOOR_UUSD,
// else the policy default. A negative/invalid value falls through to the default.
func BYOFloorMicros() int64 {
	s := strings.TrimSpace(os.Getenv("CLOUD_AI_BYO_FLOOR_UUSD"))
	if s == "" {
		return defaultBYOFloorMicros
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return defaultBYOFloorMicros
	}
	return n
}

// BYOInferenceFeeMicros is the per-CALL platform routing fee (micro-USD) for a BYO
// inference of `tokens` tokens: BYOFeeBps of the EQUIVALENT metered price
// (aiPriceUUSDPer1kTokens), FLOORED at BYOFloorMicros. A BYO model invoked with the
// org's own provider token (Cloudflare Workers AI) debits the SAME usage spine and the
// SAME per-scope caps as a Hanzo-served call, at the thin BYO fee — but never below the
// floor, so a call the token estimate CANNOT price (a non-text modality: 0 tokens) is
// still gated and billed rather than slipping through free and unchecked. This is the
// ONE BYO fee model — no per-provider variant.
func BYOInferenceFeeMicros(tokens int) int64 {
	if fee := byoTokenFeeMicros(tokens); fee > BYOFloorMicros() {
		return fee
	}
	return BYOFloorMicros()
}

// byoTokenFeeMicros is the token-denominated part of the BYO fee (before the floor):
// BYOFeeBps of the equivalent metered price. Zero tokens / zero rate / zero fee ⟹ 0.
func byoTokenFeeMicros(tokens int) int64 {
	if tokens <= 0 {
		return 0
	}
	rate := aiPriceUUSDPer1kTokens()
	bps := BYOFeeBps()
	if rate <= 0 || bps <= 0 {
		return 0
	}
	// Divide progressively to bound the intermediate product (no int64 overflow for
	// sane token/rate/bps): equivalent micros = tokens*rate/1000; fee = *bps/10000.
	return int64(tokens) * rate / 1000 * bps / 10000
}
