package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/forge"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// oneSecretKMS answers one ref with one value and nothing else.
type oneSecretKMS struct {
	ref, value string
}

func (k oneSecretKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if ref == k.ref {
		return []byte(k.value), nil
	}
	return nil, kms.ErrSecretNotFound
}
func (k oneSecretKMS) PutSecret(context.Context, string, []byte) error { return nil }
func (k oneSecretKMS) DeleteSecret(context.Context, string) error      { return nil }
func (k oneSecretKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, kms.ErrSecretNotFound
}

func forgeApp(t *testing.T, secret string) *zip.App {
	t.Helper()
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{kms: oneSecretKMS{ref: forge.WebhookRef, value: secret}}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Post(forgeWebhookPath, cloud.Terminal(cloud.Handle(s, forgeWebhook)))
	return app
}

func forgePost(t *testing.T, app *zip.App, secret string, body []byte) *http.Response {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, forgeWebhookPath, bytes.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		rq.Header.Set("X-Git-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("forge webhook: %v", err)
	}
	return resp
}

func jobBody(t *testing.T, action, owner string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action":       action,
		"workflow_job": map[string]any{"id": 7, "run_id": 3, "name": "build", "labels": []string{"hanzo-build-linux-amd64"}},
		"repository":   map[string]any{"name": "gui", "owner": map[string]any{"login": owner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An unsigned delivery, or one signed with another secret, is refused before
// the body is decoded.
func TestForgeWebhookRefusesABadSignature(t *testing.T) {
	app := forgeApp(t, "s3cret")
	for _, sig := range []string{"", "other"} {
		resp := forgePost(t, app, sig, jobBody(t, "queued", "hanzoai"))
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("signature %q: got %d, want 401", sig, resp.StatusCode)
		}
	}
}

// Only a queued job becomes a runner; every other action is acknowledged and
// ignored so the forge does not mark the delivery failed.
func TestForgeWebhookIgnoresWhatIsNotQueued(t *testing.T) {
	launched := 0
	cloud.RegisterRunner(func(context.Context, cloud.JobEvent) (string, error) { launched++; return "x", nil })
	t.Cleanup(func() { cloud.RegisterRunner(nil) })
	app := forgeApp(t, "s3cret")
	for _, action := range []string{"in_progress", "completed", "waiting"} {
		resp := forgePost(t, app, "s3cret", jobBody(t, action, "hanzoai"))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", action, resp.StatusCode)
		}
	}
	resp := forgePost(t, app, "s3cret", []byte(`{"ref":"refs/heads/main","repository":{"name":"gui"}}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push: got %d, want 200", resp.StatusCode)
	}
	if launched != 0 {
		t.Fatalf("launched %d runners for deliveries that queued nothing", launched)
	}
}

// A namespace the forge table does not map answers 200 and launches nothing:
// whose compute a job spends is decided by the closed table, never by a name.
func TestForgeWebhookIgnoresAnUnmappedNamespace(t *testing.T) {
	launched := 0
	cloud.RegisterRunner(func(context.Context, cloud.JobEvent) (string, error) { launched++; return "x", nil })
	t.Cleanup(func() { cloud.RegisterRunner(nil) })
	resp := forgePost(t, forgeApp(t, "s3cret"), "s3cret", jobBody(t, "queued", "nobody-here"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || launched != 0 {
		t.Fatalf("got %d with %d launches, want 200 and none", resp.StatusCode, launched)
	}
}
