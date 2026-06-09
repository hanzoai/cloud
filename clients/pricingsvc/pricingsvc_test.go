package pricingsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/clients/gojahost"
	hplans "github.com/hanzoai/plans"
	hpricing "github.com/hanzoai/pricing"
)

// newHost loads the REAL @hanzo/pricing goja bundle + embedded catalogs, so
// this exercises the actual server.mjs handler port + sync.mjs markup port
// running in goja (Express dropped).
func newHost(t *testing.T) *gojahost.Host {
	t.Helper()
	bundle, err := hpricing.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	pricingData, err := hpricing.Pricing()
	if err != nil {
		t.Fatalf("Pricing: %v", err)
	}
	plansExtra, err := hpricing.PlansExtra()
	if err != nil {
		t.Fatalf("PlansExtra: %v", err)
	}
	plansData, err := hplans.Data()
	if err != nil {
		t.Fatalf("plans.Data: %v", err)
	}
	h, err := gojahost.New(gojahost.Config{
		Name:   "pricing",
		Bundle: bundle,
		Globals: map[string]any{
			"__PRICING_DATA__": pricingData,
			"__PLANS_EXTRA__":  plansExtra,
			"__PLANS_DATA__":   plansData,
			"__MARKUP__":       map[string]any{"thirdParty": 1.0, "computeMonthly": 1.0},
		},
	})
	if err != nil {
		t.Fatalf("gojahost.New: %v", err)
	}
	return h
}

func TestPricing_Summary(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "summary"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		TotalModels int `json:"totalModels"`
		ZenModels   int `json:"zenModels"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.TotalModels <= 0 || body.ZenModels <= 0 {
		t.Fatalf("expected non-zero model counts, got total=%d zen=%d", body.TotalModels, body.ZenModels)
	}
}

func TestPricing_PublicStripsInternal(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "pricing"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Cloud map[string]json.RawMessage `json:"cloud"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, leaked := body.Cloud["_internal"]; leaked {
		t.Fatal("cloud._internal (provider costs / routing) leaked into public /v1/pricing")
	}
}

func TestPricing_Model404(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "model", Params: map[string]string{"name": "no-such-model"}})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestPricing_SubscriptionsFromPlans(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), gojahost.Request{Route: "subscriptions"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Plans) == 0 {
		t.Fatal("expected subscription plans from @hanzo/plans")
	}
}

// TestPricing_ApplyMarkupRunsInGoja proves the sync.mjs markup transform runs
// in goja: feed a raw OpenRouter-shaped model, assert toMTok markup math.
func TestPricing_ApplyMarkupRunsInGoja(t *testing.T) {
	h := newHost(t)
	defer h.Close()
	raw := `{"openrouter":[{"id":"openai/gpt-4o","name":"GPT-4o","context_length":128000,"pricing":{"prompt":"0.0000025","completion":"0.00001"}}],"huggingface":[{"id":"meta-llama/Llama-3.1-8B"}]}`
	out, err := h.Eval(context.Background(), "applyMarkup", []byte(raw))
	if err != nil {
		t.Fatalf("Eval applyMarkup: %v", err)
	}
	var shaped struct {
		ThirdPartyModels []struct {
			ID      string `json:"id"`
			Pricing struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"pricing"`
		} `json:"thirdPartyModels"`
	}
	if err := json.Unmarshal(out, &shaped); err != nil {
		t.Fatalf("unmarshal shaped: %v", err)
	}
	if len(shaped.ThirdPartyModels) != 2 {
		t.Fatalf("expected 2 shaped models, got %d", len(shaped.ThirdPartyModels))
	}
	// toMTok: 0.0000025 * 1e6 * 1.0 = 2.5 ; 0.00001 * 1e6 = 10
	var gpt4o *struct {
		ID      string `json:"id"`
		Pricing struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		} `json:"pricing"`
	}
	for i := range shaped.ThirdPartyModels {
		if shaped.ThirdPartyModels[i].ID == "openai/gpt-4o" {
			gpt4o = &shaped.ThirdPartyModels[i]
		}
	}
	if gpt4o == nil {
		t.Fatal("gpt-4o not in shaped output")
	}
	if gpt4o.Pricing.Input != 2.5 || gpt4o.Pricing.Output != 10 {
		t.Fatalf("markup math wrong: input=%v output=%v, want 2.5/10", gpt4o.Pricing.Input, gpt4o.Pricing.Output)
	}
}
