// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package ai

import (
	"strings"
	"testing"
	"time"

	aiobject "github.com/hanzoai/ai/object"
)

// A drained account reads on the SuperAdmin's pulse as a supply incident naming
// the vendor, the account and the model it was serving, and never a key.
func TestADrainedAccountIsASupplyPulse(t *testing.T) {
	at := time.Date(2026, 10, 17, 1, 25, 51, 0, time.UTC)
	p := drainedPulse(aiobject.Drained{Vendor: "openrouter", Account: "OPENROUTER_API_KEY_3", Model: "openai/gpt-5-mini", Status: 402, At: at})
	if p.Kind != "supply" || p.Key != "openrouter/OPENROUTER_API_KEY_3" || p.At != "2026-10-17T01:25:51Z" || p.Org != "admin" {
		t.Fatalf("pulse = %+v", p)
	}
	for _, want := range []string{"openrouter", "OPENROUTER_API_KEY_3", "402", "openai/gpt-5-mini", "provider key ref"} {
		if !strings.Contains(p.Title+" "+p.Detail, want) {
			t.Errorf("pulse %q / %q does not say %q", p.Title, p.Detail, want)
		}
	}
	// A refusal that speaks for every account of the vendor says so.
	if p := drainedPulse(aiobject.Drained{Vendor: "openrouter", Status: 402}); !strings.Contains(p.Detail, "every account") || strings.Contains(p.Detail, "serving") {
		t.Errorf("pulse for the whole vendor = %+v", p)
	}
}

// A drained account is an audit row naming the vendor and the account, so
// admin.hanzo.ai keeps it after the pulse has passed.
func TestADrainedAccountIsAnAuditRow(t *testing.T) {
	r := drainedRecord(aiobject.Drained{Vendor: "openai", Account: "OPENAI_API_KEY", Model: "openai/gpt-5-mini", Status: 402})
	if r.Action != "ai.supply.drained" || r.Resource.ID != "openai/OPENAI_API_KEY" || r.Outcome.Result != "error" || r.Outcome.Status != 402 || r.Actor.Org != "admin" {
		t.Fatalf("record = %+v", r)
	}
	if !strings.Contains(string(r.After), "openai/gpt-5-mini") {
		t.Errorf("record after = %s, want the model it was serving", r.After)
	}
}
