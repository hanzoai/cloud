package research

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// quoting answers the model API's prices for a test: each Enso tier at its own
// price and the router at its no-depth tier's, as the model API quotes them.
func quoting(t *testing.T, r *client.Rates, err error) {
	t.Helper()
	was := askRates
	askRates = func(context.Context, *client.RatesIn) (*client.Rates, error) { return r, err }
	book.Lock()
	book.at, book.p = time.Time{}, nil
	book.Unlock()
	t.Cleanup(func() {
		askRates = was
		book.Lock()
		book.at, book.p = time.Time{}, nil
		book.Unlock()
	})
}

var tiers = &client.Rates{Quotes: []client.Quote{
	{Model: "enso-flash", Price: client.Price{Input: "0.33", Output: "1.65"}},
	{Model: "enso-pro", Price: client.Price{Input: "4.4", Output: "22"}},
	{Model: "enso-ultra", Price: client.Price{Input: "5.5", Output: "27.5"}},
	{Model: "enso", Price: client.Price{Input: "0.33", Output: "1.65"}, Tier: "enso-flash"},
}}

// measure is a served row's measure by metric, and whether it is there.
func measure(row map[string]any, metric string) (float64, bool) {
	ms, _ := row["measures"].([]any)
	for _, m := range ms {
		mm := m.(map[string]any)
		if mm["metric"] == metric {
			return mm["value"].(float64), true
		}
	}
	return 0, false
}

// TestAStrangerReadsNoModelUnderEnsoZenOrKai: the anonymous listing serves an
// Enso or Zen run's numbers without its version, notes, prompt, embedder or
// reader, an enso.* benchmark's runs without notes, and no run of a base model;
// its key is over the served record, so the real key finds nothing. The org's own
// read is the full record.
func TestAStrangerReadsNoModelUnderEnsoZenOrKai(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		quoting(t, tiers, nil)
		measures := []Measure{
			{Category: "all", Metric: "accuracy", Value: 0.929293},
			{Category: "all", Metric: "prompt_tokens", Value: 56432},
			{Category: "all", Metric: "completion_tokens", Value: 191579},
		}
		upstream := "Upstream calls by model id: {'openai-gpt-5.6-sol': 190}. Gateway: the DigitalOcean inference gateway. Routed arm per item: {'gpt-5.6-sol': 190}."
		runs := []Run{
			{ID: "enso.gpqa_diamond-test-frontier-enso", Benchmark: "enso.gpqa_diamond", Split: "test", System: "enso",
				Version: "anthropic-claude-opus-4.8+openai-gpt-5.6-sol", Notes: upstream, Prompt: "prompts/gpt-5.6-sol.txt",
				Embedder: "qwen3-embedding-8b", Reader: "gpt-5.6-sol", Measures: measures},
			{ID: "enso.gpqa_diamond-test-frontier-opus-4.8", Benchmark: "enso.gpqa_diamond", Split: "test", System: "opus-4.8",
				Version: "anthropic-claude-opus-4.8", Notes: "Gateway: the DigitalOcean inference gateway.", Measures: measures},
			{ID: "gateway.cache-enso-flash", Benchmark: "gateway.cache", Split: "cache", System: "hanzo/enso-flash",
				Version: "api.hanzo.ai 2026-10-15", Notes: "Served by an upstream model."},
			{ID: "zen.prefill-zen6", Benchmark: "zen.prefill", Split: "all", System: "zen6", Version: "a base build"},
			{ID: "enso.gpqa_diamond-test-pool-oracle@K4", Benchmark: "enso.gpqa_diamond", Split: "test", System: "pool-oracle@K4",
				Version: "gpt-5.6-sol+grok-4.5"},
			{ID: "enso.gpqa_diamond-test-k3-max-kimi-k3", Benchmark: "enso.gpqa_diamond", Split: "test", System: "kimi-k3", Version: "kimi-k3"},
			{ID: "enso.gpqa_diamond-test-glm", Benchmark: "enso.gpqa_diamond", Split: "test", System: "glm-5.2@openrouter", Version: "z-ai/glm-5.2"},
			{ID: "latency-pool-multilingual-laya", Benchmark: "latency", Split: "pool", System: "laya", Version: "a base@sha"},
			{ID: "orig-test-kai", Benchmark: "orig", Split: "test", System: "kai", Version: "kai-1@0834a74f", Notes: "cold/cpu",
				Measures: []Measure{{Category: "all", Metric: "accuracy", Value: 0.9}, {Category: "all", Metric: "beats.laya", Value: 58}}},
			{ID: "gate.laya-drawn-a5-kai", Benchmark: "gate.laya", Split: "drawn", System: "kai", Version: "a5@df1c"},
			{ID: "enso.livecodebench-recent-frontier-enso-flash", Benchmark: "enso.livecodebench", Split: "recent", System: "enso-flash",
				Version: "anthropic-claude-fable-5", Measures: measures},
			{ID: "orig-test-jev", Benchmark: "orig", Split: "test", System: "jev", Version: "typesafe/jev-1.13", Notes: "hosted"},
		}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "hanzo", "", runBatch{Runs: runs}); code != http.StatusOK {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		pub := "public"
		for _, r := range runs {
			if code, m := do(t, app, http.MethodPost, "/v1/research/grants", "hanzo", "",
				GrantRequest{ID: r.ID, Visibility: &pub}); code != http.StatusOK {
				t.Fatalf("grant %s status=%d body=%v", r.ID, code, m)
			}
		}

		rows := func(path string) map[string]map[string]any {
			t.Helper()
			code, m := do(t, app, http.MethodGet, path, "", "", nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d %v", path, code, m)
			}
			out := map[string]map[string]any{}
			data, _ := m["data"].([]any)
			for _, r := range data {
				row := r.(map[string]any)
				out[row["id"].(string)] = row
			}
			if int(m["total"].(float64)) != len(out) {
				t.Fatalf("GET %s total=%v, rows=%d", path, m["total"], len(out))
			}
			return out
		}

		got := rows("/v1/research/runs?org=hanzo")
		if len(got) != 8 {
			t.Fatalf("stranger reads %d runs, want 8 (the four base runs withheld): %v", len(got), got)
		}
		for _, id := range []string{"enso.gpqa_diamond-test-k3-max-kimi-k3", "enso.gpqa_diamond-test-glm", "latency-pool-multilingual-laya", "gate.laya-drawn-a5-kai"} {
			if _, ok := got[id]; ok {
				t.Errorf("base run %s served to a stranger", id)
			}
			if one := rows("/v1/research/runs?org=hanzo&id=" + url.QueryEscape(id)); len(one) != 0 {
				t.Errorf("?id=%s served %v, want nothing", id, one)
			}
		}

		enso := got["enso.gpqa_diamond-test-frontier-enso"]
		for _, f := range []string{"version", "notes", "prompt", "embedder", "reader"} {
			if enso[f] != "" {
				t.Errorf("enso run %s = %q, want empty", f, enso[f])
			}
		}
		// The router's tokens at the dearest tier's price, and no spend measure beside it.
		if n := len(enso["measures"].([]any)); n != 2 {
			t.Errorf("enso run carries %d measures, want accuracy and %s: %v", n, ListCost, enso["measures"])
		}
		for _, m := range []string{"prompt_tokens", "completion_tokens", "reasoning_tokens", "calls", "cost_usd"} {
			if _, ok := measure(enso, m); ok {
				t.Errorf("enso run served %s", m)
			}
		}
		if v, ok := measure(enso, ListCost); !ok || math.Abs(v-(56432*5.5+191579*27.5)/1e6) > 1e-9 {
			t.Errorf("router %s = %v %v, want its tokens at enso-ultra's 5.5/27.5", ListCost, v, ok)
		}
		flash := got["enso.livecodebench-recent-frontier-enso-flash"]
		if v, ok := measure(flash, ListCost); !ok || math.Abs(v-(56432*0.33+191579*1.65)/1e6) > 1e-9 {
			t.Errorf("tier %s = %v %v, want its tokens at its own 0.33/1.65", ListCost, v, ok)
		}
		if _, ok := measure(got["enso.gpqa_diamond-test-frontier-opus-4.8"], "prompt_tokens"); !ok {
			t.Error("a competitor's tokens were left off; it keeps every measure")
		}
		if _, ok := measure(got["orig-test-kai"], "beats.laya"); ok {
			t.Error("a measure naming a base was served")
		}
		if _, ok := measure(got["orig-test-kai"], "accuracy"); !ok {
			t.Error("the Kai run lost a measure that names no base")
		}
		want := keyOf(Run{Benchmark: "enso.gpqa_diamond", Split: "test", System: "enso",
			Measures: []Measure{{Category: "all", Metric: "accuracy", Value: 0.929293}, {Category: "all", Metric: ListCost, Value: (56432*5.5 + 191579*27.5) / 1e6}}})
		if enso["key"] != want {
			t.Errorf("enso run key = %v, want the key over the served record %s", enso["key"], want)
		}
		for _, id := range []string{"gateway.cache-enso-flash", "zen.prefill-zen6", "enso.gpqa_diamond-test-pool-oracle@K4"} {
			if got[id]["version"] != "" || got[id]["notes"] != "" {
				t.Errorf("%s served version %q notes %q, want both empty", id, got[id]["version"], got[id]["notes"])
			}
		}
		opus := got["enso.gpqa_diamond-test-frontier-opus-4.8"]
		if opus["version"] != "anthropic-claude-opus-4.8" || opus["notes"] != "" {
			t.Errorf("competitor in an enso.* benchmark served version %q notes %q, want its version and no notes", opus["version"], opus["notes"])
		}
		for id, v := range map[string]string{"orig-test-kai": "kai-1@0834a74f", "orig-test-jev": "typesafe/jev-1.13"} {
			if got[id]["version"] != v || got[id]["notes"] == "" {
				t.Errorf("%s served %v, want it whole", id, got[id])
			}
		}

		real := keyOf(runs[0])
		if one := rows("/v1/research/runs?org=hanzo&key=" + real); len(one) != 0 {
			t.Errorf("the real key found %v on the public read, want nothing", one)
		}
		if one := rows("/v1/research/runs?org=hanzo&key=" + want); len(one) != 1 {
			t.Errorf("the served key found %d runs, want the enso run", len(one))
		}

		// The org's own read is the full record, and its real key still answers.
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?key="+real, "hanzo", "", nil)
		if code != http.StatusOK || m["total"].(float64) != 1 {
			t.Fatalf("owner ?key= = %d %v, want the enso run", code, m)
		}
		own := m["data"].([]any)[0].(map[string]any)
		if own["version"] != runs[0].Version || own["notes"] != upstream || own["embedder"] != runs[0].Embedder {
			t.Errorf("owner read %v, want the full record", own)
		}
		if _, ok := measure(own, "prompt_tokens"); !ok {
			t.Errorf("owner read lost its tokens: %v", own["measures"])
		}
		if _, ok := measure(own, ListCost); ok {
			t.Errorf("owner read carries %s, which only the public record serves", ListCost)
		}
		if code, m := do(t, app, http.MethodGet, "/v1/research/runs", "hanzo", "", nil); code != http.StatusOK || int(m["total"].(float64)) != len(runs) {
			t.Errorf("owner listing = %d %v, want all %d runs", code, m["total"], len(runs))
		}

	})
}

func TestServedNamesEachFamily(t *testing.T) {
	for _, c := range []struct {
		system       string
		family, base bool
	}{
		{"enso", true, false}, {"enso-ultra", true, false}, {"hanzo/enso-pro", true, false},
		{"zen", true, false}, {"zen6", true, false}, {"zen-eco", true, false}, {"hanzo/zen-embedding", true, false},
		{"pool-oracle@K11", true, false},
		{"zenith", false, false}, {"opus-4.8", false, false}, {"gpt-5.6-sol", false, false}, {"deepseek-v4-pro", false, false},
		{"kai", false, false}, {"jev", false, false}, {"typesafe/jev-1.13", false, false}, {"alaya", false, false},
		{"kimi-k3", false, true}, {"moonshotai/kimi-k3", false, true}, {"glm-5.2@openrouter", false, true},
		{"z-ai/glm-5.2", false, true}, {"Qwen3.5-397B", false, true}, {"laya", false, true},
	} {
		if family(c.system) != c.family || base(c.system) != c.base {
			t.Errorf("%s: family=%v base=%v, want %v %v", c.system, family(c.system), base(c.system), c.family, c.base)
		}
	}
}

// TestAStrangerReadsNoSpendWithoutAPrice: with the model API not answering, an
// Enso run is served without its spend and without a list cost, never with a
// guessed price.
func TestAStrangerReadsNoSpendWithoutAPrice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		quoting(t, nil, errors.New("ai unreachable"))
		run := Run{ID: "enso.gpqa_diamond-test-frontier-enso", Benchmark: "enso.gpqa_diamond", Split: "test", System: "enso",
			Measures: []Measure{{Category: "all", Metric: "accuracy", Value: 0.9}, {Category: "all", Metric: "prompt_tokens", Value: 10}, {Category: "all", Metric: "completion_tokens", Value: 20}}}
		if code, m := do(t, app, http.MethodPost, "/v1/research/runs", "hanzo", "", runBatch{Runs: []Run{run}}); code != http.StatusOK {
			t.Fatalf("POST runs status=%d body=%v", code, m)
		}
		pub := "public"
		if code, m := do(t, app, http.MethodPost, "/v1/research/grants", "hanzo", "", GrantRequest{ID: run.ID, Visibility: &pub}); code != http.StatusOK {
			t.Fatalf("grant status=%d body=%v", code, m)
		}
		code, m := do(t, app, http.MethodGet, "/v1/research/runs?org=hanzo", "", "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET = %d %v", code, m)
		}
		row := m["data"].([]any)[0].(map[string]any)
		if ms := row["measures"].([]any); len(ms) != 1 {
			t.Errorf("served %v, want accuracy alone", ms)
		}

	})
}

func TestListCostPricesTheTierACallerBuys(t *testing.T) {
	p := pricesOf(tiers, nil)
	if _, ok := p["enso"]; ok {
		t.Fatal("the router's no-depth quote is a tier price; a router bills the tier that answers")
	}
	tok := []Measure{{Category: "all", Metric: "prompt_tokens", Value: 1e6}, {Category: "all", Metric: "completion_tokens", Value: 1e6}}
	for _, c := range []struct {
		system string
		want   float64
		ok     bool
	}{
		{"enso", 33, true}, {"hanzo/enso", 33, true}, {"enso-auto", 33, true},
		{"enso-flash", 1.98, true}, {"enso-pro", 26.4, true}, {"enso-ultra", 33, true},
		{"enso-max", 0, false},
	} {
		got, ok := listCost(Run{System: c.system, Measures: tok}, p)
		if ok != c.ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: %v %v, want %v %v", c.system, got, ok, c.want, c.ok)
		}
	}
	if _, ok := listCost(Run{System: "enso", Measures: tok[:1]}, p); ok {
		t.Error("a run with no completion count was priced")
	}
	if _, ok := listCost(Run{System: "enso", Measures: tok}, nil); ok {
		t.Error("a run was priced with no prices read")
	}
}
