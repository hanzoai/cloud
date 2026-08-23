package guide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// An upstream that refuses a call routinely quotes the credential back, and this
// surface relays that refusal to a signed-in customer inside an HTTP 200 — twice,
// as the answer's own error field and as the error field of every event. So the
// property is about the WHOLE body: the key must appear nowhere in it.
//
// The test drives the real route through the real handler rather than calling the
// scrubber, because what is being checked is that this path REACHES the scrubber.
// A test of ScrubText alone passes with both call sites deleted.
const upstreamKey = "sk-ant-api03-Zx9QwErTyUiOpAsDfGhJkLzXcVbNm0123456789"

func TestDoStepNeverRelaysAnUpstreamCredential(t *testing.T) {
	app := newApp(t)
	mounted.State.ai = &fakeAI{content: "positioning copy"}
	mounted.State.model = "zen"
	mounted.State.toolOK = func(string) bool { return true }
	mounted.State.invoke = func(context.Context, string, string, map[string]any) (any, error) {
		return nil, fmt.Errorf("upstream refused: Invalid API key: %s", upstreamKey)
	}

	r := req(t, app, http.MethodPost, "/v1/guide/steps/incorporate/do", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("do want 200, got %d (%s)", r.Code, r.Body)
	}

	var resp struct {
		Error  string  `json:"error"`
		Events []event `json:"events"`
	}
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode do: %v (%s)", err, r.Body)
	}

	// Non-vacuity: the refusal must actually have travelled. Without this, deleting
	// the invoke client's error would leave the key absent for the wrong reason and
	// the assertion below would pass while proving nothing.
	var relayed string
	for _, e := range resp.Events {
		if e.Error != "" {
			relayed = e.Error
		}
	}
	if relayed == "" {
		t.Fatalf("no event carried the upstream refusal, so this test proves nothing: %s", r.Body)
	}
	if !strings.Contains(relayed, "Invalid API key") {
		t.Fatalf("the refusal reached the caller stripped of its own text (%q); the scrub must remove the credential, not the message", relayed)
	}

	// The property: the credential is nowhere in what the customer receives.
	if bytes.Contains(r.Body, []byte(upstreamKey)) {
		t.Fatalf("an upstream credential reached the caller in a 200: %s", r.Body)
	}
	// And the shape a scrub leaves behind is present, so the value was replaced
	// rather than the whole message dropped.
	if !strings.Contains(relayed, "[REDACTED-TOKEN]") {
		t.Fatalf("the credential was neither relayed nor redacted, which means the scrub did not run: %q", relayed)
	}
}
