// Copyright © 2026 Hanzo AI. MIT License.

// Package supply names the one failure a provider's budget causes: the account we
// (or an org) buy from refused the work because a spend cap, a free allocation or
// a gateway budget ran out. Cloudflare stops Workers and Durable Objects at a hard
// spending limit, stops Workers AI at the end of a daily allocation, and AI
// Gateway stops inference at a spend limit; a model vendor stops an account that
// is out of credit.
//
// Each of those used to reach the caller as whatever the transport made of it —
// a 502 from a relay, a 429 that reads as "slow down", a 503 that reads as "we
// are down" — so the layer that ran out was hidden, and the one response that
// helps (raise the cap, top up the account, wait for the period to turn) waited on
// somebody reading a log.
//
// THE CLASS. A stop is an [*Exhausted]: code [Code] ("budget_exhausted"), the
// [Layer] that stopped, the provider whose account it is, and the provider's own
// sentence. It renders as 503 with those members (HTTPError), and the fleet's
// error renderer (sdk/errmap.go) renders any propagated one the same way, so a
// client branches on the code and never on a status it shares with capacity
// (429/503 "overloaded"), sign-in (401/403) or a fault (500).
//
// 503 because the caller owes nothing and did nothing wrong: the work could not be
// served at this layer now. It is the status the model API already answers when a
// vendor account is out of credit; the code is what tells the two apart. It is
// never 402 — that is the caller's own money (insufficient_balance,
// spend_cap_exceeded), and a client sent to its billing page for a provider's
// ceiling would be sent to the wrong place.
//
// THE SIGNAL. A stop is put on the SuperAdmin's pulse (kind "supply", where the
// model API's drained vendor accounts already go) and counted on
// hanzo_provider_budget_exhausted_total{provider,layer}. A reading of a layer's
// spend against its cap ([Observe]) sets hanzo_provider_budget_used_ratio and,
// at the operator's threshold below the cap ([WarnPct], CLOUD_PROVIDER_BUDGET_WARN_PCT,
// 80 by default), says so on the pulse BEFORE the stop. The threshold is also a
// gauge (hanzo_provider_budget_warn_ratio), so one alert rule —
//
//	hanzo_provider_budget_used_ratio >= scalar(hanzo_provider_budget_warn_ratio)
//
// — follows whatever the deployment sets instead of a second copy of the number.
//
// This package is the taxonomy and the signal. Holding an org to a budget of its
// own is the ledger's (the org books), not this.
package supply

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Code is the machine-readable reason a provider budget stop is answered under.
const Code = "budget_exhausted"

// ErrExhausted is the sentinel every [*Exhausted] matches with errors.Is.
var ErrExhausted = errors.New(Code)

// Layer is the part of a provider that stopped. The set is closed and server-side:
// it labels a metric, so a caller can never choose one.
type Layer string

const (
	// Workers is Cloudflare Workers at its account spending limit (error 1103 to a
	// visitor).
	Workers Layer = "workers"
	// DurableObjects is Cloudflare Durable Objects at its account spending limit.
	DurableObjects Layer = "durable_objects"
	// WorkersAI is Cloudflare Workers AI at the end of its daily allocation
	// (error 3036).
	WorkersAI Layer = "workers_ai"
	// AIGateway is a Cloudflare AI Gateway spend limit.
	AIGateway Layer = "ai_gateway"
	// Model is a model vendor account out of credit or quota, behind the model API.
	Model Layer = "model_provider"
)

// Name is how a sentence says the layer.
func (l Layer) Name() string {
	switch l {
	case Workers:
		return "Workers"
	case DurableObjects:
		return "Durable Objects"
	case WorkersAI:
		return "Workers AI"
	case AIGateway:
		return "AI Gateway"
	case Model:
		return "model provider"
	}
	return string(l)
}

// ParseLayer reads a layer a peer named on the wire. Only the closed set parses.
func ParseLayer(s string) (Layer, bool) {
	switch l := Layer(s); l {
	case Workers, DurableObjects, WorkersAI, AIGateway, Model:
		return l, true
	}
	return "", false
}

// layers is every Cloudflare layer, so the counter can be seeded at zero: a rule
// can only say a layer stopped if the series existed before it did. A model
// vendor's series appears with its first stop, labelled by the vendor the model
// API names.
var layers = map[string][]Layer{
	"cloudflare": {Workers, DurableObjects, WorkersAI, AIGateway},
}

// Exhausted is one provider budget stop.
type Exhausted struct {
	// Provider is whose account stopped: "cloudflare" for an org's Cloudflare
	// account, "hanzo" for the model API in front of our vendor accounts.
	Provider string
	// Layer is the part of the provider that stopped.
	Layer Layer
	// Org is whose account it is; "" is the platform's own.
	Org string
	// Upstream is the status the provider answered with, 0 when none.
	Upstream int
	// Msg is the provider's own sentence, token-free; "" when it said nothing.
	Msg string
}

func (e *Exhausted) Error() string {
	s := fmt.Sprintf("%s: %s %s budget exhausted", Code, e.Provider, e.Layer)
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

// Is makes errors.Is(err, ErrExhausted) true for every stop.
func (e *Exhausted) Is(target error) bool { return target == ErrExhausted }

// HTTPError is the stop as a refusal: 503, code budget_exhausted, and the layer and
// provider as members a client reads instead of parsing the sentence.
func (e *Exhausted) HTTPError() *zip.HTTPError {
	msg := fmt.Sprintf("%s stopped this request: its %s budget is exhausted", e.Provider, e.Layer.Name())
	if e.Msg != "" {
		msg += " (" + strings.TrimSpace(e.Msg) + ")"
	}
	return (&zip.HTTPError{Status: http.StatusServiceUnavailable, Code: Code, Msg: msg}).
		With(map[string]any{"layer": string(e.Layer), "provider": e.Provider})
}

// As finds a stop anywhere in err's chain.
func As(err error) (*Exhausted, bool) {
	return errors.AsType[*Exhausted](err)
}

// WarnEnv names the threshold, a whole percentage of the hard cap.
const WarnEnv = "CLOUD_PROVIDER_BUDGET_WARN_PCT"

// defaultWarnPct is Cloudflare's own "Near limit" mark, so a dashboard and this
// signal agree unless the operator says otherwise.
const defaultWarnPct = 80

// WarnPct is the share of a hard cap, in percent, at which a layer is said to be
// near it. A value that is not a whole percentage between 1 and 99 is the
// default: 0 would warn on every reading and 100 is the stop itself, and either
// would quietly remove the one warning this exists to give.
func WarnPct() int {
	n, err := strconv.Atoi(strings.TrimSpace(env.Or(WarnEnv, "")))
	if err != nil || n < 1 || n > 99 {
		return defaultWarnPct
	}
	return n
}

// Level is where a reading stands against its cap.
type Level int

const (
	// Below is under the threshold, or a reading with no cap.
	Below Level = iota
	// Warn is at or over the threshold and under the cap.
	Warn
	// AtCap is at or over the cap: the provider stops (or will on its next
	// evaluation) whatever the layer is asked next.
	AtCap
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case AtCap:
		return "at_cap"
	}
	return "below"
}

// Reading is one layer's spend against its hard cap, in one unit (cents, for the
// spending limits Cloudflare reports).
type Reading struct {
	Provider string
	Layer    Layer
	// Org is whose account it is; "" is the platform's own.
	Org   string
	Used  int64
	Limit int64
}

// Observe records r and says it on the pulse when it is at or over the threshold.
// It returns where r stands. A reading with no cap (Limit <= 0) is Below and
// records nothing: there is no ceiling to be near.
func Observe(ctx context.Context, r Reading) Level {
	if r.Limit <= 0 {
		return Below
	}
	pct := WarnPct()
	ratio := float64(r.Used) / float64(r.Limit)
	instruments()
	if usedRatio != nil {
		usedRatio.Record(ctx, ratio, metric.WithAttributes(
			attribute.String("provider", r.Provider),
			attribute.String("layer", string(r.Layer)),
			attribute.String("org", orgOf(r.Org))))
	}
	if warnRatio != nil {
		warnRatio.Record(ctx, float64(pct)/100)
	}
	lv := Below
	switch {
	case r.Used >= r.Limit:
		lv = AtCap
	case r.Used*100 >= int64(pct)*r.Limit:
		lv = Warn
	}
	if lv == Below {
		return lv
	}
	share := r.Used * 100 / r.Limit // integer: 85% must never print as 84
	title := fmt.Sprintf("%s %s at %d%% of its spending cap", r.Provider, r.Layer.Name(), share)
	detail := fmt.Sprintf("%d of %d spent; the warning threshold is %d%% (%s). Raise the cap or cut the spend before the provider stops serving.",
		r.Used, r.Limit, pct, WarnEnv)
	if lv == AtCap {
		title = fmt.Sprintf("%s %s has reached its spending cap", r.Provider, r.Layer.Name())
		detail = fmt.Sprintf("%d of %d spent: the provider stops this layer until the cap is raised or the period turns.", r.Used, r.Limit)
	}
	say(r.Provider, r.Layer, r.Org, lv.String(), title, detail)
	return lv
}

// Report counts a stop and says it on the pulse, once per [Every] for each
// (provider, layer, org). Call it where a provider's refusal is first read as a
// stop; the request it refused is answered by the caller, not here.
func Report(ctx context.Context, e *Exhausted) {
	if e == nil {
		return
	}
	Count(ctx, e.Provider, e.Layer)
	detail := "The provider refused the request because this budget is spent."
	if e.Msg != "" {
		detail = e.Msg
	}
	if e.Upstream != 0 {
		detail = fmt.Sprintf("%s (status %d)", detail, e.Upstream)
	}
	say(e.Provider, e.Layer, e.Org, "exhausted",
		fmt.Sprintf("%s %s budget exhausted", e.Provider, e.Layer.Name()), detail)
}

// Count counts one stop on hanzo_provider_budget_exhausted_total without saying
// it, for a caller whose stop is already said elsewhere — the model API puts a
// drained vendor account on the pulse itself, and a second line would be noise.
func Count(ctx context.Context, provider string, layer Layer) {
	instruments()
	if exhausted == nil {
		return
	}
	exhausted.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", provider),
		attribute.String("layer", string(layer))))
}

// Every is how often one layer's state is said again while it holds: once when it
// is found, again each Every after, so a reader who missed the first still hears
// it and a busy route does not say it on every request. The model API's drained
// accounts repeat on the same period.
const Every = 10 * time.Minute

var (
	// announcer is where a signal goes: the bus's pulse, unless SetAnnounce
	// installed another.
	announcer atomic.Pointer[func(bus.Pulse) error]
	// now is the clock the repeat window reads; tests replace it.
	now = time.Now

	saidMu sync.Mutex
	said   = map[string]time.Time{}
)

// SetAnnounce installs where this package's signals go and returns what restores
// the one before it; nil restores the bus. The pulse is the platform's path, so a
// host has no reason to change it — a test of a reader does, to hear what the
// reader said without a bus.
func SetAnnounce(f func(bus.Pulse) error) (restore func()) {
	prev := announcer.Load()
	if f == nil {
		announcer.Store(nil)
	} else {
		announcer.Store(&f)
	}
	return func() { announcer.Store(prev) }
}

// announce puts one pulse where signals go.
func announce(p bus.Pulse) error {
	if f := announcer.Load(); f != nil {
		return (*f)(p)
	}
	return bus.Announce(p)
}

// forget clears what was already said, for tests.
func forget() {
	saidMu.Lock()
	said = map[string]time.Time{}
	saidMu.Unlock()
}

// say announces one state of one layer unless it was announced inside the window.
// Best effort: a bus that is down loses a feed line, never the request.
func say(provider string, layer Layer, org, state, title, detail string) {
	at := now()
	key := provider + "/" + string(layer) + "/" + orgOf(org)
	id := key + "/" + state
	saidMu.Lock()
	if last, ok := said[id]; ok && at.Sub(last) < Every {
		saidMu.Unlock()
		return
	}
	said[id] = at
	saidMu.Unlock()
	_ = announce(bus.Pulse{
		Kind:   "supply",
		Key:    key,
		At:     at.UTC().Format(time.RFC3339),
		Title:  title,
		Detail: detail,
		Org:    orgOf(org),
	})
}

// orgOf is the org a state is about: the platform's own is the admin org, where
// the model API's drained accounts are said too.
func orgOf(org string) string {
	if org == "" {
		return "admin"
	}
	return org
}

// meterName scopes this package's instruments.
const meterName = "github.com/hanzoai/cloud/sdk/supply"

var (
	instOnce  sync.Once
	exhausted metric.Int64Counter // hanzo_provider_budget_exhausted_total{provider,layer}
	usedRatio metric.Float64Gauge // hanzo_provider_budget_used_ratio{provider,layer,org}
	warnRatio metric.Float64Gauge // hanzo_provider_budget_warn_ratio
)

// instruments resolves the instruments on first use — never at init, where the
// meter would bind before the host installs its provider — and seeds the stop
// counter at zero for every known layer, so `increase(...) > 0` is a sentence
// about a live series rather than about a name nobody wrote yet.
func instruments() {
	instOnce.Do(func() {
		m := otel.Meter(meterName)
		exhausted, _ = m.Int64Counter("hanzo_provider_budget_exhausted_total",
			metric.WithDescription("Requests a provider refused because a budget ran out (budget_exhausted), by provider and layer."))
		usedRatio, _ = m.Float64Gauge("hanzo_provider_budget_used_ratio",
			metric.WithDescription("Share of a provider layer's hard spending cap used, at the last reading."))
		warnRatio, _ = m.Float64Gauge("hanzo_provider_budget_warn_ratio",
			metric.WithDescription("The share of a hard cap at which a provider layer is said to be near it ("+WarnEnv+"/100)."))
		if exhausted == nil {
			return
		}
		ctx := context.Background()
		for provider, ls := range layers {
			for _, l := range ls {
				exhausted.Add(ctx, 0, metric.WithAttributes(
					attribute.String("provider", provider),
					attribute.String("layer", string(l))))
			}
		}
	})
}
