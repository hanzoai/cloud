// SPDX-License-Identifier: Apache-2.0 OR MIT

package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/types"
)

func TestAIHTTPChatCompletionSuccessAndHeaders(t *testing.T) {
	var gotAuth, gotOrg, gotPath string
	var gotReq chatCompletionReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOrg = r.Header.Get("X-Org-Id")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"hanzo/zen","choices":[{"message":{"role":"assistant","content":"Summary complete."}}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`))
	}))
	defer srv.Close()

	c := AIHTTPAt(srv.URL, "fallback-key")
	ctx := WithAIBearer(context.Background(), "caller-iam-jwt")
	resp, err := c.ChatCompletion(ctx, &types.ChatRequest{
		Model:  "zen",
		Prompt: "Summarize aggregate metrics.",
		Org:    "acme",
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer caller-iam-jwt" {
		t.Fatalf("Authorization = %q, want caller bearer relay", gotAuth)
	}
	if gotOrg != "acme" {
		t.Fatalf("X-Org-Id = %q, want acme", gotOrg)
	}
	if gotReq.Model != "zen" {
		t.Fatalf("model = %q, want zen", gotReq.Model)
	}
	if resp.Content != "Summary complete." || resp.TotalTokens != 17 {
		t.Fatalf("resp = %+v, want Summary complete. / 17 tokens", resp)
	}
}

func TestAIHTTPFailsClosedOnOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model backend overloaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := AIHTTPAt(srv.URL, "")
	_, err := c.ChatCompletion(context.Background(), &types.ChatRequest{
		Model:  "zen",
		Prompt: "hello",
		Org:    "acme",
	})
	if !errors.Is(err, types.ErrUpstreamBusy) {
		t.Fatalf("err = %v, want types.ErrUpstreamBusy on 503 outage", err)
	}
}

func TestAIHTTPRefusesExternalProviderByDefault(t *testing.T) {
	c := AIHTTPAt("https://api.openai.com", "sk-test")
	_, err := c.ChatCompletion(context.Background(), &types.ChatRequest{
		Model:  "gpt-4o",
		Prompt: "hello",
		Org:    "acme",
	})
	if err == nil || !strings.Contains(err.Error(), "not loopback or Hanzo-owned") {
		t.Fatalf("err = %v, want non-Hanzo host refusal", err)
	}
}
