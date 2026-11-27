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

import (
	"slices"
	"testing"
)

// The Claude models api.hanzo.ai lists (GET /v1/models, 2026-11-17) and the
// first-party ids Enso's rungs name: every one of them resolves to a record.
var routed = []string{
	"anthropic/claude-fable-5", "anthropic/claude-fable-5.1", "anthropic/claude-fable-5.1:batch",
	"anthropic/claude-fable-5.2", "anthropic/claude-fable-5.2:batch", "~anthropic/claude-fable-latest",
	"anthropic/claude-opus-4.1", "anthropic/claude-opus-4.5", "anthropic/claude-opus-4.6",
	"anthropic/claude-opus-4.7", "anthropic/claude-opus-4.8", "anthropic/claude-opus-5",
	"anthropic/claude-opus-5:batch", "anthropic/claude-opus-5.5", "anthropic/claude-opus-5.5:batch",
	"~anthropic/claude-opus-latest", "anthropic/claude-sonnet-4.5", "anthropic/claude-sonnet-4.6",
	"anthropic/claude-sonnet-5", "anthropic/claude-sonnet-5.5", "anthropic/claude-sonnet-5.5:batch",
	"~anthropic/claude-sonnet-latest", "anthropic/claude-haiku-4.5", "anthropic/claude-haiku-5.5",
	"anthropic/claude-haiku-5.5:batch", "~anthropic/claude-haiku-latest",
	"claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-5", "claude-haiku-5-5",
}

func TestEveryRoutedClaudeModelHasACompleteRecord(t *testing.T) {
	for _, id := range routed {
		r, ok := Lookup(id)
		if !ok {
			t.Errorf("%s: no capability record", id)
			continue
		}
		if r.Model == "" || r.Route == "" || r.Residency.Account == "" {
			t.Errorf("%s: record names no model, route or account: %+v", id, r)
		}
		if r.Thinking.Family == "" || !slices.Contains(r.Thinking.Reads, r.Thinking.Family) {
			t.Errorf("%s: a model reads its own thinking blocks; family %q, reads %v", id, r.Thinking.Family, r.Thinking.Reads)
		}
		p := r.Pricing
		if p.Input == "" || p.Output == "" || p.CacheRead == "" || p.CacheWrite == "" || p.CacheWrite1h == "" {
			t.Errorf("%s: base and cache pricing must all be stated: %+v", id, p)
		}
		if r.Context <= 0 || r.Output <= 0 {
			t.Errorf("%s: context %d, output %d", id, r.Context, r.Output)
		}
	}
	if err := validate(records); err != nil {
		t.Fatal(err)
	}
}

func TestFable52IsRecordedAsTheProviderDocumentsIt(t *testing.T) {
	r, ok := Lookup("anthropic/claude-fable-5.2")
	if !ok {
		t.Fatal("no record for Fable 5.2")
	}
	if r.Model != "claude-fable-5-2" || r.Route != "openrouter" {
		t.Fatalf("model %q route %q", r.Model, r.Route)
	}
	if r.Retention.Days != 30 || r.Retention.ZeroAuthorized {
		t.Errorf("Fable 5.2 keeps 30 days and has no zero-retention authorization: %+v", r.Retention)
	}
	if r.Tools.Forced {
		t.Error("Fable 5.2 answers 400 to a forced tool choice")
	}
	for _, p := range []string{"temperature", "top_p", "top_k"} {
		if !slices.Contains(r.Sampling.Rejects, p) {
			t.Errorf("Fable 5.2 answers 400 to %s", p)
		}
	}
	if r.Thinking.Off || r.Thinking.Budget || !r.Thinking.AccountBound {
		t.Errorf("Fable 5.2 thinks always, takes no budget, and its blocks are bound to the account: %+v", r.Thinking)
	}
	want := Pricing{Input: "8.00", Output: "40.00", CacheRead: "0.20", CacheWrite: "10.00", CacheWrite1h: "16.00",
		Fast: &Speed{Input: "16.00", Output: "80.00"}}
	if r.Pricing.Input != want.Input || r.Pricing.Output != want.Output || r.Pricing.CacheRead != want.CacheRead ||
		r.Pricing.CacheWrite != want.CacheWrite || r.Pricing.CacheWrite1h != want.CacheWrite1h ||
		r.Pricing.Fast == nil || *r.Pricing.Fast != *want.Fast {
		t.Errorf("pricing %+v (fast %+v), want %+v (fast %+v)", r.Pricing, r.Pricing.Fast, want, want.Fast)
	}
	if r.Context != 1_000_000 || r.Output != 128_000 {
		t.Errorf("context %d output %d", r.Context, r.Output)
	}
	if len(r.Residency.Regions) != 0 {
		t.Errorf("OpenRouter states no region it keeps data in: %v", r.Residency.Regions)
	}
}

func TestLookup(t *testing.T) {
	for _, id := range []string{" Anthropic/Claude-Fable-5.2 ", "anthropic/claude-fable-5.2:batch", "~anthropic/claude-fable-latest"} {
		if r, ok := Lookup(id); !ok || r.Model != "claude-fable-5-2" {
			t.Errorf("%q: %v %+v", id, ok, r)
		}
	}
	if r, ok := Lookup("claude-fable-5-2"); !ok || r.Route != "anthropic" {
		t.Errorf("the first-party id is the Anthropic account's route: %v %+v", ok, r)
	}
	// Enso's tiers and Zen's SKUs are routers, not models: their rungs answer from
	// the engine's catalog (hanzo-inc/engine), which carries its own per-rung rules.
	for _, id := range []string{"enso-max", "enso-auto", "zen5", "", "openai/gpt-4o"} {
		if _, ok := Lookup(id); ok {
			t.Errorf("%q has no record", id)
		}
	}
}

func TestReads(t *testing.T) {
	get := func(id string) Record {
		r, ok := Lookup(id)
		if !ok {
			t.Fatalf("no record for %s", id)
		}
		return r
	}
	cases := []struct {
		writer, reader string
		want           bool
	}{
		{"claude-fable-5-2", "claude-fable-5-2", true},
		{"claude-opus-5-5", "claude-fable-5-2", true},             // Fable 5.2 reads Opus 5.5's blocks
		{"claude-fable-5-2", "claude-opus-5-5", false},            // no earlier model reads Fable 5.2's
		{"claude-sonnet-5-5", "claude-opus-5-5", true},            // Opus 5.5 reads Sonnet 5.5's
		{"claude-opus-5-5", "claude-sonnet-5-5", false},           // Sonnet 5.5 does not read Opus 5.5's
		{"claude-haiku-5-5", "claude-sonnet-5-5", true},           // Sonnet 5.5 reads Haiku 5.5's
		{"claude-sonnet-5-5", "claude-haiku-5-5", false},          // and not the other way
		{"anthropic/claude-fable-5.2", "claude-fable-5-2", false}, // account-bound: OpenRouter's blocks are not ours
		{"anthropic/claude-opus-5.5", "claude-fable-5-2", true},   // Opus 5.5's blocks are not account-bound
	}
	for _, c := range cases {
		if got := Reads(get(c.writer), get(c.reader)); got != c.want {
			t.Errorf("Reads(%s → %s) = %v, want %v", c.writer, c.reader, got, c.want)
		}
	}
}
