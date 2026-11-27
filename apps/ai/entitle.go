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

// entitle.go — the plan entitlements (apps/ai/limits entitle.go, hanzo-inc/cloud#803)
// at both doors, before any provider is asked:
//
//   - HTTP: the usage policy's Admit asks them first (limitAsk), told a fast request
//     in every spelling (fast);
//   - the plane (buy): a sibling's call, which its sibling pays for and no plan
//     counts, so it never meets Admit, asks them here.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
)

// vendorFast reports whether a request body asks for a vendor's own faster lane:
// speed "fast", Anthropic's fast mode, priced at a multiple of the model's standard
// rates. It is fast mode as much as ours is, and needs the same entitlement.
func vendorFast(body []byte) bool {
	var b struct {
		Speed string `json:"speed"`
	}
	_ = json.Unmarshal(body, &b)
	return strings.EqualFold(strings.TrimSpace(b.Speed), "fast")
}

// entitled refuses a sibling's call at path whose model or mode needs a plan
// entitlement its payer's plan does not include (limits.Entitle), as Admit refuses
// one on HTTP: 402 entitlement_required naming the entitlement and a plan that
// includes it, or 503 when the plan cannot be read. Nil lets the call on. A
// multipart body (a recording) names no model.
func entitled(ctx context.Context, lim *limits.Limiter, path string, in *client.ChatIn) *client.Chat {
	if lim == nil || in.ContentType != "" {
		return nil
	}
	var named struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(in.Request, &named) != nil || strings.TrimSpace(named.Model) == "" {
		return nil
	}
	var ours struct {
		Fast bool `json:"fast"`
	}
	_ = json.Unmarshal(in.Request, &ours)
	who := cloud.Who(ctx)
	payer := meter.PayerFrom(ctx)
	chat := aictl.ChatPath(path)
	hit, err := lim.Entitle(ctx, limits.Ask{
		Subject: payer.Subject(), Org: cmp.Or(payer.Org(), who.Org), Actor: client.Person(who),
		Model: named.Model, Priced: true, Spend: chat, Fast: chat && (ours.Fast || vendorFast(in.Request)),
	})
	switch {
	case err != nil:
		slog.Warn("ai: plan entitlement unreadable, refusing a sibling's call", "org", who.Org, "model", named.Model, "err", err)
		return &client.Chat{Status: http.StatusServiceUnavailable, Body: planeError(aiobject.UsageUnavailable().Message,
			"api_error", aiobject.CodeUsageUnavailable, nil)}
	case hit != nil:
		msg := strings.TrimSuffix(strings.TrimSpace(hit.Message), ":") + "."
		return &client.Chat{Status: http.StatusPaymentRequired, Body: planeError(msg, "billing_error", hit.Code,
			map[string]string{"entitlement": hit.Name, "model": hit.Model, "upgrade": hit.Upgrade})}
	}
	return nil
}

// planeError is a refusal in the model API's own error shape, with what more it
// names beside the message and code; an empty member is left out.
func planeError(message, kind, code string, more map[string]string) []byte {
	e := map[string]string{"message": message, "type": kind, "code": code}
	for k, v := range more {
		if v != "" {
			e[k] = v
		}
	}
	raw, _ := json.Marshal(map[string]any{"error": e})
	return raw
}
