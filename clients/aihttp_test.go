package clients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// TestAIHTTP_DefaultModelAndContent asserts the two happy-path contracts the
// agents run path depends on: (1) an empty request model is replaced by the
// configured default before the call leaves the process, an explicit model is
// passed through verbatim, and (2) the assistant content is parsed out of
// choices[0].message.content. It also asserts the key rides only in the
// Authorization header (Bearer <key>) — the wiring the gateway authenticates.
func TestAIHTTP_DefaultModelAndContent(t *testing.T) {
	const defaultModel = "deepseek-v4-flash"
	var gotModel, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = req.Model
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("want one user message, got %+v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "created": 1, "model": req.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "hi there"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	ai := AIHTTPAt(srv.URL, "sk-test", defaultModel)

	// (1) empty model → default substituted; content parsed.
	got, err := ai.ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "say hi"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotModel != defaultModel {
		t.Errorf("default model: got %q want %q", gotModel, defaultModel)
	}
	if got.Content != "hi there" {
		t.Errorf("content: got %q want %q", got.Content, "hi there")
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth header: got %q want %q", gotAuth, "Bearer sk-test")
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path: got %q want /chat/completions", gotPath)
	}

	// (2) explicit model wins over the default.
	if _, err := ai.ChatCompletion(context.Background(), &types.ChatRequest{Model: "zen3-nano", Prompt: "x"}); err != nil {
		t.Fatalf("ChatCompletion explicit model: %v", err)
	}
	if gotModel != "zen3-nano" {
		t.Errorf("explicit model: got %q want zen3-nano", gotModel)
	}
}

// TestAIHTTP_UpstreamErrorMapped asserts a non-2xx upstream (429) becomes an
// explicit wrapped error — executeRun renders it as an error-status run, never
// a fabricated "ok".
func TestAIHTTP_UpstreamErrorMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()
	_, err := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash").
		ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !strings.Contains(err.Error(), "chat completion") {
		t.Errorf("error not wrapped by client: %v", err)
	}
}

// TestAIHTTP_TransientTagged proves the classification the agent runner relies on:
// a transient overload (429 / 5xx / empty-choices) is tagged types.ErrUpstreamBusy
// so the runner retries/fails over, while a permanent 4xx (400) is NOT tagged so it
// fails fast. Without this split the runner would either drop replies on a passing
// 429 or spin retries on an unrecoverable bad request.
func TestAIHTTP_TransientTagged(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if status != 0 {
				w.WriteHeader(status)
			}
			_, _ = w.Write([]byte(body))
		}))
	}
	cases := []struct {
		name    string
		status  int
		body    string
		transit bool
	}{
		{"429 overloaded", http.StatusTooManyRequests, `{"error":{"message":"Platform overloaded"}}`, true},
		{"503 unavailable", http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`, true},
		{"500 server", http.StatusInternalServerError, `{"error":{"message":"boom"}}`, true},
		{"200 empty choices", 0, `{"id":"x","object":"chat.completion","choices":[]}`, true},
		{"400 bad request", http.StatusBadRequest, `{"error":{"message":"bad input"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(tc.status, tc.body)
			defer srv.Close()
			_, err := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash").
				ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "x"})
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if got := errors.Is(err, types.ErrUpstreamBusy); got != tc.transit {
				t.Fatalf("%s: errors.Is(ErrUpstreamBusy)=%v, want %v (err=%v)", tc.name, got, tc.transit, err)
			}
		})
	}
}

// TestAIHTTP_ServerErrorMapped asserts a 5xx upstream also maps to an error.
func TestAIHTTP_ServerErrorMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	if _, err := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash").
		ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "x"}); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

// TestAIHTTP_M2M asserts the M2M path mints a client-credentials token from the
// IAM token endpoint and presents it as the completion's Bearer — the durable
// no-static-key credential. One httptest server plays both roles: the token
// endpoint (form-encoded client_credentials -> {access_token}) and the
// completions endpoint (asserts Authorization == the minted token).
func TestAIHTTP_M2M(t *testing.T) {
	const minted = "iam-access-token-xyz"
	var tokenHits int
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/oauth/token":
			tokenHits++
			_ = r.ParseForm()
			if r.PostFormValue("grant_type") != "client_credentials" {
				t.Errorf("grant_type: got %q", r.PostFormValue("grant_type"))
			}
			if r.PostFormValue("client_id") != "hanzo-cloud" || r.PostFormValue("client_secret") != "s3cr3t" {
				t.Errorf("creds not in form body: id=%q", r.PostFormValue("client_id"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + minted + `","token_type":"Bearer","expires_in":3600}`))
		case "/chat/completions":
			sawAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-m2m", "object": "chat.completion", "model": "deepseek-v4-flash",
				"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}},
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ai := AIHTTPM2M(srv.URL /*baseURL*/, srv.URL+"/v1/iam/oauth/token" /*tokenURL*/, "hanzo-cloud", "s3cr3t", "deepseek-v4-flash")
	got, err := ai.ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "ping"})
	if err != nil {
		t.Fatalf("M2M ChatCompletion: %v", err)
	}
	if got.Content != "pong" {
		t.Errorf("content: got %q want pong", got.Content)
	}
	if sawAuth != "Bearer "+minted {
		t.Errorf("completion Authorization: got %q want %q", sawAuth, "Bearer "+minted)
	}
	if tokenHits == 0 {
		t.Error("token endpoint was never called — M2M token was not minted")
	}

	// Second call reuses the cached token (no re-mint within its lifetime).
	if _, err := ai.ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "ping2"}); err != nil {
		t.Fatalf("M2M second call: %v", err)
	}
	if tokenHits != 1 {
		t.Errorf("expected token cached (1 mint), got %d mints", tokenHits)
	}
}

// TestAIHTTP_EmptyChoices asserts a 200 with an empty choices array is a hard
// error, not a silent empty completion.
func TestAIHTTP_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[]}`))
	}))
	defer srv.Close()
	_, err := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash").
		ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error on empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected no-choices error, got: %v", err)
	}
}

// TestAIHTTP_UnparsedCallRefused pins the SAFETY NET, not the fix: when an
// upstream fails to turn a model's native tool-call tokens into structured
// tool_calls and hands them back as prose, that text is serialization internals
// and must never reach the person waiting on the answer.
//
// The bodies here are verbatim shapes from the four families this gateway routes
// to. Each carries a real tool call the upstream did not parse, so tool_calls is
// empty and the loop would otherwise return the markup as the assistant's reply —
// which is exactly how a Slack turn came to print one.
//
// Two things are asserted, and the second matters as much as the first: the
// completion is REFUSED, and the refusal itself carries none of the markup. An
// error that quoted the bytes would leak the same internals by a shorter path.
// The refusal is tagged ErrUpstreamBusy because an upstream that drops its own
// parse is a fault a retry or a failover can actually clear.
func TestAIHTTP_UnparsedCallRefused(t *testing.T) {
	for _, tc := range []struct {
		family  string
		content string
	}{
		{"deepseek dsml", "<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"websearch\">\n" +
			"<｜DSML｜parameter name=\"op\" string=\"true\">search_web</｜DSML｜parameter>\n" +
			"</｜DSML｜invoke>\n</｜DSML｜tool_calls>"},
		{"deepseek native", "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>function<｜tool▁sep｜>websearch\n" +
			"```json\n{\"op\":\"search_web\"}\n```<｜tool▁call▁end｜>"},
		{"qwen hermes", "<tool_call>\n{\"name\":\"websearch\",\"arguments\":{\"op\":\"search_web\"}}\n</tool_call>"},
		{"mistral", "[TOOL_CALLS][{\"name\":\"websearch\",\"arguments\":{\"op\":\"search_web\"}}]"},
		{"llama", "<|python_tag|>{\"name\":\"websearch\",\"parameters\":{\"op\":\"search_web\"}}"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
					"choices": []map[string]any{{
						"index":         0,
						"message":       map[string]string{"role": "assistant", "content": tc.content},
						"finish_reason": "stop",
					}},
				})
			}))
			defer srv.Close()

			got, err := AIHTTPAt(srv.URL, "sk-test", "enso").
				ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "latest on x.com?"})
			if err == nil {
				t.Fatalf("an unparsed tool call was returned as an answer: %q", got.Content)
			}
			if !errors.Is(err, types.ErrUpstreamBusy) {
				t.Errorf("want ErrUpstreamBusy so the runner retries/fails over, got: %v", err)
			}
			for _, leak := range []string{"DSML", "｜", "tool▁", "<tool_call>", "[TOOL_CALLS]", "<|python_tag|>"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the refusal quotes the markup it exists to withhold (%q): %v", leak, err)
				}
			}
		})
	}
}

// TestAIHTTP_ToolCallsKept is the control for the refusal above: a completion the
// upstream DID parse is untouched. Structured tool_calls are read off the choice,
// and prose that merely mentions a tool is prose — the net recognises markup, and
// recognising it is all it does.
func TestAIHTTP_ToolCallsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,
			"message":{"role":"assistant","content":"Let me look that up with the websearch tool.",
			"tool_calls":[{"id":"call_1","type":"function",
			"function":{"name":"websearch","arguments":"{\"op\":\"search_web\"}"}}]},
			"finish_reason":"tool_calls"}]}`))
	}))
	defer srv.Close()

	got, err := AIHTTPAt(srv.URL, "sk-test", "enso").
		ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "latest on x.com?"})
	if err != nil {
		t.Fatalf("a parsed tool call was refused: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "websearch" {
		t.Fatalf("tool calls lost: %+v", got.ToolCalls)
	}
	if got.FinishReason != "tool_calls" {
		t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
	}
}

// TestAIHTTP_Models asserts httpAI implements types.ModelLister: it GETs the
// gateway's OpenAI-compatible /models list (Bearer-authenticated) and returns the
// served model ids — the catalog the agents subsystem validates a model against.
func TestAIHTTP_Models(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "zen-flash", "object": "model"},
				{"id": "deepseek-v4-flash", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	lister, ok := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash").(types.ModelLister)
	if !ok {
		t.Fatal("httpAI must implement types.ModelLister")
	}
	ids, err := lister.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(ids) != 2 || ids[0] != "zen-flash" || ids[1] != "deepseek-v4-flash" {
		t.Fatalf("model ids: got %v want [zen-flash deepseek-v4-flash]", ids)
	}
	if gotPath != "/models" {
		t.Errorf("path: got %q want /models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth: got %q want Bearer sk-test", gotAuth)
	}
}

func TestAIHTTP_Rerank(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		// Cohere order: best first — the client must put scores back by index.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "model": "zen-rerank",
			"results": []map[string]any{
				{"index": 2, "relevance_score": 0.9},
				{"index": 0, "relevance_score": 0.4},
				{"index": 1, "relevance_score": 0.1},
			},
		})
	}))
	defer srv.Close()

	ai := AIHTTPAt(srv.URL, "sk-test", "deepseek-v4-flash")
	scores, err := ai.Rerank(context.Background(), &types.RerankRequest{
		Model: "zen-rerank", Query: "q", Documents: []string{"a", "b", "c"}, Org: "acme",
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 3 || scores[0] != 0.4 || scores[1] != 0.1 || scores[2] != 0.9 {
		t.Fatalf("scores by index: got %v want [0.4 0.1 0.9]", scores)
	}
	if gotPath != "/rerank" || gotAuth != "Bearer sk-test" {
		t.Errorf("path/auth: got %q %q", gotPath, gotAuth)
	}
	if gotBody["model"] != "zen-rerank" || gotBody["query"] != "q" || gotBody["top_n"] != float64(3) {
		t.Errorf("body: got %v", gotBody)
	}
	if empty, err := ai.Rerank(context.Background(), &types.RerankRequest{Model: "zen-rerank"}); err != nil || empty != nil {
		t.Fatalf("empty documents: got %v, %v want nil, nil", empty, err)
	}
}

func TestAIHTTP_RerankRefusesPartialAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 0, "relevance_score": 1}}})
	}))
	defer srv.Close()
	_, err := AIHTTPAt(srv.URL, "sk-test", "m").Rerank(context.Background(), &types.RerankRequest{
		Model: "zen-rerank", Query: "q", Documents: []string{"a", "b"},
	})
	if err == nil {
		t.Fatal("one score for two documents must be an error, not a zero for the missing one")
	}
}
