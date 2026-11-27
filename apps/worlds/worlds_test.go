package worlds

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

func caller(org, project string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, Project: project, User: "validated-user"})
}
func TestPrivateBoundary(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	var tenants []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer backend-only" {
			t.Error("backend credential missing")
		}
		tenants = append(tenants, r.Header.Get("X-Org-Id"))
		io.WriteString(w, `{"belief":{"mean":[0,0,0,0]},"capture":{"features":[1],"base_sha256":"private"}}`)
	}))
	defer srv.Close()
	s := &service{base: srv.URL, token: "backend-only", orgs: map[string]bool{"hanzo": true}, http: srv.Client()}
	for _, project := range []string{"one", "two"} {
		out, err := s.call(caller("hanzo", project), "POST", "/v1/worlds/world_1/events", &Event{EntityID: "sim-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := (*out)["capture"]; ok {
			t.Fatal("private representation leaked")
		}
	}
	if len(tenants) != 2 || tenants[0] == tenants[1] || !strings.HasPrefix(tenants[0], "tenant_") {
		t.Fatalf("project isolation failed: %v", tenants)
	}
	if _, err := s.call(context.Background(), "GET", "/v1/worlds/models", nil); err == nil {
		t.Fatal("anonymous request admitted")
	}
	if _, err := s.call(caller("stranger", "one"), "GET", "/v1/worlds/models", nil); err == nil {
		t.Fatal("unapproved org admitted")
	}
	if len(tenants) != 2 {
		t.Fatal("refused request reached upstream")
	}
}
func TestSimulationGateAndErrors(t *testing.T) {
	s := &service{}
	for _, in := range []Create{{Model: alpha, Domain: "drone"}, {Model: "sekai-1-smoke", Domain: "simulation"}} {
		if _, err := s.create(context.Background(), &in); err == nil {
			t.Fatal("unsupported model/domain admitted")
		}
	}
	if _, err := path("../weights"); err == nil {
		t.Fatal("unsafe path admitted")
	}
	t.Setenv("CLOUD_BRAND", "hanzo")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "/private/checkpoint.pt")
	}))
	defer srv.Close()
	s = &service{base: srv.URL, token: "key", orgs: map[string]bool{"hanzo": true}, http: srv.Client()}
	_, err := s.call(caller("hanzo", "p"), "GET", "/v1/worlds/models", nil)
	if err == nil || strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("internal error leaked: %v", err)
	}
}
func TestMountedPathsAndBody(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("SEKAI_ALPHA_ORGS", "hanzo")
	t.Setenv("SEKAI_UPSTREAM_KEY", "key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/worlds/world_123/events" {
			t.Errorf("bad path: %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["entity_id"] != "sim-1" {
			t.Errorf("bad body: %v", body)
		}
		io.WriteString(w, `{"belief":{"mean":[0,0,0,0]}}`)
	}))
	defer srv.Close()
	t.Setenv("SEKAI_UPSTREAM_URL", srv.URL)
	app := zip.New(zip.Config{})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/worlds/world_123/events", strings.NewReader(`{"entity_id":"sim-1","event_id":"e1","modality":"telemetry","features":[0,0,0,0],"timestamp":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "validated-user")
	req.Header.Set("X-Project-Id", "p")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
}

func TestWorldInferenceBoundaryAndIsolation(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("SEKAI_ALPHA_ORGS", "hanzo,other")
	t.Setenv("WORLDS_INFERENCE_UPSTREAM_KEY", "engine-secret")

	// Mock native hanzo-engine worlds registry enforcing tenant ownership.
	var seenOrg string
	var ownerOrg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/worlds/world_sim_1/inference" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer engine-secret" {
			w.WriteHeader(401)
			return
		}
		org := r.Header.Get("X-Org-Id")
		seenOrg = org
		if ownerOrg == "" {
			ownerOrg = org
		}
		if org != ownerOrg {
			// Cross-tenant access fails closed with 404.
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"world not found"}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		for _, forbidden := range []string{"admissible", "allowed", "habits", "preferences", "precision", "tenant_id", "execution_authorized"} {
			if _, ok := body[forbidden]; ok {
				t.Errorf("forbidden override field leaked to upstream: %s", forbidden)
			}
		}
		io.WriteString(w, `{
			"world_id":"world_sim_1",
			"model_version":"two-state-sim-v1",
			"inference":{"posterior":{"belief":[1,0],"free_energy":0.693},"policies":{"probabilities":[0,1],"terms":[],"selected":1}},
			"recommendation":{"policy_index":1,"actions":[1],"probability":1.0,"expected_free_energy":0.1,"advisory_only":true},
			"execution_authorized":false,
			"capture":{"private":"strip-me"}
		}`)
	}))
	defer srv.Close()
	t.Setenv("WORLDS_INFERENCE_UPSTREAM_URL", srv.URL)

	app := zip.New(zip.Config{})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatal(err)
	}

	post := func(worldID, org, project, body string) (*http.Response, []byte) {
		req := httptest.NewRequest("POST", "/v1/worlds/"+worldID+"/inference", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if org != "" {
			req.Header.Set("X-Org-Id", org)
			req.Header.Set("X-User-Id", "validated-user")
			req.Header.Set("X-Project-Id", project)
		}
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, raw
	}

	// 1. Valid authenticated inference request succeeds, strips capture, keeps execution_authorized=false.
	validBody := `{"belief":[0.5,0.5],"observation":[0,0],"policies":[[0],[1]]}`
	res, raw := post("world_sim_1", "hanzo", "proj-a", validBody)
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["execution_authorized"] != false {
		t.Fatalf("execution_authorized must be false: %v", got["execution_authorized"])
	}
	if _, leaked := got["capture"]; leaked {
		t.Fatalf("private capture leaked: %v", got)
	}
	rec, ok := got["recommendation"].(map[string]any)
	if !ok || rec["advisory_only"] != true {
		t.Fatalf("recommendation advisory_only must be true: %v", got["recommendation"])
	}
	if !strings.HasPrefix(seenOrg, "tenant_") {
		t.Fatalf("expected hashed tenant header, got %q", seenOrg)
	}

	// 2. Anonymous caller -> 401 Unauthorized (validated principal required).
	res, raw = post("world_sim_1", "", "", validBody)
	if res.StatusCode != 401 {
		t.Fatalf("anonymous request must be 401, got %d: %s", res.StatusCode, raw)
	}

	// 3. Unapproved org -> 403.
	res, raw = post("world_sim_1", "stranger", "proj-a", validBody)
	if res.StatusCode != 403 {
		t.Fatalf("unapproved org must be 403, got %d: %s", res.StatusCode, raw)
	}

	// 4. Cross-tenant (approved different org OR different project) -> 404 from isolated tenant key.
	res, raw = post("world_sim_1", "other", "proj-a", validBody)
	if res.StatusCode != 404 {
		t.Fatalf("cross-org access must be 404, got %d: %s", res.StatusCode, raw)
	}
	res, raw = post("world_sim_1", "hanzo", "proj-b", validBody)
	if res.StatusCode != 404 {
		t.Fatalf("cross-project access must be 404, got %d: %s", res.StatusCode, raw)
	}

	// 5. Malformed payloads and override injection attempts -> 400.
	badPayloads := []string{
		`{"belief":[0.5,0.5],"policies":[[0]],"admissible":[true]}`,
		`{"belief":[0.5,0.5],"policies":[[0]],"habits":[1.0]}`,
		`{"belief":[0.5,0.5],"policies":[[0]],"preferences":[0.9,0.1]}`,
		`{"belief":[0.5,0.5],"policies":[[0]],"execution_authorized":true}`,
		`{"belief":[0.5,0.5],"policies":[[0]],"tenant_id":"victim"}`,
		`{"belief":[0.2,0.2],"policies":[[0]]}`,
		`{"belief":[-0.1,1.1],"policies":[[0]]}`,
		`{"belief":[],"policies":[[0]]}`,
		`{"belief":[0.5,0.5],"observation":[0],"policies":[[0]]}`,
		`{"belief":[0.5,0.5],"policies":[]}`,
		`{"belief":[0.5,0.5],"policies":[[]]}`,
		`{"belief":[0.5,0.5],"policies":[[-1]]}`,
		`not-json`,
	}
	for _, body := range badPayloads {
		res, raw = post("world_sim_1", "hanzo", "proj-a", body)
		if res.StatusCode != 400 {
			t.Fatalf("malformed body %s must be 400, got %d: %s", body, res.StatusCode, raw)
		}
	}

	// 6. Path traversal world ID -> 400.
	res, raw = post("..world", "hanzo", "proj-a", validBody)
	if res.StatusCode != 400 {
		t.Fatalf("unsafe world id must be 400, got %d: %s", res.StatusCode, raw)
	}
}

func TestWorldInferenceFailsClosedIfUpstreamClaimsExecution(t *testing.T) {
	t.Setenv("CLOUD_BRAND", "hanzo")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"execution_authorized":true,"recommendation":{"policy_index":0,"advisory_only":true}}`)
	}))
	defer srv.Close()
	s := &service{
		inferenceBase:  srv.URL,
		inferenceToken: "key",
		orgs:           map[string]bool{"hanzo": true},
		http:           srv.Client(),
	}
	_, err := s.infer(caller("hanzo", "p"), &Inference{
		ID:       "world_1",
		Belief:   []float64{0.5, 0.5},
		Policies: [][]int{{0}},
	})
	if err == nil {
		t.Fatal("upstream returning execution_authorized=true must fail closed")
	}
}
