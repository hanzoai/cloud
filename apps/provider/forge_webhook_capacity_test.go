package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// refKMS answers each ref it holds and nothing else.
type refKMS map[string]string

func (k refKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if v, ok := k[ref]; ok {
		return []byte(v), nil
	}
	return nil, secret.ErrNone
}
func (refKMS) PutSecret(context.Context, string, []byte) error { return nil }
func (refKMS) DeleteSecret(context.Context, string) error      { return nil }
func (refKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, secret.ErrNone
}

// deliverQueued sends one signed, queued job for a mapped namespace to a receiver
// whose forge mints a registration token and whose launcher answers launch.
func deliverQueued(t *testing.T, launch func(context.Context, cloud.JobEvent) (string, error)) (int, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == forge.API+"/admin/actions/runners/registration-token" {
			_, _ = w.Write([]byte(`{"token":"reg"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)
	cloud.RegisterRunner(launch)
	t.Cleanup(func() { cloud.RegisterRunner(nil) })

	s := &cloud.Service[state]{Log: luxlog.New("test"), State: state{kms: refKMS{forge.WebhookRef: "s3cret", forge.TokenRef: "machine"}}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Raw(http.MethodPost, forgeWebhookPath, cloud.Terminal(cloud.Handle(s, forgeWebhook)))
	resp := forgePost(t, app, "s3cret", jobBody(t, "queued", "hanzoai"))
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// A full cluster defers the job: 202 {queued, reason}. The forge never
// redelivers, and nothing needs it to — the job stays queued there and the
// platform's queue loop starts it when a slot frees. A 500 only put a failed
// delivery on the forge's page and "redeliver" in front of an operator.
func TestForgeWebhookDefersAJobWhenNoRunnerSlotIsFree(t *testing.T) {
	code, body := deliverQueued(t, func(context.Context, cloud.JobEvent) (string, error) {
		return "", fmt.Errorf("platform job: %w", cloud.ErrRunnerCapacity)
	})
	if code != http.StatusAccepted {
		t.Fatalf("capacity refusal = %d, want 202", code)
	}
	if body["queued"] != true || body["reason"] == "" || body["reason"] == nil {
		t.Fatalf("body %v, want queued:true and a reason", body)
	}
}

// Any other launch failure is still a server error.
func TestForgeWebhookReportsARealLaunchFailure(t *testing.T) {
	code, _ := deliverQueued(t, func(context.Context, cloud.JobEvent) (string, error) {
		return "", errors.New("apiserver unreachable")
	})
	if code != http.StatusInternalServerError {
		t.Fatalf("launch failure = %d, want 500", code)
	}
}

// A launched runner is still answered 200 with its name.
func TestForgeWebhookNamesTheRunnerItLaunched(t *testing.T) {
	code, body := deliverQueued(t, func(context.Context, cloud.JobEvent) (string, error) {
		return "runner-forge-gui-7", nil
	})
	if code != http.StatusOK || body["runner"] != "runner-forge-gui-7" {
		t.Fatalf("launched = %d %v, want 200 naming the runner", code, body)
	}
}
