package cloud

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// pricedPlane serves the model ops as app "ai" beside the fake commerce, each
// answer carrying the price the model API told for it, and records what crossed.
func pricedPlane(t *testing.T, billed int64, seen chan<- *client.ChatIn) {
	t.Helper()
	answer := func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seen <- in
		body := `{"id":"c1","object":"chat.completion","model":"enso-ultra",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1000000,"completion_tokens":0,"total_tokens":1000000}}`
		if in.ContentType != "" {
			body = `{"text":"hello there"}`
		}
		return &client.Chat{Status: http.StatusOK, Body: []byte(body), Billed: &billed}, nil
	}
	app := zip.New(zip.Config{AppName: "ai"})
	app.Post("/ai/chat", answer, zip.WithOperationID(client.AIChat))
	app.Post("/ai/transcribe", answer, zip.WithOperationID(client.AITranscribe))
	planetest.Bind(t, "ai", app)
}
