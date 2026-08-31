// Copyright © 2026 Hanzo AI. MIT License.

package ml

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// GPU inference is the most expensive call this cloud makes, so it is the one that
// most has to be visible. These pin what the console reads: one span per inference,
// always ended, carrying the outcome the predictor actually gave.

// record swaps the global provider, which is what tracer() reads.
func record(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })
	return sr
}

// servePredictor stands a data plane that answers every /infer with status, and an
// InferenceService whose address points at it.
func servePredictor(t *testing.T, status int) *cloud.Service[state] {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"outputs":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	isvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1beta1",
		"kind":       "InferenceService",
		"metadata":   map[string]any{"name": "m1", "namespace": "ml-acme"},
		"status":     map[string]any{"address": map[string]any{"url": srv.URL}},
	}}
	return &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Env: "mainnet"}, "ml"),
		State: state{hc: &http.Client{}, dyn: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), isvc)},
	}
}

func infer(t *testing.T, s *cloud.Service[state]) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	app.Use(cloud.DenyEnvelope())
	app.Post("/v1/ml/models/:name/predict", cloud.Handle(s, predict))
	req, _ := http.NewRequest("POST", "/v1/ml/models/m1/predict", strings.NewReader(`{"inputs":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

func only(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, sp := range sr.Ended() {
		if sp.Name() == "ml.infer" {
			found = append(found, sp)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ended ml.infer spans = %d; want exactly 1 — an inference is one act and the span must end on every path", len(found))
	}
	return found[0]
}

func attr(sp sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range sp.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

func TestAnInferenceIsOneEndedSpan(t *testing.T) {
	sr := record(t)
	if resp := infer(t, servePredictor(t, http.StatusOK)); resp.StatusCode != http.StatusOK {
		t.Fatalf("predict = %d; want 200", resp.StatusCode)
	}
	sp := only(t, sr)

	if got := attr(sp, "ml.model"); got != "m1" {
		t.Errorf("ml.model = %q; want m1", got)
	}
	if got := attr(sp, "ml.namespace"); got != "ml-acme" {
		t.Errorf("ml.namespace = %q; want ml-acme", got)
	}
	if got := attr(sp, "http.response.status_code"); got != "200" {
		t.Errorf("status attribute = %q; want 200", got)
	}
	if sp.Status().Code == codes.Error {
		t.Error("a served inference was recorded as an error")
	}
}

// A model that refuses is a failed inference, not a success carrying an error body.
// The proxy returns the predictor's status verbatim either way, so the span is the
// only place that outcome is legible.
func TestARefusedInferenceIsAFailedSpan(t *testing.T) {
	sr := record(t)
	resp := infer(t, servePredictor(t, http.StatusBadRequest))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("predict = %d; want the predictor's own 400", resp.StatusCode)
	}
	sp := only(t, sr)

	if sp.Status().Code != codes.Error {
		t.Errorf("span status = %v; want Error — a refused inference must not read as served", sp.Status().Code)
	}
	if got := attr(sp, "http.response.status_code"); got != "400" {
		t.Errorf("status attribute = %q; want 400", got)
	}
}

// The data plane being unreachable ends the span too, with the reason on it.
func TestAnUnreachableDataPlaneEndsTheSpan(t *testing.T) {
	sr := record(t)
	s := servePredictor(t, http.StatusOK)
	// Point the address at a port nothing listens on.
	isvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1beta1",
		"kind":       "InferenceService",
		"metadata":   map[string]any{"name": "m1", "namespace": "ml-acme"},
		"status":     map[string]any{"address": map[string]any{"url": "http://127.0.0.1:1"}},
	}}
	s.State.dyn = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), isvc)

	if resp := infer(t, s); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("predict = %d; want 502", resp.StatusCode)
	}
	sp := only(t, sr)
	if sp.Status().Code != codes.Error {
		t.Errorf("span status = %v; want Error", sp.Status().Code)
	}
	if len(sp.Events()) == 0 {
		t.Error("no exception recorded for an unreachable data plane")
	}
}
