package cloud

// Credential contract for AI inference. CHAT COMPLETIONS (deps.AI, a WRITE endpoint)
// must NEVER ride a read-only publishable (pk-) key — the gateway 403s a pk- key on
// any write endpoint ("Publishable keys can only access read-only endpoints … use a
// secret key (sk-)"), which was the intermittent bot-reply failure. Completions,
// embeddings and reranks all take the deployment's own identity wherever it has one
// and cross the plane for the org they are for; a static key serves only where there
// is no identity. These tests pin it on the wire.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
)

// gatewayStub is a fake OpenAI-compatible gateway + IAM token endpoint. It records
// the Authorization header seen at /chat/completions and /embeddings so a test can
// assert exactly which credential each path presented.
type gatewayStub struct {
	srv       *httptest.Server
	minted    string // the M2M access token the token endpoint issues
	chatAuth  string // Authorization seen at /chat/completions ("" ⇒ never called)
	embedAuth string // Authorization seen at /embeddings      ("" ⇒ never called)
}

func newGatewayStub(t *testing.T) *gatewayStub {
	t.Helper()
	g := &gatewayStub{minted: "m2m-access-token-xyz"}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + g.minted + `","token_type":"Bearer","expires_in":3600}`))
		case "/chat/completions":
			g.chatAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-x", "object": "chat.completion", "model": "deepseek-v4-flash",
				"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}},
			})
		case "/embeddings":
			g.embedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]any{{"index": 0, "embedding": []float32{0.1, 0.2}}},
			})
		default:
			t.Errorf("unexpected gateway path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// prodLikeConfig mirrors the live Hanzo deployment: a read-only publishable (pk-)
// key in CLOUD_AI_API_KEY plus the binary's IAM M2M identity, gateway pointed at the
// stub. The IAM token URL is pinned to the stub so M2M mints deterministically.
func prodLikeConfig(t *testing.T, g *gatewayStub, pkKey string) *Config {
	t.Helper()
	t.Setenv("CLOUD_AI_IAM_TOKEN_URL", g.srv.URL+"/v1/iam/oauth/token")
	return &Config{
		AIBaseURL:          g.srv.URL,
		AIAPIKey:           pkKey,
		AIAuthClientID:     "hanzo-cloud",
		AIAuthClientSecret: "s3cr3t",
	}
}

// servePlaneAI serves the completion, embeddings and rerank ops as app "ai" on this
// process's plane, with answer standing in for the model API, or points the plane
// at an empty run dir when answer is nil — a fleet that runs no ai.
func servePlaneAI(t *testing.T, answer func(ctx context.Context, in *client.ChatIn) (*client.Chat, error)) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", kit.Sockets(t))
	ResetPlane()
	t.Cleanup(ResetPlane)
	if answer == nil {
		return
	}
	Plane().Post("/ai/chat", answer, zip.WithOperationID(client.AIChat))
	Plane().Post("/ai/embed", answer, zip.WithOperationID(client.AIEmbed))
	Plane().Post("/ai/rerank", answer, zip.WithOperationID(client.AIRerank))
	stop, err := ServePlane("ai", luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}
