package infer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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

	ai := HTTPAt(srv.URL, "sk-test", FixedModel(defaultModel))

	// (1) empty model → default substituted; content parsed.
	got, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "say hi"})
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
	if _, err := ai.ChatCompletion(context.Background(), &ChatRequest{Model: "zen3-nano", Prompt: "x"}); err != nil {
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
	_, err := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !strings.Contains(err.Error(), "chat completion") {
		t.Errorf("error not wrapped by client: %v", err)
	}
}

// TestAIHTTP_TransientTagged proves the classification the agent runner relies on:
// a transient overload (429 / 5xx / empty-choices) is tagged ErrUpstreamBusy
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
		{"400 provider error", http.StatusBadRequest, `{"error":{"message":"provider returned error: context length exceeded"}}`, true},
		{"422 could not serve", http.StatusUnprocessableEntity, `{"error":{"message":"could not serve this request"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(tc.status, tc.body)
			defer srv.Close()
			_, err := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash")).
				ChatCompletion(context.Background(), &ChatRequest{Prompt: "x"})
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if got := errors.Is(err, ErrUpstreamBusy); got != tc.transit {
				t.Fatalf("%s: errors.Is(ErrUpstreamBusy)=%v, want %v (err=%v)", tc.name, got, tc.transit, err)
			}
		})
	}
}

func TestAIHTTP_ConsecutiveUserTurnsMerged(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: RoleUser, Content: "Instructions for turn"},
			{Role: RoleUser, Content: "User message 1"},
			{Role: RoleUser, Content: "User message 2"},
			{Role: RoleAssistant, Content: "Assistant response"},
			{Role: RoleUser, Content: "User message 3"},
		},
	}
	msgs := openaiMessages(req)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages after merging adjacent user turns, got %d: %+v", len(msgs), msgs)
	}
	wantFirst := "Instructions for turn\n\nUser message 1\n\nUser message 2"
	if msgs[0].Content != wantFirst {
		t.Fatalf("expected merged content %q, got %q", wantFirst, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Assistant response" {
		t.Fatalf("unexpected second message: %+v", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Content != "User message 3" {
		t.Fatalf("unexpected third message: %+v", msgs[2])
	}
}

// Regression test for hanzo-inc/cloud#937: a leading system prompt must remain
// role="system" and never be merged into consecutive user turns from a Slack thread.
func TestAIHTTP_SystemPromptNotMergedWithUserTurns(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: RoleSystem, Content: "You are Hanzo, an AI assistant made by Hanzo AI."},
			{Role: RoleUser, Content: "Denis: docs page broken"},
			{Role: RoleUser, Content: "@Hanzo fix it"},
		},
	}
	msgs := openaiMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + merged user), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleSystem || msgs[0].Content != "You are Hanzo, an AI assistant made by Hanzo AI." {
		t.Fatalf("system message must stay separate, got %+v", msgs[0])
	}
	if msgs[1].Role != RoleUser || msgs[1].Content != "Denis: docs page broken\n\n@Hanzo fix it" {
		t.Fatalf("consecutive user turns must merge without absorbing system prompt, got %+v", msgs[1])
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
	if _, err := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "x"}); err == nil {
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

	ai := HTTPM2M(srv.URL /*baseURL*/, srv.URL+"/v1/iam/oauth/token" /*tokenURL*/, "hanzo-cloud", "s3cr3t", FixedModel("deepseek-v4-flash"))
	got, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping"})
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
	if _, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping2"}); err != nil {
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
	_, err := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error on empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected no-choices error, got: %v", err)
	}
}

// TestAIHTTP_UnparsedCallRefused asserts that tool-call markup the upstream did not
// parse is REFUSED for formats we don't recognize, so the agent loop never sees
// serialization internals. DSML and Begin Patch are now PARSED (see
// TestAIHTTP_DSMLToolCallsParsed and TestAIHTTP_BeginPatchParsed), so they are
// no longer refused. The other families (deepseek native, qwen/hermes, mistral,
// llama) are still refused with ErrUpstreamBusy, and the refusal carries none of
// the markup.
func TestAIHTTP_UnparsedCallRefused(t *testing.T) {
	for _, tc := range []struct {
		family      string
		content     string
		shouldParse bool // true = we now parse this format, expect success
	}{
		// DSML is now parsed - see TestAIHTTP_DSMLToolCallsParsed
		{"deepseek dsml", "<\uFF5CDSML\uFF5Ctool_calls>\n<\uFF5CDSML\uFF5Cinvoke name=\"websearch\">\n" +
			"<\uFF5CDSML\uFF5Cparameter name=\"op\" string=\"true\">search_web</\uFF5CDSML\uFF5Cparameter>\n" +
			"</\uFF5CDSML\uFF5Cinvoke>\n</\uFF5CDSML\uFF5Ctool_calls>", true},
		// These formats are still refused
		{"deepseek native", "<\uFF5Ctool\u2581calls\u2581begin\uFF5C><\uFF5Ctool\u2581call\u2581begin\uFF5C>function<\uFF5Ctool\u2581sep\uFF5C>websearch\n" +
			"```json\n{\"op\":\"search_web\"}\n```<\uFF5Ctool\u2581call\u2581end\uFF5C>", false},
		{"qwen hermes", "<tool_call>\n{\"name\":\"websearch\",\"arguments\":{\"op\":\"search_web\"}}\n</tool_call>", false},
		{"mistral", string([]byte{91, 84, 79, 79, 76, 95, 67, 65, 76, 76, 83, 93}) + "[{\"name\":\"websearch\",\"arguments\":{\"op\":\"search_web\"}}]", false},
		{"llama", "<|python_tag|>{\"name\":\"websearch\",\"parameters\":{\"op\":\"search_web\"}}", false},
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

			got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
				ChatCompletion(context.Background(), &ChatRequest{Prompt: "latest on x.com?"})
			if tc.shouldParse {
				// DSML should now be parsed successfully
				if err != nil {
					t.Fatalf("DSML tool call was refused but should be parsed: %v", err)
				}
				if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "websearch" {
					t.Fatalf("tool calls not parsed: %+v", got.ToolCalls)
				}
				if got.FinishReason != "tool_calls" {
					t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
				}
			} else {
				// Other formats still refused
				if err == nil {
					t.Fatalf("an unparsed tool call was returned as an answer: %q", got.Content)
				}
				if !errors.Is(err, ErrUpstreamBusy) {
					t.Errorf("want ErrUpstreamBusy so the runner retries/fails over, got: %v", err)
				}
				marker := string([]byte{91, 84, 79, 79, 76, 95, 67, 65, 76, 76, 83, 93})
				for _, leak := range []string{"DSML", "\uFF5C", "tool\u2581", marker, "<|python_tag|>"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("the refusal quotes the markup it exists to withhold (%q): %v", leak, err)
					}
				}
			}
		})
	}
}

// TestUnparsedMistralMarker asserts unparsed returns the mistral marker when it
// appears anywhere in content.
func TestUnparsedMistralMarker(t *testing.T) {
	marker := string([]byte{91, 84, 79, 79, 76, 95, 67, 65, 76, 76, 83, 93})
	if got := unparsed("prefix " + marker + " suffix"); got != marker {
		t.Fatalf("unparsed returned %q, want the mistral marker", got)
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

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "latest on x.com?"})
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

// TestAIHTTP_Models asserts httpAI implements ModelLister: it GETs the
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

	lister, ok := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash")).(ModelLister)
	if !ok {
		t.Fatal("httpAI must implement ModelLister")
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

	ai := HTTPAt(srv.URL, "sk-test", FixedModel("deepseek-v4-flash"))
	scores, err := ai.Rerank(context.Background(), &RerankRequest{
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
	if empty, err := ai.Rerank(context.Background(), &RerankRequest{Model: "zen-rerank"}); err != nil || empty != nil {
		t.Fatalf("empty documents: got %v, %v want nil, nil", empty, err)
	}
}

func TestAIHTTP_RerankRefusesPartialAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 0, "relevance_score": 1}}})
	}))
	defer srv.Close()
	_, err := HTTPAt(srv.URL, "sk-test", FixedModel("m")).Rerank(context.Background(), &RerankRequest{
		Model: "zen-rerank", Query: "q", Documents: []string{"a", "b"},
	})
	if err == nil {
		t.Fatal("one score for two documents must be an error, not a zero for the missing one")
	}
}

// TestAIHTTP_DSMLToolCallsParsed asserts that DSML tool_calls markup in assistant
// content is parsed into structured ToolCalls rather than treated as a finished
// prose turn. DeepSeek emits <｜DSML｜tool_calls><｜DSML｜invoke name="exec_command">...
// when the serving stack has not parsed it; we must extract it so the agent loop
// can execute the call.
func TestAIHTTP_DSMLToolCallsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"exec_command\">\n<｜DSML｜parameter name=\"cmd\" string=\"true\">ls -la</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜tool_calls>"},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "list files"})
	if err != nil {
		t.Fatalf("DSML tool call was refused: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("tool calls not parsed: %+v", got.ToolCalls)
	}
	if got.FinishReason != "tool_calls" {
		t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
	}
}

// TestAIHTTP_BeginPatchParsed asserts that a "Begin Patch" block in assistant
// content is parsed into a structured patch tool call, not treated as finished
// prose. The coding agent emits *** Begin Patch ... *** End Patch when the
// serving stack has not parsed it as a tool call.
func TestAIHTTP_BeginPatchParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "*** Begin Patch\n*** Update File: handler.go\n@@ func handle() {\n- return 1\n+ return 2\n*** End Patch"},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "fix handler"})
	if err != nil {
		t.Fatalf("Begin Patch was refused: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "patch" {
		t.Fatalf("patch tool call not parsed: %+v", got.ToolCalls)
	}
	if got.FinishReason != "tool_calls" {
		t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
	}
}

// TestAIHTTP_StreamBeginPatchParsed asserts that a streamed "Begin Patch" body
// becomes tool calls rather than an ErrUpstreamBusy refusal or raw prose.
func TestAIHTTP_StreamBeginPatchParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(
			"data: {\"id\":\"chatcmpl-x\",\"object\":\"chat.completion.chunk\",\"model\":\"enso\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"*** Begin Patch\\n*** Update File: handler.go\\n@@ func handle() {\\n- return 1\\n+ return 2\\n*** End Patch\"},\"finish_reason\":null}]}" + "\n\n" +
				"data: {\"id\":\"chatcmpl-x\",\"object\":\"chat.completion.chunk\",\"model\":\"enso\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}" + "\n\n" +
				"data: [DONE]" + "\n\n",
		))
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).(StreamCompleter).ChatStream(
		context.Background(), &ChatRequest{Prompt: "fix handler"},
		func(d string) error { return nil },
	)
	if err != nil {
		t.Fatalf("streamed Begin Patch was refused: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "patch" {
		t.Fatalf("patch tool call not parsed from stream: %+v", got.ToolCalls)
	}
	if got.FinishReason != "tool_calls" {
		t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
	}
}

// TestAIHTTP_ProgressMessageNotComplete asserts that a text-only progress
// message like "Now let me run the tests" does NOT mark the run task_complete.
// The agent loop must continue to execute the announced tool call.
func TestAIHTTP_ProgressMessageNotComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "Now let me run the tests:"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "run tests"})
	if err != nil {
		t.Fatalf("progress message was refused: %v", err)
	}
	if got.FinishReason == "tool_calls" {
		t.Fatalf("progress message incorrectly parsed as tool_calls: %+v", got.ToolCalls)
	}
	// Should be a normal completion with finish_reason "stop", not tool_calls
	if got.FinishReason != "stop" {
		t.Errorf("finish reason: got %q want stop", got.FinishReason)
	}
	if len(got.ToolCalls) != 0 {
		t.Errorf("progress message should not have tool calls: %+v", got.ToolCalls)
	}
}

// TestAIHTTP_NormalToolCallsStillWorks asserts that a normal structured
// tool_calls payload from the upstream still works correctly.
func TestAIHTTP_NormalToolCallsStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": "call_1", "type": "function", "function": map[string]any{"name": "websearch", "arguments": "{\"query\": \"hanzo\"}"}}}},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "search"})
	if err != nil {
		t.Fatalf("normal tool calls were refused: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "websearch" {
		t.Fatalf("tool calls lost: %+v", got.ToolCalls)
	}
	if got.FinishReason != "tool_calls" {
		t.Errorf("finish reason: got %q want tool_calls", got.FinishReason)
	}
}

// TestAIHTTP_FinalAnswerCompletes asserts that a genuinely final answer with
// no tool markup can still complete the run.
func TestAIHTTP_FinalAnswerCompletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "The task is complete. I have fixed the bug."},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "fix bug"})
	if err != nil {
		t.Fatalf("final answer was refused: %v", err)
	}
	if got.FinishReason != "stop" {
		t.Errorf("finish reason: got %q want stop", got.FinishReason)
	}
	if len(got.ToolCalls) != 0 {
		t.Errorf("final answer should not have tool calls: %+v", got.ToolCalls)
	}
	if !strings.Contains(got.Content, "task is complete") {
		t.Errorf("content not preserved: %q", got.Content)
	}
}

// TestAIHTTP_StripsChannelReasoning pins issue #782: upstream channel/thought
// reasoning is serialization internals, not part of the visible reply. A
// completion whose content carries <|channel>thought reasoning plus a visible
// answer returns only the visible answer in Content, with the reasoning moved
// to Reasoning and no channel/thought markup leaking to the client.
func TestAIHTTP_StripsChannelReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso-auto",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]string{"role": "assistant",
					"content": "<|channel>thought</|channel>\nI need to check the latest on x.com.\n</|channel>\nHere is the latest: it launched today."},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso-auto")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "latest on x.com?"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got.Content != "Here is the latest: it launched today." {
		t.Errorf("content: got %q, want only the visible answer", got.Content)
	}
	if got.Reasoning != "I need to check the latest on x.com." {
		t.Errorf("reasoning: got %q, want the stripped thought block", got.Reasoning)
	}
	for _, leak := range []string{"<|channel>", "thought", "</|channel>"} {
		if strings.Contains(got.Content, leak) {
			t.Errorf("content leaks channel/thought markup %q: %q", leak, got.Content)
		}
	}
}

// TestAIHTTP_ReasoningDoesNotEatTheVisibleBudget pins issue #782's second half:
// hidden reasoning tokens must not consume the visible max_tokens budget so the
// visible answer is cut off mid-sentence. The fake upstream enforces the cap it
// receives: with a tight requested max_tokens and no internal bump it would
// truncate the visible answer. With the internal bump the full visible answer
// survives even though the requested budget is tight.
func TestAIHTTP_ReasoningDoesNotEatTheVisibleBudget(t *testing.T) {
	var sawMax int
	const tight = 16 // what the caller asked for: far too small for reasoning+answer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sawMax = req.MaxTokens
		var answer strings.Builder
		answer.WriteString("<|channel>thought</|channel>\n")
		for i := 0; i < 40; i++ {
			answer.WriteString("thinking token ") // long hidden reasoning block
		}
		answer.WriteString("</|channel>\n")
		answer.WriteString("The visible answer completes its sentence here.")
		content := answer.String()
		// The upstream truncates at its received cap, exactly as the bug
		// reproduces: reasoning eats the budget and the visible answer is cut.
		if len(content) > sawMax {
			content = content[:sawMax]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso-auto",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": "length",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso-auto")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "x", MaxTokens: tight})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Internally the cap sent upstream must exceed the requested tight cap so
	// hidden reasoning does not starve the visible answer.
	if sawMax <= tight {
		t.Errorf("internal max_tokens %d did not exceed the requested %d — reasoning can still eat the visible budget", sawMax, tight)
	}
	if !strings.Contains(got.Content, "The visible answer completes its sentence here.") {
		t.Errorf("content truncated by hidden reasoning: %q", got.Content)
	}
	if strings.Contains(got.Content, "<|channel>") {
		t.Errorf("content still carries channel markup: %q", got.Content)
	}
}

// TestAIHTTP_NoReasoningUnchanged is the control: an ordinary completion with no
// reasoning passes through exactly as before, with empty Reasoning.
func TestAIHTTP_NoReasoningUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-x", "object": "chat.completion", "model": "enso-auto",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "plain answer"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso-auto")).
		ChatCompletion(context.Background(), &ChatRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got.Content != "plain answer" {
		t.Errorf("content: got %q want plain answer", got.Content)
	}
	if got.Reasoning != "" {
		t.Errorf("reasoning should be empty with no channel markup, got %q", got.Reasoning)
	}
}

// TestAIHTTP_StreamingStripsChannelReasoning proves streaming and non-streaming
// agree on the stripped content: a streamed channel/thought block is removed from
// the emitted deltas and the returned Content.
func TestAIHTTP_StreamingStripsChannelReasoning(t *testing.T) {

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		frames := []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"<|channel>thought</|channel>thinking...\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"</|channel>visible answer\"}}]}\n\n",
			"data: [DONE]\n\n",
		}
		for _, fr := range frames {
			io.WriteString(w, fr)
			f.Flush()
		}
		r.Context().Done()
	}))
	defer srv.Close()

	sc, ok := HTTPAt(srv.URL, "sk-test", FixedModel("enso-auto")).(StreamCompleter)
	if !ok {
		t.Fatal("the http transport must implement StreamCompleter")
	}
	var got []string
	resp, err := sc.ChatStream(context.Background(), &ChatRequest{Prompt: "x"},
		func(d string) error { got = append(got, d); return nil })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	joined := strings.Join(got, "")
	if strings.Contains(joined, "<|channel>") || strings.Contains(joined, "thought") {
		t.Errorf("streamed deltas leak channel/thought markup: %q", joined)
	}
	if resp.Content != "visible answer" {
		t.Errorf("stream Content: got %q want %q", resp.Content, "visible answer")
	}
	if resp.Reasoning != "thinking..." {
		t.Errorf("stream Reasoning: got %q want thinking...", resp.Reasoning)
	}
}

// A streamed completion offers the caller's tools and returns the calls the model
// made, assembled from the pieces the deltas carry, as ChatCompletion would.
func TestAStreamCarriesToolsAndAssemblesTheirCalls(t *testing.T) {
	var offered []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offered, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta string, finish string) string {
			fr := "null"
			if finish != "" {
				fr = `"` + finish + `"`
			}
			return `data: {"id":"c","object":"chat.completion.chunk","model":"enso","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + fr + "}]}\n\n"
		}
		_, _ = w.Write([]byte(
			chunk(`{"content":"Running it."}`, "") +
				chunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":""}}]}`, "") +
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"argv\":"}}]}`, "") +
				chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"[\"ls\"]}"}}]}`, "") +
				chunk(`{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}`, "") +
				chunk(`{}`, "tool_calls") + "data: [DONE]\n\n"))
	}))
	defer srv.Close()

	var said []string
	got, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).(StreamCompleter).ChatStream(
		context.Background(),
		&ChatRequest{Prompt: "list", Tools: []ToolDef{{Name: "exec", Schema: json.RawMessage(`{"type":"object"}`)}}},
		func(d string) error { said = append(said, d); return nil },
	)
	if err != nil {
		t.Fatalf("a stream of tool calls was refused: %v", err)
	}
	if !strings.Contains(string(offered), `"tools"`) || !strings.Contains(string(offered), `"exec"`) {
		t.Errorf("the stream offered no tools: %s", offered)
	}
	want := []ToolCall{{ID: "call_1", Name: "exec", Arguments: `{"argv":["ls"]}`}, {ID: "call_2", Name: "read", Arguments: `{"path":"a"}`}}
	if !reflect.DeepEqual(got.ToolCalls, want) {
		t.Errorf("tool calls %+v, want %+v", got.ToolCalls, want)
	}
	if got.Content != "Running it." || strings.Join(said, "") != "Running it." || got.FinishReason != "tool_calls" {
		t.Errorf("content %q, said %v, finish %q", got.Content, said, got.FinishReason)
	}
}
