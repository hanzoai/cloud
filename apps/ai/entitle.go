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

// entitle.go — the usage policy and plan entitlements (apps/ai/limits,
// hanzo-inc/cloud#803) at both doors, before any provider is asked:
//
//   - HTTP: the usage policy's Admit asks them first (limitAsk), told a fast request
//     in every spelling (fast);
//   - the plane (buy / buyStreamed): a sibling's call meets the same usage policy
//     here (admitPlane), so paid subscribers spend their included plan allowance
//     with zero wallet debit, entitlements refuse before dispatch, and third-party /
//     fast / overage calls settle against the wallet as on HTTP.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/account"
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

// planePayer resolves the billing account for a sibling's plane call: first from
// the attested principal (meter.PayerFrom), else from the stated billing-account
// claim (zip.Caller.Account) carried by detached runs, embeddings, reranks and
// transcriptions that name no user.
func planePayer(ctx context.Context) account.Account {
	if p := meter.PayerFrom(ctx); !p.Zero() {
		return p
	}
	return account.Parse(cloud.Who(ctx).Account)
}

// planeModelFast extracts the requested model name and whether fast mode was asked
// from either a JSON body or a multipart/form-data body (audio transcriptions).
func planeModelFast(in *client.ChatIn) (model string, fast bool) {
	if in == nil || len(in.Request) == 0 {
		return "", false
	}
	ct := strings.TrimSpace(in.ContentType)
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
			return "", false
		}
		boundary := params["boundary"]
		if boundary == "" {
			return "", false
		}
		mr := multipart.NewReader(bytes.NewReader(in.Request), boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "model" {
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				_ = part.Close()
				return strings.TrimSpace(string(b)), false
			}
			_ = part.Close()
		}
		return "", false
	}
	var req struct {
		Model string `json:"model"`
		Fast  bool   `json:"fast"`
	}
	if json.Unmarshal(in.Request, &req) != nil {
		return "", false
	}
	model = strings.TrimSpace(req.Model)
	if model == "" {
		return "", false
	}
	return model, req.Fast || vendorFast(in.Request)
}

// planeAsk constructs the usage-policy Ask for a plane call using the same model
// catalog helpers (aictl.FamilyOf, aictl.ClassOf, aictl.ModelCostsNothing,
// aictl.ChatPath) as the HTTP gate.
func planeAsk(ctx context.Context, path, model string, fast bool) (limits.Ask, bool) {
	who := cloud.Who(ctx)
	payer := planePayer(ctx)
	org := cmp.Or(payer.Org(), who.Org)
	chat := aictl.ChatPath(path)
	return limits.Ask{
		Subject: payer.Subject(),
		Org:     org,
		Actor:   client.Person(who),
		Model:   model,
		Family:  aictl.FamilyOf(model),
		Class:   aictl.ClassOf(model),
		Priced:  !aictl.ModelCostsNothing(model, org),
		Spend:   chat,
		Fast:    chat && fast,
	}, !payer.Zero()
}

// admitPlane runs the usage policy for a sibling's plane call before zen or the
// model API is asked. When a billing payer is resolved, it calls lim.Admit (which
// checks plan entitlements first, then plan allowance, then wallet funds / free
// caps). When no payer can be named (e.g. a system call), it still checks plan
// entitlements (lim.Entitle) so unattributed calls never bypass Fable/fast gates.
func admitPlane(ctx context.Context, lim *limits.Limiter, path string, in *client.ChatIn) (*limits.Grant, *client.Chat) {
	if lim == nil || !aictl.Entitled(path) {
		return nil, nil
	}
	model, fast := planeModelFast(in)
	if model == "" {
		return nil, nil
	}
	ask, hasPayer := planeAsk(ctx, path, model, fast)
	if !hasPayer {
		hit, err := lim.Entitle(ctx, ask)
		switch {
		case err != nil:
			slog.Warn("ai: plan entitlement unreadable, refusing a sibling's call", "org", ask.Org, "model", model, "err", err)
			return nil, &client.Chat{Status: http.StatusServiceUnavailable, Body: planeError(aiobject.UsageUnavailable().Message,
				"api_error", aiobject.CodeUsageUnavailable, nil)}
		case hit != nil:
			return nil, planeHit(hit)
		}
		return nil, nil
	}
	g, hit, err := lim.Admit(ctx, ask)
	switch {
	case err != nil:
		slog.Warn("ai: usage policy unreadable, refusing a sibling's call", "org", ask.Org, "subject", ask.Subject, "model", model, "err", err)
		return nil, &client.Chat{Status: http.StatusServiceUnavailable, Body: planeError(aiobject.UsageUnavailable().Message,
			"api_error", aiobject.CodeUsageUnavailable, nil)}
	case hit != nil:
		return nil, planeHit(hit)
	}
	return g, nil
}

// planeHit renders a limits.Hit in the model API's OpenAI error shape with the
// matching HTTP status (429 for window/allowance/model/free caps, 402 for
// entitlement/prepaid/balance refusals).
func planeHit(hit *limits.Hit) *client.Chat {
	code := cmp.Or(hit.Code, limits.CodeUsageCap)
	status, kind := http.StatusPaymentRequired, "billing_error"
	switch code {
	case limits.CodeUsageCap, limits.CodeFreePlanCap:
		status, kind = http.StatusTooManyRequests, "rate_limit_error"
	case limits.CodePlanAllowance, limits.CodeModelCap:
		status = http.StatusTooManyRequests
	}
	msg := hit.Message
	if msg == "" && code == limits.CodeUsageCap && hit.Name != "" {
		msg = "You've used this " + hit.Name + "'s requests on your plan."
	}
	msg = strings.TrimSuffix(strings.TrimSpace(cmp.Or(msg, "This request is outside what your plan includes.")), ":")
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	more := map[string]string{
		"class":    hit.Class,
		"model":    hit.Model,
		"fallback": hit.Fallback,
		"upgrade":  hit.Upgrade,
	}
	if code == limits.CodeEntitlement {
		more["entitlement"] = hit.Name
	} else if hit.Name != "" {
		more["limit"] = hit.Name
	}
	if !hit.ResetsAt.IsZero() {
		more["resets_at"] = hit.ResetsAt.UTC().Format(time.RFC3339)
	}
	return &client.Chat{Status: status, Body: planeError(msg, kind, code, more)}
}

// settlePlane records actual nano-USD spend against a plan-covered grant using a
// bounded background context so client disconnect cannot drop plan accounting.
func settlePlane(lim *limits.Limiter, g *limits.Grant, nanos int64) {
	if lim == nil || g == nil || nanos <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	if err := lim.Settle(ctx, g, nanos); err != nil {
		slog.Error("ai plan: plane settle failed; the hold lapses", "class", g.Class, "nanos", nanos, "err", err)
	}
}

// releasePlane returns any counted request window or daily cap slot taken by an
// unserved plane call.
func releasePlane(lim *limits.Limiter, g *limits.Grant) {
	if lim == nil || !g.Counted() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	if err := lim.Release(ctx, g); err != nil {
		slog.Error("ai plan: unserved plane request could not be released", "class", g.Class, "err", err)
	}
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
