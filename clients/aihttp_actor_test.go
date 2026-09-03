package clients

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// TestActorRidesTheWireAsUser is the last hop, and the one nothing upstream can
// compensate for: an actor the client holds and does not SEND is an actor the
// gateway never sees, so the usage row still names the credential.
//
// It rides as `user`, OpenAI's own field for the end user a completion is made
// for, so this invents no vocabulary and the gateway already parses it. Both
// deliveries are checked because streaming must not be a way to buy inference
// nobody is named for.
func TestActorRidesTheWireAsUser(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "m",
			"choices": []map[string]any{{
				"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	ai := AIHTTPAt(srv.URL, "sk-test", FixedModel("m"))
	if _, err := ai.ChatCompletion(context.Background(),
		&types.ChatRequest{Prompt: "hi", Actor: "acme/alice"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	var got struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.User != "acme/alice" {
		t.Fatalf("wire user = %q, want %q — the person was held in the client and "+
			"never sent, so the gateway still records the credential", got.User, "acme/alice")
	}

	// A request with nobody behind it sends no `user` at all: omitempty, so a
	// system call is the same bytes on the wire it always was.
	if _, err := ai.ChatCompletion(context.Background(), &types.ChatRequest{Prompt: "hi"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["user"]; present {
		t.Fatalf("an unattributed call must send no user key, got %v", raw["user"])
	}
}

// TestActorRidesTheStreamToo: same field, same person, on the streaming
// delivery. A tool-using agent run streams, so leaving this out would leave
// exactly the expensive path anonymous.
func TestActorRidesTheStreamToo(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,"+
			"\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	sc, ok := AIHTTPAt(srv.URL, "sk-test", FixedModel("m")).(types.StreamCompleter)
	if !ok {
		t.Fatal("the http transport must implement StreamCompleter")
	}
	if _, err := sc.ChatStream(context.Background(),
		&types.ChatRequest{Prompt: "hi", Actor: "acme/alice"},
		func(string) error { return nil }); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var got struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.User != "acme/alice" {
		t.Fatalf("stream wire user = %q, want %q", got.User, "acme/alice")
	}
}
