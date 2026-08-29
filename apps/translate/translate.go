// Package translate is text in, the same text out in the language you asked for.
//
// POST /v1/translate is the ONE translation surface: two tiers behind one
// endpoint, one auth path, one meter (HIP-0516).
//
//	POST /v1/translate  { text | batch[], target, source?, tier?, glossary?, format? }
//	                 -> { translations[], detected_source?, tier, usage }
//
// `tier` picks the engine and defaults to quality:
//
//   - quality routes to the model plane (deps.AI — zen through the gateway), which
//     carries context, terminology and tone.
//   - bulk routes to MADLAD-400 under CTranslate2 (engine.go), for high-volume,
//     low-latency work where a model is overkill. A deployment with no bulk backend
//     answers 503; bulk NEVER falls back to quality, so a caller is never quietly
//     served — or charged — at a tier it did not ask for.
//
// The translation memory (memory.go) is NORMATIVE, not a cache. Every string keys
// on (source_text, target, glossary_version, tier); a hit returns the stored value
// unchanged, so only new or changed source strings reach an engine. That is what
// makes a locale rebuild idempotent under a non-deterministic model, and it is what
// makes the bill proportional to what actually changed.
//
// The review lane rides the same memory: an entry carries a state on the ladder
// machine -> suggested -> approved -> published, and human work is IMMUNE to
// machine churn (memory.put). A rebuild can never silently revert an approved
// string.
//
// Tenancy: org-scoped by the validated principal. The memory is a per-org SQLite
// file (HIP-0302 physical isolation), so a request cannot reach another org's
// memory. Submitted text is customer content, held only in that org's own memory.
package translate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The PROSE for the translate endpoint. Its two review-lane siblings are typed ops
// whose descriptions zipdoc lifts from their doc comments; this one is raw by
// construction (serve, below, names the blocker), so there is no comment to lift
// and the subsystem's ONLY product route was reaching the document — and every SDK
// and CLI generated from it — as an operationId and nothing else.
//
// Declared through the registry openapi.Register shares, so it renders only while the
// router actually serves the route.
func init() {
	openapi.Describe("/v1/translate", http.MethodPost,
		"Translate a string or a batch into one target language",
		"Returns one translation per input string, in input order, each carrying where it sits on "+
			"the review ladder and whether it came from your memory rather than an engine — plus a "+
			"usage block of REAL counts (strings, cached, translated, and the source characters that "+
			"actually reached an engine). Send `text` for one string or `batch` for many, never both. "+
			"When you name no `source`, the detected one is reported back.\n\n"+
			"THE TRANSLATION MEMORY IS CONSULTED FIRST AND IT IS NORMATIVE, NOT A CACHE. Every string "+
			"keys on (source text, target, glossary version, tier); a hit is returned VERBATIM and "+
			"never re-translated, which is what makes a locale rebuild idempotent under a "+
			"non-deterministic model and the bill proportional to what actually changed. Misses go to "+
			"the engine and are written back at state `machine`. Editing a glossary term changes the "+
			"key, so a stale rendering can never be served.\n\n"+
			"IT CANNOT TRAMPLE REVIEWED WORK. A write from this route may create an entry or refresh "+
			"one still at `machine`, and nothing else — a string a human moved to approved or "+
			"published through the memory review lane survives every rebuild, and comes back here "+
			"unchanged. The memory is the caller's OWN org's, a separate store per org: the source "+
			"text you send is customer content and lands nowhere else. Read it back or review it at "+
			"/v1/translate/memory.\n\n"+
			"`tier` picks the engine and defaults to quality — the model plane, which carries context, "+
			"terminology and tone, and which bills its own tokens, so nothing is charged twice here. "+
			"`bulk` is the high-volume engine and is metered HERE, on the source characters that "+
			"reached it: a fully-cached rebuild reports zero characters and costs zero. BULK NEVER "+
			"FALLS BACK TO QUALITY — on a deployment that does not serve it the answer is 503 for that "+
			"tier, so a caller is never quietly served, or charged, at a tier it did not ask for. A "+
			"bulk request beyond its balance is refused with the nested "+
			"{\"error\":{\"code\",\"message\"}} body at 402/503.\n\n"+
			"`target` IS CHECKED FOR SHAPE, NOT FOR SUPPORT: anything BCP-47-shaped is accepted (`es`, "+
			"`pt-BR`), anything else is 400. There is no unsupported-language error — a well-formed "+
			"tag no engine can actually render is passed straight through, and whatever comes back is "+
			"what gets stored and returned. `format` (text, html, markdown) tells the engine what "+
			"markup to preserve; `glossary` fixes terms verbatim.\n\n"+
			"Requires a validated principal — 401 without one, and the org is always that principal's. "+
			"At most 512 strings per call and 32768 characters per string; an engine that fails or "+
			"answers a reply that does not cover every input is 502, and nothing is stored.")
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/translate openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Tier selects the engine. Two values, one endpoint.
type Tier string

const (
	TierQuality Tier = "quality"
	TierBulk    Tier = "bulk"
)

// Format tells an engine what markup the source carries so placeholders and tags
// survive the round trip.
type Format string

const (
	FormatText     Format = "text"
	FormatHTML     Format = "html"
	FormatMarkdown Format = "markdown"
)

const (
	// maxBatch bounds one request's string count and maxChars one string's length:
	// a translate call is a bounded unit of work, not a bulk-upload channel.
	maxBatch = 512
	maxChars = 32 * 1024
	// listLimit / maxListLimit bound a review-lane page.
	listLimit    = 200
	maxListLimit = 1000
	// meterKind is the commerce product axis a bulk debit attributes to.
	meterKind = "translate"
)

// langRE bounds a language tag to BCP-47 shape. Tags are stored and reach an
// engine's instruction, so they are validated at the boundary rather than trusted.
var langRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// state is the subsystem's own data: the per-org memories plus one engine per tier.
type state struct {
	stores  *cloud.OrgStore[*memory]
	quality Engine
	bulk    Engine
}

// engine resolves a tier to its engine. The ONE place a tier becomes an engine.
func (s *state) engine(t Tier) Engine {
	if t == TierBulk {
		return s.bulk
	}
	return s.quality
}

var mounted *state

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single entry point for a validated org —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
func storeFor(s *cloud.Service[*state], org string) (*memory, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires the /v1/translate surface: the quality engine over the model plane
// deps.AI already gates and meters, and the bulk engine over the MADLAD backend
// named by TRANSLATE_BULK_URL (unset ⇒ the tier answers 503).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("translate.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("translate.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "translate")
	mounted = &state{
		stores:  cloud.NewOrgStore[*memory](b, "translate", open),
		quality: newQuality(deps.AI, cloud.DefaultModel),
		bulk:    newBulk(),
	}
	s := &cloud.Service[*state]{Base: b, State: mounted}

	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the validated org, and for the review write the
	// request the attribution is read off, are parked on the context by that
	// root install.

	app.Post("/v1/translate", cloud.Handle(s, serve))
	g := app.Group("/v1/translate")
	o := ops{s: s}
	zip.Get(g, "/memory", o.list)
	zip.Put(g, "/memory", o.review)
	b.Log.Info("translate mounted", "prefix", "/v1/translate", "tiers", "quality,bulk", "bulk_backend", bulkURL() != "")
	return nil
}

// ops binds the mounted Service so each review-lane op can be a method value — the
// only bound form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[*state] }

// Shutdown closes every open per-org memory.
func Shutdown() error {
	if mounted == nil || mounted.stores == nil {
		return nil
	}
	return mounted.stores.CloseAll()
}

// ---- wire contract ----

// Request is the POST /v1/translate body. Exactly one of Text or Batch carries the
// work; Batch preserves order in the reply.
type Request struct {
	Text     string            `json:"text"`
	Batch    []string          `json:"batch"`
	Target   string            `json:"target"`
	Source   string            `json:"source"`
	Tier     Tier              `json:"tier"`
	Glossary map[string]string `json:"glossary"`
	Format   Format            `json:"format"`
}

// Translation is one string's result: the translation, where on the review ladder
// it sits, and whether it came from the memory rather than an engine.
type Translation struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	State  State  `json:"state"`
	Cached bool   `json:"cached"`
}

// Usage reports what the call actually did — real counts, never an estimate.
// Characters counts only the source that REACHED an engine, which is what the bulk
// tier bills on; a fully-cached rebuild therefore reports (and costs) zero.
type Usage struct {
	Strings    int `json:"strings"`
	Cached     int `json:"cached"`
	Translated int `json:"translated"`
	Characters int `json:"characters"`
}

// Response is the POST /v1/translate reply.
type Response struct {
	Translations   []Translation `json:"translations"`
	DetectedSource string        `json:"detected_source,omitempty"`
	Tier           Tier          `json:"tier"`
	Usage          Usage         `json:"usage"`
}

// ---- handlers ----

// serve answers POST /v1/translate for the caller's OWN org: resolve every string
// against the org's memory, send only the misses to the tier's engine, record what
// the engine returned, and reply in input order.
//
// UNTYPED BY DESIGN — the bulk tier's SPEND DENIAL is the money wire. A gate or
// engine refusal answers cloud.DenyResource (below), the fleet-wide NESTED
// {"error":{"code","message"}} contract at 402/503 that the console routes to a
// top-up prompt, and DenyResource writes that object BARE. A typed op's ONLY refusal
// is a returned error, which zip renders as RFC 9457 problem members
// (type/title/status/detail, plus code): the nested body rides Detail intact and
// gains those members beside it. Converting is therefore a change to the MONEY wire,
// owned by whoever owns that wire — the same call apps/agents and apps/guide record.
func serve(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to translate")
	}
	var in Request
	if err := c.Bind(&in); err != nil {
		return err
	}
	texts, err := in.strings()
	if err != nil {
		return err
	}
	target, err := lang(in.Target)
	if err != nil {
		return zip.ErrBadRequest("target must be a language tag (e.g. \"es\", \"pt-BR\")")
	}
	source := ""
	if strings.TrimSpace(in.Source) != "" {
		if source, err = lang(in.Source); err != nil {
			return zip.ErrBadRequest("source must be a language tag (e.g. \"en\")")
		}
	}
	tier, err := tierOf(in.Tier)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	format, err := formatOf(in.Format)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	glossary := version(in.Glossary)

	mem, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	ctx := c.Context()

	// 1. The memory is consulted FIRST and its value is returned unchanged. This is
	// the idempotence contract, not an optimisation: an unchanged source string
	// yields a byte-identical translation on every rebuild.
	out := make([]Translation, len(texts))
	var missAt []int
	var missText []string
	for i, t := range texts {
		e, found, err := mem.get(ctx, key(t, target, glossary, tier))
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
		}
		if found {
			out[i] = Translation{Source: t, Text: e.Text, State: e.State, Cached: true}
			continue
		}
		missAt = append(missAt, i)
		missText = append(missText, t)
	}

	usage := Usage{Strings: len(texts), Cached: len(texts) - len(missAt), Translated: len(missAt), Characters: chars(missText)}
	resp := Response{Translations: out, Tier: tier, Usage: usage}
	if len(missAt) == 0 {
		return c.JSON(http.StatusOK, resp)
	}

	// 2. bulk is not on the model plane, so it carries its own per-org gate+meter —
	// the same ResourceMeter contract every non-LLM unit uses. quality needs none:
	// deps.AI already authorizes and debits its own tokens, and a second charge here
	// would double-bill.
	payer := principal.Payer(c)
	project, projectValidated := principal.ValidatedProject(c)
	if tier == TierBulk {
		if err := s.Bill.Gate(ctx, payer, project, projectValidated, meterKind, cloud.MicrosToGateCents(bulkMicros(ctx, usage.Characters))); err != nil {
			return cloud.DenyResource(c, err)
		}
	}

	res, err := s.State.engine(tier).Translate(ctx, Job{
		Texts: missText, Source: source, Target: target, Format: format, Glossary: in.Glossary,
		Org: org, BillingOrg: payer.Subject(), Project: project,
	})
	if err != nil {
		if errors.Is(err, ErrNoEngine) {
			return zip.Errorf(http.StatusServiceUnavailable, "the %s tier is not configured on this deployment", tier)
		}
		if errors.Is(err, metering.ErrInsufficientBalance) || errors.Is(err, metering.ErrSpendCapExceeded) {
			return cloud.DenyResource(c, err)
		}
		s.Log.Warn("translate engine failed", "tier", tier, "target", target, "err", err)
		return zip.Errorf(http.StatusBadGateway, "translation failed")
	}

	if tier == TierBulk {
		s.Bill.MeterUsage(payer, meterKind, metering.Usage{
			Model: string(TierBulk), Project: project, Actor: c.User(),
			AmountMicros: bulkMicros(ctx, usage.Characters),
			RequestID:    c.RequestID(), ClientIP: cloud.ClientIP(c),
		})
	}

	// 3. Record what the engine produced. A machine write can only create a row or
	// refresh one still in StateMachine, so an approved or published string that
	// arrived here by another key is never trampled.
	now := time.Now().Unix()
	for n, i := range missAt {
		e := MemoryEntry{
			Source: texts[i], Target: target, Tier: tier, Glossary: glossary,
			Text: res.Texts[n], State: StateMachine, UpdatedAt: now,
		}
		if err := mem.put(ctx, e, false); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
		}
		out[i] = Translation{Source: texts[i], Text: e.Text, State: e.State}
	}
	if source == "" {
		resp.DetectedSource = res.Detected
	}
	return c.JSON(http.StatusOK, resp)
}

// MemoryQuery narrows a review-lane read to part of the org's translation memory.
// Every field is optional; an omitted one does not filter.
type MemoryQuery struct {
	// Target narrows to one target language tag (BCP-47, e.g. "es" or "pt-BR").
	Target string `json:"target"`
	// State narrows to one position on the review ladder: machine, suggested,
	// approved or published.
	State State `json:"state"`
	// Limit caps the rows returned. Non-positive or unparseable means the server
	// default (200); the ceiling is 1000.
	Limit int `json:"limit"`
}

// MemoryPage is the review lane's read result: the org's own memory entries, newest
// first. Data is always a (possibly empty) array, never null.
type MemoryPage struct {
	// Data is the matching memory entries, newest first.
	Data []MemoryEntry `json:"data"`
}

// List returns the org's own translation-memory entries, newest first, optionally
// narrowed to one target language and/or one position on the review ladder. It is
// the review lane's read: what a human reviewer works through.
//
// The org is ALWAYS the validated principal's org, never a request field, so one
// tenant can never read another's memory — the entries hold customer source text.
func (o ops) list(ctx context.Context, in *MemoryQuery) (*MemoryPage, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to read the translation memory")
	}
	target := ""
	if v := strings.TrimSpace(in.Target); v != "" {
		t, err := lang(v)
		if err != nil {
			return nil, zip.ErrBadRequest("target must be a language tag")
		}
		target = t
	}
	var st State
	if v := strings.TrimSpace(string(in.State)); v != "" {
		filter, err := stateOf(State(v), true)
		if err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		st = filter
	}
	// A non-positive limit is the server default, exactly as an unparseable one was:
	// zip's setScalar leaves an int field at zero when ?limit= cannot be parsed, so
	// `?limit=abc` and `?limit=0` and an absent limit all land here as 0.
	limit := listLimit
	if in.Limit > 0 {
		limit = min(in.Limit, maxListLimit)
	}
	mem, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	rows, err := mem.list(ctx, target, st, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	return &MemoryPage{Data: rows}, nil
}

// ReviewRequest is the human write: the same tuple a translate call carries, plus
// the reviewed text and its new position on the ladder.
type ReviewRequest struct {
	// Source is the ORIGINAL string this entry translates. Required; part of the
	// entry's identity, so a different source is a different entry.
	Source string `json:"source"`
	// Target is the target language tag (BCP-47, e.g. "es" or "pt-BR"). Required;
	// part of the entry's identity.
	Target string `json:"target"`
	// Tier is the engine tier the entry belongs to, quality (the default) or bulk.
	// Part of the entry's identity: the two tiers keep separate renderings.
	Tier Tier `json:"tier"`
	// Glossary is the terminology the entry was translated under. Its VERSION — the
	// digest of the sorted terms — is part of the entry's identity, so editing a term
	// yields a new entry rather than overwriting the old rendering.
	Glossary map[string]string `json:"glossary"`
	// Text is the reviewed translation to store. A human write always wins over the
	// stored value.
	Text string `json:"text"`
	// State is the entry's new position on the review ladder: suggested, approved or
	// published. `machine` is engine-only and is refused here — a human may not demote
	// a string back into the churn.
	State State `json:"state"`
}

// Review records a human decision on one translation-memory entry, and returns the
// entry as stored. A human write always wins over the stored value, and once it lands
// at approved or published no machine write can move it again — which is what makes a
// locale rebuild safe to run against reviewed work.
//
// The org is ALWAYS the validated principal's org, never a request field, so a review
// can only ever land in the caller's own memory.
//
// Example: {"source": "Hello", "target": "es", "text": "Hola", "state": "approved"}
func (o ops) review(ctx context.Context, in *ReviewRequest) (*MemoryEntry, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to review translations")
	}
	if strings.TrimSpace(in.Source) == "" {
		return nil, zip.ErrBadRequest("source is required")
	}
	if len(in.Source) > maxChars || len(in.Text) > maxChars {
		return nil, zip.ErrBadRequest(fmt.Sprintf("a string may not exceed %d characters", maxChars))
	}
	target, err := lang(in.Target)
	if err != nil {
		return nil, zip.ErrBadRequest("target must be a language tag (e.g. \"es\", \"pt-BR\")")
	}
	tier, err := tierOf(in.Tier)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	// machine is engine-only: a human may not demote a string back into the churn.
	st, err := stateOf(in.State, false)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	mem, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	e := MemoryEntry{
		Source: in.Source, Target: target, Tier: tier, Glossary: version(in.Glossary),
		Text: in.Text, State: st, Actor: reviewer(ctx), UpdatedAt: time.Now().Unix(),
	}
	if err := mem.put(ctx, e, true); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	return &e, nil
}

// reviewer names the human a review is ATTRIBUTED to: the validated user id
// (X-User-Id), which principal.OrgFrom does not carry — the org says which tenant,
// not which person. Off the HTTP path there is no request and no actor, and the entry
// records an empty one rather than inventing a name, exactly as a pre-attribution row
// already reads.
func reviewer(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// ---- request parsing ----

// strings resolves the request's work: `text` OR `batch`, never both, and returns
// them in reply order.
func (r Request) strings() ([]string, error) {
	text := r.Text
	switch {
	case text != "" && len(r.Batch) > 0:
		return nil, zip.ErrBadRequest("send either text or batch, not both")
	case text != "":
		if len(text) > maxChars {
			return nil, zip.ErrBadRequest(fmt.Sprintf("a string may not exceed %d characters", maxChars))
		}
		return []string{text}, nil
	case len(r.Batch) == 0:
		return nil, zip.ErrBadRequest("text or batch is required")
	case len(r.Batch) > maxBatch:
		return nil, zip.ErrBadRequest(fmt.Sprintf("batch may not exceed %d strings", maxBatch))
	}
	for _, t := range r.Batch {
		if len(t) > maxChars {
			return nil, zip.ErrBadRequest(fmt.Sprintf("a string may not exceed %d characters", maxChars))
		}
	}
	return r.Batch, nil
}

func lang(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !langRE.MatchString(v) {
		return "", fmt.Errorf("not a language tag: %q", v)
	}
	return v, nil
}

// tierOf resolves the requested tier, defaulting to quality per HIP-0516.
func tierOf(t Tier) (Tier, error) {
	switch Tier(strings.TrimSpace(string(t))) {
	case "", TierQuality:
		return TierQuality, nil
	case TierBulk:
		return TierBulk, nil
	}
	return "", fmt.Errorf("tier must be %q or %q", TierQuality, TierBulk)
}

// formatOf resolves the source markup, defaulting to plain text.
func formatOf(f Format) (Format, error) {
	switch Format(strings.TrimSpace(string(f))) {
	case "", FormatText:
		return FormatText, nil
	case FormatHTML:
		return FormatHTML, nil
	case FormatMarkdown:
		return FormatMarkdown, nil
	}
	return "", fmt.Errorf("format must be %q, %q or %q", FormatText, FormatHTML, FormatMarkdown)
}

// stateOf validates a review-ladder state. machine is accepted only as a read
// filter (allowMachine); a human write may never set it.
func stateOf(s State, allowMachine bool) (State, error) {
	switch State(strings.TrimSpace(string(s))) {
	case StateMachine:
		if allowMachine {
			return StateMachine, nil
		}
	case StateSuggested:
		return StateSuggested, nil
	case StateApproved:
		return StateApproved, nil
	case StatePublished:
		return StatePublished, nil
	}
	return "", fmt.Errorf("state must be %q, %q or %q", StateSuggested, StateApproved, StatePublished)
}

func chars(texts []string) int {
	n := 0
	for _, t := range texts {
		n += len(t)
	}
	return n
}
