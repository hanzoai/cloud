// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package host

import (
	"context"
	"errors"
	"sync"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/meter"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The MONEY PLANE's own measurements — what it refused, what it debited, and the
// work those debits priced.
//
// The ledger is the record and stays the record: it is exact, double-entry and
// idempotent, and nothing here is consulted to decide what anyone owes. What the
// ledger cannot answer is the OPERATOR's question, because answering it means
// reading every org's books and summing. "Is anything still being billed" is not
// a question about money; it is a question about whether a code path runs, and
// that is a measurement.
//
// It was unmeasured. cloud carried an O11yClient on Deps with a Counter/Timing/
// Span surface, a disabled stub for it, and exactly ONE call site in the whole
// binary — and the field was never assigned, so that call site was dead and every
// other product emitted nothing. Two instrument APIs, one of them unreachable.
// This is the one that is wired: the same OTel meter metrics_http.go and
// metrics_plane.go use, collected into the process registry and pushed to the
// telemetry store in-process by apps/o11y/metricspush.go.
//
// ZERO IS A MEASUREMENT, and it is why the plane-level series carry no product
// label. A counter that springs into existence on its first write cannot say
// "this stopped" — absent is what the store shows for a path that died, a product
// nobody deployed, and a typo alike. So the two questions an operator asks about
// the plane itself are seeded at boot across their whole finite vocabulary, and
// silence reads as zero from the first push.
//
// That vocabulary is finite; the set of products that BILL is not. Every
// cloud.Service gets a Meter named after its app, and most of them never charge
// for anything, so seeding per product would mint a hundred series that can only
// ever read zero — the same false "it stopped", pointed the other way. The
// per-product question is asked of the attribution counters instead, which are
// born on a product's first real charge and are therefore only ever asked about
// products that charge.
//
// CARDINALITY. outcome and verdict are closed sets fixed in this file. product is
// the meter's own provider string, bounded by the route table. org is the
// VALIDATED payer's books, never a client-chosen string — the same envelope
// metrics_http.go already reasons about.

const (
	// A debit reached the ledger and moved money.
	debitPosted = "posted"
	// The call was correct and there was no ledger in this deployment to take it.
	// Distinct from failed on purpose: a product metering into a deployment with
	// no commerce is working exactly as designed, and a product metering into a
	// deployment that HAS one and reads this is losing revenue silently.
	debitUnbilled = "unbilled"
	// The ledger was there and the debit did not land. The resource was created
	// and nobody was charged for it.
	debitFailed = "failed"
)

const (
	gateAllow    = "allow"
	gateNoFunds  = "no-funds"
	gateCapSpent = "cap-spent"
	// The balance could not be determined. Fail-closed turns this into a refusal
	// the customer cannot fix and the operator must — so it is counted apart from
	// the two verdicts that are the customer's own.
	gateUnknown = "unknown"
)

var (
	usageOnce sync.Once

	usageDebits  metric.Int64Counter   // hanzo_usage_debits_total{outcome}
	usageGate    metric.Int64Counter   // hanzo_usage_gate_total{verdict}
	usageCharged metric.Float64Counter // hanzo_usage_charged_usd_total{product,org}
	usageTokens  metric.Int64Counter   // hanzo_usage_tokens_total{product,org,direction}

	debitOutcomes = []string{debitPosted, debitUnbilled, debitFailed}
	gateVerdicts  = []string{gateAllow, gateNoFunds, gateCapSpent, gateUnknown}
)

// usageInstruments resolves the money plane's instruments and seeds the two
// plane-level series, once.
//
// LAZY, for the reason metrics_http.go states: a meter taken at init binds to the
// no-op provider that exists before the composition root installs the real one,
// and every measurement afterwards is discarded while the code looks perfectly
// instrumented. Every emitter below calls this first, so the seed lands on
// whichever of them the process reaches first — after Serve has installed the
// provider, since meters are constructed as subsystems mount.
func usageInstruments() {
	usageOnce.Do(func() {
		m := otel.Meter(meterName)
		usageDebits, _ = m.Int64Counter("hanzo_usage_debits_total",
			metric.WithDescription("Usage debits attempted against the prepaid ledger, by outcome."))
		usageGate, _ = m.Int64Counter("hanzo_usage_gate_total",
			metric.WithDescription("Pre-flight balance decisions, by verdict."))
		usageCharged, _ = m.Float64Counter("hanzo_usage_charged_usd_total",
			metric.WithDescription("Money debited by product and payer org, in US dollars."),
			metric.WithUnit("{USD}"))
		usageTokens, _ = m.Int64Counter("hanzo_usage_tokens_total",
			metric.WithDescription("Tokens the charge priced, by product, payer org and direction."))

		ctx := context.Background()
		for _, o := range debitOutcomes {
			if usageDebits != nil {
				usageDebits.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", o)))
			}
		}
		for _, v := range gateVerdicts {
			if usageGate != nil {
				usageGate.Add(ctx, 0, metric.WithAttributes(attribute.String("verdict", v)))
			}
		}
	})
}

// observeGate records one balance decision in the gate's own vocabulary.
//
// It takes the ERROR rather than a string so the mapping from a sentinel to a
// verdict lives here, once, instead of at each meter that gates. nil is an allow;
// the two sentinels are the customer's to fix; anything else is the operator's.
func observeGate(err error) {
	usageInstruments()
	if usageGate == nil {
		return
	}
	v := gateUnknown
	switch {
	case err == nil:
		v = gateAllow
	case errors.Is(err, meter.ErrInsufficientBalance):
		v = gateNoFunds
	case errors.Is(err, meter.ErrSpendCapExceeded):
		v = gateCapSpent
	}
	usageGate.Add(context.Background(), 1, metric.WithAttributes(attribute.String("verdict", v)))
}

// observeDebit records one debit and, when it landed, what it charged and what
// work that charge priced.
//
// The charge and the units are written ONLY on a posted debit, so the sum of the
// money series is the money the books took and not the money somebody meant to
// take. The units are the quantity the amount was computed from — the ledger
// holds the dollars and is right to hold nothing else, so this is where the
// question "how many tokens did that org burn" is answered. They join on nothing
// and need to: one is a rollup, the other is the record.
//
// A nil result with a nil error is the no-peer shape [metering.Client.Record]
// documents — there was no ledger anywhere to take the debit. It is counted
// apart, because a deployment that HAS commerce and reads unbilled is losing
// money in a way a green error rate will never show.
func observeDebit(product string, u meter.Usage, res *meter.RecordResult, err error) {
	usageInstruments()
	if usageDebits == nil {
		return
	}
	ctx := context.Background()
	switch {
	case err != nil:
		usageDebits.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", debitFailed)))
		return
	case res == nil:
		usageDebits.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", debitUnbilled)))
		return
	}
	usageDebits.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", debitPosted)))

	who := metric.WithAttributes(
		attribute.String("product", product),
		attribute.String("org", u.Org),
	)
	if usageCharged != nil {
		// Micros, not cents: a per-token charge priced at 18 decimals is real money
		// and rounding it to the cent here would make the rollup disagree with the
		// books by exactly the amount that is interesting.
		usageCharged.Add(ctx, float64(money.FromDecimal(u.Money().Decimal()).Micros())/1e6, who)
	}
	if usageTokens == nil {
		return
	}
	if n := int64(u.PromptTokens); n > 0 {
		usageTokens.Add(ctx, n, who, metric.WithAttributes(attribute.String("direction", "prompt")))
	}
	if n := int64(u.CompletionTokens); n > 0 {
		usageTokens.Add(ctx, n, who, metric.WithAttributes(attribute.String("direction", "completion")))
	}
}
