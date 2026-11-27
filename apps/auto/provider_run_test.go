package auto

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/script"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// mountScript wires the runtime as auto's Mount does: the provider registry is the
// predicate that tells a known connector from no connector.
func mountScript(t *testing.T) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := script.Use(app, cloud.Deps{}, provider.Registered); err != nil {
		t.Fatalf("script.Use: %v", err)
	}
}

// The plane run answers a connector the provider app knows but this runtime has no
// program for with 422 and the truth, and a nonsense id with the 404 it always had.
func TestPlaneProviderRun_KnownConnectorWithNoProgramIsNotUnknown(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "acme/worker"})
	mountScript(t)

	_, err := planeProviderRun(ctx, &client.ProviderRunIn{Provider: "shopify", Action: "x"})
	if err == nil || !strings.Contains(err.Error(), "shopify is a known connector but has no action that runs here yet") || strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("known connector: want the known-connector refusal, got %v", err)
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnprocessableEntity {
		t.Fatalf("known connector: want 422, got %v", err)
	}
	_, err = planeProviderRun(ctx, &client.ProviderRunIn{Provider: "nope", Action: "x"})
	if err == nil || !strings.Contains(err.Error(), `unknown provider "nope"`) {
		t.Fatalf("nonsense id: want unknown provider, got %v", err)
	}
}

// A connector that has a program still runs: the guard refuses neither it nor its
// failing action (an answer with ok false, never a transport error).
func TestPlaneProviderRun_RunnableConnectorStillRuns(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "acme/worker"})
	mountScript(t)

	out, err := planeProviderRun(ctx, &client.ProviderRunIn{Provider: "notion", Action: "no_such_action"})
	if err != nil {
		t.Fatalf("runnable connector: want an answer, got error %v", err)
	}
	if out == nil || out.Ok || !strings.Contains(out.Error, "no_such_action") {
		t.Fatalf("runnable connector: want ok:false naming the action, got %+v", out)
	}
}
