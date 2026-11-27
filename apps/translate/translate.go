package translate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// The PROSE for the translate endpoint. Its two review-lane siblings are typed ops
// whose descriptions zipdoc lifts from their doc comments; this one is raw by
// construction (serve, below, names the blocker), so there is no comment to lift
// and the subsystem's ONLY product route was reaching the document — and every SDK
// and CLI generated from it — as an operationId and nothing else.
//
// Declared through the registry openapi.Register shares, so it renders only while the
// router actually serves the route. The BODIES are declared there too (Brief in,
// Delivery out): without them the document carried no requestBody and no response,
// and every generated SDK offered post_translate() with no parameters at all.
func init() {
	spec.Register("/v1/translate", http.MethodPost, Brief{}, Delivery{})
	spec.Describe("/v1/translate", http.MethodPost,
		"Translate a string or a batch into one target language",
		"Returns one translation per input string, in input order, each carrying where it sits on "+
			"the review ladder and whether it came from your memory rather than an engine — plus a "+
			"usage block of REAL counts (strings, cached, translated, failed, and the source characters "+
			"of the strings an engine translated). Send `text` for one string or `batch` for many, never both. "+
			"When you name no `source`, the detected one is reported back.\n\n"+
			"THE TRANSLATION MEMORY IS CONSULTED FIRST AND IT IS NORMATIVE, NOT A CACHE. Every string "+
			"keys on (source text, declared source language, target, glossary version, tier, format, "+
			"model); a hit is returned VERBATIM and never re-translated, which is what makes a locale "+
			"rebuild idempotent under a non-deterministic model and the bill proportional to what "+
			"actually changed. The same text through another model or another format is another "+
			"entry. A string translated with its source detected also answers a later call that "+
			"declares one, so declaring `source` never throws away work done without it; one "+
			"translated under a declared source answers only that source. Misses go to the engine "+
			"and are written back at state `machine`. Editing a glossary term changes the key, so a "+
			"stale rendering can never be served.\n\n"+
			"IT CANNOT TRAMPLE REVIEWED WORK. A write from this route may create an entry or refresh "+
			"one still at `machine`, and nothing else — a string a human moved to approved or "+
			"published through the memory review lane survives every rebuild, and comes back here "+
			"unchanged. The memory is the caller's OWN org's, a separate store per org: the source "+
			"text you send is customer content and lands nowhere else. Read it back or review it at "+
			"/v1/translate/memory.\n\n"+
			"`tier` picks the engine and defaults to quality — the model plane, which carries context, "+
			"terminology and tone, and which bills its own tokens, so nothing is charged twice here. "+
			"`bulk` is the high-volume engine and is metered HERE, on the source characters of the "+
			"strings it translated: a fully-cached rebuild, or a string that failed, costs zero. BULK NEVER "+
			"FALLS BACK TO QUALITY — on a deployment that does not serve it the answer is 503 for that "+
			"tier, so a caller is never quietly served, or charged, at a tier it did not ask for. A "+
			"bulk request beyond its balance is refused with the nested "+
			"{\"error\":{\"code\",\"message\"}} body at 402/503.\n\n"+
			"`model` names the model the quality tier runs on, through the same metered model plane "+
			"as the default (enso-auto), which decides whether your org may use it exactly as it does "+
			"for chat. That is how an org points translation at a model it serves itself, on its own "+
			"linked GPU say: by the alias it registered for it. A name from an upstream model family "+
			"is rewritten to the default before it runs, so a model of your own must be named by your "+
			"org's alias; the reply's `model` names the one it runs on. `model` with `tier: bulk` is 400.\n\n"+
			"`target` IS CHECKED FOR SHAPE, NOT FOR SUPPORT: anything BCP-47-shaped is accepted (`es`, "+
			"`pt-BR`), anything else is 400. There is no unsupported-language error — a well-formed "+
			"tag no engine can actually render is passed straight through, and whatever comes back is "+
			"what gets stored and returned. `format` (text, html, markdown) tells the engine what "+
			"markup to preserve; `glossary` fixes terms verbatim.\n\n"+
			"A BATCH IS TRANSLATED IN PARTS of about 8 KB, each its own completion with a ceiling sized "+
			"to it. A part that comes back cut short (any finish other than stop), malformed, or "+
			"covering the wrong number of strings is asked again one string at a time. Every "+
			"translation must keep its source's shape: the same {placeholders}, printf conversions "+
			"in the same order, the same ICU arguments and selectors with an other branch (the words in "+
			"their branches are translated), the same tags with the same attributes (alt, title, "+
			"aria-label and placeholder are words), the same link targets and URLs; a string whose "+
			"shape moved is asked once more. A string that still fails comes back with `error` naming what went "+
			"wrong (a lost placeholder, an added URL) and no text, is not stored, and fails nothing "+
			"else.\n\n"+
			"THE WORK IS BOUNDED: at most two model calls per part, one held for every part still to "+
			"come, a stop after two parts in a row "+
			"return nothing, 90 seconds per call, and a stop when the caller disconnects. Strings the "+
			"call did not reach come back with `error` saying why. What came back is stored, so sending "+
			"the batch again asks only for the rest. When no string at all can be returned, or the "+
			"engine fails, the answer is 502 with code `translate_failed`.\n\n"+
			"Requires a validated principal — 401 without one, and the org is always that principal's. "+
			"At most 512 strings per call and 32768 characters per string (400 past either), and "+
			"262144 characters in all (413 past it: split the batch).")
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
	// maxBatch bounds one request's string count, maxChars one string's length, and
	// maxTotal all of a request's strings together: a translate call is a bounded
	// unit of work, not a bulk-upload channel. maxTotal is what keeps the parts a
	// call is asked in (chunkBudget) to a few dozen.
	maxBatch = 512
	maxChars = 32 * 1024
	maxTotal = 256 * 1024
	// listLimit / maxListLimit bound a review-lane page.
	listLimit    = 200
	maxListLimit = 1000
	// meterKind is the commerce product axis a bulk debit attributes to.
	meterKind = "translate"
	// feature is the marker this surface's telemetry carries as hanzo.feature: on
	// the translate span, the bulk call's span, and every completion the quality
	// tier asks of the model plane (infer.ChatRequest.Feature).
	feature = "translate"
)

// langRE bounds a language tag to BCP-47 shape. Tags are stored and reach an
// engine's instruction, so they are validated at the boundary rather than trusted.
var langRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// modelRE bounds a model id: a letter or digit, then up to 127 of the characters
// model ids and org aliases are spelled with (vendor/name, name:tag, name@rev). It
// is stored in the memory and sent to the model plane, so it is checked here.
var modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)

// deadline bounds the work one call asks of an engine. A var so a test can end a
// call in milliseconds.
var deadline = 90 * time.Second

// errClass is what a log line says of an engine's error: its kind, never its
// text, which can quote what the engine was sent.
func errClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, infer.ErrUpstreamBusy):
		return "busy"
	}
	return "engine"
}

// failed is the answer when no string came back whole, or the engine failed: a
// fixed sentence under a stable code, because a reason built from the call would
// carry the caller's text into the error the request's log line and span record.
func failed(msg string) error {
	return &zip.HTTPError{Status: http.StatusBadGateway, Code: "translate_failed", Msg: msg}
}

// tracer is the one this package opens its spans on. Read at the call, not held,
// so the process's provider is the one that records them.
func tracer() trace.Tracer { return otel.Tracer("hanzo.ai/cloud/translate") }

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
// through orgns.OrgNamespace — the single entry point for a validated org —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
func storeFor(s *cloud.Service[*state], org string) (*memory, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires the /v1/translate surface: the quality engine over the model plane
// deps.AI already gates and meters, and the bulk engine over the MADLAD backend
// named by TRANSLATE_BULK_URL (unset ⇒ the tier answers 503).
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("translate: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("translate.Use:  empty cloud.DataDir()")
	}
	b := cloud.NewBase(deps, "translate")
	mounted = &state{
		stores:  cloud.NewOrgStore[*memory](b, "translate", open),
		quality: newQuality(deps.AI),
		bulk:    newBulk(),
	}
	s := &cloud.Service[*state]{Base: b, State: mounted}

	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the validated org, and for the review write the
	// request the attribution is read off, are parked on the context by that
	// root install.

	translate := app.Group("/v1/translate")
	translate.Raw(http.MethodPost, "", cloud.Handle(s, serve))
	o := ops{s: s}
	g := app.Group("/v1/translate")
	g.Get("/memory", o.list)
	g.Put("/memory", o.review)
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

// Brief is the POST /v1/translate body: what a translator is handed — the text,
// the language it goes into, and the terms it must keep. Exactly one of Text or
// Batch carries the work; Batch preserves order in the reply.
//
// The Go names are the document's schema names (openapi.Register names a
// component after its type), so they are specific nouns: Usage is another app's
// schema already, and Request and Response collide with the HTTP types of the
// SDKs generated from the document.
type Brief struct {
	Text     string            `json:"text"`
	Batch    []string          `json:"batch"`
	Target   string            `json:"target" validate:"required"`
	Source   string            `json:"source"`
	Tier     Tier              `json:"tier"`
	Glossary map[string]string `json:"glossary"`
	Format   Format            `json:"format"`
	// Model is the quality tier's model; empty is aimodel.DefaultModel.
	Model string `json:"model"`
}

// Translation is one string's result: the translation, where on the review ladder
// it sits, and whether it came from the memory rather than an engine — or, when
// Error is set, why it has no translation. A string with an error is not stored,
// so sending it again asks an engine again.
type Translation struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	State  State  `json:"state"`
	Cached bool   `json:"cached"`
	Error  string `json:"error,omitempty"`
}

// Tally reports what the call actually did — real counts, never an estimate.
// Characters counts the source of the strings an engine translated this call,
// which is what the bulk tier bills on: a fully-cached rebuild reports (and costs)
// zero, and so does a string that failed.
// Strings = Cached + Translated + Failed.
type Tally struct {
	Strings    int `json:"strings"`
	Cached     int `json:"cached"`
	Translated int `json:"translated"`
	Characters int `json:"characters"`
	Failed     int `json:"failed"`
}

// Delivery is the POST /v1/translate reply. Model is the model the quality tier
// runs this brief on — the default, or the one the brief named after the rewrite
// an upstream family name gets — and is absent for bulk.
type Delivery struct {
	Translations   []Translation `json:"translations"`
	DetectedSource string        `json:"detected_source,omitempty"`
	Tier           Tier          `json:"tier"`
	Usage          Tally         `json:"usage"`
	Model          string        `json:"model,omitempty"`
}

// ---- handlers ----

// serve answers POST /v1/translate for the caller's OWN org: resolve every string
// against the org's memory, send only the misses to the tier's engine, record what
// the engine returned intact, and reply in input order.
//
// UNTYPED BY DESIGN — the bulk tier's SPEND DENIAL is the money wire. A gate or
// engine refusal answers cloud.DenyResource (below), the fleet-wide NESTED
// {"error":{"code","message"}} contract at 402/503 that the console routes to a
// top-up prompt, and DenyResource writes that object BARE. A typed op's ONLY refusal
// is a returned error, which zip renders as RFC 9457 problem members
// (type/title/status/detail, plus code): the nested body rides Detail intact and
// gains those members beside it. Converting is therefore a change to the MONEY wire,
// owned by whoever owns that wire — the same call apps/agents and apps/guide record.
//
// ONE SPAN PER CALL, marked hanzo.feature=translate and carrying the call's tags and
// counts — never a string the caller sent, which is customer content and lands only
// in the org's own memory. A call the memory answered whole says so: stored 0,
// chunks 0, because it asked no engine and wrote nothing.
func serve(s *cloud.Service[*state], c *zip.Ctx) error {
	ctx, span := tracer().Start(c.Context(), "translate", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(attribute.String("hanzo.feature", feature))
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	var in Brief
	if err := c.Bind(&in); err != nil {
		return err
	}
	texts, id, err := in.resolve()
	if err != nil {
		return err
	}
	// ran is the model the quality tier asks for: the one named, or the default.
	ran := ""
	if id.Tier == TierQuality {
		ran = cmp.Or(id.Model, aimodel.DefaultModel)
	}
	payer := meter.Payer(c)
	project, projectValidated := principal.ValidatedProject(c)
	span.SetAttributes(
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.translate.tier", string(id.Tier)),
		attribute.String("hanzo.translate.target", id.Target),
		attribute.String("hanzo.translate.source", id.Lang),
		attribute.String("hanzo.translate.format", string(id.Format)),
		attribute.String("hanzo.translate.model", ran),
	)
	if project != "" {
		span.SetAttributes(attribute.String("hanzo.project", project))
	}
	var t Tally
	var res Result
	stored := 0
	defer func() {
		span.SetAttributes(
			attribute.Int("hanzo.translate.strings", t.Strings),
			attribute.Int("hanzo.translate.cached", t.Cached),
			attribute.Int("hanzo.translate.translated", t.Translated),
			attribute.Int("hanzo.translate.failed", t.Failed),
			attribute.Int("hanzo.translate.characters", t.Characters),
			attribute.Int("hanzo.translate.chunks", res.Chunks),
			attribute.Int("hanzo.translate.retries", res.Retries),
			attribute.Int("hanzo.translate.stored", stored),
		)
	}()

	mem, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
	}

	// 1. The memory is consulted FIRST and its value is returned unchanged. This is
	// the idempotence contract, not an optimisation: an unchanged source string
	// yields a byte-identical translation on every rebuild.
	out := make([]Translation, len(texts))
	var missAt []int
	var missText []string
	for i, text := range texts {
		e := id
		e.Source = text
		got, found, err := mem.recall(ctx, e)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
		}
		if found {
			out[i] = Translation{Source: text, Text: got.Text, State: got.State, Cached: true}
			continue
		}
		missAt = append(missAt, i)
		missText = append(missText, text)
	}

	t = Tally{Strings: len(texts), Cached: len(texts) - len(missAt)}
	if len(missAt) == 0 {
		return c.JSON(http.StatusOK, Delivery{Translations: out, Tier: id.Tier, Model: ran, Usage: t})
	}

	// 2. bulk is not on the model plane, so it carries its own per-org gate+meter —
	// the same Meter contract every non-LLM unit uses. quality needs none:
	// deps.AI already authorizes and debits its own tokens, and a second charge here
	// would double-bill.
	if id.Tier == TierBulk {
		if err := s.Bill.Authorize(ctx, payer, project, projectValidated, meterKind, cloud.MicrosToGateCents(bulkMicros(ctx, chars(missText)))); err != nil {
			span.SetStatus(codes.Error, "spend denied")
			return cloud.DenyResource(c, err)
		}
	}

	// The engine works under the call's deadline and stops when its caller leaves;
	// the memory below is written on the request's own context, so what came back
	// before either is still kept.
	work, gone := hangup(ctx, c)
	defer gone()
	work, done := context.WithTimeout(work, deadline)
	defer done()
	res, err = s.State.engine(id.Tier).Translate(work, Job{
		Texts: missText, Source: id.Lang, Target: id.Target, Format: id.Format, Glossary: in.Glossary,
		Model: ran, Org: org, Payer: payer, Project: project,
	})

	// 3. Record what the engine produced and kept intact — before answering an engine
	// failure too, so the parts of a batch already paid for are not asked for twice.
	// A machine write can only create a row or refresh one still in StateMachine, so
	// an approved or published string that arrived here by another key is never
	// trampled. A string the engine could not give, or whose translation lost what
	// its source must keep, is answered with the reason and written nowhere.
	now := time.Now().Unix()
	if len(res.Texts) == len(missAt) {
		for n, i := range missAt {
			why := res.Failed[n]
			if why == "" {
				why = kept(texts[i], res.Texts[n])
			}
			if why != "" {
				out[i] = Translation{Source: texts[i], Error: why}
				t.Failed++
				continue
			}
			e := id
			e.Source, e.Text, e.State, e.RequestID, e.UpdatedAt = texts[i], res.Texts[n], StateMachine, c.RequestID(), now
			if err := mem.put(ctx, e, false); err != nil {
				return zip.Errorf(http.StatusInternalServerError, "memory: %v", err)
			}
			out[i] = Translation{Source: texts[i], Text: e.Text, State: e.State}
			t.Translated++
			t.Characters += len(texts[i])
			stored++
		}
	}
	if err != nil {
		span.SetStatus(codes.Error, "engine failed")
		if errors.Is(err, ErrNoEngine) {
			return zip.Errorf(http.StatusServiceUnavailable, "the %s tier is not configured on this deployment", id.Tier)
		}
		if errors.Is(err, meter.ErrInsufficientBalance) || errors.Is(err, meter.ErrSpendCapExceeded) {
			return cloud.DenyResource(c, err)
		}
		s.Log.Warn("translate engine failed", "tier", id.Tier, "target", id.Target, "model", ran, "stored", stored, "class", errClass(err))
		return failed("translation failed: the engine did not answer; what it finished is stored, so sending the batch again asks only for the rest")
	}

	if t.Translated == 0 && t.Cached == 0 {
		span.SetStatus(codes.Error, "no string translated")
		return failed("translation failed: no string came back whole")
	}

	// Bulk is billed for what came back whole, after the fact: a string that failed
	// is asked for again on the next call, and billing it each time would charge a
	// caller forever for a string it never got.
	if id.Tier == TierBulk {
		s.Bill.Record(payer, meterKind, meter.Usage{
			Model: string(TierBulk), Project: project, Actor: c.User(),
			AmountMicros: bulkMicros(ctx, t.Characters),
			RequestID:    c.RequestID(), ClientIP: geo.IP(c),
		})
	}
	reply := Delivery{Translations: out, Tier: id.Tier, Model: ran, Usage: t}
	if id.Lang == "" {
		reply.DetectedSource = res.Detected
	}
	return c.JSON(http.StatusOK, reply)
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
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
	// The parts below are appended, never inserted: the call plane lays a struct
	// out in field order.

	// Lang is the source language the translate calls this entry serves declare
	// (their `source`), empty for calls that let the engine detect it. Part of the
	// entry's identity; an entry with none also serves calls that declare one.
	Lang string `json:"lang"`
	// Format is the markup the source carries: text (the default), html or markdown.
	// Part of the entry's identity.
	Format Format `json:"format"`
	// Model is the quality-tier model the translate calls this entry serves name;
	// empty for the default. Part of the entry's identity, and refused with bulk.
	Model string `json:"model"`
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
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
	source := ""
	if v := strings.TrimSpace(in.Lang); v != "" {
		if source, err = lang(v); err != nil {
			return nil, zip.ErrBadRequest("lang must be a language tag (e.g. \"en\")")
		}
	}
	tier, err := tierOf(in.Tier)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	format, err := formatOf(in.Format)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	model, err := modelOf(tier, in.Model)
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
		Source: in.Source, Lang: source, Target: target, Tier: tier, Format: format, Model: model,
		Glossary: version(in.Glossary), Text: in.Text, State: st, Actor: reviewer(ctx), UpdatedAt: time.Now().Unix(),
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

// resolve validates the brief and returns its strings in reply order, plus the
// identity they share in the memory: every part of the key but the source text.
func (r Brief) resolve() ([]string, MemoryEntry, error) {
	texts, err := r.strings()
	if err != nil {
		return nil, MemoryEntry{}, err
	}
	target, err := lang(r.Target)
	if err != nil {
		return nil, MemoryEntry{}, zip.ErrBadRequest("target must be a language tag (e.g. \"es\", \"pt-BR\")")
	}
	source := ""
	if strings.TrimSpace(r.Source) != "" {
		if source, err = lang(r.Source); err != nil {
			return nil, MemoryEntry{}, zip.ErrBadRequest("source must be a language tag (e.g. \"en\")")
		}
	}
	tier, err := tierOf(r.Tier)
	if err != nil {
		return nil, MemoryEntry{}, zip.ErrBadRequest(err.Error())
	}
	format, err := formatOf(r.Format)
	if err != nil {
		return nil, MemoryEntry{}, zip.ErrBadRequest(err.Error())
	}
	model, err := modelOf(tier, r.Model)
	if err != nil {
		return nil, MemoryEntry{}, zip.ErrBadRequest(err.Error())
	}
	return texts, MemoryEntry{
		Lang: source, Target: target, Tier: tier, Format: format, Model: model, Glossary: version(r.Glossary),
	}, nil
}

// strings resolves the brief's work: `text` OR `batch`, never both, and returns
// them in reply order.
func (r Brief) strings() ([]string, error) {
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
	if chars(r.Batch) > maxTotal {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge,
			"a batch may carry at most %d characters in all; split it into smaller requests", maxTotal)
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

// modelOf resolves the model a brief names for its tier: "" for the default (named
// or not), else the name as the model plane runs it. That is aimodel.ZenModel's answer, so a name
// from an upstream family becomes the default's name here as it does everywhere
// a model is named; a model of the org's own is reached by its org alias. bulk runs
// no model, so naming one there is refused rather than ignored.
func modelOf(t Tier, m string) (string, error) {
	m = strings.TrimSpace(m)
	switch {
	case m == "":
		return "", nil
	case t == TierBulk:
		return "", errors.New("model applies to the quality tier; bulk runs on its own engine")
	case !modelRE.MatchString(m):
		return "", errors.New("model must be a model id or your org's alias for one: up to 128 of " +
			"A-Z a-z 0-9 . _ : / @ + -, starting with a letter or digit")
	}
	// The default named is the default: one key, so naming it finds what was made
	// without naming it.
	if m = aimodel.ZenModel(m); m == aimodel.DefaultModel {
		return "", nil
	}
	return m, nil
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
