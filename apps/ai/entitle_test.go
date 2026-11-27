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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// fable52 is Claude Fable 5.2 by OpenRouter's slug, as a caller of the API names it.
var fable52 = aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: "anthropic/claude-fable-5.2",
	Class: aiobject.ClassPremium, Priced: true, Spend: true}

// The HTTP door: the hook refuses Claude Fable 5.2 to a plan that does not include
// it, in the ai module's words — the code, the model and the plan that includes it —
// before any provider is asked, and admits it on a plan that does.
func TestTheHookRefusesFableToAPlanWithoutIt(t *testing.T) {
	_, hit, err := limitAsk(payer(t, "dev", "card", 5*usd, 0))(context.Background(), fable52)
	if err != nil || hit == nil || hit.Code != limits.CodeEntitlement || hit.Model != fable52.Model || hit.Upgrade != "max-20x" ||
		!strings.Contains(hit.Message, "fable entitlement") {
		t.Fatalf("Pro asking Fable 5.2: hit %+v err %v, want entitlement_required naming the model and max-20x", hit, err)
	}
	g, hit, err := limitAsk(payer(t, "max-20x", "card", 5*usd, 0))(context.Background(), fable52)
	if err != nil || hit != nil || g == nil || g.Pays != aiobject.PaysPlan {
		t.Fatalf("Max 20x asking Fable 5.2: grant %+v hit %+v err %v, want the plan paying", g, hit, err)
	}
}

// A vendor's own faster lane — speed "fast" in the body, here Opus 5.5's, which no
// entitlement gates at standard speed — is fast mode to the policy: it needs the fast
// entitlement, and on a plan that includes it is paid from prepaid, cash only, as
// ours is.
func TestAVendorsFastLaneIsFastMode(t *testing.T) {
	for plan, want := range map[string]string{"dev": limits.CodeEntitlement, "max-20x": ""} {
		lim := payer(t, plan, "external:comp", 3*usd, 0)
		app := zip.New(zip.Config{AppName: "ai", DisableStartupMessage: true})
		app.Use(cloud.Bridge())
		var got *aiobject.LimitGrant
		var hit *aiobject.LimitHit
		app.Raw(http.MethodPost, "/v1/messages", func(c *zip.Ctx) error {
			got, hit, _ = limitAsk(lim)(c.Context(), premium)
			return c.Bytes(http.StatusOK, nil)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","speed":"fast"}`))
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		switch {
		case want != "" && (hit == nil || hit.Code != want || !strings.Contains(hit.Message, "Fast mode")):
			t.Fatalf("%s asking speed fast: hit %+v, want %s naming fast mode", plan, hit, want)
		case want == "" && (hit != nil || got == nil || got.Pays != aiobject.PaysPrepaid || !got.Cash):
			t.Fatalf("%s asking speed fast: grant %+v hit %+v, want prepaid, cash only", plan, got, hit)
		}
	}
	if vendorFast([]byte(`{"speed":"standard"}`)) || vendorFast([]byte(`{}`)) || !vendorFast([]byte(`{"speed":"FAST"}`)) {
		t.Fatal("vendorFast reads a speed other than fast as fast, or fast as not")
	}
}

// The plane's door: a sibling's call never meets the hook, so buy asks the same
// question first. A plan without the entitlement is refused 402 in the model API's
// error shape, naming the entitlement and a plan that includes it, before zen or the
// model API is asked; a call that needs none, or a plan that includes it, is served.
func TestASiblingsCallNeedsTheEntitlementToo(t *testing.T) {
	served := 0
	ServeZen(func(ctx context.Context, path string, request []byte) (*client.Chat, bool) {
		served++
		return &client.Chat{Status: http.StatusOK, Body: []byte(completion)}, true
	})
	t.Cleanup(func() { zen.Store(nil) })
	was := limiter
	t.Cleanup(func() { limiter = was })
	sibling := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-alice", Name: "alice"})
	call := func(body string) (int, map[string]string) {
		t.Helper()
		out, err := buy(sibling, "/v1/chat/completions", &client.ChatIn{Request: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		var e struct {
			Error map[string]string `json:"error"`
		}
		_ = json.Unmarshal(out.Body, &e)
		return out.Status, e.Error
	}

	limiter = payer(t, "dev", "card", 5*usd, 0)
	for body, name := range map[string]string{
		`{"model":"anthropic/claude-fable-5.2","messages":[]}`: "fable",
		`{"model":"claude-fable-5-2","messages":[]}`:           "fable",
		`{"model":"zen5","messages":[],"fast":true}`:           "fast",
		`{"model":"zen5","messages":[],"speed":"fast"}`:        "fast",
	} {
		status, e := call(body)
		if status != http.StatusPaymentRequired || e["code"] != limits.CodeEntitlement || e["entitlement"] != name ||
			e["upgrade"] != "max-20x" || e["type"] != "billing_error" {
			t.Fatalf("Pro's sibling sending %s: %d %v, want 402 entitlement_required for %s naming max-20x", body, status, e, name)
		}
	}
	if served != 0 {
		t.Fatalf("a refused sibling's call reached zen %d times", served)
	}
	if status, e := call(`{"model":"zen5","messages":[]}`); status != http.StatusOK {
		t.Fatalf("Pro's sibling asking a model that needs nothing: %d %v, want it served", status, e)
	}

	limiter = payer(t, "max-20x", "card", 5*usd, 0)
	if status, e := call(`{"model":"zen5","messages":[],"fast":true}`); status != http.StatusOK {
		t.Fatalf("Max 20x's sibling asking fast mode: %d %v, want it served", status, e)
	}
}

// The usage read says which entitlements the caller's plan includes, so a model picker
// hides or marks Claude Fable 5.2 and fast mode before a request meets the 402: each
// one present, included or not, and for one that is not, the plan that includes it.
// It carries no figure, as the rest of the read does not.
func TestTheUsageReadSaysWhichEntitlementsThePlanIncludes(t *testing.T) {
	ctx := context.Background()
	for plan, included := range map[string]bool{"dev": false, "max-20x": true} {
		l := payer(t, plan, "card", 5*usd, 0)
		s, err := l.Read(ctx, "acme", "acme")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(view(s, "https://hanzo.ai/pay"))
		if err != nil {
			t.Fatal(err)
		}
		var got aiLimits
		_ = json.Unmarshal(raw, &got)
		for _, name := range []string{"fable", "fast"} {
			e := got.Entitlements[name]
			switch {
			case e == nil:
				t.Fatalf("%s: the read names no %s entitlement: %s", plan, name, raw)
			case e.Included != included:
				t.Fatalf("%s: %s included = %v, want %v: %s", plan, name, e.Included, included, raw)
			case !included && e.Upgrade != "max-20x":
				t.Fatalf("%s: %s names %q as the way on, want max-20x: %s", plan, name, e.Upgrade, raw)
			case included && e.Upgrade != "":
				t.Fatalf("%s: an included %s names a way on (%q): %s", plan, name, e.Upgrade, raw)
			}
		}
		if m := figure.FindString(string(raw)); m != "" {
			t.Fatalf("%s: the read carries a figure (%q): %s", plan, m, raw)
		}
	}
}
