// Package translate serves POST /v1/translate — the ONE translation surface, two
// tiers behind one endpoint, one auth path, one meter (HIP-0516).
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
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

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

// Mount wires the /v1/translate surface: the quality engine over the model plane
// deps.AI already gates and meters, and the bulk engine over the MADLAD backend
// named by TRANSLATE_BULK_URL (unset ⇒ the tier answers 503).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("translate.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("translate.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("translate.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "translate")
	mounted = &state{
		stores:  cloud.NewOrgStore[*memory](deps.DataDir, "translate", open, cloud.WithDurable(deps.Durable), cloud.WithStoreLogger(b.Log)),
		quality: newQuality(deps.AI, deps.AIDefaultModel),
		bulk:    newBulk(),
	}
	s := &cloud.Service[*state]{Base: b, State: mounted}
	app.Post("/v1/translate", cloud.Handle(s, serve))
	g := app.Group("/v1/translate")
	g.Get("/memory", cloud.Handle(s, list))
	g.Put("/memory", cloud.Handle(s, review))
	b.Log.Info("translate mounted", "prefix", "/v1/translate", "tiers", "quality,bulk", "bulk_backend", bulkURL() != "")
	return nil
}

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

	mem, err := s.State.stores.For(org, "")
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
	payer := principal.Ledger(c)
	project, projectValidated := principal.ValidatedProject(c)
	if tier == TierBulk {
		if err := s.Bill.Gate(ctx, payer, project, projectValidated, meterKind, cloud.MicrosToGateCents(bulkMicros(usage.Characters))); err != nil {
			return cloud.DenyResource(c, err)
		}
	}

	res, err := s.State.engine(tier).Translate(ctx, Job{
		Texts: missText, Source: source, Target: target, Format: format, Glossary: in.Glossary,
		Org: org, BillingOrg: payer, Project: project,
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
			AmountMicros: bulkMicros(usage.Characters),
			RequestID:    c.RequestID(), ClientIP: cloud.ClientIP(c),
		})
	}

	// 3. Record what the engine produced. A machine write can only create a row or
	// refresh one still in StateMachine, so an approved or published string that
	// arrived here by another key is never trampled.
	now := time.Now().Unix()
	for n, i := range missAt {
		e := Entry{
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

// list answers GET /v1/translate/memory — the review lane's read: the org's own
// entries, newest first, optionally narrowed to one target language or one state.
func list(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to read the translation memory")
	}
	target := ""
	if v := strings.TrimSpace(c.Query("target")); v != "" {
		t, err := lang(v)
		if err != nil {
			return zip.ErrBadRequest("target must be a language tag")
		}
		target = t
	}
	var st State
	if v := strings.TrimSpace(c.Query("state")); v != "" {
		filter, err := stateOf(State(v), true)
		if err != nil {
			return zip.ErrBadRequest(err.Error())
		}
		st = filter
	}
	limit := listLimit
	if n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit"))); err == nil && n > 0 {
		limit = min(n, maxListLimit)
	}
	mem, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	rows, err := mem.list(c.Context(), target, st, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// ReviewRequest is the human write: the same tuple a translate call carries, plus
// the reviewed text and its new position on the ladder.
type ReviewRequest struct {
	Source   string            `json:"source"`
	Target   string            `json:"target"`
	Tier     Tier              `json:"tier"`
	Glossary map[string]string `json:"glossary"`
	Text     string            `json:"text"`
	State    State             `json:"state"`
}

// review answers PUT /v1/translate/memory — the review lane's write. A human write
// always wins over the stored value, and once it lands at approved or published no
// machine write can move it again.
func review(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to review translations")
	}
	var in ReviewRequest
	if err := c.Bind(&in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Source) == "" {
		return zip.ErrBadRequest("source is required")
	}
	if len(in.Source) > maxChars || len(in.Text) > maxChars {
		return zip.ErrBadRequest(fmt.Sprintf("a string may not exceed %d characters", maxChars))
	}
	target, err := lang(in.Target)
	if err != nil {
		return zip.ErrBadRequest("target must be a language tag (e.g. \"es\", \"pt-BR\")")
	}
	tier, err := tierOf(in.Tier)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	// machine is engine-only: a human may not demote a string back into the churn.
	st, err := stateOf(in.State, false)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	mem, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	e := Entry{
		Source: in.Source, Target: target, Tier: tier, Glossary: version(in.Glossary),
		Text: in.Text, State: st, Actor: c.User(), UpdatedAt: time.Now().Unix(),
	}
	if err := mem.put(c.Context(), e, true); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}
	return c.JSON(http.StatusOK, e)
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
