package pricing

// tariff.go — the rate card, published.
//
// WHAT A BILL IS MADE OF is not in the @hanzo/pricing bundle and must not be:
// the four components, the compute rates and the per-call web rates are all
// CHARGED, and a charged number that is also typed into a published snapshot is
// the exact shape that went stale before — the snapshot beside this one advertised
// a first-party family at a third of what it billed, for weeks (see commerce.go).
//
// So the card is assembled in Go, beside the floors, and every priced line on it
// is read through [cloud.RateMicros] — the same resolver the metering path calls.
// The number a reader is quoted and the number the ledger books are then one
// number, resolved once per read, rather than two that agree until someone edits
// one of them.
//
// IT IS PUBLISHED TWICE AND SOURCED ONCE. A caller who wants only the rates reads
// /v1/pricing/tariff; a caller who reads the whole document finds the same card
// under `tariff`, because the whole document is what the marketing build syncs and
// a rate it could not see there is a rate it would end up typing. Both go through
// [priceCard], so there is one assembly and no chance of two answers.
//
// AND IT REPRICES THE TOOL SECTION. The bundle's `tools` list has always carried a
// "Web Search" row at its own price. That row and the card's web_search are one
// fact, so the card wins and the row is rewritten from it, exactly as commerce's
// rates rewrite the first-party models. The rows the card does not price — code
// interpreter, storage, speech — are not its to touch and are passed through.

import (
	"context"

	"github.com/hanzoai/cloud"
)

// GetTariff returns the platform's rate card: what a bill is made of, what each
// part costs, and the completion windows a request may ask for.
//
// FOUR COMPONENTS, and every charge is one of them — model inference, computer,
// web tools and media generation. Two are quoted before they run, so an agent is
// refused before it breaches its budget; two are booked from what they used,
// because neither a provider's charge nor a render's cost is knowable in advance.
//
// EVERY AMOUNT IS INTEGER MICRO-USD (1 USD = 1,000,000), stated once in `unit`,
// and each rate says what one unit of it is in `per`. The compute rates are per
// HOUR because that is the unit a span is priced in — rate × seconds / 3600 — and
// because a GiB-second is four and a half micro-USD, which no integer holds.
//
// The rates are the ones the ledger books: each is resolved through the same
// authority the metering path reads, falling back to the same compiled floor. A
// rate of zero is a price and not an absence — a paused computer, a computer's
// creation, the interfaces and a seat all cost nothing by design.
func (o ops) tariff(ctx context.Context, _ *pricingNoInput) (*cloud.Card, error) {
	card := cloud.Current(ctx)
	return &card, nil
}

// priceCard writes the card into a served document and reprices the tool rows the
// card owns. It is the ONE assembly both published forms go through.
func priceCard(ctx context.Context, doc map[string]any) {
	card := cloud.Current(ctx)
	doc["tariff"] = card
	if tools, ok := doc["tools"].([]any); ok {
		doc["tools"] = repriceTools(tools, card)
	}
}

// webRows are the tool rows the card prices, by the name the bundle's list uses.
// A row the card does not name is not the card's to reprice.
var webRows = map[string]string{"Web Search": "web_search", "Web Fetch": "web_fetch"}

// repriceTools rewrites the web rows of the bundle's tool list from the card, and
// appends any the list does not carry.
//
// The bundle owns a row's EDITORIAL copy — its name and the words its unit reads
// in — and the card owns the number, which is the same division of ownership the
// first-party model overlay keeps. A row whose shape is not the list's is passed
// through untouched rather than guessed at.
func repriceTools(tools []any, card cloud.Card) []any {
	priced := map[string]cloud.Rate{}
	for _, r := range card.Rates {
		priced[r.Name] = r
	}
	out := make([]any, 0, len(tools)+len(webRows))
	seen := map[string]bool{}
	for _, raw := range tools {
		row, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		name, _ := row["name"].(string)
		rate, owned := priced[webRows[name]]
		if !owned {
			out = append(out, raw)
			continue
		}
		row["price"] = dollars(rate.Micros)
		row["unit"] = "per " + rate.Per
		seen[rate.Name] = true
		out = append(out, row)
	}
	// A web row the list has never carried — web_fetch is one — still belongs on
	// it, or the list is a second answer to "what does a web call cost" that is
	// missing half the question.
	for display, name := range webRows {
		if seen[name] {
			continue
		}
		if rate, owned := priced[name]; owned {
			out = append(out, map[string]any{
				"name":  display,
				"price": dollars(rate.Micros),
				"unit":  "per " + rate.Per,
			})
		}
	}
	return out
}

// dollars renders micro-USD as the decimal the tool list has always carried. It
// is a DISPLAY conversion at the publish boundary — the card beside it keeps the
// integer, and nothing bills off this number.
func dollars(micros int64) float64 { return float64(micros) / 1e6 }

// repricedTools is [repriceTools] over the shape the tool SECTION answers in, so
// the leaf and the whole document cannot disagree about what a web call costs.
func repricedTools(ctx context.Context, tools []pricingBlob) []pricingBlob {
	raw := make([]any, 0, len(tools))
	for _, t := range tools {
		raw = append(raw, map[string]any(t))
	}
	out := repriceTools(raw, cloud.Current(ctx))
	rows := make([]pricingBlob, 0, len(out))
	for _, r := range out {
		if m, ok := r.(map[string]any); ok {
			rows = append(rows, pricingBlob(m))
			continue
		}
		if m, ok := r.(pricingBlob); ok {
			rows = append(rows, m)
		}
	}
	return rows
}
