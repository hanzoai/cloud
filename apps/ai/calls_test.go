// Copyright © 2026 Hanzo AI. MIT License.

package ai

// calls_test.go drives the three ops through the REAL replay: the routes they name
// are registered on the same app, so client.ServeHere carries the caller's request
// to them exactly as it reaches hanzoai/ai's in production, and every assertion
// reads what that route was handed and what the tool answered.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
)

// seen is what a stand-in route was handed.
type seen struct {
	mu      sync.Mutex
	method  string
	path    string
	body    string
	headers http.Header
}

func (s *seen) record(c *zip.Ctx) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.method, s.path, s.body = c.Method(), c.Path(), string(c.Body())
	s.headers = http.Header{}
	for _, h := range []string{"Authorization", "X-Org-Id", "X-Hanzo-Fallback", "Content-Type", "Accept"} {
		if v := c.Header(h); v != "" {
			s.headers.Set(h, v)
		}
	}
}

// route is one stand-in for a model API route: the status, headers and body it
// answers.
type route struct {
	status  int
	headers map[string]string
	body    string
}

// model stands up the ops over stand-ins for /v1/models, /v1/chat/completions and
// /v1/decisions, with the identity carrier installed as cloud.Listen installs it.
func model(t *testing.T, routes map[string]route) (*zip.App, *seen) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "ai", Logger: luxlog.New("aicallstest"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	mountCalls(app)
	got := &seen{}
	for key, r := range routes {
		method, path, _ := strings.Cut(key, " ")
		app.Raw(method, path, func(c *zip.Ctx) error {
			got.record(c)
			for k, v := range r.headers {
				c.SetHeader(k, v)
			}
			c.SetHeader("Content-Type", "application/json")
			return c.Bytes(r.status, []byte(r.body))
		})
	}
	return app, got
}

// tool runs one tools/call on the app's MCP server as a caller holding a bearer, and
// returns the text and isError.
func tool(t *testing.T, app *zip.App, name, args string) (string, bool) {
	t.Helper()
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args)
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(msg))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-caller")
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-Hanzo-Fallback", "allow")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &env); err != nil || env.Error != nil || len(env.Result.Content) == 0 {
		t.Fatalf("tools/call %s did not answer a tool result: %s", name, trunc(string(b)))
	}
	return env.Result.Content[0].Text, env.Result.IsError
}

const completion = `{"id":"c1","object":"chat.completion","model":"zen5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

// TestChatRunsTheRouteAsTheCaller: the completion is the caller's own request to
// /v1/chat/completions — its credential, its org, its fallback choice — and the
// answer carries which model served it.
func TestChatRunsTheRouteAsTheCaller(t *testing.T) {
	app, got := model(t, map[string]route{
		"POST /v1/chat/completions": {status: 200, headers: map[string]string{"X-Hanzo-Served": "zen5"}, body: completion},
	})
	text, isErr := tool(t, app, "aiChat", `{"model":"zen5","prompt":"hi","system":"be brief","max_tokens":16}`)
	if isErr {
		t.Fatalf("aiChat refused: %s", text)
	}
	var out aiChat
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("aiChat answered %s: %v", text, err)
	}
	if out.Answer != "hello" || out.Model != "zen5" || out.Receipt.Served != "zen5" {
		t.Fatalf("aiChat answered %+v, want hello from zen5 served by zen5", out)
	}
	if out.Receipt.PaidBy != "" {
		t.Errorf("the route named no payer and the tool says %q — absent must read absent", out.Receipt.PaidBy)
	}

	if got.method != http.MethodPost || got.path != "/v1/chat/completions" {
		t.Fatalf("the replay reached %s %s, want POST /v1/chat/completions", got.method, got.path)
	}
	for h, want := range map[string]string{
		"Authorization": "Bearer tok-caller", "X-Org-Id": "hanzo", "X-Hanzo-Fallback": "allow",
		"Content-Type": "application/json", "Accept": "application/json",
	} {
		if v := got.headers.Get(h); v != want {
			t.Errorf("the route saw %s=%q, want %q — the caller's request did not ride along", h, v, want)
		}
	}
	var body struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role, Content string
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("the route was handed %q: %v", got.body, err)
	}
	if body.Model != "zen5" || body.Stream || body.MaxTokens != 16 || len(body.Messages) != 2 ||
		body.Messages[0].Role != "system" || body.Messages[1].Content != "hi" {
		t.Fatalf("the route was handed %s", got.body)
	}
}

// TestChatReceiptNamesWhoPaid: a priced model's receipt is the route's own headers,
// read into the body an MCP client can see.
func TestChatReceiptNamesWhoPaid(t *testing.T) {
	app, _ := model(t, map[string]route{
		"POST /v1/chat/completions": {status: 200, headers: map[string]string{
			"X-Hanzo-Served": "anthropic/claude-sonnet-4.5", "X-Hanzo-Paid-By": "credits",
			"X-Hanzo-Usage-Class": "premium", "X-Hanzo-Usage": "ok",
			"X-RateLimit-Limit": "50", "X-RateLimit-Remaining": "49", "X-Spend-Warn": "80",
		}, body: strings.Replace(completion, `"zen5"`, `"anthropic/claude-sonnet-4.5"`, 1)},
	})
	text, isErr := tool(t, app, "aiChat", `{"model":"anthropic/claude-sonnet-4.5","prompt":"hi"}`)
	if isErr {
		t.Fatalf("aiChat refused: %s", text)
	}
	var out aiChat
	_ = json.Unmarshal([]byte(text), &out)
	want := aiReceipt{Served: "anthropic/claude-sonnet-4.5", PaidBy: "credits", Class: "premium", Usage: "ok"}
	if out.Receipt != want {
		t.Fatalf("receipt %+v, want %+v", out.Receipt, want)
	}
	for _, figure := range []string{"50", "49", "80"} {
		if strings.Contains(text, figure) {
			t.Errorf("the tool result carries %q, a figure from a rate or spend header: %s", figure, text)
		}
	}
}

// TestChatAsksTheDefaultModelWhenNoneIsNamed: an empty model is the deployment's
// default Hanzo model, never a third-party one.
func TestChatAsksTheDefaultModelWhenNoneIsNamed(t *testing.T) {
	app, got := model(t, map[string]route{
		"POST /v1/chat/completions": {status: 200, body: completion},
	})
	text, isErr := tool(t, app, "aiChat", `{"prompt":"hi"}`)
	if isErr {
		t.Fatalf("aiChat refused: %s", text)
	}
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal([]byte(got.body), &body)
	if body.Model != aimodel.DefaultModel {
		t.Fatalf("an empty model asked %q, want the default %q", body.Model, aimodel.DefaultModel)
	}
}

// TestARefusalIsAnIsErrorWithItsCodeAndNoFigure: the gate's refusal reaches the
// model as isError content naming the code, the class, when it lifts and what lifts
// it — and not the sentence, which states the allowance.
func TestARefusalIsAnIsErrorWithItsCodeAndNoFigure(t *testing.T) {
	refusal := `{"error":{"message":"Usage reached",` +
		`"type":"rate_limit_error","code":"free_plan_cap","class":"ours","limit":"day","resets_at":"2026-10-05T00:00:00Z",` +
		`"upgrade_url":"https://hanzo.ai/pay/cart?plan=dev","actions":[{"kind":"upgrade","label":"Upgrade your plan","url":"https://hanzo.ai/pay/cart?plan=dev","plan":"dev"}]}}`
	app, got := model(t, map[string]route{
		"POST /v1/decisions": {status: 429, headers: map[string]string{
			"X-Hanzo-Usage": "limited", "X-Hanzo-Usage-Class": "ours",
			"X-RateLimit-Limit": "50", "X-RateLimit-Remaining": "0", "Retry-After": "3600",
		}, body: refusal},
	})
	text, isErr := tool(t, app, "aiDecide",
		`{"model":"kai","state":"a ticket","questions":{"urgent":{"type":"noul","instructions":"Is it urgent?"}}}`)
	if !isErr {
		t.Fatalf("a refused decision answered as a success: %s", text)
	}
	var env struct {
		Error aiRefused `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("the refusal is not JSON an agent can read: %q", text)
	}
	r := env.Error
	if r.Status != 429 || r.Code != "free_plan_cap" || r.Class != "ours" || r.ResetsAt != "2026-10-05T00:00:00Z" ||
		len(r.Actions) != 1 || r.Actions[0].Kind != "upgrade" {
		t.Fatalf("the refusal reads %+v", r)
	}
	for _, figure := range []string{"50", "3600", "Usage reached"} {
		if strings.Contains(text, figure) {
			t.Errorf("the refusal carries %q: %s", figure, text)
		}
	}
	if got.path != "/v1/decisions" || got.headers.Get("Authorization") != "Bearer tok-caller" {
		t.Fatalf("the decision reached %s as %q", got.path, got.headers.Get("Authorization"))
	}
	var sent aiDecideIn
	if err := json.Unmarshal([]byte(got.body), &sent); err != nil || sent.Model != "kai" || len(sent.Questions) != 1 {
		t.Fatalf("the route was handed %s", got.body)
	}

	// The same op over REST answers the route's own status.
	req := httptest.NewRequest(http.MethodPost, "/v1/ai/mcp/decisions",
		strings.NewReader(`{"model":"kai","state":"x","questions":{"q":{"type":"noul"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-caller")
	req.Header.Set("X-User-Id", "u-1")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("REST answered %d, the route answered 429", resp.StatusCode)
	}
}

// TestARequestRefusalKeepsItsReason: a refusal about the request, not money, says
// what was wrong with it.
func TestARequestRefusalKeepsItsReason(t *testing.T) {
	app, _ := model(t, map[string]route{
		"POST /v1/decisions": {status: 400, body: `{"error":{"code":"invalid_request","message":"unknown model: jev-latest"}}`},
	})
	text, isErr := tool(t, app, "aiDecide", `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`)
	if !isErr || !strings.Contains(text, "unknown model: jev-latest") || !strings.Contains(text, "invalid_request") {
		t.Fatalf("a 400 answered isError=%v %s", isErr, text)
	}
}

// TestDecideAnswersTheDecisionAndItsReceipt: success passes the route's answer
// through whole, beside the receipt.
func TestDecideAnswersTheDecisionAndItsReceipt(t *testing.T) {
	decision := `{"id":"d1","model":"kai","provider":"hanzo","answers":{"urgent":{"type":"noul","noul":0.91}},"usage":{"input_tokens":12,"output_tokens":0},"routing":{},"state_hash":"ab","latency_ms":40}`
	app, _ := model(t, map[string]route{
		"POST /v1/decisions": {status: 200, headers: map[string]string{"X-Hanzo-Paid-By": "plan", "X-Hanzo-Usage-Class": "ours"}, body: decision},
	})
	text, isErr := tool(t, app, "aiDecide", `{"model":"kai","state":"x","questions":{"urgent":{"type":"noul"}}}`)
	if isErr {
		t.Fatalf("aiDecide refused: %s", text)
	}
	var out aiDecision
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("aiDecide answered %s", text)
	}
	if string(out.Decision) != decision {
		t.Fatalf("the decision was rewritten:\n got %s\nwant %s", out.Decision, decision)
	}
	if out.Receipt != (aiReceipt{Served: "kai", PaidBy: "plan", Class: "ours"}) {
		t.Fatalf("receipt %+v", out.Receipt)
	}
}

const catalog = `{"object":"list","data":[
{"id":"anthropic/claude-sonnet-4.5","name":"Claude Sonnet 4.5","owned_by":"anthropic","class":"premium","context_window":1000000,"inputs":["text","image"],"outputs":["text"],"supports_tools":true,"supports_vision":true,"supports_reasoning":true,"pricing":{"prompt":"0.0000036","completion":"0.000018","input_per_million":3.6,"output_per_million":18}},
{"id":"kai","name":"Kai","owned_by":"hanzo","class":"ours","family":"kai","description":"Hanzo's decision model","outputs":["decision"],"pricing":{"prompt":"0.000000021","completion":"0","input_per_million":0.021,"output_per_million":0}},
{"id":"openrouter/auto","name":"Auto Router","owned_by":"openrouter","class":"premium","context_window":2000000,"pricing":{"prompt":"0.00018","completion":"0.00072","input_per_million":180,"output_per_million":720,"variable":true}},
{"id":"zen5","owned_by":"zenlm","class":"free","family":"zen","free":true,"context_window":1000000,"outputs":["text"],"supports_vision":true,"pricing":{"prompt":"0","completion":"0","input_per_million":0,"output_per_million":0}},
{"id":"anthropic/claude-haiku-5.5","owned_by":"anthropic","class":"premium","context_window":1000000,"max_output_tokens":128000,"free":false,"pricing":{"prompt":"0.00000012","completion":"0.0000006","input_per_million":0.12,"output_per_million":0.6,"rates":{"prompt":"0.00000012","completion":"0.0000006","input_cache_read":"0.000000012","web_search":"0.012"},"overrides":[{"min_prompt_tokens":100000,"prompt":"0.0000006"}]}},
{"id":"google/gemma-4-31b-it:free","owned_by":"google","class":"free","free":true,"expires":"2026-12-31","context_window":262144,"pricing":{"prompt":"0","completion":"0","input_per_million":0,"output_per_million":0,"rates":{"prompt":"0","completion":"0"}}}
]}`

// TestModelsSearchesTheCatalog: each filter narrows the one catalog GET /v1/models
// lists, and each row carries its class, family, price and capabilities.
func TestModelsSearchesTheCatalog(t *testing.T) {
	app, got := model(t, map[string]route{"GET /v1/models": {status: 200, body: catalog}})
	ids := func(args string) ([]string, aiModels) {
		t.Helper()
		text, isErr := tool(t, app, "aiModels", args)
		if isErr {
			t.Fatalf("aiModels %s refused: %s", args, text)
		}
		var out aiModels
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("aiModels answered %s", text)
		}
		var names []string
		for _, m := range out.Models {
			names = append(names, m.ID)
		}
		return names, out
	}
	for args, want := range map[string]string{
		`{}`:                        "anthropic/claude-sonnet-4.5,kai,openrouter/auto,zen5,anthropic/claude-haiku-5.5,google/gemma-4-31b-it:free",
		`{"free":true}`:             "zen5,google/gemma-4-31b-it:free",
		`{"class":"ours"}`:          "kai",
		`{"family":"zen"}`:          "zen5",
		`{"q":"SONNET"}`:            "anthropic/claude-sonnet-4.5",
		`{"q":"decision"}`:          "kai",
		`{"capability":"vision"}`:   "anthropic/claude-sonnet-4.5,zen5",
		`{"capability":"decision"}`: "kai",
		`{"class":"premium","capability":"tools"}`: "anthropic/claude-sonnet-4.5",
	} {
		if names, _ := ids(args); strings.Join(names, ",") != want {
			t.Errorf("aiModels %s = %v, want %s", args, names, want)
		}
	}
	names, page := ids(`{"limit":1}`)
	if len(names) != 1 || page.Total != 6 {
		t.Errorf("limit 1 answered %v of total %d, want 1 of 6", names, page.Total)
	}
	_, haiku := ids(`{"q":"haiku"}`)
	if m := haiku.Models[0]; m.MaxOutputTokens != 128000 || m.Free || m.Pricing.Rates["web_search"] != "0.012" ||
		len(m.Pricing.Overrides) != 1 || m.Pricing.Overrides[0]["prompt"] != "0.0000006" {
		t.Errorf("the haiku row reads %+v", m)
	}
	_, gemma := ids(`{"q":"gemma"}`)
	if m := gemma.Models[0]; !m.Free || m.Expires != "2026-12-31" {
		t.Errorf("the gemma row reads %+v", m)
	}
	_, router := ids(`{"q":"openrouter/auto"}`)
	if m := router.Models[0]; !m.Pricing.Variable || m.Pricing.InputPerMillion != 180 || m.Class != "premium" {
		t.Errorf("the router row reads %+v", m)
	}
	_, claude := ids(`{"q":"sonnet"}`)
	if m := claude.Models[0]; strings.Join(m.Capabilities, ",") != "tools,vision,reasoning" ||
		m.ContextWindow != 1000000 || m.Pricing.Prompt != "0.0000036" || m.Name != "Claude Sonnet 4.5" {
		t.Errorf("the claude row reads %+v", m)
	}
	if got.method != http.MethodGet || got.path != "/v1/models" {
		t.Fatalf("the catalog was read at %s %s", got.method, got.path)
	}
}

// TestNoRequestNoCall: off the HTTP path there is no caller to run a route as, so
// the op refuses rather than call the model API as nobody.
func TestNoRequestNoCall(t *testing.T) {
	o := callOps{serve: func(*fasthttp.Request) (*fasthttp.Response, error) {
		t.Fatal("the model API was called with no caller")
		return nil, nil
	}}
	_, err := o.decide(context.Background(), &aiDecideIn{Model: "kai"})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("decide with no request answered %v, want 401", err)
	}
	if _, err := o.chat(context.Background(), &aiChatIn{}); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("chat with no prompt answered %v", err)
	}
}

// TestTheThreeAreToolsWithProse: each op is on ai's MCP server, described — the
// prose zipdoc lifts is what a model reads to pick one.
func TestTheThreeAreToolsWithProse(t *testing.T) {
	app, _ := model(t, nil)
	names := map[string]bool{}
	for _, n := range list(t, app) {
		names[n] = true
	}
	for _, n := range []string{"aiModels", "aiChat", "aiDecide", "aiFrameGenerate", "aiFrameEdit"} {
		if !names[n] {
			t.Errorf("%s is not on ai's MCP server: %v", n, names)
		}
	}
}
