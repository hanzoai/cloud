// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package capability

// records.go — the table. Every fact here is Anthropic's documented API behavior
// (platform.claude.com: models, model migration, pricing, fast mode, as of
// 2026-11-17), and the model list is api.hanzo.ai's GET /v1/models of the same day:
// every Claude model it lists (OpenRouter's slugs, their ":batch" spellings and the
// "~anthropic/…-latest" aliases) and the first-party ids Enso's rungs name on
// Anthropic's own key. A model changes here, in one line of the table below.
//
// Neither route states a region: OpenRouter names none, and Anthropic's own API
// runs globally unless a request sets inference_geo, which no route sends. So a
// residency policy is met by no Claude route today, which is what the gate says.

// claude is one Claude model's facts, which both of its routes share.
type claude struct {
	id      string   // first-party id: claude-fable-5-2
	slug    string   // OpenRouter's: anthropic/claude-fable-5.2
	latest  string   // the ~anthropic alias that resolves to it today, if any
	ctx     int      // context window
	out     int      // max output tokens
	retain  int      // days of retention required (Anthropic's Covered Models: 30)
	forced  bool     // takes a forced tool_choice
	rejects []string // sampling parameters it answers 400 to
	off     bool     // takes thinking {"type":"disabled"}
	budget  bool     // takes thinking {"type":"enabled","budget_tokens":N}
	bound   bool     // its thinking blocks work only in the account that wrote them
	reads   []string // the families whose thinking blocks it reads, its own first
	price   Pricing
	entitle string // the plan entitlement a request to it needs (Record.Entitlement)
}

var (
	// sampling is the set Claude 4.7 and later answer 400 to (Sonnet 5.5 and Haiku
	// 5.5 to any value but the default, which is what omitting one sends).
	sampling = []string{"temperature", "top_p", "top_k"}

	// earlier is the Opus, Sonnet and Haiku models before Claude 5 whose blocks the
	// 5.5 line reads.
	earlier = []string{"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5",
		"claude-opus-4-1", "claude-sonnet-4-6", "claude-sonnet-4-5", "claude-haiku-4-5"}
)

func price(in, out, read, write, write1h string, fast *Speed) Pricing {
	return Pricing{Input: in, Output: out, CacheRead: read, CacheWrite: write, CacheWrite1h: write1h, Fast: fast}
}

func with(own string, more ...[]string) []string {
	r := []string{own}
	for _, m := range more {
		r = append(r, m...)
	}
	return r
}

var claudes = []claude{
	// Fable 5.2: a Covered Model (30-day retention, zero retention only by Anthropic's
	// express authorization); forced tool_choice, sampling, disabled thinking and a
	// thinking budget all 400; no earlier model reads its blocks, and they work only
	// in the account that wrote them or one linked to it. It reads Fable 5.1's and
	// 5's, and Opus, Sonnet and Haiku 5.5's. A plan serves it only when it includes
	// the fable entitlement (hanzo-inc/cloud#803).
	{id: "claude-fable-5-2", slug: "anthropic/claude-fable-5.2", latest: "~anthropic/claude-fable-latest",
		ctx: 1_000_000, out: 128_000, retain: 30, rejects: sampling, bound: true,
		reads:   with("claude-fable-5-2", []string{"claude-fable-5-1", "claude-fable-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}),
		price:   price("8.00", "40.00", "0.20", "10.00", "16.00", &Speed{Input: "16.00", Output: "80.00"}),
		entitle: "fable"},
	{id: "claude-fable-5-1", slug: "anthropic/claude-fable-5.1",
		ctx: 1_000_000, out: 128_000, retain: 30, rejects: sampling,
		reads: with("claude-fable-5-1", []string{"claude-fable-5", "claude-opus-5-5"}),
		price: price("10.00", "50.00", "0.25", "12.50", "20.00", nil)},
	{id: "claude-fable-5", slug: "anthropic/claude-fable-5",
		ctx: 1_000_000, out: 128_000, retain: 30, forced: true, rejects: sampling,
		reads: with("claude-fable-5"),
		price: price("10.00", "50.00", "1.00", "12.50", "20.00", nil)},

	{id: "claude-opus-5-5", slug: "anthropic/claude-opus-5.5", latest: "~anthropic/claude-opus-latest",
		ctx: 1_000_000, out: 128_000, rejects: sampling,
		reads: with("claude-opus-5-5", []string{"claude-opus-5", "claude-sonnet-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, earlier),
		price: price("4.00", "20.00", "0.20", "5.00", "8.00", &Speed{Input: "8.00", Output: "40.00"})},
	{id: "claude-opus-5", slug: "anthropic/claude-opus-5",
		ctx: 1_000_000, out: 128_000, forced: true, rejects: sampling, off: true,
		reads: with("claude-opus-5"),
		price: price("5.00", "25.00", "0.50", "6.25", "10.00", nil)},
	{id: "claude-opus-4-8", slug: "anthropic/claude-opus-4.8",
		ctx: 1_000_000, out: 128_000, forced: true, rejects: sampling, off: true,
		reads: with("claude-opus-4-8"),
		price: price("5.00", "25.00", "0.50", "6.25", "10.00", nil)},
	{id: "claude-opus-4-7", slug: "anthropic/claude-opus-4.7",
		ctx: 1_000_000, out: 128_000, forced: true, rejects: sampling, off: true,
		reads: with("claude-opus-4-7"),
		price: price("5.00", "25.00", "0.50", "6.25", "10.00", nil)},
	{id: "claude-opus-4-6", slug: "anthropic/claude-opus-4.6",
		ctx: 1_000_000, out: 128_000, forced: true, off: true, budget: true,
		reads: with("claude-opus-4-6"),
		price: price("5.00", "25.00", "0.50", "6.25", "10.00", nil)},
	{id: "claude-opus-4-5", slug: "anthropic/claude-opus-4.5",
		ctx: 200_000, out: 64_000, forced: true, off: true, budget: true,
		reads: with("claude-opus-4-5"),
		price: price("5.00", "25.00", "0.50", "6.25", "10.00", nil)},
	{id: "claude-opus-4-1", slug: "anthropic/claude-opus-4.1",
		ctx: 200_000, out: 32_000, forced: true, off: true, budget: true,
		reads: with("claude-opus-4-1"),
		price: price("15.00", "75.00", "1.50", "18.75", "30.00", nil)},

	{id: "claude-sonnet-5-5", slug: "anthropic/claude-sonnet-5.5", latest: "~anthropic/claude-sonnet-latest",
		ctx: 1_000_000, out: 128_000, rejects: sampling,
		reads: with("claude-sonnet-5-5", []string{"claude-sonnet-5", "claude-haiku-5-5"}, earlier),
		price: price("2.00", "10.00", "0.10", "2.50", "4.00", nil)},
	{id: "claude-sonnet-5", slug: "anthropic/claude-sonnet-5",
		ctx: 1_000_000, out: 128_000, forced: true, rejects: sampling, off: true,
		reads: with("claude-sonnet-5"),
		price: price("2.00", "10.00", "0.20", "2.50", "4.00", nil)},
	{id: "claude-sonnet-4-6", slug: "anthropic/claude-sonnet-4.6",
		ctx: 1_000_000, out: 128_000, forced: true, off: true, budget: true,
		reads: with("claude-sonnet-4-6"),
		price: price("3.00", "15.00", "0.30", "3.75", "6.00", nil)},
	{id: "claude-sonnet-4-5", slug: "anthropic/claude-sonnet-4.5",
		ctx: 1_000_000, out: 64_000, forced: true, off: true, budget: true,
		reads: with("claude-sonnet-4-5"),
		price: price("3.00", "15.00", "0.30", "3.75", "6.00", nil)},

	// Haiku 5.5's prices are for prompts up to 100K tokens; past that it is 0.50/2.50.
	{id: "claude-haiku-5-5", slug: "anthropic/claude-haiku-5.5", latest: "~anthropic/claude-haiku-latest",
		ctx: 1_000_000, out: 128_000, forced: true, rejects: sampling, off: true,
		reads: with("claude-haiku-5-5", []string{"claude-sonnet-5"}, earlier),
		price: price("0.10", "0.50", "0.01", "0.125", "0.20", nil)},
	{id: "claude-haiku-4-5", slug: "anthropic/claude-haiku-4.5",
		ctx: 200_000, out: 64_000, forced: true, off: true, budget: true,
		reads: with("claude-haiku-4-5"),
		price: price("1.00", "5.00", "0.10", "1.25", "2.00", nil)},
}

// records is every route to every model above: OpenRouter's, which ai serves the
// "anthropic/…" ids through, and Anthropic's own key, which a first-party id names.
var records = func() []Record {
	var rs []Record
	for _, c := range claudes {
		base := Record{
			Model: c.id, Context: c.ctx, Output: c.out,
			Retention: Retention{Days: c.retain},
			Thinking:  Thinking{Family: c.id, Reads: c.reads, AccountBound: c.bound, Off: c.off, Budget: c.budget},
			Tools:     Tools{Forced: c.forced},
			Sampling:  Sampling{Rejects: c.rejects},
			Pricing:   c.price,

			Entitlement: c.entitle,
		}
		or := base
		or.Route, or.Residency = "openrouter", Residency{Account: "openrouter"}
		or.Names = []string{c.slug}
		if c.latest != "" {
			or.Names = append(or.Names, c.latest)
		}
		own := base
		own.Route, own.Residency = "anthropic", Residency{Account: "anthropic"}
		own.Names = []string{c.id}
		rs = append(rs, or, own)
	}
	return rs
}()
