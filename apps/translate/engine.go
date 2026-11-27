package translate

// engine.go — the two tiers behind the one endpoint (HIP-0516).
//
//   - quality runs on the model plane (deps.AI — zen through the gateway), which
//     carries context, terminology and tone, and which already authorizes and
//     debits its own tokens per org. It runs the model the brief names, or the
//     default, in bounded parts (chunks) it checks before trusting.
//   - bulk runs on MADLAD-400 under CTranslate2, the permissive-weights path
//     HIP-0516 selects (MADLAD-400 3B/10B are apache-2.0). It is reached over the
//     small JSON contract below, so the weights can be served independently of this
//     binary; a deployment that has not served them yet answers ErrNoEngine and the
//     caller gets a 503 for the tier it asked for. There is no cross-tier fallback:
//     serving bulk work on the quality tier would charge a caller for something it
//     did not request.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrNoEngine marks a tier whose backend this deployment does not serve. The
// handler renders it 503 for that tier and never re-routes to another one.
var ErrNoEngine = errors.New("translate: tier backend not configured")

// Job is one engine call: the strings to translate plus the billing scope, carried
// on the value exactly as infer.ChatRequest carries it, so an engine can never run
// unattributed.
type Job struct {
	Texts    []string
	Source   string // "" ⇒ the engine detects and reports it
	Target   string
	Format   Format
	Glossary map[string]string
	Model    string // the model the quality tier asks for; bulk has none

	Org     string          // the effective org — the data scope
	Payer   account.Account // the account that pays
	Project string
}

// Result is one engine call's answer: Texts and Failed hold one entry per input, in
// input order — the translation, or why there is none — plus the language the
// engine detected when the caller named none, and how many completions (Chunks) and
// second asks (Retries) it took. An engine that fails part way returns what it has
// beside the error, every string it did not reach marked failed.
type Result struct {
	Texts    []string
	Failed   []string
	Detected string
	Chunks   int
	Retries  int
}

// Engine translates a job's strings into one target language. Two implementations,
// one per tier; the endpoint, the memory, and the meter are shared.
type Engine interface {
	Translate(ctx context.Context, j Job) (Result, error)
}

// ---- quality: the model plane ----

type quality struct{ ai infer.Client }

func newQuality(ai infer.Client) Engine { return quality{ai: ai} }

const (
	// chunkBudget bounds the source one completion carries. A batch is packed in
	// order into parts no larger, so a 512-string locale is several bounded answers
	// rather than one the model may cut off part way.
	chunkBudget = 8 << 10
	// stringCost is what a string weighs beyond its bytes: its quotes, comma and
	// escapes in the envelope, on the way in and again on the way out.
	stringCost = 32
	// thinkTokens is the room a completion keeps for a model that reasons before it
	// answers, which the enso rungs do.
	thinkTokens = 4096
	// maxCompletion is the most one completion may buy. The model plane lowers it
	// again to the model's own most, so it never asks a model for more than it holds.
	maxCompletion = 32768
)

// Translate packs the strings into parts (chunks), asks for each part in one
// completion, and keeps a translation only when it is whole: the reply finished,
// is the envelope, covers every string of the part, and each translation keeps
// its source's shape (kept). What falls short is asked once more (session.part);
// what still fails is reported per string, never stored, and fails nothing else.
//
// THE WORK IS BOUNDED, because every ask is billed and a reply that never fits
// would otherwise be asked for again part after part: at most two asks per part
// across the request, of which one is held for every part still to come, so a
// part's retries never take a later part's first ask; and the request stops after
// two parts in a row in which no string came back, or when ctx ends — the
// request's deadline, or its caller leaving. Each string the request did not
// reach fails with the reason it stopped.
//
// A reply that does not cover its strings exactly is never trusted: a short or
// re-ordered array would silently mis-key the memory, and a wrong translation
// stored under a source string is worse than a failed one.
func (q quality) Translate(ctx context.Context, j Job) (Result, error) {
	if q.ai == nil {
		return Result{}, ErrNoEngine
	}
	res := Result{Texts: make([]string, len(j.Texts)), Failed: make([]string, len(j.Texts))}
	parts := chunks(j.Texts)
	s := &session{q: q, j: j, res: &res, done: make([]bool, len(j.Texts)), asks: 2 * len(parts)}
	failing := 0
	for p, part := range parts {
		why := ""
		switch {
		case ctx.Err() != nil:
			why = whyTime
		case failing == 2:
			why = whyStopped
		}
		if why != "" {
			s.fail(why, parts[p:]...)
			break
		}
		res.Chunks++
		if err := s.part(ctx, part, len(parts)-p-1); err != nil {
			if ctx.Err() != nil {
				s.fail(whyTime, parts[p:]...)
				break
			}
			s.fail(whyEngine, parts[p:]...)
			return res, err
		}
		failing++
		for _, i := range part {
			if s.done[i] {
				failing = 0
				break
			}
		}
	}
	return res, nil
}

// Why a string the request did not finish has no translation. None names the
// caller's text or the model's output.
const (
	whyTime    = "not translated: the request ran out of time, or its caller left"
	whyStopped = "not translated: two parts in a row failed, so the request stopped"
	whyBudget  = "not translated: the request spent the model calls it may make"
	whyEngine  = "not translated: the engine failed before this string"
)

// session is one request's asks: the job, what came back, and how many asks are
// left.
type session struct {
	q    quality
	j    Job
	res  *Result
	done []bool
	asks int
}

// part asks for one part, then once more for what fell short. A reply that was
// unusable — cut short, malformed, merging or splitting strings — is asked for
// again ONE STRING AT A TIME, since one string that does not fit fails every
// neighbour in a shared reply; strings whose translation changed shape are asked
// for again together. A retry spends only asks no later part is owed (later is
// how many parts follow this one), and a string the budget does not reach fails
// with whyBudget.
func (s *session) part(ctx context.Context, part []int, later int) error {
	usable, err := s.ask(ctx, part)
	if err != nil {
		return err
	}
	if !usable {
		for _, i := range part {
			if s.asks <= later {
				s.res.Failed[i] = whyBudget
				continue
			}
			s.res.Retries++
			if _, err := s.ask(ctx, []int{i}); err != nil {
				return err
			}
		}
		return nil
	}
	var again []int
	for _, i := range part {
		if !s.done[i] {
			again = append(again, i)
		}
	}
	if len(again) == 0 || s.asks <= later {
		return nil
	}
	s.res.Retries++
	_, err = s.ask(ctx, again)
	return err
}

// ask spends one ask on the strings at idx. An unusable reply marks each of them
// with why and reports false; a usable one keeps each translation that kept its
// source's shape and marks the rest. An error is the engine's, or ctx's.
func (s *session) ask(ctx context.Context, idx []int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.asks--
	got, detected, err := s.q.complete(ctx, s.j, idx)
	var bad unusable
	if errors.As(err, &bad) {
		for _, i := range idx {
			s.res.Failed[i] = bad.Error()
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.res.Detected = cmp.Or(s.res.Detected, detected)
	for n, i := range idx {
		if why := kept(s.j.Texts[i], got[n]); why != "" {
			s.res.Failed[i] = why
			continue
		}
		s.res.Texts[i], s.res.Failed[i], s.done[i] = got[n], "", true
	}
	return true, nil
}

// fail marks every string in parts that has no translation and no reason yet
// with why.
func (s *session) fail(why string, parts ...[]int) {
	for _, part := range parts {
		for _, i := range part {
			if !s.done[i] && s.res.Failed[i] == "" {
				s.res.Failed[i] = why
			}
		}
	}
}

// complete asks one completion for the strings at idx and returns their
// translations in order. A reply that came back and cannot be used is unusable;
// any other error is the engine's.
func (q quality) complete(ctx context.Context, j Job, idx []int) ([]string, string, error) {
	part := j
	part.Texts = make([]string, len(idx))
	for n, i := range idx {
		part.Texts[n] = j.Texts[i]
	}
	resp, err := q.ai.ChatCompletion(ctx, &infer.ChatRequest{
		Model:     j.Model,
		Prompt:    prompt(part),
		MaxTokens: ceiling(part.Texts),
		Org:       j.Org,
		Payer:     j.Payer,
		Project:   j.Project,
		Feature:   feature,
	})
	if err != nil {
		return nil, "", err
	}
	if resp == nil {
		return nil, "", unusable("the model returned no reply")
	}
	// Only "stop" is a finished answer. "length" is one cut at its ceiling, and the
	// rest (a filter, a tool call) are not a translation either. An unreported reason
	// is let through to decode, which refuses a cut-off envelope on its own.
	if r := resp.FinishReason; r != "" && r != "stop" {
		return nil, "", unusable(fmt.Sprintf("the model stopped before finishing (%s)", r))
	}
	res, err := decode(resp.Content, len(part.Texts))
	return res.Texts, res.Detected, err
}

// unusable is a reply that came back and cannot be used: cut short, not the
// envelope, or the wrong number of strings. It costs the part a second ask, never
// the request, and its text is what the caller reads on each string it failed —
// so it never quotes the reply, which is the caller's own content.
type unusable string

func (u unusable) Error() string { return string(u) }

// chunks packs the strings, in order, into parts no heavier than chunkBudget, each
// string weighing its bytes plus stringCost. A string heavier than the budget is a
// part of its own and never split: half a sentence is not a unit a model can
// translate.
func chunks(texts []string) [][]int {
	var out [][]int
	var part []int
	weight := 0
	for i, t := range texts {
		w := len(t) + stringCost
		if len(part) > 0 && weight+w > chunkBudget {
			out = append(out, part)
			part, weight = nil, 0
		}
		part = append(part, i)
		weight += w
	}
	if len(part) > 0 {
		out = append(out, part)
	}
	return out
}

// ceiling is the completion one part may buy: room to think, then twice its weight
// — a translation can run longer than its source, and a script like Japanese spends
// more tokens per character than the Latin it came from — and never above
// maxCompletion. Stating it is also what bounds the hold the meter takes for the
// call (cloud.atMost).
func ceiling(texts []string) int {
	n := thinkTokens
	for _, t := range texts {
		n += 2 * (len(t) + stringCost)
	}
	return min(n, maxCompletion)
}

// prompt states the task, the invariants (markup, placeholders, URLs, whitespace),
// the glossary, and the exact reply envelope. The input is a JSON array so the model
// sees unambiguous string boundaries.
func prompt(j Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Translate every string in INPUT into %s", j.Target)
	if j.Source != "" {
		fmt.Fprintf(&b, " from %s", j.Source)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "The strings are %s: preserve markup tags, {placeholders}, ICU message arguments (translate only the text inside plural and select branches), URLs, escapes and leading/trailing whitespace exactly, and translate only human-readable text.\n", j.Format)
	if len(j.Glossary) > 0 {
		b.WriteString("Use these renderings verbatim:\n")
		terms := make([]string, 0, len(j.Glossary))
		for t := range j.Glossary {
			terms = append(terms, t)
		}
		sort.Strings(terms)
		for _, t := range terms {
			fmt.Fprintf(&b, "  %s = %s\n", t, j.Glossary[t])
		}
	}
	b.WriteString(`Reply with one JSON object and nothing else: {"source":"<BCP-47 tag of the input language>","translations":["…"]}` + "\n")
	fmt.Fprintf(&b, "translations MUST hold exactly %d strings, in input order.\nINPUT: ", len(j.Texts))
	in, _ := json.Marshal(j.Texts)
	b.Write(in)
	return b.String()
}

// decode parses the reply envelope and proves it covers every input.
func decode(raw string, n int) (Result, error) {
	var env struct {
		Source       string   `json:"source"`
		Translations []string `json:"translations"`
	}
	if err := json.Unmarshal([]byte(unfence(raw)), &env); err != nil {
		return Result{}, unusable("the reply was not the JSON envelope")
	}
	if len(env.Translations) != n {
		return Result{}, unusable(fmt.Sprintf("the reply held %d translations for %d strings", len(env.Translations), n))
	}
	return Result{Texts: env.Translations, Detected: strings.TrimSpace(env.Source)}, nil
}

// unfence strips the ```/```json wrapper models put around JSON.
func unfence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// ---- bulk: MADLAD-400 under CTranslate2 ----

// bulkRequest / bulkReply are the contract a MADLAD-400 backend serves. It is
// deliberately the same shape as this endpoint's own, minus the tier and the
// memory (both of which stay here): the backend translates, and nothing else.
// Rendering a target as MADLAD's `<2xx>` control token is the backend's business.
type bulkRequest struct {
	Texts  []string `json:"texts"`
	Source string   `json:"source,omitempty"`
	Target string   `json:"target"`
	Format Format   `json:"format,omitempty"`
}

type bulkReply struct {
	Translations   []string `json:"translations"`
	DetectedSource string   `json:"detected_source"`
}

type bulk struct {
	url  string
	http *http.Client
}

func newBulk() Engine {
	return bulk{url: bulkURL(), http: &http.Client{Timeout: bulkTimeout}}
}

// bulkTimeout bounds one bulk call so a wedged backend cannot hold a request open.
const bulkTimeout = 60 * time.Second

// bulkURL names the MADLAD-400 backend. Unset ⇒ this deployment does not serve the
// bulk tier, and every bulk request says so.
func bulkURL() string { return env.Or("TRANSLATE_BULK_URL", "") }

// Translate is one call to the backend, under a span of its own: the bulk tier
// asks no model, so no gen_ai span would otherwise say it ran. The span carries the
// marker and the counts, never a string.
func (b bulk) Translate(ctx context.Context, j Job) (Result, error) {
	if b.url == "" {
		return Result{}, ErrNoEngine
	}
	ctx, span := tracer().Start(ctx, "translate.bulk", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String("hanzo.feature", feature),
		attribute.String("hanzo.translate.target", j.Target),
		attribute.Int("hanzo.translate.strings", len(j.Texts)),
		attribute.Int("hanzo.translate.characters", chars(j.Texts)),
	)
	res, err := b.call(ctx, j, span)
	if err != nil {
		span.SetStatus(codes.Error, "bulk backend failed")
	}
	return res, err
}

func (b bulk) call(ctx context.Context, j Job, span trace.Span) (Result, error) {
	body, err := json.Marshal(bulkRequest{Texts: j.Texts, Source: j.Source, Target: j.Target, Format: j.Format})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("translate: bulk backend: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("translate: bulk backend: status %d", resp.StatusCode)
	}
	var reply bulkReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Result{}, errors.New("translate: bulk backend reply is not the JSON contract")
	}
	if len(reply.Translations) != len(j.Texts) {
		return Result{}, fmt.Errorf("translate: bulk backend returned %d translations for %d strings", len(reply.Translations), len(j.Texts))
	}
	return Result{
		Texts: reply.Translations, Failed: make([]string, len(j.Texts)),
		Detected: strings.TrimSpace(reply.DetectedSource), Chunks: 1,
	}, nil
}

// ---- bulk pricing ----

// defaultBulkPriceUUSDPer1kChars is the FLOOR for bulk translation, in micro-USD
// per 1000 SOURCE characters: what this charged before the meter authority
// existed, so an unreadable authority keeps charging exactly what it charged
// yesterday. 0 makes the tier free (and thus un-gated), mirroring the edge gate's
// price==0 pass-through. The quality tier has no price here: it prices as the
// inference it is, through the model plane's meter.
const defaultBulkPriceUUSDPer1kChars int64 = 20

// charsPerUnit is what ONE billed unit of bulk translation is. The authority
// prices a unit; this says how many characters make one.
const charsPerUnit int64 = 1000

// bulkMicros converts a source-character count to the bulk debit in micro-USD.
func bulkMicros(ctx context.Context, chars int) int64 {
	rate := bulkRate(ctx)
	if chars <= 0 || rate <= 0 {
		return 0
	}
	return int64(chars) * rate / charsPerUnit
}

// bulkRate resolves the price of one billed unit from the meter authority, in
// micro-USD, falling back to the compiled floor.
//
// THE ENV OVERRIDE IS GONE. TRANSLATE_PRICE_UUSD_PER_1K_CHARS priced this before
// and was set by no deployment; an env var keeps no history, so nothing could say
// what we charged in March or who changed it. It is a row now, edited at
// admin.hanzo.ai with an audit trail, over the floor above.
//
// A var so a test can price the tier at ZERO — a legal price that makes it free,
// and the case the gate-order proof needs.
var bulkRate = func(ctx context.Context) int64 {
	return cloud.RateMicros(ctx, "translate", "bulk-chars", defaultBulkPriceUUSDPer1kChars)
}
