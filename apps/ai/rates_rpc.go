// Copyright © 2026 Hanzo AI. MIT License.

package ai

// rates_rpc.go — what an Enso model costs, answered by the process that bills it.
//
// An Enso tier sells at one fixed price (hanzoai/ai rates.go ensoPrices): the list
// price of the dearest arm of its route times the tier's margin, the same at every
// reasoning depth and whichever arm answers. The model API in this process lists
// it at /v1/models, bills it, and sends a tier only while every arm of its route
// costs no more than it bills (the guard). No Enso model is free; enso, enso-auto
// and hanzo/enso are the router over the tiers (router.depth), billed at the tier
// that answers, so none of them is a SKU here. The price list
// (apps/pricing) asks for the price here rather than keeping a second copy that
// could disagree.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	aictl "github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// exposeRates publishes the prices on the internal plane. Use calls it.
func exposeRates() {
	cloud.Plane().Post("/ai/rates", planeRates,
		zip.WithOperationID(client.AIRates),
		zip.WithSummary("What each Enso model costs: one fixed price a model"))
}

// Answers each Enso tier's one price, dollars a million tokens by class, and the
// models the family serves free: none, and never a router id. A router id among
// Models is quoted too, at the price of the tier a request that states no depth is
// served at, which is the price /v1/models lists it at; any other model among them
// at the row the model API bills it from, which is what the in-process meter holds
// a call at before it is made. Admin adds how each price
// was set (the upstream the model typically routes to, its list price and the
// multiple), each model's route held to its price, why a model is refused, and the
// day's ledger, which name upstreams: the caller sets it for a SuperAdmin alone.
//
// It reads no org's data: the prices are the platform's.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRates(ctx context.Context, in *client.RatesIn) (*client.Rates, error) {
	if in == nil {
		in = &client.RatesIn{}
	}
	out := ratesOf(aictl.Prices(ctx, in.Admin))
	out.Quotes = append(out.Quotes, routed(out.Quotes, in.Models)...)
	out.Quotes = append(out.Quotes, listed(out.Quotes, in.Models)...)
	return out, nil
}

// depth resolves a router id to the tier a request that states no depth is served
// at (aictl.DepthRoute), indirected so a test states the router table directly.
var depth = func(id string) (tier string, router bool) {
	tier, _, router = aictl.DepthRoute(id, nil)
	return tier, router
}

// routed is a quote for each router id among ids, at its default tier's price from
// tiers, the quotes already made. A router id whose tier is unserved or unpriced has
// none, and neither has any other id.
func routed(tiers []client.Quote, ids []string) []client.Quote {
	by := make(map[string]client.Quote, len(tiers))
	for _, q := range tiers {
		by[strings.ToLower(q.Model)] = q
	}
	var out []client.Quote
	for _, id := range ids {
		key := strings.ToLower(strings.TrimSpace(id))
		if _, quoted := by[key]; quoted || key == "" {
			continue
		}
		tier, router := depth(key)
		q, ok := by[strings.ToLower(tier)]
		if !router || !ok {
			continue
		}
		r := client.Quote{Model: key, Price: q.Price, Tier: q.Model}
		by[key] = r
		out = append(out, r)
	}
	return out
}

// priced is the row the model API bills id from (aictl.PriceOf), indirected so a
// test states the price table directly.
var priced = func(id string) (aictl.Rate, bool) { return aictl.PriceOf(id, "") }

// listed is a quote for each id among ids that is neither quoted already nor a
// router id, at the row the model API bills it from. An id no row prices has none.
func listed(quoted []client.Quote, ids []string) []client.Quote {
	seen := make(map[string]bool, len(quoted))
	for _, q := range quoted {
		seen[strings.ToLower(q.Model)] = true
	}
	var out []client.Quote
	for _, id := range ids {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, router := depth(key); router {
			continue
		}
		if r, ok := priced(key); ok {
			out = append(out, client.Quote{Model: key, Price: priceOf(r)})
		}
	}
	return out
}

// ratesOf moves a board onto the wire. What names upstreams is on the board only
// when a SuperAdmin asked.
func ratesOf(b aictl.Board) *client.Rates {
	out := &client.Rates{Free: skus(b.Free), Error: b.Error, Quotes: make([]client.Quote, 0, len(b.Prices))}
	for _, l := range b.Prices {
		out.Quotes = append(out.Quotes, quoteOf(l))
	}
	if l := b.Ledger; l != nil {
		out.Ledger = &client.RateLedger{
			Since: unix(l.Since), Until: unix(l.Until), Requests: l.Requests, Failovers: l.Failovers,
			Fallbacks: l.Fallbacks, FallbackShare: l.FallbackShare, Uncosted: l.Uncosted, Losses: l.Losses,
			Loss: l.LossNano, Cost: l.CostNano, Billed: l.BilledNano, Margin: l.MarginNano,
			Routes: make([]client.RateRoute, 0, len(l.Routes)),
		}
		for _, r := range l.Routes {
			out.Ledger.Routes = append(out.Ledger.Routes, client.RateRoute{
				Model: r.Model, Served: r.Served, Vendor: r.Vendor, Requests: r.Requests,
				Failovers: r.Failovers, Uncosted: r.Uncosted,
				Cost: r.CostNano, Billed: r.BilledNano, Margin: r.MarginNano, Loss: r.LossNano,
			})
		}
	}
	return out
}

// skus is ids less the router ids among them. A router id the family's service
// still lists as a free model is billed at the tier that answers it
// (aictl.DepthRoute), so it is never free.
func skus(ids []string) []string {
	var out []string
	for _, id := range ids {
		if _, router := depth(id); !router {
			out = append(out, id)
		}
	}
	return out
}

// quoteOf moves one model's price onto the wire.
func quoteOf(l aictl.Listing) client.Quote {
	out := client.Quote{Model: l.Model, Price: priceOf(l.Price), Typical: l.Typical, Multiple: l.Multiple, Refused: l.Refused}
	if l.List != nil {
		p := priceOf(*l.List)
		out.List = &p
	}
	for _, a := range l.Arms {
		arm := client.RateArm{Upstream: a.Upstream, Provider: a.Provider, Kind: a.Kind, Fee: a.Fee, List: priceOf(a.List), Above: a.Above, Over: a.Over}
		if a.Long != nil {
			p := priceOf(*a.Long)
			arm.Long = &p
		}
		out.Arms = append(out.Arms, arm)
	}
	return out
}

// priceOf writes a rate's dollars a million tokens as exact decimals: each is
// the shortest decimal its float reads back as, which is the decimal ai computed
// it from. A cache class the rate does not state is "".
func priceOf(r aictl.Rate) client.Price {
	dec := func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
	p := client.Price{Input: dec(r.Input), Output: dec(r.Output)}
	if r.CacheRead > 0 {
		p.CacheRead = dec(r.CacheRead)
	}
	if r.CacheWrite > 0 {
		p.CacheWrite = dec(r.CacheWrite)
	}
	return p
}

// unix is t in unix seconds, 0 for the zero time.
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
