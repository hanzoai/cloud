package connectorruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// loadBrave compiles the committed brave-search bundle — the REAL ActivePieces
// piece source (packages/pieces/community/brave-search) esbuild-bundled with
// the framework left external. This is the keystone: one real connector
// running native in goja proves the pattern for all of them.
func loadBrave(t *testing.T, rt *Runtime) *Connector {
	t.Helper()
	js, err := os.ReadFile(filepath.Join("connectors", "brave-search.connector.js"))
	if err != nil {
		t.Fatalf("read brave bundle: %v", err)
	}
	c, err := rt.Compile("brave-search", js)
	if err != nil {
		t.Fatalf("compile brave: %v", err)
	}
	return c
}

// TestBraveWebSearch_NativeGoja runs brave-search's real web_search action
// in-process. A doer captures the request the connector built so we can prove
// the framework shim delivered auth + props correctly: the action reads
// context.auth.secret_text into the X-Subscription-Token header and
// context.propsValue.{query,count} into the query params, then returns
// response.body. No Node, no auto pod.
func TestBraveWebSearch_NativeGoja(t *testing.T) {
	var gotReq HTTPRequest
	rt, err := NewRuntime(func(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
		gotReq = req
		return HTTPResponse{
			Status:  200,
			Headers: map[string]string{"content-type": "application/json"},
			Body: map[string]any{
				"web": map[string]any{
					"results": []any{
						map[string]any{"title": "Hanzo AI", "url": "https://hanzo.ai"},
					},
				},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	brave := loadBrave(t, rt)

	out, err := rt.Run(context.Background(), brave, RunInput{
		Action: "web_search",
		Auth:   map[string]any{"secret_text": "sk-brave-test"},
		Props:  map[string]any{"query": "hanzo ai", "count": 5},
	})
	if err != nil {
		t.Fatalf("run web_search: %v", err)
	}

	// The action built the exact Brave request from the shim-delivered context.
	if gotReq.Method != "GET" {
		t.Errorf("method = %q, want GET", gotReq.Method)
	}
	if gotReq.URL != "https://api.search.brave.com/res/v1/web/search" {
		t.Errorf("url = %q", gotReq.URL)
	}
	if gotReq.Headers["X-Subscription-Token"] != "sk-brave-test" {
		t.Errorf("auth header = %q, want sk-brave-test (auth injection failed)", gotReq.Headers["X-Subscription-Token"])
	}
	if gotReq.QueryParams["q"] != "hanzo ai" {
		t.Errorf("query q = %q, want 'hanzo ai'", gotReq.QueryParams["q"])
	}
	if gotReq.QueryParams["count"] != "5" {
		t.Errorf("query count = %q, want 5", gotReq.QueryParams["count"])
	}

	// The action returned response.body — the search results — verbatim.
	body, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type = %T, want map", out)
	}
	web, _ := body["web"].(map[string]any)
	results, _ := web["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", body)
	}
	first, _ := results[0].(map[string]any)
	if first["title"] != "Hanzo AI" {
		t.Errorf("first result title = %v", first["title"])
	}
}

// TestCustomApiCall_DefaultDoer_EndToEnd exercises the FULL native path with
// the real net/http doer against a live test server: brave-search's
// createCustomApiCallAction (the same generic action the KB long-tail sync
// invokes for notion). It proves the default doer builds a correct request,
// the shim's authMapping injects auth, and the JSON response round-trips.
func TestCustomApiCall_DefaultDoer_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "sk-live" {
			t.Errorf("server saw auth header %q, want sk-live", got)
		}
		if got := r.URL.Query().Get("q"); got != "native" {
			t.Errorf("server saw q=%q, want native", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "echo": "native"})
	}))
	defer srv.Close()

	rt, err := NewRuntime(nil) // nil => default net/http doer
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	brave := loadBrave(t, rt)

	out, err := rt.Run(context.Background(), brave, RunInput{
		Action: "custom_api_call",
		Auth:   map[string]any{"secret_text": "sk-live"},
		Props: map[string]any{
			"method":      "GET",
			"url":         map[string]any{"url": srv.URL},
			"queryParams": map[string]any{"q": "native"},
			"headers":     map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("run custom_api_call: %v", err)
	}
	resp, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type = %T, want map", out)
	}
	if resp["status"] != int64(200) && resp["status"] != float64(200) {
		t.Errorf("status = %v (%T), want 200", resp["status"], resp["status"])
	}
	body, _ := resp["body"].(map[string]any)
	if body["ok"] != true || body["echo"] != "native" {
		t.Errorf("body = %v, want {ok:true, echo:native}", body)
	}
}

// TestUnknownActionAndConnector proves honest failures (no silent success).
func TestUnknownAction(t *testing.T) {
	rt, err := NewRuntime(func(_ context.Context, _ HTTPRequest) (HTTPResponse, error) {
		return HTTPResponse{Status: 200}, nil
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	brave := loadBrave(t, rt)
	if _, err := rt.Run(context.Background(), brave, RunInput{Action: "nope"}); err == nil {
		t.Fatal("expected error for unknown action, got nil")
	}
}
