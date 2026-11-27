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

package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	aiobject "github.com/hanzoai/ai/object"
)

// Past a model's share, usage credits the payer bought keep that model, with no
// choice to make. With none, the paused model reaches the customer read as names,
// states and actions only — the paused family, its fallback, buying usage credits —
// and no figure.
func TestAPausedModelReadsWithoutAFigure(t *testing.T) {
	ctx := context.Background()
	cash := payer(t, "max-20x", "external:comp", 3*usd, 0)
	g, _, _ := limitAsk(cash)(ctx, premium) // claude-opus, capped by the seed at $30 on max-20x
	g.Settle(30 * usd)
	if g, hit, _ := limitAsk(cash)(ctx, premium); hit != nil || g == nil || g.Pays != aiobject.PaysPrepaid || !g.Cash {
		t.Fatalf("opus past its share with cash: grant %+v hit %+v, want opus on the cash", g, hit)
	}
	l := payer(t, "max-20x", "external:comp", 0, 0)
	g, _, _ = limitAsk(l)(ctx, premium)
	g.Settle(30 * usd)
	_, hit, _ := limitAsk(l)(ctx, premium)
	if hit == nil || hit.Code != aiobject.CodeModelCap || hit.Fallback != "enso-auto" {
		t.Fatalf("opus past its share: %+v, want model_cap to enso-auto, offering nothing granted", hit)
	}
	s, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view(s, "https://hanzo.ai/pay"))
	var got aiLimits
	_ = json.Unmarshal(raw, &got)
	if len(got.Paused) != 1 || got.Paused[0].Model != "anthropic/claude-opus*" || got.Paused[0].Fallback != "enso-auto" {
		t.Fatalf("view %s", raw)
	}
	kinds := []string{}
	for _, a := range got.Actions {
		kinds = append(kinds, a.Kind)
	}
	if strings.Join(kinds, ",") != "upgrade,topup" {
		t.Fatalf("actions %v, want Max 50x above it, then buying usage credits", kinds)
	}
	if m := figure.FindString(string(raw)); m != "" {
		t.Fatalf("the customer read carries a figure (%q): %s", m, raw)
	}
}
