package infer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamWithholdsUnparsedToolCall(t *testing.T) {
	const prose = "I can build that.\n"
	const object = `{"name":"Hanzo","action":"coding","repo":"example"}`
	for _, tc := range []struct {
		name   string
		deltas []string
		busy   bool
		want   string
	}{
		{"closing tag only", []string{prose, "{", `"name":"Hanzo",`, `"action":"coding","repo":"example"}`, "</parameter></invoke></ernie:", "tool_call>"}, true, prose},
		{"exact screenshot churnguard payload", []string{"Let me build it:\n\n", `{"name":"Hanzo","action":"coding","repo":"churnguard","prompt":"Build ChurnGuard MVP"}`, `</parameter><parameter name="op">create_agent_coding</parameter></invoke> </ernie:tool_call>`}, true, "Let me build it:\n\n"},
		{"inline same-line envelope", []string{"Let me build it: ", `{"name":"Hanzo","action":"coding"}`, `</parameter></invoke></ernie:tool_call>`}, true, "Let me build it: "},
		{"closing parameter invoke without ernie tag", []string{prose, `{"op":"create_agent_coding"}`, `</parameter></invoke>`}, true, prose},
		{"opening tag", []string{prose + "<er", "nie:tool_call>", object, "</ernie:tool_call>"}, true, prose},
		{"ordinary JSON", []string{prose, object}, false, prose + object},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, d := range tc.deltas {
					data, _ := json.Marshal(d)
					fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s}}]}`+"\n\n", data)
					w.(http.Flusher).Flush()
				}
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			var visible strings.Builder
			resp, err := HTTPAt(srv.URL, "sk-test", FixedModel("enso")).(StreamCompleter).
				ChatStream(context.Background(), &ChatRequest{Prompt: "Build an example"},
					func(d string) error { visible.WriteString(d); return nil })
			if tc.busy {
				if !errors.Is(err, ErrUpstreamBusy) || resp != nil {
					t.Fatalf("expected retryable inference error, got %+v, %v", resp, err)
				}
			} else if err != nil || resp.Content != tc.want {
				t.Fatalf("expected ordinary content %q, got %+v, %v", tc.want, resp, err)
			}
			if visible.String() != tc.want {
				t.Fatalf("emitted %q, want %q", visible.String(), tc.want)
			}
		})
	}
}
