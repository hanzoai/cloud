package cloud

// The ONE inference gate+meter. meteredAI wraps the transport AIClient (deps.AI)
// so EVERY chat + embed call is authorized against the payer's balance, budget and
// freeze BEFORE the call and debited to that same account AFTER — the single
// chokepoint through which no inference runs unattributed. The payer rides the
// request value (ChatRequest.Payer and its siblings), so it is impossible to call
// AI for an org without declaring who pays; there is no exempt path, no bypass, no
// side-channel key.
//
// IT HAS NO PRICE OF ITS OWN. A call crosses the plane to the fleet's model API,
// which prices it exactly as it prices the same answer for any caller of
// api.hanzo.ai — the routed model, its route and rate rows, Enso's fixed prices,
// zen's retail — and tells the price on the call's Bill (types.Bill). The meter
// charges that. Before the call it holds the most the call can cost at the model's
// listed price (ai_rates), never less than a cent, so an empty wallet is refused
// before a model is asked; the hold is given back when the debit lands. The flat
// $2-a-million rate it used to charge for every model billed enso-ultra below its
// cost and the free models above nothing; it is gone.
//
// It reuses the SAME commerce path (Meter over Deps.Metering) as every other Hanzo
// surface. With no meter constructed the wrapper is a transparent pass-through, so
// a dev deployment is never blocked.

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/decimal"
	hz "github.com/hanzoai/money"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
)

// AIMeterProvider is the commerce provider/service label every inference debit
// carries, so LLM spend is attributable and the per-scope + account caps sum over
// (project, "ai"). It is exported so any OTHER inference path that does not run
// through this wrapper — a BYO model invoked with the org's own provider token
// (e.g. Cloudflare Workers AI) — debits the SAME product axis and shares the same
// per-scope caps, rather than inventing a parallel usage label.
const AIMeterProvider = "ai"

type meteredAI struct {
	inner infer.Client
	meter *Meter
	log   luxlog.Logger
	// prices is the price list the hold is weighed at (ai_rates), never a charge.
	prices *priceList

	// inflight is what this pod's in-progress calls have COMMITTED but not yet
	// spent, so a second caller weighs the balance against the first one's
	// commitment instead of against money that is already being spent.
	inflight meter.Commitments
}

// MeteredAIClient wraps inner so every inference call meters through the ONE
// commerce path. It preserves the optional ModelLister capability when inner
// implements it (agents' model-catalog validation must survive the wrap). deps
// must already carry Metering/Env/Logger.
//
// It carries StreamCompleter (ChatStream), metered exactly as ChatCompletion is: the
// same hold, one settle at the price the model API told. A stream crosses the plane
// as ai_chat_stream, so the model API prices it for the account the request names,
// as it does a buffered completion. Before that op a stream could only reach the
// gateway as this deployment's own application (billed there to the deployment's
// org at the gateway's price, and here to the customer at the meter's own), so the
// meter offered none. Over an inner client that cannot stream, ChatStream is
// ChatCompletion, with the whole answer as its one delta.
func MeteredAIClient(inner infer.Client, deps Deps) infer.Client {
	m := &meteredAI{
		inner:  inner,
		meter:  NewMeter(deps, AIMeterProvider),
		log:    luxlog.Default(),
		prices: modelPrices,
	}
	if _, lists := inner.(infer.ModelLister); lists {
		return &meteredAIModels{m}
	}
	return m
}

// meteredAIModels re-exposes ModelLister (agents' model-catalog validation): a
// caller that type-asserts it against deps.AI would otherwise see the decorator,
// not the transport, and silently lose the capability.
type meteredAIModels struct{ *meteredAI }

func (m *meteredAIModels) Models(ctx context.Context) ([]string, error) {
	return m.inner.(infer.ModelLister).Models(ctx)
}

// ChatCompletion holds the most the call can cost at the model's listed price (an
// out-of-funds / over-cap / frozen payer is refused BEFORE upstream tokens are
// spent), runs the call, then debits what the model API priced it at.
func (m *meteredAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	return m.complete(ctx, req, m.inner.ChatCompletion)
}

// ChatStream is ChatCompletion delivered a delta at a time: the same hold before
// anything is asked, the same one settle after, at the price the model API told
// the call's Bill. A stream is never a way around the meter: an inner client that
// cannot stream answers as ChatCompletion, the whole answer its one delta.
func (m *meteredAI) ChatStream(ctx context.Context, req *infer.ChatRequest, emit func(delta string) error) (*infer.ChatResponse, error) {
	sc, ok := m.inner.(infer.StreamCompleter)
	if !ok {
		resp, err := m.ChatCompletion(ctx, req)
		if err == nil && resp != nil && resp.Content != "" && emit != nil {
			_ = emit(resp.Content)
		}
		return resp, err
	}
	return m.complete(ctx, req, func(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
		return sc.ChatStream(ctx, req, emit)
	})
}

// complete is the one metered path a completion takes, buffered or streamed.
func (m *meteredAI) complete(ctx context.Context, req *infer.ChatRequest, call func(context.Context, *infer.ChatRequest) (*infer.ChatResponse, error)) (*infer.ChatResponse, error) {
	// The balance gate and the debit key on the account the request names (Payer,
	// resolved where the work started). The inner AI call still receives req.Org,
	// the EFFECTIVE org, for its data scope (BYO keys, RAG).
	req = named(ctx, req)
	payer, err := payerOf(req.Payer, req.Org)
	if err != nil {
		return nil, err
	}
	h, err := m.reserve(ctx, payer, req.Project, m.prices.hold(ctx, req.Model, EstTokens(req.Text()), atMost(req)))
	if err != nil {
		return nil, err
	}
	defer h.Release() // no-op once settlement has taken it; the safety net for every other exit.
	ctx, bill := infer.WithBill(ctx)
	resp, cerr := call(ctx, req)
	if cerr != nil {
		m.failed(ctx, payer, req.Project, req.Model, bill, meter.Usage{Actor: req.Actor, RequestID: req.RunID}, h)
		return resp, cerr
	}
	if resp == nil {
		return resp, nil
	}
	resp.Billed = m.settle(ctx, payer, req.Project, req.Model, bill, meter.Usage{
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		TotalTokens:      max(resp.TotalTokens, resp.PromptTokens+resp.CompletionTokens),
		// WHO the debit was made for. It is audit only: the account charged is
		// `payer`, above, and reads nothing here.
		Actor: req.Actor,
		// The run that caused this charge, when a run did. It is the correlation
		// id, never the idempotency key: a tool loop settles once per ROUND, and
		// every round of one run carries the same value here on purpose — that is
		// what makes the run's total cost a SUM rather than a guess.
		RequestID: req.RunID,
	}, h)
	return resp, nil
}

// Embed holds the input at the model's listed price and debits what the model API
// priced the embeddings at, through the same one path as chat.
func (m *meteredAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	if req == nil || len(req.Inputs) == 0 {
		return m.inner.Embed(ctx, req) // nothing to bill; let the transport no-op.
	}
	toks := EstTokens(req.Inputs...)
	payer, err := payerOf(req.Payer, req.Org) // req.Org stays the data scope.
	if err != nil {
		return nil, err
	}
	h, err := m.reserve(ctx, payer, req.Project, m.prices.hold(ctx, req.Model, toks, 0))
	if err != nil {
		return nil, err
	}
	defer h.Release()
	ctx, bill := infer.WithBill(ctx)
	vecs, eerr := m.inner.Embed(ctx, req)
	if eerr != nil {
		m.failed(ctx, payer, req.Project, req.Model, bill, meter.Usage{TotalTokens: toks}, h)
		return vecs, eerr
	}
	m.settle(ctx, payer, req.Project, req.Model, bill, meter.Usage{TotalTokens: toks}, h)
	return vecs, nil
}

// Rerank meters exactly as Embed does: the query and documents held at the model's
// listed price, the debit what the model API priced the call at.
func (m *meteredAI) Rerank(ctx context.Context, req *infer.RerankRequest) ([]float64, error) {
	if req == nil || len(req.Documents) == 0 {
		return m.inner.Rerank(ctx, req)
	}
	toks := EstTokens(append([]string{req.Query}, req.Documents...)...)
	payer, err := payerOf(req.Payer, req.Org)
	if err != nil {
		return nil, err
	}
	h, err := m.reserve(ctx, payer, req.Project, m.prices.hold(ctx, req.Model, toks, 0))
	if err != nil {
		return nil, err
	}
	defer h.Release()
	ctx, bill := infer.WithBill(ctx)
	scores, rerr := m.inner.Rerank(ctx, req)
	if rerr != nil {
		m.failed(ctx, payer, req.Project, req.Model, bill, meter.Usage{TotalTokens: toks}, h)
		return scores, rerr
	}
	m.settle(ctx, payer, req.Project, req.Model, bill, meter.Usage{TotalTokens: toks}, h)
	return scores, nil
}

// Transcribe meters a transcription through the same one path as Embed. A
// recording is priced by its length, which no price list states in tokens, so the
// hold is the one-cent floor that keeps the gate running; the debit is what the
// model API priced the transcription at.
//
// Every metered wrapper carries it (they embed *meteredAI), so a caller
// type-asserting types.Transcriber against deps.AI reaches the transport's
// speech path through the meter, never around it.
func (m *meteredAI) Transcribe(ctx context.Context, req *infer.TranscribeRequest) (string, error) {
	t, ok := m.inner.(infer.Transcriber)
	if !ok {
		return "", infer.ErrNoTranscriber
	}
	if req == nil || len(req.Audio) == 0 {
		return t.Transcribe(ctx, req)
	}
	payer, err := payerOf(req.Payer, req.Org)
	if err != nil {
		return "", err
	}
	h, err := m.reserve(ctx, payer, req.Project, holdFloorCents)
	if err != nil {
		return "", err
	}
	defer h.Release()
	ctx, bill := infer.WithBill(ctx)
	text, terr := t.Transcribe(ctx, req)
	if terr != nil {
		m.failed(ctx, payer, req.Project, req.Model, bill, meter.Usage{}, h)
		return text, terr
	}
	m.settle(ctx, payer, req.Project, req.Model, bill, meter.Usage{TotalTokens: max(1, EstTokens(text))}, h)
	return text, nil
}

// named answers WHO an inference is for, on the request that travels.
//
// A request that STATES an actor keeps it, and that ordering is the whole rule: a
// run executes on a detached context long after the asker's HTTP request is gone,
// so the runner carries the person it acts for and nothing here may overwrite
// them. Every other caller is a live one, and the validated identity the boundary
// already minted IS the answer — no second header, no second lookup, and nothing
// a caller sends can name anyone but themselves.
//
// It is here because this is the ONE wrapper every inference passes through, so
// an inference cannot be made anonymous by a call site forgetting a field — the
// same reason the gate and the debit live here rather than at twelve callers.
//
// It returns a COPY when it has something to add: the caller built the value and
// may reuse it, so the actor is stated on the request that goes to the gateway,
// never written back into theirs.
func named(ctx context.Context, req *infer.ChatRequest) *infer.ChatRequest {
	if req == nil || strings.TrimSpace(req.Actor) != "" {
		return req
	}
	c, ok := Request(ctx)
	if !ok {
		return req // no request behind this call, and no person to invent
	}
	actor := principal.Actor(c)
	if actor == "" {
		return req
	}
	r := *req
	r.Actor = actor
	return &r
}

// payerOf is the account an inference is charged to: the one the request names.
//
// AN ORG IS NOT A PAYER. A request that reads an org's data and names nobody to pay
// for it is refused here, before a model is asked: in the signup org the org's own
// account is the platform's pool, and standing the org in for a missing payer is
// exactly how a stranger's calls were billed to Hanzo. The caller resolves the
// payer where the work started (principal.Payer, cloud.PayerOf, a payer persisted
// on the row a background job runs from) and carries it on the request.
//
// A request with no org is the platform's own work — a system call with no
// customer behind it — and runs billed to nobody, as it always has.
func payerOf(payer account.Account, org string) (account.Account, error) {
	if payer.Zero() && strings.TrimSpace(org) != "" {
		return account.Account{}, ErrNoLedger
	}
	return payer, nil
}

// reserve is the pre-call balance/budget/freeze check. It COMMITS the call's
// worst-case cost before checking, so the balance is weighed against everything
// this pod already has in flight, and returns the hold the settlement releases.
//
// The returned hold is never nil, so a caller may unconditionally defer its
// release; a system call (an unattributable payer) is not gated and holds nothing.
func (m *meteredAI) reserve(ctx context.Context, payer account.Account, project string, cents int64) (*meter.Hold, error) {
	if payer.Zero() {
		return &meter.Hold{}, nil
	}
	h := meter.HoldOf(&m.inflight, payer.Subject(), cents)
	// Commit FIRST, then weigh. The figure the balance must cover is this call's
	// cost PLUS every other call already committed — which is what makes two
	// simultaneous callers see each other instead of both clearing the same cents.
	committed := h.Commit(ctx)
	if err := m.gate(ctx, payer, project, committed); err != nil {
		h.Release()
		return &meter.Hold{}, err
	}
	return h, nil
}

// gate asks the ONE metering path whether the payer can afford cents right now. A
// system call (no payer) is not gated: there is no customer to bill.
func (m *meteredAI) gate(ctx context.Context, payer account.Account, project string, cents int64) error {
	if payer.Zero() {
		return nil
	}
	// project rides the ChatRequest/EmbedRequest value (internal S2S), not a
	// server-minted identity claim, so it is unvalidated here → a project-scoped
	// cap stays soft. The request-edge BillingGate already hardens the validated
	// project axis for the inbound LLM path.
	return m.meter.Authorize(ctx, payer, project, false, AIMeterProvider, cents)
}

// atMost is the most completion a chat can buy: what the caller asked for, else the
// most the MODEL can produce.
//
// The ceiling is per-MODEL and comes from the catalog, never from a constant. A
// constant is wrong by construction — it caps a 1M-context model at whatever
// number was typed, and it is one release out of date the moment a model ships.
// This estate has paid for that twice already: ai/model's deleted name-matching
// table gave deepseek-v4-pro 16384 and 402'd every long prompt, and glm-5.2
// dead-ended /compact on a stale 16K fallback. Both were fixed the same way —
// declare it in models.yaml, resolve it here.
//
// It does NOT write the ceiling onto the request. What we reserve is a billing
// fact; req.MaxTokens is the CALLER's, and forwarding a limit they never asked
// for silently truncates their answer. The reservation is sound regardless: a
// model cannot exceed its own max output, so the bound holds whether or not we
// restate it on the wire.
func atMost(req *infer.ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return completionCeiling(req.Model)
}

// ceilingOf resolves a model's max completion length. Installed at wire-up from
// the model catalog (see SetCompletionCeiling); nil until then.
var ceilingOf func(model string) int

// SetCompletionCeiling installs the per-model completion-ceiling lookup the
// prepaid gate reserves against. Called once at startup by the package that
// links the model catalog (apps/ai), so package cloud states WHAT it needs
// without importing where the answer lives — the same client shape the AI module
// uses for SetContextWindowResolver.
func SetCompletionCeiling(f func(model string) int) { ceilingOf = f }

// completionCeiling answers the most tokens a completion of `model` can be: the
// catalog's number when it declares one, else the floor.
func completionCeiling(model string) int {
	if ceilingOf != nil {
		if n := ceilingOf(model); n > 0 {
			return n
		}
	}
	return maxCompletionTokens()
}

// defaultMaxCompletionTokens is the FLOOR used when the catalog declares nothing
// for a model — a deployment with no models.yaml, or a model reaching the
// gateway before its entry lands. It is not a per-model answer and must never be
// used as one; MaxOutput in the catalog is the answer.
//
// A floor, not a guess, in the same sense as ai/model.DefaultContextLength: the
// value that is safe across the lineup. It is only ever RESERVED, never charged
// (settlement debits the model API's price and releases the rest) and never sent
// upstream, so it cannot truncate an answer. Its only effect is which nearly
// empty wallets are refused early — so it is set generously. Ops overrides it
// per deployment with CLOUD_AI_MAX_COMPLETION_TOKENS.
const defaultMaxCompletionTokens = 32768

// maxCompletionTokens resolves the assumed completion ceiling. A
// negative/invalid value falls through to the default, so a typo cannot silently
// un-reserve every completion.
func maxCompletionTokens() int {
	s := strings.TrimSpace(os.Getenv("CLOUD_AI_MAX_COMPLETION_TOKENS"))
	if s == "" {
		return defaultMaxCompletionTokens
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultMaxCompletionTokens
	}
	return n
}

// settle debits what the model API priced the call at — the ONE place an inference
// debit is decided — and answers the price charged, nil when nothing was.
//
// A call nobody priced is charged nothing, and said so: there is no second price
// to fall back to, and a guess billed as a price is how this meter came to charge
// $2 a million for every model. It happens when the call did not cross the plane —
// a static key, or a fleet with no `ai` — and then the gateway that answered priced
// it for whoever held the credential. A support session's call is charged nothing
// either: its use is the operator's, not the person's (impersonation.go), and the
// gate above weighed the person's wallet as it does at the edge. The hold is given
// back either way.
func (m *meteredAI) settle(ctx context.Context, payer account.Account, project, model string, bill *infer.Bill, u meter.Usage, h *meter.Hold) *int64 {
	nanos, told := bill.Nanos()
	if !told {
		h.Release()
		if m.log != nil && !payer.Zero() {
			m.log.Warn("AI call priced by nobody — not charged", "payer", payer.Subject(), "model", model)
		}
		return nil
	}
	if op := PayerOf(ctx).Operator; op != "" {
		h.Release()
		if m.log != nil {
			m.log.Info("AI call in a support session — not charged", "operator", op, "payer", payer.Subject(), "model", model)
		}
		return nil
	}
	m.record(payer, project, model, u, nanos, h)
	return &nanos
}

// failed settles a call that failed here after the model API priced it — an answer
// with no choices, a tool call this process could not read. The vendor was paid
// for it and the same call costs any other caller the same, so the told price is
// charged; a failure nothing priced (a refusal, a dropped connection) is not.
func (m *meteredAI) failed(ctx context.Context, payer account.Account, project, model string, bill *infer.Bill, u meter.Usage, h *meter.Hold) {
	if _, told := bill.Nanos(); told {
		m.settle(ctx, payer, project, model, bill, u, h)
	}
}

// record debits nanos to the payer, attributed to the project scope + model. A
// system call (no payer) is recorded nowhere but logged, so any unattributed
// inference is detectable rather than silent. h is the pre-call reservation this
// debit settles: it is released once the debit REACHES the ledger, so the balance
// the next gate reads has already had this call taken out of it.
func (m *meteredAI) record(payer account.Account, project, model string, u meter.Usage, nanos int64, h *meter.Hold) {
	if payer.Zero() {
		h.Release()
		if m.log != nil {
			m.log.Warn("AI call with no payer — inference not attributed", "model", model, "nanos", nanos)
		}
		return
	}
	u.Amount = hz.New(decimal.New(nanos, 9), creditUSD)
	u.Project = project
	u.Model = model
	u.Service = AIMeterProvider
	m.meter.record(payer, AIMeterProvider, u, h.Release)
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
// used to size the pre-call hold before the call's real usage is known.
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

// defaultBYOFloorMicros is the per-CALL BYO inference fee, in micro-USD ($0.0001).
// A BYO call runs on the org's OWN provider token — the provider already billed the
// org for the compute — so Hanzo charges only this fee for gating, metering and
// observing it, never an inference price (that would double-bill). It is a fee per
// call rather than a share of a price because a BYO model is one the model API
// never priced: there is no price here to take a share of. It forces every /ai/run
// to reserve at least a cent (so a frozen / broke / over-cap org is refused) and to
// leave a debit row. Overridable; 0 opts out (an explicit ops choice).
const defaultBYOFloorMicros int64 = 100

// BYOFloorMicros resolves the per-call BYO fee (micro-USD) from CLOUD_AI_BYO_FLOOR_UUSD,
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

// holdFloorCents is the least any in-process call holds: one cent, so the gate always
// runs and an empty or frozen wallet is refused before a model is asked, whatever
// the model lists at — a free model included.
const holdFloorCents int64 = 1

// priceList is what each model lists at, as the model API that bills it answers
// (ai_rates), cached for a minute — the price a hold is weighed at. It is never a
// charge: the charge is what the model API tells the call's Bill.
type priceList struct {
	mu    sync.Mutex
	rates map[string]listed
	ask   func(ctx context.Context, model string) (ModelPrice, bool, error)
}

// listed is one model's list price, in nano-USD a token, and until when it holds.
type listed struct {
	in, out float64
	ok      bool
	until   time.Time
}

// modelPrices is the price list every meter in this process holds against.
var modelPrices = &priceList{ask: askRates}

// listTTL is how long a list price is held before it is asked again.
const listTTL = time.Minute

// HoldMicros is what the meter holds for a call before it is made, in micro-USD: the
// most it can cost at the model's listed price (prompt tokens at the input price,
// completion tokens at the output price), never under the one-cent floor. An agent
// budget weighs a call by the same figure, so a budget and the wallet refuse alike.
func HoldMicros(ctx context.Context, model string, prompt, completion int) int64 {
	return modelPrices.micros(ctx, model, prompt, completion)
}

// micros is the hold in micro-USD, rounded up, and never under holdFloorCents.
func (p *priceList) micros(ctx context.Context, model string, prompt, completion int) int64 {
	return max(int64(math.Ceil(p.nanos(ctx, model, prompt, completion)/1000)), holdFloorCents*10_000)
}

// hold is the hold in whole cents, the meter's unit.
func (p *priceList) hold(ctx context.Context, model string, prompt, completion int) int64 {
	return MicrosToGateCents(p.micros(ctx, model, prompt, completion))
}

// nanos prices prompt and completion tokens at the model's list price.
func (p *priceList) nanos(ctx context.Context, model string, prompt, completion int) float64 {
	if p == nil {
		return 0
	}
	l := p.lookup(ctx, strings.ToLower(strings.TrimSpace(model)))
	if !l.ok {
		return 0
	}
	return float64(max(prompt, 0))*l.in + float64(max(completion, 0))*l.out
}

func (p *priceList) lookup(ctx context.Context, model string) listed {
	if model == "" || p.ask == nil {
		return listed{}
	}
	now := time.Now()
	p.mu.Lock()
	l, ok := p.rates[model]
	p.mu.Unlock()
	if ok && now.Before(l.until) {
		return l
	}
	l = listed{until: now.Add(listTTL)}
	price, priced, err := p.ask(ctx, model)
	if err != nil {
		return listed{}
	}
	if priced {
		l.in, l.out, l.ok = perToken(price.Input), perToken(price.Output), true
	}
	p.mu.Lock()
	if p.rates == nil || len(p.rates) >= listMax {
		p.rates = map[string]listed{} // a caller names the model, so the list is bounded
	}
	p.rates[model] = l
	p.mu.Unlock()
	return l
}

// listMax bounds how many models' prices are held at once.
const listMax = 4096

// perToken reads dollars a million tokens as nano-USD a token.
func perToken(perMillion string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(perMillion), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f * 1000
}

// askRates asks the model API's own process what model lists at (ai_rates): the
// same row it bills the model from.
func askRates(ctx context.Context, model string) (ModelPrice, bool, error) {
	ask := peersOf().ModelPriceOf
	if ask == nil {
		return ModelPrice{}, false, ErrNoPeer
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return ask(ctx, model)
}

// creditUSD is the cloud's ONE money unit: US dollars at 18-decimal precision, the
// ledger's own, so a nano-USD price converts to it exactly.
var creditUSD = hz.Currency{Code: "USD", Decimals: 18, Symbol: "$", Numeric: "840", Name: "US Dollar credit"}
