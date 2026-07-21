package cli

// gpu_engine_test.go — the `--serve-engine` capability: probing a local hanzo-engine
// and assembling the fleet advertisement + provider registration. Uses an httptest
// stub for the engine so no model (or GPU) is needed.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubEngine stands in for hanzo-engine's OpenAI-shaped GET /v1/models.
func stubEngine(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		data := make([]map[string]any, 0, len(models))
		for _, m := range models {
			data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "local"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	return httptest.NewServer(mux)
}

func TestProbeEngine(t *testing.T) {
	srv := stubEngine(t, "default", "zen-omni-30b")
	defer srv.Close()

	got, err := probeEngine(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("probeEngine: %v", err)
	}
	if len(got) != 2 || got[0] != "default" || got[1] != "zen-omni-30b" {
		t.Fatalf("models = %v, want [default zen-omni-30b]", got)
	}
}

func TestProbeEngineDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "loading", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := probeEngine(context.Background(), srv.URL); err == nil {
		t.Fatal("probeEngine: want error for a 503 engine, got nil")
	}
}

func TestRefreshEngineAdvertises(t *testing.T) {
	srv := stubEngine(t, "default")
	defer srv.Close()

	w := &worker{
		identity:     "gb10",
		hostname:     "gb10",
		jobsNS:       "gpu-jobs",
		serveEngine:  true,
		engineURL:    srv.URL,
		engineAdvURL: "http://node.example:1234",
	}

	if changed := w.refreshEngine(context.Background()); !changed {
		t.Fatal("first refreshEngine should report a change (nil -> ready)")
	}
	if w.engine == nil || w.engine.Status != "ready" {
		t.Fatalf("engine = %+v, want status ready", w.engine)
	}
	if w.engine.URL != "http://node.example:1234" {
		t.Fatalf("advertised URL = %q, want the endpoint, not the local probe URL", w.engine.URL)
	}
	if !contains(w.engine.APIs, "openai") || !contains(w.engine.APIs, "anthropic") {
		t.Fatalf("APIs = %v, want both openai and anthropic (hanzo-engine serves both)", w.engine.APIs)
	}
	if len(w.engine.Models) != 1 || w.engine.Models[0] != "default" {
		t.Fatalf("models = %v, want [default]", w.engine.Models)
	}
	// A second probe with an unchanged engine is a no-op (no needless re-register).
	if changed := w.refreshEngine(context.Background()); changed {
		t.Fatal("second refreshEngine with an unchanged engine should report no change")
	}
}

func TestRefreshEngineUnreachable(t *testing.T) {
	w := &worker{
		identity:     "gb10",
		serveEngine:  true,
		engineURL:    "http://127.0.0.1:0", // nothing listening
		engineAdvURL: "http://127.0.0.1:0",
	}
	w.refreshEngine(context.Background())
	if w.engine == nil || w.engine.Status != "unreachable" {
		t.Fatalf("engine = %+v, want status unreachable", w.engine)
	}
}

func TestBuildRegistrationCarriesEngine(t *testing.T) {
	srv := stubEngine(t, "default")
	defer srv.Close()

	w := &worker{
		identity:     "gb10",
		hostname:     "gb10",
		jobsNS:       "gpu-jobs",
		gpus:         []gpuInfo{{Name: "NVIDIA GB10", MemoryTotal: "122880 MiB"}},
		serveEngine:  true,
		studioReady:  true,
		engineURL:    srv.URL,
		engineAdvURL: "http://node.example:1234",
	}
	w.refreshEngine(context.Background())

	reg := w.buildRegistration()
	if !contains(reg.Capabilities, studioCap) || !contains(reg.Capabilities, engineCap) {
		t.Fatalf("capabilities = %v, want both %q and %q", reg.Capabilities, studioCap, engineCap)
	}
	if reg.Engine == nil || reg.Engine.URL != "http://node.example:1234" {
		t.Fatalf("registration engine = %+v, want the advertised endpoint", reg.Engine)
	}

	// The presence record's Input must carry the endpoint so GET /v1/fleet/workers
	// (which decodes this exact JSON) can advertise it.
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}
	for _, want := range []string{`"engine.serve"`, `"http://node.example:1234"`, `"openai"`, `"anthropic"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("registration JSON missing %s:\n%s", want, raw)
		}
	}
}

func TestCapabilitiesWithoutEngine(t *testing.T) {
	w := &worker{serveEngine: false, studioReady: true}
	caps := w.capabilities()
	if len(caps) != 1 || caps[0] != studioCap {
		t.Fatalf("capabilities = %v, want just [%q] when not serving an engine", caps, studioCap)
	}
}

func TestProviderBodyIsOpenAICompatible(t *testing.T) {
	w := &worker{
		identity: "gb10",
		engine:   &engineAdvertisement{URL: "http://node.example:1234", Status: "ready", Models: []string{"zen-omni-30b"}},
	}
	body := w.providerBody()
	if body["type"] != "Local" {
		t.Fatalf("type = %v, want Local (OpenAI-compatible; gateway auto-appends /v1)", body["type"])
	}
	if body["providerUrl"] != "http://node.example:1234" {
		t.Fatalf("providerUrl = %v", body["providerUrl"])
	}
	if body["name"] != "gpu-gb10" {
		t.Fatalf("name = %v, want gpu-gb10", body["name"])
	}
	if body["subType"] != "zen-omni-30b" {
		t.Fatalf("subType = %v, want the served model", body["subType"])
	}
}

// TestConnectServeEngineRoundTrip closes the loop end-to-end on this box, no model
// and no production: a stub hanzo-engine (GET /v1/models) + a stub cloud that stores
// the presence Input and serves it back on GET /v1/fleet/workers. It exercises the
// real chain — probe → build registration → POST the fleet activity → GET
// /v1/fleet/workers → the engine endpoint is advertised.
func TestConnectServeEngineRoundTrip(t *testing.T) {
	engine := stubEngine(t, "default", "zen-omni-30b")
	defer engine.Close()

	var storedInput registration // what the CLI POSTed as the presence record's Input
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/fleet/activities"):
			var body struct {
				Input registration `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			storedInput = body.Input
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fleet/workers":
			// Fold the stored registration into the fleet worker shape, exactly as
			// clients/visor/fleet.go byoWorkers does.
			_ = json.NewEncoder(w).Encode(map[string]any{"workers": []fleetWorker{{
				ID: "gb10", Hostname: storedInput.Hostname, Provider: "byo", Status: "online",
				GPUs: storedInput.GPUs, Capabilities: storedInput.Capabilities, Engine: storedInput.Engine,
			}}})
		default: // namespace ensure + anything else
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer cloud.Close()

	t.Setenv("HANZO_TOKEN", "test-token") // ensureToken honors this; no `hanzo login` needed

	w := &worker{
		env:          &Env{CloudURL: cloud.URL},
		http:         &http.Client{Timeout: 5 * time.Second},
		baseURL:      cloud.URL,
		identity:     "gb10",
		hostname:     "gb10",
		jobsNS:       "gpu-jobs",
		gpus:         []gpuInfo{{Name: "NVIDIA GB10", MemoryTotal: "122880 MiB"}},
		handlers:     map[string]jobHandler{},
		serveEngine:  true,
		engineURL:    engine.URL,
		engineAdvURL: "http://node.example:1234",
	}
	ctx := context.Background()
	w.refreshEngine(ctx)
	if err := w.register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	var resp struct {
		Workers []fleetWorker `json:"workers"`
	}
	if _, err := w.call(ctx, http.MethodGet, "/v1/fleet/workers", nil, &resp); err != nil {
		t.Fatalf("GET /v1/fleet/workers: %v", err)
	}
	if len(resp.Workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(resp.Workers))
	}
	fw := resp.Workers[0]
	if !contains(fw.Capabilities, engineCap) {
		t.Fatalf("fleet worker capabilities = %v, want engine.serve", fw.Capabilities)
	}
	if fw.Engine == nil || fw.Engine.URL != "http://node.example:1234" || fw.Engine.Status != "ready" {
		t.Fatalf("fleet worker engine = %+v, want the advertised ready endpoint", fw.Engine)
	}
	if len(fw.Engine.Models) != 2 {
		t.Fatalf("fleet worker engine models = %v, want 2", fw.Engine.Models)
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
