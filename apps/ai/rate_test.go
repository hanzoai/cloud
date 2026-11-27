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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	aiobject "github.com/hanzoai/ai/object"
)

// rated is an app as the ai process composes one: the per-minute limit first, the
// MCP door, its own requests a minute, and stand-ins for the model routes, a chat
// answered as a stream when it asks for one. served counts what reached a route.
func rated(t *testing.T, lim *limits.Limiter, trail *audit.Recorder) (*zip.App, *atomic.Int64) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "ai", Logger: luxlog.New("ratetest"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	app.Use(zip.H(cloud.Under("/v1", Paced(lim))))
	mountCalls(app)
	mountLimits(app, lim, trail)
	served := &atomic.Int64{}
	answer := func(c *zip.Ctx) error {
		served.Add(1)
		c.SetHeader("Content-Type", "application/json")
		if strings.Contains(string(c.Body()), `"stream":true`) {
			c.SetHeader("Content-Type", "text/event-stream")
			c.Fiber().RequestCtx().SetBodyStreamWriter(func(w *bufio.Writer) {
				_, _ = w.WriteString("data: {\"choices\":[]}\n\n")
				_ = w.Flush()
				_, _ = w.WriteString("data: [DONE]\n\n")
				_ = w.Flush()
			})
			return nil
		}
		return c.Bytes(http.StatusOK, []byte(completion))
	}
	for _, p := range []string{"/v1/chat/completions", "/v1/decisions", "/v1/embeddings", "/v1/chat/public", "/v1/audio/transcriptions/public"} {
		app.Raw(http.MethodPost, p, answer)
	}
	return app, served
}

// alice is the person every surface below is asked as: "acme/alice".
var alice = map[string]string{"Authorization": "Bearer tok-alice", "X-Org-Id": "acme", "X-User-Id": "u-alice", "X-User-Name": "alice", "X-User-Owner": "acme"}

// post sends one request to app as hdr.
func post(t *testing.T, app *zip.App, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// standing reads the three OpenAI headers off a response.
func standing(t *testing.T, resp *http.Response) (limit, remaining int, reset time.Duration) {
	t.Helper()
	limit, err := strconv.Atoi(resp.Header.Get("x-ratelimit-limit-requests"))
	if err != nil {
		t.Fatalf("x-ratelimit-limit-requests = %q", resp.Header.Get("x-ratelimit-limit-requests"))
	}
	if remaining, err = strconv.Atoi(resp.Header.Get("x-ratelimit-remaining-requests")); err != nil {
		t.Fatalf("x-ratelimit-remaining-requests = %q", resp.Header.Get("x-ratelimit-remaining-requests"))
	}
	if reset, err = time.ParseDuration(resp.Header.Get("x-ratelimit-reset-requests")); err != nil || reset <= 0 || reset > time.Minute {
		t.Fatalf("x-ratelimit-reset-requests = %q, want a duration within a minute", resp.Header.Get("x-ratelimit-reset-requests"))
	}
	return limit, remaining, reset
}

// Every model response says where its person stands, from the count the limit
// decides by — a stream before its first byte, the same — and the 41st request in a
// minute is 429 with Retry-After and the same headers, reaching no model, while
// another person is served. Retrieval is not counted.
func TestTheFortyFirstModelRequestIsRefusedWithItsStanding(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	app, served := rated(t, lim, nil)
	for i := 1; i <= 39; i++ {
		resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
		if limit, remaining, _ := standing(t, resp); limit != flags.SeedRate || remaining != flags.SeedRate-i {
			t.Fatalf("request %d stands at %d of %d, want %d of %d", i, remaining, limit, flags.SeedRate-i, flags.SeedRate)
		}
	}
	if resp := post(t, app, "/v1/embeddings", `{"model":"zen-embedding","input":["x"]}`, alice); resp.StatusCode != http.StatusOK ||
		resp.Header.Get("x-ratelimit-limit-requests") != "" {
		t.Fatalf("an embedding: %d with %q, want served and not counted", resp.StatusCode, resp.Header.Get("x-ratelimit-limit-requests"))
	}

	stream := post(t, app, "/v1/chat/completions", `{"model":"zen5","stream":true,"messages":[]}`, alice)
	body, _ := io.ReadAll(stream.Body)
	if stream.StatusCode != http.StatusOK || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("the stream: %d %q", stream.StatusCode, body)
	}
	if limit, remaining, _ := standing(t, stream); limit != flags.SeedRate || remaining != 0 {
		t.Fatalf("the 40th, streamed, stands at %d of %d, want 0 of %d", remaining, limit, flags.SeedRate)
	}

	before := served.Load()
	over := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice)
	if over.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the 41st: %d, want 429", over.StatusCode)
	}
	if served.Load() != before {
		t.Fatal("the 41st reached a model")
	}
	limit, remaining, reset := standing(t, over)
	if limit != flags.SeedRate || remaining != 0 {
		t.Fatalf("the refusal stands at %d of %d, want 0 of %d", remaining, limit, flags.SeedRate)
	}
	retry, err := strconv.Atoi(over.Header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 || time.Duration(retry)*time.Second != reset {
		t.Fatalf("Retry-After %q beside a reset of %v, want the same whole seconds", over.Header.Get("Retry-After"), reset)
	}
	var refused struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	raw, _ := io.ReadAll(over.Body)
	if err := json.Unmarshal(raw, &refused); err != nil || refused.Error.Code != limits.CodeRate || refused.Error.Type != "rate_limit_error" {
		t.Fatalf("the refusal's body %s, want code %s", raw, limits.CodeRate)
	}
	if strings.Contains(refused.Error.Message, strconv.Itoa(flags.SeedRate)) {
		t.Fatalf("the refusal's sentence states the limit: %q", refused.Error.Message)
	}

	bob := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-bob", "X-User-Name": "bob", "X-User-Owner": "acme"}
	if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, bob); resp.StatusCode != http.StatusOK {
		t.Fatalf("another person of the same org: %d, want served", resp.StatusCode)
	}
}

// A person's model calls spend ONE count whichever surface asks: the API, a
// decision, the MCP door's aiChat, and a sibling's call over the plane (a coding
// run, a bot) — forty in all, and the next is refused on every one of them.
func TestEverySurfaceSpendsOneCount(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	app, _ := rated(t, lim, nil)
	was := limiter
	limiter = lim
	t.Cleanup(func() { limiter = was })
	ServeZen(func(ctx context.Context, path string, request []byte) (*client.Chat, bool) {
		return &client.Chat{Status: http.StatusOK, Body: []byte(completion)}, true
	})
	t.Cleanup(func() { zen.Store(nil) })
	sibling := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-alice", Name: "alice"})
	plane := func() int {
		out, err := buy(sibling, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"zen5","messages":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		return out.Status
	}
	mcp := func() (string, bool) {
		msg := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"aiChat","arguments":{"model":"zen5","prompt":"hi"}}}`
		req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(msg))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range alice {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		var env struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
				IsError bool                    `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &env); err != nil || len(env.Result.Content) == 0 {
			t.Fatalf("aiChat answered %s", trunc(string(b)))
		}
		return env.Result.Content[0].Text, env.Result.IsError
	}
	for i := 0; i < 10; i++ {
		if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice); resp.StatusCode != http.StatusOK {
			t.Fatalf("API %d: %d", i, resp.StatusCode)
		}
		if resp := post(t, app, "/v1/decisions", `{"model":"kai","input":"x"}`, alice); resp.StatusCode != http.StatusOK {
			t.Fatalf("decision %d: %d", i, resp.StatusCode)
		}
		if text, isErr := mcp(); isErr {
			t.Fatalf("MCP %d refused: %s", i, text)
		}
		if status := plane(); status != http.StatusOK {
			t.Fatalf("plane %d: %d", i, status)
		}
	}
	if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the API past forty: %d, want 429", resp.StatusCode)
	}
	if resp := post(t, app, "/v1/decisions", `{"model":"kai","input":"x"}`, alice); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a decision past forty: %d, want 429", resp.StatusCode)
	}
	if text, isErr := mcp(); !isErr || !strings.Contains(text, "429") {
		t.Fatalf("MCP past forty: %s, want the 429", text)
	}
	if status := plane(); status != http.StatusTooManyRequests {
		t.Fatalf("the plane past forty: %d, want 429", status)
	}
}

// An organization's admin sets less than the default for its people, never more;
// a member may not set it; each change is on the trail before it lands, with its
// outcome after; and a process with no trail refuses the change.
func TestAnOrgsAdminSetsItsRequestsAMinute(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app, _ := rated(t, lim, trail)
	admin := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-ann", "X-User-Name": "ann", "X-User-Owner": "acme", "X-User-IsOrgAdmin": "true"}
	put := func(hdr map[string]string, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPut, "/v1/ai/limits/rate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if status, out := put(alice, `{"rpm":5}`); status != http.StatusForbidden {
		t.Fatalf("a member set it: %d %s, want 403", status, out)
	}
	if status, out := put(admin, fmt.Sprintf(`{"rpm":%d}`, flags.SeedRate+1)); status != http.StatusBadRequest {
		t.Fatalf("an admin raised it past the default: %d %s, want 400", status, out)
	}
	status, out := put(admin, `{"rpm":3}`)
	if status != http.StatusOK || !strings.Contains(out, `"rpm":3`) || !strings.Contains(out, fmt.Sprintf(`"default":%d`, flags.SeedRate)) {
		t.Fatalf("an admin lowered it: %d %s", status, out)
	}
	for i := 0; i < 3; i++ {
		if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d under acme's 3: %d", i, resp.StatusCode)
		}
	}
	if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, alice); resp.StatusCode != http.StatusTooManyRequests ||
		resp.Header.Get("x-ratelimit-limit-requests") != "3" {
		t.Fatalf("the 4th under acme's 3: %d at %q, want 429 at 3", resp.StatusCode, resp.Header.Get("x-ratelimit-limit-requests"))
	}
	for action, result := range map[string]string{"ai.limits.rate.attempt": "success", "ai.limits.rate": "success"} {
		rows, total, err := trail.Query(context.Background(), audit.Filter{Action: action})
		if err != nil || total != 1 || rows[0].Outcome.Result != result || rows[0].Resource.ID != "acme" {
			t.Fatalf("%s on the trail %d times (%v), want once for acme, %s", action, total, err, result)
		}
	}

	bare, _ := rated(t, lim, nil)
	req := httptest.NewRequest(http.MethodPut, "/v1/ai/limits/rate", strings.NewReader(`{"rpm":2}`))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range admin {
		req.Header.Set(k, v)
	}
	resp, err := bare.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a change with no trail: %v %d, want 503", err, resp.StatusCode)
	}
	if _, _, own := lim.Rates(context.Background(), "acme"); own != 3 {
		t.Fatalf("a change with no trail moved acme to %d", own)
	}
}

// from is the address a request came from, as the edge states it.
func from(ip string, hdr map[string]string) map[string]string {
	out := map[string]string{"X-Forwarded-For": ip}
	for k, v := range hdr {
		out[k] = v
	}
	return out
}

// The two routes that ask for no account are counted by the visitor's address, one
// window across both, and say where the visitor stands: the 41st in a minute from
// one address is 429 with Retry-After and the headers, reaching nothing, whether the
// visitor is anonymous or signed in; another address is served. Mutation: leave
// public out of Paced and a visitor posts forever, with no header.
func TestThePublicRoutesAreCountedByAddress(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	app, served := rated(t, lim, nil)
	a, b := from("203.0.113.10", nil), from("203.0.113.11", nil)
	for i := 1; i <= 20; i++ {
		resp := post(t, app, "/v1/chat/public", `{"messages":[]}`, a)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chat %d: %d", i, resp.StatusCode)
		}
		if limit, remaining, _ := standing(t, resp); limit != flags.SeedRate || remaining != flags.SeedRate-i {
			t.Fatalf("chat %d stands at %d of %d, want %d of %d", i, remaining, limit, flags.SeedRate-i, flags.SeedRate)
		}
	}
	for i := 21; i <= 40; i++ {
		// A signed-in caller from the same address spends the visitor's window.
		hdr := a
		if i%2 == 0 {
			hdr = from("203.0.113.10", alice)
		}
		if resp := post(t, app, "/v1/audio/transcriptions/public", `x`, hdr); resp.StatusCode != http.StatusOK {
			t.Fatalf("audio %d: %d", i, resp.StatusCode)
		}
	}
	before := served.Load()
	for _, path := range []string{"/v1/chat/public", "/v1/audio/transcriptions/public"} {
		over := post(t, app, path, `{"messages":[]}`, a)
		if over.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("the 41st to %s: %d, want 429", path, over.StatusCode)
		}
		if limit, remaining, reset := standing(t, over); limit != flags.SeedRate || remaining != 0 ||
			over.Header.Get("Retry-After") != strconv.Itoa(int(reset/time.Second)) {
			t.Fatalf("the refusal stands at %d of %d, Retry-After %q beside %v", remaining, limit, over.Header.Get("Retry-After"), reset)
		}
	}
	if served.Load() != before {
		t.Fatal("a refused visitor reached a route")
	}
	if resp := post(t, app, "/v1/chat/public", `{"messages":[]}`, b); resp.StatusCode != http.StatusOK {
		t.Fatalf("another address: %d, want served", resp.StatusCode)
	}
}

// A caller the boundary cannot place in an org is counted by its user id, and one it
// cannot name at all by its address: nobody is admitted uncounted. Mutation: return
// "" from pacer for them and the 41st is served.
func TestNobodyIsAdmittedUncounted(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	app, _ := rated(t, lim, nil)
	orgless := func(id string) map[string]string { return from("203.0.113.20", map[string]string{"X-User-Id": id}) }
	anon := func(ip string) map[string]string { return from(ip, nil) }
	for name, hdr := range map[string]map[string]string{"a user with no org": orgless("u-x"), "an anonymous address": anon("203.0.113.30")} {
		for i := 1; i <= 40; i++ {
			resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, hdr)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s, request %d: %d", name, i, resp.StatusCode)
			}
			if limit, remaining, _ := standing(t, resp); limit != flags.SeedRate || remaining != flags.SeedRate-i {
				t.Fatalf("%s, request %d stands at %d of %d", name, i, remaining, limit)
			}
		}
		if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, hdr); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s, the 41st: %d, want 429", name, resp.StatusCode)
		}
	}
	// Another user at the same address has a window of its own; so has another address.
	if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, orgless("u-y")); resp.StatusCode != http.StatusOK {
		t.Fatalf("another user with no org: %d, want served", resp.StatusCode)
	}
	if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, anon("203.0.113.31")); resp.StatusCode != http.StatusOK {
		t.Fatalf("another anonymous address: %d, want served", resp.StatusCode)
	}
}

// A member whose home org lowered the rate does not get past it by acting in an org
// that did not: the door counts acme's carol at acme's 3 while she acts in globex.
// Mutation: pass Pace the acting org only and she is served a 4th.
func TestAMembersHomeOrgsRateFollowsThemIntoAnotherOrg(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	app, _ := rated(t, lim, nil)
	if err := lim.SetRate(context.Background(), "acme", 3, "z@acme.test"); err != nil {
		t.Fatal(err)
	}
	carol := map[string]string{"X-Org-Id": "globex", "X-User-Id": "u-carol", "X-User-Name": "carol", "X-User-Owner": "acme"}
	for i := 1; i <= 3; i++ {
		if resp := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, carol); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	over := post(t, app, "/v1/chat/completions", `{"model":"zen5","messages":[]}`, carol)
	if over.StatusCode != http.StatusTooManyRequests || over.Header.Get("x-ratelimit-limit-requests") != "3" {
		t.Fatalf("the 4th: %d at %q, want 429 at acme's 3", over.StatusCode, over.Header.Get("x-ratelimit-limit-requests"))
	}
}

// Setting an org's requests a minute is a change the admin makes, not a page the
// admin visited: with the admin's cookie and no credential, a request the browser
// says came from another site is refused (403) and moves nothing, and the admin's
// own page is served. Mutation: drop cloud.CSRF from setRate and the cross-site
// request lowers the rate.
func TestARateChangeIsRefusedWhenAPageMadeIt(t *testing.T) {
	lim := payer(t, "", "", 0, 0)
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	app, _ := rated(t, lim, trail)
	admin := map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-ann", "X-User-Name": "ann", "X-User-Owner": "acme", "X-User-IsOrgAdmin": "true", "Cookie": "session=abc"}
	put := func(site string) (int, string) {
		req := httptest.NewRequest(http.MethodPut, "/v1/ai/limits/rate", strings.NewReader(`{"rpm":3}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range admin {
			req.Header.Set(k, v)
		}
		req.Header.Set("Sec-Fetch-Site", site)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, site := range []string{"cross-site", "same-site"} {
		if status, out := put(site); status != http.StatusForbidden || !strings.Contains(out, cloud.Unasked) {
			t.Fatalf("a %s change: %d %s, want 403 (%s)", site, status, out, cloud.Unasked)
		}
	}
	if _, _, own := lim.Rates(context.Background(), "acme"); own != 0 {
		t.Fatalf("a refused change moved acme to %d", own)
	}
	if rows, total, err := trail.Query(context.Background(), audit.Filter{Action: "ai.limits.rate.attempt"}); err != nil || total != 0 {
		t.Fatalf("a refused change reached the trail %d times (%v): %v", total, err, rows)
	}
	if status, out := put("same-origin"); status != http.StatusOK || !strings.Contains(out, `"rpm":3`) {
		t.Fatalf("the admin's own page: %d %s, want 200 at 3", status, out)
	}
}

// Fast mode reaches the policy from the request, in either spelling the model API
// reads it in.
func TestTheGateTellsThePolicyAFastRequest(t *testing.T) {
	cash := payer(t, "max-20x", "external:comp", 3*usd, 0)
	app := zip.New(zip.Config{AppName: "ai", DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var got *aiobject.LimitGrant
	app.Raw(http.MethodPost, "/v1/chat/completions", func(c *zip.Ctx) error {
		got, _, _ = limitAsk(cash)(c.Context(), premium)
		return c.Bytes(http.StatusOK, nil)
	})
	for name, set := range map[string]func(*http.Request){
		"the body":   func(r *http.Request) {},
		"the header": func(r *http.Request) { r.Header.Set("X-Fast", "1") },
	} {
		body := `{"model":"x"}`
		if name == "the body" {
			body = `{"model":"x","fast":true}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		set(req)
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Pays != aiobject.PaysPrepaid || !got.Cash {
			t.Fatalf("fast in %s on a comp'd Max plan: %+v, want prepaid, cash only", name, got)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Pays != aiobject.PaysPlan {
		t.Fatalf("without fast mode: %+v, want the plan paying", got)
	}
}
