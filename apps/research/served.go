package research

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	"github.com/hanzoai/cloud/internal/cluster"
	"github.com/hanzoai/cloud/sdk"
)

// served.go — what the public run listing serves of a run.
//
// The anonymous read (GET /v1/research/runs?org=, [ops.public]) is the one read
// of a run by a caller outside the org that filed it. It serves each public run
// through [served], which keeps every id, count, date and score and leaves out
// what an Enso, Zen or Kai run says about the models under it:
//
//   - a run of a base model those families are built from, or whose id or
//     benchmark names one, is not served at all, exactly as a private run is
//     not; a measure whose metric or category names one is left off any run;
//   - an Enso or Zen run is served with its version, notes, prompt, embedder and
//     reader empty, since there they name the models a tier routes to;
//   - an Enso, Zen or pool run is served without its spend measures (token
//     counts, calls, the harness's cost), whose counts match the single-model run
//     of the arm that answered; an Enso run carries instead `list_cost_usd`, its
//     tokens at the model API's current price for the model a caller buys
//     ([listCost]);
//   - every run of an enso.* benchmark is served without notes, which there state
//     the gateway the harness called and the calls it made to each model.
//
// The key is derived again over what is served, and the listing's filters judge
// the served record, so ?key= answers only for the record a stranger can see.
//
// The org's own read (orgStore, the owner org and a SuperAdmin acting for it) is
// the full record: a harness asks ?key= there before it spends, and that key is
// over the version it ran.

// bases are the base models whose runs the public listing does not serve: those
// Zen is built from and the one Kai is trained from. A token starts the text or
// follows a non-letter, so `kimi-k3`, `z-ai/glm-5.2`, `gate.laya-drawn` and
// `qwen3.5-397b` match and `alaya` does not.
var bases = regexp.MustCompile(`(^|[^a-z])(qwen|kimi|glm|alibaba|moonshot|zhipu|laya)`)

// base reports whether s names one of [bases].
func base(s string) bool { return bases.MatchString(strings.ToLower(s)) }

// family reports whether system is an Enso tier or router, a Zen model, or the
// pool of models Enso routes over: enso, enso-*, zen, zen-*, zen<digit>…, each
// with or without a hanzo/ prefix, and pool-oracle@*.
func family(system string) bool {
	s := strings.TrimPrefix(strings.ToLower(system), "hanzo/")
	switch {
	case ensoOf(system):
		return true
	case s == "zen":
		return true
	case strings.HasPrefix(s, "zen-") || strings.HasPrefix(s, "zen@"):
		return true
	case len(s) > 3 && strings.HasPrefix(s, "zen") && s[3] >= '0' && s[3] <= '9':
		return true
	case strings.HasPrefix(s, "pool-oracle@"):
		return true
	}
	return false
}

// ensoOf reports whether system is an Enso tier or the Enso router.
func ensoOf(system string) bool {
	s := strings.TrimPrefix(strings.ToLower(system), "hanzo/")
	return s == "enso" || strings.HasPrefix(s, "enso-") || strings.HasPrefix(s, "enso@")
}

// router reports whether system is the Enso router, which bills the tier that
// answers rather than a price of its own.
func router(system string) bool {
	s := strings.TrimPrefix(strings.ToLower(system), "hanzo/")
	return s == "enso" || s == "enso-auto"
}

// spend are the measures a run's spend is recorded in. On an Enso, Zen or pool
// run they are left off the public record: equal token counts on two runs of one
// question set say which arm answered.
var spend = map[string]bool{"prompt_tokens": true, "completion_tokens": true, "reasoning_tokens": true, "calls": true, "cost_usd": true}

// ListCost is the measure an Enso run's spend is served as on the public record:
// its prompt and completion tokens at the model API's current price, in dollars.
const ListCost = "list_cost_usd"

// rate is a model's price, dollars a million input and output tokens.
type rate struct{ input, output float64 }

// prices is the price of each Enso tier by its id, as the model API answers it
// now; nil when it could not be read.
type prices map[string]rate

// listCost is what an Enso run's recorded tokens cost at the price a caller pays
// for the model it ran: a tier at its own price; the router at the dearest
// tier's, since it bills the tier that answers and the run does not record which.
// False when the run lacks either count or the price is not known.
func listCost(r Run, p prices) (float64, bool) {
	var in, out *float64
	for i := range r.Measures {
		m := &r.Measures[i]
		if m.Category != "all" {
			continue
		}
		switch m.Metric {
		case "prompt_tokens":
			in = &m.Value
		case "completion_tokens":
			out = &m.Value
		}
	}
	if in == nil || out == nil || len(p) == 0 {
		return 0, false
	}
	var at rate
	if router(r.System) {
		for _, q := range p {
			if q.output > at.output || (q.output == at.output && q.input > at.input) {
				at = q
			}
		}
	} else {
		q, ok := p[strings.TrimPrefix(strings.ToLower(r.System), "hanzo/")]
		if !ok {
			return 0, false
		}
		at = q
	}
	if at.output == 0 && at.input == 0 {
		return 0, false
	}
	return (*in*at.input + *out*at.output) / 1e6, true
}

// served is a run as the public listing serves it, and false when that listing
// does not serve it at all. p is the Enso tiers' prices for [listCost].
func served(r Run, p prices) (Run, bool) {
	if base(r.System) || base(r.ID) || base(r.Benchmark) {
		return Run{}, false
	}
	priv := family(r.System)
	if priv {
		r.Version, r.Notes, r.Prompt, r.Embedder, r.Reader = "", "", "", "", ""
	}
	cost, priced := 0.0, false
	if ensoOf(r.System) {
		cost, priced = listCost(r, p)
	}
	kept := make([]Measure, 0, len(r.Measures)+1)
	for _, m := range r.Measures {
		if base(m.Metric) || base(m.Category) || (priv && spend[m.Metric]) {
			continue
		}
		kept = append(kept, m)
	}
	if priced {
		kept = append(kept, Measure{Category: "all", Metric: ListCost, Value: cost})
	}
	r.Measures = kept
	if strings.HasPrefix(r.Benchmark, "enso.") {
		r.Notes = ""
	}
	r.Key = keyOf(r)
	return r, true
}

// rateTTL is how long the Enso tiers' prices are served from the last read before
// the model API is asked again; rateWait bounds one ask.
const (
	rateTTL  = time.Minute
	rateWait = 5 * time.Second
)

// askRates reaches the model API over the plane. A package variable so a test
// answers in its place.
var askRates = aipeer.AIRates

// book keeps the Enso tiers' prices as last read, so a public read asks the model
// API at most once a rateTTL. A failed ask is kept for the same time and serves no
// price, so a listing never waits on a model API that is not answering.
var book struct {
	sync.Mutex
	at time.Time
	p  prices
}

// tierPrices is the Enso tiers' prices, read under the platform, which owns them.
func tierPrices(ctx context.Context) prices {
	book.Lock()
	defer book.Unlock()
	if !book.at.IsZero() && time.Since(book.at) < rateTTL {
		return book.p
	}
	cctx, cancel := context.WithTimeout(cloud.For(ctx, cluster.Platform), rateWait)
	defer cancel()
	r, err := askRates(cctx, &client.RatesIn{})
	book.at, book.p = time.Now(), pricesOf(r, err)
	return book.p
}

// pricesOf is the model API's answer as tier prices; nil for a failed ask.
func pricesOf(r *client.Rates, err error) prices {
	if err != nil || r == nil {
		return nil
	}
	out := prices{}
	for _, q := range r.Quotes {
		in, ierr := strconv.ParseFloat(strings.TrimSpace(q.Price.Input), 64)
		o, oerr := strconv.ParseFloat(strings.TrimSpace(q.Price.Output), 64)
		if ierr != nil || oerr != nil || router(q.Model) {
			continue
		}
		out[strings.TrimPrefix(strings.ToLower(q.Model), "hanzo/")] = rate{in, o}
	}
	return out
}
