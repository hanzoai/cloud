// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// askedOn answers what the usage policy says to a model request made on key,
// holding grant, through an app composed the way the ai process is: the boundary
// and Bridge on the host, the model API as a sub-app that sets its own context
// (as ai's tenant filter does) before the gate asks the policy with it.
func askedOn(t *testing.T, ask aiobject.LimitFunc, key string, grant cloud.Grant) (*aiobject.LimitHit, error) {
	t.Helper()
	var hit *aiobject.LimitHit
	var err error
	host := zip.New(zip.Config{})
	host.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: "acme", User: "acme/ann", Kind: principal.Key, Key: key, Limit: grant})
		return c.Continue()
	}))
	host.Use(cloud.Bridge())
	sub := zip.New(zip.Config{})
	sub.Use(zip.H(func(c *zip.Ctx) error {
		c.SetContext(aiobject.WithGenAIAttribution(c.Context(), aiobject.GenAIAttribution{Key: key}))
		return c.Continue()
	}))
	sub.Raw(http.MethodPost, "/v1/chat/completions", func(c *zip.Ctx) error {
		_, hit, err = ask(c.Context(), enso)
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})
	host.Use(sub)
	resp, terr := host.Test(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if terr != nil {
		t.Fatalf("request: %v", terr)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return hit, err
}

// A key over its budget is refused, naming the budget, and a sibling key of the
// same person keeps working. The spend is what record counted for each key.
func TestAnOverBudgetKeyIsRefusedWhileASiblingWorks(t *testing.T) {
	lim := payer(t, "", "", 100*usd, 0)
	ask := limitAsk(lim)
	rec := record(lim, func(context.Context, aiobject.UsageEvent) error { return nil })

	capped := cloud.Grant(nil).Policy(500, 0, 0) // $5.00 a month
	for _, u := range []aiobject.UsageEvent{
		{Namespace: "acme", Key: "acme/ann-secret-aaaaaaaa", USD: "3.00"},
		// Billed to the holder's home org; still the acme key's spend.
		{Namespace: "ann-home", Key: "acme/ann-secret-aaaaaaaa", USD: "2.00"},
		{Namespace: "acme", Key: "acme/ann-secret-bbbbbbbb", USD: "7.00"},
	} {
		if err := rec(context.Background(), u); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	hit, err := askedOn(t, ask, "ann-secret-aaaaaaaa", capped)
	if err != nil || hit == nil || hit.Code != limits.CodeKeyBudget {
		t.Fatalf("a key that spent its $5.00 was not refused: hit %+v err %v", hit, err)
	}
	if want := "This API key has spent its $5.00 monthly budget"; len(hit.Message) < len(want) || hit.Message[:len(want)] != want {
		t.Fatalf("the refusal must name the budget, said %q", hit.Message)
	}
	if hit.ResetsAt.IsZero() || hit.ResetsAt.Day() != 1 {
		t.Fatalf("a monthly budget resets on the 1st, said %v", hit.ResetsAt)
	}
	// The sibling spent more but has no budget, and a capped key under its budget
	// is admitted too.
	if hit, err := askedOn(t, ask, "ann-secret-bbbbbbbb", nil); err != nil || (hit != nil && hit.Code == limits.CodeKeyBudget) {
		t.Fatalf("the sibling was refused: %+v %v", hit, err)
	}
	if hit, err := askedOn(t, ask, "ann-secret-bbbbbbbb", cloud.Grant(nil).Policy(800, 0, 0)); err != nil || (hit != nil && hit.Code == limits.CodeKeyBudget) {
		t.Fatalf("a key under its budget was refused: %+v %v", hit, err)
	}
	// A key limited to some models may use only those.
	if hit, _ := askedOn(t, ask, "ann-secret-bbbbbbbb", cloud.Grant{"model:zen5"}); hit == nil || hit.Code != limits.CodeKeyModel {
		t.Fatalf("a key limited to zen5 asked for enso: %+v", hit)
	}
	if hit, _ := askedOn(t, ask, "ann-secret-bbbbbbbb", cloud.Grant{"model:enso"}); hit != nil && hit.Code == limits.CodeKeyModel {
		t.Fatalf("a key limited to enso was refused enso: %+v", hit)
	}
	// A total budget binds across months.
	if hit, _ := askedOn(t, ask, "ann-secret-bbbbbbbb", cloud.Grant(nil).Policy(0, 700, 0)); hit == nil || hit.Code != limits.CodeKeyBudget || hit.Name != "total" {
		t.Fatalf("a key at its total budget was not refused: %+v", hit)
	}

}

// What each key spent is answered in cents, for the caller's org only.
func TestTheSpendOfEachKeyIsAnsweredInCents(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	rec := record(lim, func(context.Context, aiobject.UsageEvent) error { return nil })
	for _, u := range []aiobject.UsageEvent{
		{Namespace: "acme", Key: "acme/k1", USD: "0.004"},
		{Namespace: "acme", Key: "acme/k1", USD: "1.25"},
		{Namespace: "acme", Key: "evil/k1", USD: "9.00"},
		{Namespace: "acme", Key: "k1", USD: "5.00"}, // names no org: counted against nothing
		{Namespace: "acme", USD: "4.00"},            // no key: a session's call counts against nothing
	} {
		if err := rec(context.Background(), u); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	spendOf = lim
	t.Cleanup(func() { spendOf = nil })
	out, err := planeSpend(cloud.For(context.Background(), "acme"), &client.KeysIn{Keys: []string{"k1", "k2"}})
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	b, _ := json.Marshal(out)
	if string(b) != `{"keys":[{"key":"k1","month":126,"total":126},{"key":"k2","month":0,"total":0}]}` {
		t.Fatalf("spend = %s", b)
	}
}
