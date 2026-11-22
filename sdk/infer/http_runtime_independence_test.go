package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// bufferedServer answers a non-streamed completion. The buffered path never sets
// a time to first token (there is no first token to see), so its gen_ai span is
// the shape that proves the runtime/connector attribution is independent of
// gen_ai.latency.ttft_ms: the two facts are recorded from different signals.
func bufferedServer(t *testing.T, model string) *httptest.Server {
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

// The runtime digest and cache connector are recorded on the gen_ai span EVEN
// WHEN the span carries no time to first token. The bounty requires the three
// attribution attributes to be emitted independently of gen_ai.latency.ttft_ms;
// this is the direct proof: a buffered completion, which by construction has no
// TTFT, still names the runtime and the connector, because they come from the
// request, not from the clock.
func TestRuntimeAndConnectorAreIndependentOfTTFT(t *testing.T) {
	sr := spanRecorder(t)
	srv := bufferedServer(t, "zen5-flash")

	ai := HTTPAt(srv.URL, "sk-test", FixedModel("zen5-flash"))
	req := &ChatRequest{
		Prompt:         "hi",
		Org:            "acme",
		RuntimeDigest:  "sha256:beef",
		CacheConnector: "radix-cache",
	}
	if _, err := ai.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("gen_ai spans = %d, want 1", len(ended))
	}
	span := ended[0]

	// The precondition that makes this test mean something: no TTFT on this span.
	if v, ok := spanAttr(span, AttrTTFT); ok {
		t.Fatalf("precondition broken: a buffered completion carries %s = %s", AttrTTFT, v)
	}

	// Yet both attribution attributes are present, independent of TTFT.
	if got, ok := spanAttr(span, "gen_ai.hanzo.runtime_digest"); !ok || got != "sha256:beef" {
		t.Errorf("gen_ai.hanzo.runtime_digest = %q (present=%v) without TTFT, want %q", got, ok, "sha256:beef")
	}
	if got, ok := spanAttr(span, "gen_ai.hanzo.cache_connector"); !ok || got != "radix-cache" {
		t.Errorf("gen_ai.hanzo.cache_connector = %q (present=%v) without TTFT, want %q", got, ok, "radix-cache")
	}
}

// When a stream DOES reach a first content token, the span carries TTFT AND the
// two attribution attributes side by side — they coexist, neither displacing the
// other, so a reader correlates runtime, connector, and latency on one span.
func TestRuntimeAndConnectorCoexistWithTTFT(t *testing.T) {
	sr := spanRecorder(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"model\":\"zen5-flash\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok \"}}]}\n\n")
		f.Flush()
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
		CacheConnector: "prompt-cache-v2",
	}
	if _, err := s.ChatStream(context.Background(), req, func(string) error { return nil }); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("gen_ai spans = %d, want 1", len(ended))
	}
	span := ended[0]

	if _, ok := spanAttr(span, AttrTTFT); !ok {
		t.Error("a stream that reached a content token carries no time to first token")
	}
	if got, ok := spanAttr(span, "gen_ai.hanzo.runtime_digest"); !ok || got != "sha256:cafe" {
		t.Errorf("gen_ai.hanzo.runtime_digest = %q (present=%v) alongside TTFT, want %q", got, ok, "sha256:cafe")
	}
	if got, ok := spanAttr(span, "gen_ai.hanzo.cache_connector"); !ok || got != "prompt-cache-v2" {
		t.Errorf("gen_ai.hanzo.cache_connector = %q (present=%v) alongside TTFT, want %q", got, ok, "prompt-cache-v2")
	}
}
