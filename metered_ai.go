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

	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
)

// aiMeterProvider is the commerce provider/service label every inference debit
// carries, so LLM spend is attributable and the per-scope + account caps sum over
// (project, "ai").
const aiMeterProvider = "ai"

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
		meter: NewResourceMeter(deps, aiMeterProvider),
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
	if err := m.gate(ctx, req.Org, req.Project, estTokens(req.Prompt)); err != nil {
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
			total = estTokens(req.Prompt) // gateway omitted usage → fall back to the estimate.
		}
		m.record(req.Org, req.Project, req.Model, metering.Usage{
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
	toks := estTokens(req.Inputs...)
	if err := m.gate(ctx, req.Org, req.Project, toks); err != nil {
		return nil, err
	}
	vecs, err := m.inner.Embed(ctx, req)
	if err != nil {
		return vecs, err
	}
	m.record(req.Org, req.Project, req.Model, metering.Usage{TotalTokens: toks}, toks)
	return vecs, nil
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
	return m.meter.Gate(ctx, org, project, false, aiMeterProvider, m.cents(tokens))
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
	u.Service = aiMeterProvider
	m.meter.MeterUsage(org, aiMeterProvider, u)
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
	micros := m.micros(tokens)
	if micros <= 0 {
		return 0
	}
	if c := (micros + 9999) / 10000; c >= 1 { // ceil: 1 cent = 10_000 micro-USD.
		return c
	}
	return 1
}

// estTokens is a deterministic, provider-agnostic token estimate (~4 chars/token)
// used to price embeddings (no usage returned) and to pre-gate chat before the
// completion tokens are known.
func estTokens(texts ...string) int {
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
