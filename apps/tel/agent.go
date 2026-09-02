package tel

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Assistants answer calls, and they are OURS.
//
// A call handed to an agent is a conversation with a Hanzo assistant running on
// Hanzo inference — the same models, prompts and tools every other surface uses.
// The carrier moves the audio; it does not decide what is said.
//
// This talks to the platform's own AI endpoint rather than reaching into a model
// package directly, so an assistant improved for chat is improved for calls in
// the same deploy, and there is one place that decides which model answers.
type Assistants interface {
	// Reply produces the assistant's next turn given what the caller said.
	Reply(ctx context.Context, agent string, org string, said string) (string, error)
}

// aiEndpoint is the platform AI surface, reached over the loopback the rest of the
// fleet already uses. Base and key are configuration; on a cluster the key comes
// from KMS like every other credential.
type aiEndpoint struct {
	base string
	key  string
	http *http.Client
}

func assistantsFromEnv() Assistants {
	base := strings.TrimRight(environ.Or("HANZO_AI_BASE", ""), "/")
	if base == "" {
		// The platform's own address. Same host the console and every SDK use, so a
		// deployment that sets nothing still reaches our stack rather than none.
		base = "https://api.hanzo.ai"
	}
	key := environ.Or("HANZO_AI_KEY", "")
	if key == "" {
		return nil
	}
	return &aiEndpoint{base: base, key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

// Reply asks the assistant for its next turn.
//
// The model is NOT named here. Which model an assistant runs on is the catalog's
// decision, resolved behind this endpoint — naming one in a telecom package is how
// a model change becomes a change in six unrelated repositories.
func (a *aiEndpoint) Reply(ctx context.Context, agent, org, said string) (string, error) {
	req := map[string]any{
		"assistant": agent,
		"messages":  []map[string]string{{"role": "user", "content": said}},
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/v1/assistants/respond", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", "Bearer "+a.key)
	r.Header.Set("Content-Type", "application/json")
	// The org travels as a header only because this is a server-to-server hop
	// INSIDE the platform, after the caller's own org was validated from their
	// bearer. It is never read from a client request — see the isolation note in
	// tel.go.
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}

	res, err := a.http.Do(r)
	if err != nil {
		return "", fmt.Errorf("assistant unreachable: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("assistant %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Content string `json:"content"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if body.Content != "" {
		return body.Content, nil
	}
	return body.Message.Content, nil
}
