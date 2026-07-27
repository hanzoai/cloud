package translate

// engine.go — the two tiers behind the one endpoint (HIP-0516).
//
//   - quality runs on the model plane (deps.AI — zen through the gateway), which
//     carries context, terminology and tone, and which already authorizes and
//     debits its own tokens per org.
//   - bulk runs on MADLAD-400 under CTranslate2, the permissive-weights path
//     HIP-0516 selects (MADLAD-400 3B/10B are apache-2.0). It is reached over the
//     small JSON contract below, so the weights can be served independently of this
//     binary; a deployment that has not served them yet answers ErrNoEngine and the
//     caller gets a 503 for the tier it asked for. There is no cross-tier fallback:
//     serving bulk work on the quality tier would charge a caller for something it
//     did not request.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// ErrNoEngine marks a tier whose backend this deployment does not serve. The
// handler renders it 503 for that tier and never re-routes to another one.
var ErrNoEngine = errors.New("translate: tier backend not configured")

// Job is one engine call: the strings to translate plus the billing scope, carried
// on the value exactly as types.ChatRequest carries it, so an engine can never run
// unattributed.
type Job struct {
	Texts    []string
	Source   string // "" ⇒ the engine detects and reports it
	Target   string
	Format   Format
	Glossary map[string]string

	Org        string // the effective org — the data scope
	BillingOrg string // the home org that pays
	Project    string
}

// Result is one engine call's answer: one translation per input, in input order,
// plus the language the engine detected when the caller named none.
type Result struct {
	Texts    []string
	Detected string
}

// Engine translates a job's strings into one target language. Two implementations,
// one per tier; the endpoint, the memory, and the meter are shared.
type Engine interface {
	Translate(ctx context.Context, j Job) (Result, error)
}

// ---- quality: the model plane ----

type quality struct {
	ai    cloud.AIClient
	model string
}

func newQuality(ai cloud.AIClient, model string) Engine {
	return quality{ai: ai, model: strings.TrimSpace(model)}
}

// Translate runs ONE completion for the whole batch and requires the model to
// answer with the exact envelope. A reply that is not the envelope, or that holds
// the wrong number of translations, is an error: a short or re-ordered array would
// silently mis-key the memory, and a wrong translation stored under a source string
// is worse than a failed call.
func (q quality) Translate(ctx context.Context, j Job) (Result, error) {
	if q.ai == nil {
		return Result{}, ErrNoEngine
	}
	resp, err := q.ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:      q.model,
		Prompt:     prompt(j),
		Org:        j.Org,
		BillingOrg: j.BillingOrg,
		Project:    j.Project,
	})
	if err != nil {
		return Result{}, err
	}
	if resp == nil {
		return Result{}, fmt.Errorf("translate: model returned no reply")
	}
	return decode(resp.Content, len(j.Texts))
}

// prompt states the task, the invariants (markup, placeholders, whitespace), the
// glossary, and the exact reply envelope. The input is a JSON array so the model
// sees unambiguous string boundaries.
func prompt(j Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Translate every string in INPUT into %s", j.Target)
	if j.Source != "" {
		fmt.Fprintf(&b, " from %s", j.Source)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "The strings are %s: preserve markup, placeholders, escapes and leading/trailing whitespace exactly, and translate only human-readable text.\n", j.Format)
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
		return Result{}, fmt.Errorf("translate: model reply is not the JSON envelope: %w", err)
	}
	if len(env.Translations) != n {
		return Result{}, fmt.Errorf("translate: model returned %d translations for %d strings", len(env.Translations), n)
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
func bulkURL() string { return strings.TrimSpace(os.Getenv("TRANSLATE_BULK_URL")) }

func (b bulk) Translate(ctx context.Context, j Job) (Result, error) {
	if b.url == "" {
		return Result{}, ErrNoEngine
	}
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("translate: bulk backend: status %d", resp.StatusCode)
	}
	var reply bulkReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Result{}, fmt.Errorf("translate: bulk backend reply: %w", err)
	}
	if len(reply.Translations) != len(j.Texts) {
		return Result{}, fmt.Errorf("translate: bulk backend returned %d translations for %d strings", len(reply.Translations), len(j.Texts))
	}
	return Result{Texts: reply.Translations, Detected: strings.TrimSpace(reply.DetectedSource)}, nil
}

// ---- bulk pricing ----

// defaultBulkPriceUUSDPer1kChars is the fallback bulk price in micro-USD per 1000
// SOURCE characters when no operator override is set. A clearly-named, configurable
// POLICY default — never a fabricated market price; ops sets the real number per
// deployment via TRANSLATE_PRICE_UUSD_PER_1K_CHARS. 0 makes the tier free (and thus
// un-gated), mirroring the edge gate's price==0 pass-through. The quality tier has
// no knob here: it prices as the inference it is, through the model plane's meter.
const defaultBulkPriceUUSDPer1kChars int64 = 20

// bulkMicros converts a source-character count to the bulk debit in micro-USD.
func bulkMicros(chars int) int64 {
	rate := bulkRate()
	if chars <= 0 || rate <= 0 {
		return 0
	}
	return int64(chars) * rate / 1000
}

// bulkRate resolves the bulk price from TRANSLATE_PRICE_UUSD_PER_1K_CHARS, else the
// policy default. A negative or unparseable value falls through to the default, so
// a typo can never silently zero out billing.
func bulkRate() int64 {
	s := strings.TrimSpace(os.Getenv("TRANSLATE_PRICE_UUSD_PER_1K_CHARS"))
	if s == "" {
		return defaultBulkPriceUUSDPer1kChars
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return defaultBulkPriceUUSDPer1kChars
	}
	return n
}
