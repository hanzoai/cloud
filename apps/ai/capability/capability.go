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

// Package capability is what a routed model requires and accepts, stated per route,
// and the gate that holds a request to it before any provider is called
// (hanzo-inc/cloud#801).
//
// A model is not a drop-in for another. Claude Fable 5.2 keeps every request 30 days
// unless its provider expressly authorized zero retention, answers 400 to a forced
// tool choice and to any sampling parameter, always thinks, and writes thinking blocks
// that only it and Mythos 5.2 read, in the account that wrote them. Sent a request
// it cannot take, it answers an opaque 400; sent one an organization's policy forbids,
// it serves it. A Record states those facts; Gate reads them against the request and
// the caller's policy before dispatch, rewrites what can be rewritten without
// changing what was asked, refuses what cannot, and says which checks passed.
//
// Records are routes, not models: the same model reached through OpenRouter and
// through Anthropic's own key is two records, because the account a thinking block
// is bound to and the place data is kept are the route's.
//
// Enso's tiers and Zen's SKUs have no record here. They are routers: the rung that
// answers is the engine's choice (hanzo-inc/engine for Enso, hanzo-inc/zen for Zen),
// and the engine catalog carries the per-rung rules (`unforced`, `drops`, `reasons`,
// `efforts`; universe charts/app/files/enso/catalog.yaml).
package capability

import (
	"fmt"
	"slices"
	"strings"
)

// Record is one route to one model: what it keeps, what it reads, what it takes,
// where it runs and what it costs.
type Record struct {
	// Model is the model's first-party id (claude-fable-5-2), the name its
	// thinking-block family is stated in.
	Model string
	// Route is the account that serves it: openrouter, anthropic.
	Route string
	// Names are the ids a request names it by on this route. A ":batch" suffix
	// resolves to the same record.
	Names []string
	// Context is the most tokens it reads at once; Output the most one answer holds.
	Context, Output int

	Retention Retention
	Thinking  Thinking
	Tools     Tools
	Sampling  Sampling
	Residency Residency
	Pricing   Pricing

	// Entitlement is the plan entitlement a request to this route needs before it is
	// dispatched, "" for none: the usage policy (apps/ai/limits) refuses it to a
	// payer whose plan does not include it by name (apps/flags Entitlements,
	// hanzo-inc/cloud#803).
	Entitlement string
}

// Retention is how long the route keeps a request.
type Retention struct {
	// Days is the retention the route requires: a request sent to it is kept that
	// long. 0 serves under zero data retention.
	Days int
	// ZeroAuthorized says the provider expressly authorized zero retention for the
	// account this route runs on, which waives Days.
	ZeroAuthorized bool
}

// Thinking is how the model thinks and whose thinking blocks it reads.
type Thinking struct {
	// Family is the family its own thinking blocks belong to: its first-party id.
	Family string
	// Reads are the families whose thinking blocks it reads. A block it does not
	// read is dropped by the provider, unbilled; a conversation that moves onto
	// it runs on a fresh reasoning context.
	Reads []string
	// AccountBound says its blocks work only in the account that wrote them.
	AccountBound bool
	// Off says it takes thinking {"type": "disabled"}; Budget that it takes
	// {"type": "enabled", "budget_tokens": N}. A model that takes neither runs
	// adaptive thinking whatever the request says, and answers 400 to either.
	Off, Budget bool
}

// Tools is what it takes of a tool choice.
type Tools struct {
	// Forced says it takes a forced tool choice: "required"/"any", or one named
	// tool. A model that does not answers 400 to one.
	Forced bool
}

// Sampling is what it takes of the sampling parameters.
type Sampling struct {
	// Rejects are the top-level parameters it answers 400 to (temperature, top_p,
	// top_k). Omitting one runs the model's default.
	Rejects []string
}

// Residency is where the route runs and whose account it runs on.
type Residency struct {
	// Regions are the regions the route keeps data in, as a policy names them
	// ("us", "eu"). Empty: the route states none, so no residency policy is met.
	Regions []string
	// Account is the provider account the route spends and its thinking blocks
	// are bound to.
	Account string
}

// Pricing is the route's list price in USD per million tokens, each an exact
// decimal string: the cost basis. Retail is ai's (its listing adds OpenRouter's
// fee and the platform's) and the catalogs' (Enso's and Zen's margins); what a
// route costs too much to serve on every plan says so in Record.Entitlement.
type Pricing struct {
	Input, Output string
	// CacheRead is a cached prompt token; CacheWrite one written to the 5-minute
	// cache, CacheWrite1h to the 1-hour cache.
	CacheRead, CacheWrite, CacheWrite1h string
	// Fast is the price of fast mode (speed: "fast"), nil where the route has none.
	// A batch request (":batch") pays half of Input and Output.
	Fast *Speed
}

// Speed is a faster mode's price, USD per million tokens.
type Speed struct{ Input, Output string }

// Lookup is the record a request naming model resolves to.
func Lookup(model string) (Record, bool) {
	id := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), ":batch")
	if id == "" {
		return Record{}, false
	}
	r, ok := byName[id]
	return r, ok
}

// Reads reports whether reader reads the thinking blocks writer wrote: its family
// is one reader reads, and a block bound to its account stays in that account.
func Reads(writer, reader Record) bool {
	if !slices.Contains(reader.Thinking.Reads, writer.Thinking.Family) {
		return false
	}
	return !writer.Thinking.AccountBound || writer.Residency.Account == reader.Residency.Account
}

// byName indexes every record by every name it answers to.
var byName = func() map[string]Record {
	if err := validate(records); err != nil {
		panic(err)
	}
	m := map[string]Record{}
	for _, r := range records {
		for _, n := range r.Names {
			m[strings.ToLower(n)] = r
		}
	}
	return m
}()

// validate refuses a table that states a route only in part, or names one id twice.
func validate(rs []Record) error {
	seen := map[string]string{}
	for _, r := range rs {
		where := r.Route + ":" + r.Model
		switch {
		case r.Model == "" || r.Route == "" || len(r.Names) == 0:
			return fmt.Errorf("capability: %s names no model, route or name", where)
		case r.Residency.Account == "":
			return fmt.Errorf("capability: %s names no account", where)
		case r.Thinking.Family == "" || !slices.Contains(r.Thinking.Reads, r.Thinking.Family):
			return fmt.Errorf("capability: %s must read its own thinking family", where)
		case r.Pricing.Input == "" || r.Pricing.Output == "" || r.Pricing.CacheRead == "" ||
			r.Pricing.CacheWrite == "" || r.Pricing.CacheWrite1h == "":
			return fmt.Errorf("capability: %s states its base and cache pricing only in part", where)
		case r.Context <= 0 || r.Output <= 0:
			return fmt.Errorf("capability: %s states no context or output bound", where)
		case r.Retention.Days < 0:
			return fmt.Errorf("capability: %s keeps a negative number of days", where)
		}
		for _, n := range r.Names {
			n = strings.ToLower(n)
			if prior, dup := seen[n]; dup {
				return fmt.Errorf("capability: %q names both %s and %s", n, prior, where)
			}
			seen[n] = where
		}
	}
	return nil
}
