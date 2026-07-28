package connectorruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegistry_NotionCustomApiCall exercises the EXACT call the KB long-tail
// sync makes — Run(ctx, org, "notion", "custom_api_call", auth, props) — end to
// end through the package registry and the default net/http doer. It proves the
// KB path runs native in-process: the Notion bearer auth is injected, the
// request KB builds (url/method/headers/body) is sent, and the JSON response is
// returned. No auto pod, no PIECES_RUNNER_SECRET, no cross-service hop.
func TestRegistry_NotionCustomApiCall(t *testing.T) {
	var gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("Notion-Version")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object":  "list",
			"results": []any{map[string]any{"id": "page-1"}},
		})
	}))
	defer srv.Close()

	if !Has("notion") {
		t.Fatal("notion connector not registered")
	}

	out, err := Run(context.Background(), "org-abc", "notion", "custom_api_call",
		map[string]any{"access_token": "secret_notion_token"},
		map[string]any{
			"method":    "POST",
			"url":       map[string]any{"url": srv.URL},
			"headers":   map[string]any{"Notion-Version": "2022-06-28"},
			"body_type": "json",
			"body":      map[string]any{"data": map[string]any{"page_size": 100}},
		},
	)
	if err != nil {
		t.Fatalf("notion custom_api_call: %v", err)
	}

	if gotAuth != "Bearer secret_notion_token" {
		t.Errorf("Authorization = %q, want 'Bearer secret_notion_token'", gotAuth)
	}
	if gotVersion != "2022-06-28" {
		t.Errorf("Notion-Version = %q, want 2022-06-28", gotVersion)
	}

	resp, _ := out.(map[string]any)
	body, _ := resp["body"].(map[string]any)
	results, _ := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", body)
	}
}

// TestRegistry_UnknownConnector fails closed on an unregistered provider.
func TestRegistry_UnknownConnector(t *testing.T) {
	if _, err := Run(context.Background(), "org", "does-not-exist", "x", nil, nil); err == nil {
		t.Fatal("expected error for unknown connector")
	}
}

// TestRegistry_Lists confirms both the real bundle and the generic connector
// are present.
func TestRegistry_Lists(t *testing.T) {
	got := map[string]bool{}
	for _, id := range Connectors() {
		got[id] = true
	}
	for _, want := range []string{"brave-search", "notion"} {
		if !got[want] {
			t.Errorf("connector %q not registered; have %v", want, Connectors())
		}
	}
}
