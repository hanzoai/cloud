package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// chatServer is a one-shot OpenAI-compatible chat server: it answers any request
// with a fixed non-streamed completion carrying the given model, so a test can
// drive chatOnce without a real gateway.
func chatServer(t *testing.T, model string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": model,
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A completion that names the runtime that issued it and the cache connector in
// front of the model carries both on its gen_ai span, as
// gen_ai.hanzo.runtime_digest and gen_ai.hanzo.cache_connector — the attribution
// the unified-tracing bounty asks for, independent of time to first token.
func TestChatCompletionCarriesRuntimeDigestAndCacheConnector(t *testing.T) {
	sr := spanRecorder(t)
	srv := chatServer(t, "zen5-flash")

	ai := HTTPAt(srv.URL, "sk-test", FixedModel("zen5-flash"))
	req := &ChatRequest{
		Prompt:         "hi",
		Org:            "acme",
		RuntimeDigest:  "sha256:deadbeef",
		CacheConnector: "prompt-cache-v2",
	}
	if _, err := ai.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("gen_ai spans = %d, want 1", len(ended))
	}
	span := ended[0]

	if got, ok := spanAttr(span, "gen_ai.hanzo.runtime_digest"); !ok || got != "sha256:deadbeef" {
		_ = sdktrace.ReadOnlySpan(span)
		t.Errorf("gen_ai.hanzo.runtime_digest = %q (present=%v), want %q", got, ok, "sha256:deadbeef")
	}
	if got, ok := spanAttr(span, "gen_ai.hanzo.cache_connector"); !ok || got != "prompt-cache-v2" {
		t.Errorf("gen_ai.hanzo.cache_connector = %q (present=%v), want %q", got, ok, "prompt-cache-v2")
	}
}

// A completion that names no runtime and no cache connector carries NEITHER
// attribute — never an empty one that would read as a runtime named "" or a
// connector named "". This is the same contract setRunAttr/setFeatureAttr keep.
func TestChatCompletionOmitsEmptyRuntimeDigestAndCacheConnector(t *testing.T) {
	sr := spanRecorder(t)
	srv := chatServer(t, "zen5-flash")

	ai := HTTPAt(srv.URL, "sk-test", FixedModel("zen5-flash"))
	if _, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "hi", Org: "acme"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("gen_ai spans = %d, want 1", len(ended))
	}
	if _, ok := spanAttr(ended[0], "gen_ai.hanzo.runtime_digest"); ok {
		t.Error("an empty RuntimeDigest still stamped gen_ai.hanzo.runtime_digest")
	}
	if _, ok := spanAttr(ended[0], "gen_ai.hanzo.cache_connector"); ok {
		t.Error("an empty CacheConnector still stamped gen_ai.hanzo.cache_connector")
	}
}

// A STREAMED completion carries the same two attribution attributes as a
// non-streamed one: the stream path stamps them on its gen_ai span too, so a
// reader filtering on runtime or connector sees both shapes.
func TestStreamedCompletionCarriesRuntimeDigestAndCacheConnector(t *testing.T) {
	sr := spanRecorder(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for range 2 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok \"}}]}\n\n")
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	ai := HTTPAt(srv.URL, "sk-test", FixedModel("zen5-flash"))
	s, ok := ai.(StreamCompleter)
	if !ok {
		t.Fatal("the HTTP client does not stream")
	}
	req := &ChatRequest{
		Prompt:         "hi",
		Org:            "acme",
		RuntimeDigest:  "sha256:cafe",
		CacheConnector: "radix-cache",
	}
	if _, err := s.ChatStream(context.Background(), req, func(string) error { return nil }); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("gen_ai spans = %d, want 1", len(ended))
	}
	if got, ok := spanAttr(ended[0], "gen_ai.hanzo.runtime_digest"); !ok || got != "sha256:cafe" {
		t.Errorf("streamed gen_ai.hanzo.runtime_digest = %q (present=%v), want %q", got, ok, "sha256:cafe")
	}
	if got, ok := spanAttr(ended[0], "gen_ai.hanzo.cache_connector"); !ok || got != "radix-cache" {
		t.Errorf("streamed gen_ai.hanzo.cache_connector = %q (present=%v), want %q", got, ok, "radix-cache")
	}
}
