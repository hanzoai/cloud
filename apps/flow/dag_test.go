package flow

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

func doWithAuth(t *testing.T, app *zip.App, method, path, user, org, bearer, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test(%s %s): %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func createUnifiedWorkflow(t *testing.T, app *zip.App, user, org, bearer, name string, graph map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"name":        name,
		"description": "unified dag test",
		"data":        graph,
	})
	if err != nil {
		t.Fatalf("marshal create: %v", err)
	}
	st, body := doWithAuth(t, app, http.MethodPost, "/v1/flow/workflows", user, org, bearer, string(payload))
	if st != http.StatusOK {
		t.Fatalf("create workflow = %d body=%s", st, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.ID == "" {
		t.Fatalf("unparseable create response: %s", body)
	}
	return out.ID
}

// TestUnifiedDAGTopologicalExecution proves end-to-end pure-Go execution of a
// hanzo.flow/v1 DAG: trigger -> set -> branch -> (true: agent -> http -> output,
// false: skipped_agent -> output), verifying:
//   - upstream Python /v1/run/{id} is NOT invoked for hanzo.flow/v1 graphs,
//   - AI gateway receives caller bearer + validated X-Org-Id + default enso-auto model,
//   - external HTTP action receives interpolated payload and NEVER receives caller bearer,
//   - untaken branch node is marked skipped and does not invoke AI,
//   - diamond merge output node executes and aggregates summary + outputs,
//   - GET /v1/flow/runs?workflow={id} surfaces persisted native Go run traces alongside vertex_builds.
func TestUnifiedDAGTopologicalExecution(t *testing.T) {
	app, f := harness(t)

	var aiMu sync.Mutex
	var aiCalls []struct {
		auth  string
		org   string
		model string
		body  map[string]any
	}
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		model, _ := req["model"].(string)
		aiMu.Lock()
		aiCalls = append(aiCalls, struct {
			auth  string
			org   string
			model string
			body  map[string]any
		}{
			auth:  r.Header.Get("Authorization"),
			org:   r.Header.Get("X-Org-Id"),
			model: model,
			body:  req,
		})
		aiMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-test-1",
			"model": model,
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": "VIP solar quote prepared for Elena",
				},
			}},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
		})
	}))
	t.Cleanup(aiSrv.Close)
	t.Setenv("HANZO_AI_URL", aiSrv.URL)

	var hookMu sync.Mutex
	var hookAuth string
	var hookCustomHdr string
	var hookPayload map[string]any
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hookMu.Lock()
		hookAuth = r.Header.Get("Authorization")
		hookCustomHdr = r.Header.Get("X-Webhook-Source")
		_ = json.Unmarshal(raw, &hookPayload)
		hookMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true,"ticket":"SOL-42"}`))
	}))
	t.Cleanup(hookSrv.Close)

	// Swap outbound client ONLY for this loopback happy-path test; SSRF tests below
	// run against the real untouched hanzoai/outbound client.
	prevClient := dagOutboundClient
	dagOutboundClient = hookSrv.Client()
	t.Cleanup(func() { dagOutboundClient = prevClient })

	graph := map[string]any{
		"schema": "hanzo.flow/v1",
		"nodes": []any{
			map[string]any{
				"id":   "n_trigger",
				"type": "trigger",
				"data": map[string]any{
					"kind":  "trigger",
					"label": "Inbound Lead",
					"event": "lead.created",
				},
			},
			map[string]any{
				"id":   "n_set",
				"type": "set",
				"data": map[string]any{
					"kind": "set",
					"variables": map[string]any{
						"tier":    "vip",
						"summary": "Lead {{payload.customer}} score={{payload.score}}",
					},
				},
			},
			map[string]any{
				"id":   "n_branch",
				"type": "branch",
				"data": map[string]any{
					"kind":       "branch",
					"expression": "{{payload.score}} >= 80 && tier == 'vip'",
				},
			},
			map[string]any{
				"id":   "n_agent_vip",
				"type": "agent",
				"data": map[string]any{
					"kind":   "agent",
					"label":  "VIP Agent",
					"system": "You qualify solar leads.",
					"prompt": "Qualify: {{vars.summary}}",
					// model omitted deliberately to verify default "enso-auto".
				},
			},
			map[string]any{
				"id":   "n_agent_std",
				"type": "agent",
				"data": map[string]any{
					"kind":   "agent",
					"label":  "Standard Agent",
					"model":  "zen-4",
					"prompt": "Should be skipped on VIP path",
				},
			},
			map[string]any{
				"id":   "n_http",
				"type": "http",
				"data": map[string]any{
					"kind":   "http",
					"method": "POST",
					"url":    hookSrv.URL + "/crm/hook",
					"headers": map[string]any{
						"X-Webhook-Source": "hanzo-flow",
					},
					"body": map[string]any{
						"customer": "{{payload.customer}}",
						"quote":    "{{n_agent_vip.text}}",
						"tier":     "{{vars.tier}}",
					},
				},
			},
			map[string]any{
				"id":   "n_output",
				"type": "output",
				"data": map[string]any{
					"kind":     "output",
					"template": "{{n_agent_vip.text}} [ticket={{n_http.json.ticket}}]",
				},
			},
		},
		"edges": []any{
			map[string]any{"source": "n_trigger", "target": "n_set"},
			map[string]any{"source": "n_set", "target": "n_branch"},
			map[string]any{"source": "n_branch", "target": "n_agent_vip", "sourceHandle": "true"},
			map[string]any{"source": "n_branch", "target": "n_agent_std", "sourceHandle": "false"},
			map[string]any{"source": "n_agent_vip", "target": "n_http"},
			map[string]any{"source": "n_http", "target": "n_output"},
			map[string]any{"source": "n_agent_std", "target": "n_output"},
		},
	}

	jwt := "hdr.alice-solar.sig"
	wfID := createUnifiedWorkflow(t, app, "u-alice", "acme", jwt, "solar-lead-router", graph)

	runReq, _ := json.Marshal(map[string]any{
		"workflow": wfID,
		"session":  "sess-solar-1",
		"input":    `{"customer":"Elena","score":92}`,
	})
	st, body := doWithAuth(t, app, http.MethodPost, "/v1/flow/runs", "u-alice", "acme", jwt, string(runReq))
	if st != http.StatusOK {
		t.Fatalf("POST /v1/flow/runs = %d body=%s", st, body)
	}

	var res dagRunResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("unmarshal run response: %v body=%s", err, body)
	}
	if res.Engine != "hanzo-flow-go" {
		t.Fatalf("engine = %q, want hanzo-flow-go", res.Engine)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if res.SessionID != "sess-solar-1" {
		t.Fatalf("session_id = %q, want sess-solar-1", res.SessionID)
	}
	if res.Summary != "VIP solar quote prepared for Elena [ticket=SOL-42]" {
		t.Fatalf("summary = %q, want VIP solar quote prepared for Elena [ticket=SOL-42]", res.Summary)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].NodeID != "n_output" {
		t.Fatalf("unexpected outputs: %+v", res.Outputs)
	}

	// Verify step statuses: n_agent_std skipped, all others completed.
	stepStatus := map[string]string{}
	for _, s := range res.Steps {
		stepStatus[s.ID] = s.Status
	}
	if stepStatus["n_agent_std"] != "skipped" {
		t.Fatalf("untaken branch n_agent_std status = %q, want skipped", stepStatus["n_agent_std"])
	}
	for _, id := range []string{"n_trigger", "n_set", "n_branch", "n_agent_vip", "n_http", "n_output"} {
		if stepStatus[id] != "completed" {
			t.Fatalf("step %s status = %q, want completed", id, stepStatus[id])
		}
	}

	// Verify AI gateway received exactly 1 call (skipped branch never called AI)
	// with caller's bearer, validated X-Org-Id, and default model enso-auto.
	aiMu.Lock()
	defer aiMu.Unlock()
	if len(aiCalls) != 1 {
		t.Fatalf("expected 1 AI call, got %d", len(aiCalls))
	}
	if aiCalls[0].auth != "Bearer "+jwt {
		t.Errorf("AI Authorization = %q, want Bearer %s", aiCalls[0].auth, jwt)
	}
	if aiCalls[0].org != "acme" {
		t.Errorf("AI X-Org-Id = %q, want acme", aiCalls[0].org)
	}
	if aiCalls[0].model != "enso-auto" {
		t.Errorf("AI model = %q, want enso-auto", aiCalls[0].model)
	}

	// Verify external HTTP step got interpolated body and NEVER leaked caller's bearer.
	hookMu.Lock()
	defer hookMu.Unlock()
	if hookAuth != "" {
		t.Errorf("external HTTP step leaked Authorization header %q; must never forward caller bearer", hookAuth)
	}
	if hookCustomHdr != "hanzo-flow" {
		t.Errorf("external HTTP X-Webhook-Source = %q, want hanzo-flow", hookCustomHdr)
	}
	if hookPayload["customer"] != "Elena" || hookPayload["tier"] != "vip" {
		t.Errorf("external HTTP payload = %+v", hookPayload)
	}

	// Verify Python Langflow /v1/run/{id} was NEVER called.
	f.mu.Lock()
	for _, c := range f.calls {
		if strings.HasPrefix(c.path, "/v1/run/") {
			t.Errorf("upstream Python /v1/run was called (%s) for hanzo.flow/v1 graph", c.path)
		}
	}
	f.mu.Unlock()

	// Verify GET /v1/flow/runs?workflow={id} returns native Go run trace alongside vertex_builds.
	st, runsBody := doWithAuth(t, app, http.MethodGet, "/v1/flow/runs?workflow="+wfID, "u-alice", "acme", jwt, "")
	if st != http.StatusOK {
		t.Fatalf("GET /v1/flow/runs = %d body=%s", st, runsBody)
	}
	var runsEnvelope struct {
		Engine       string           `json:"engine"`
		VertexBuilds map[string]any   `json:"vertex_builds"`
		Runs         []dagRunResponse `json:"runs"`
	}
	if err := json.Unmarshal([]byte(runsBody), &runsEnvelope); err != nil {
		t.Fatalf("unmarshal runs: %v body=%s", err, runsBody)
	}
	if runsEnvelope.Engine != "hanzo-flow-go" || runsEnvelope.VertexBuilds == nil {
		t.Fatalf("runs envelope missing engine/vertex_builds: %s", runsBody)
	}
	if len(runsEnvelope.Runs) != 1 || runsEnvelope.Runs[0].RunID != res.RunID {
		t.Fatalf("runs = %+v, want 1 run with id %s", runsEnvelope.Runs, res.RunID)
	}
}

// TestUnifiedDAGCycleRejection proves cyclic and malformed hanzo.flow/v1 graphs
// fail fast with HTTP 400 before executing any step.
func TestUnifiedDAGCycleRejection(t *testing.T) {
	app, _ := harness(t)

	var aiHits atomic.Int32
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(aiSrv.Close)
	t.Setenv("HANZO_AI_URL", aiSrv.URL)

	cases := []struct {
		name  string
		graph map[string]any
	}{
		{
			name: "two-node cycle",
			graph: map[string]any{
				"schema": "hanzo.flow/v1",
				"nodes": []any{
					map[string]any{"id": "a", "type": "agent", "data": map[string]any{"prompt": "hi"}},
					map[string]any{"id": "b", "type": "set", "data": map[string]any{"key": "k", "value": "v"}},
				},
				"edges": []any{
					map[string]any{"source": "a", "target": "b"},
					map[string]any{"source": "b", "target": "a"},
				},
			},
		},
		{
			name: "three-node cycle",
			graph: map[string]any{
				"schema": "hanzo.flow/v1",
				"nodes": []any{
					map[string]any{"id": "a", "type": "set"},
					map[string]any{"id": "b", "type": "set"},
					map[string]any{"id": "c", "type": "set"},
				},
				"edges": []any{
					map[string]any{"source": "a", "target": "b"},
					map[string]any{"source": "b", "target": "c"},
					map[string]any{"source": "c", "target": "a"},
				},
			},
		},
		{
			name: "self-loop",
			graph: map[string]any{
				"schema": "hanzo.flow/v1",
				"nodes":  []any{map[string]any{"id": "a", "type": "set"}},
				"edges":  []any{map[string]any{"source": "a", "target": "a"}},
			},
		},
		{
			name: "unknown target node",
			graph: map[string]any{
				"schema": "hanzo.flow/v1",
				"nodes":  []any{map[string]any{"id": "a", "type": "set"}},
				"edges":  []any{map[string]any{"source": "a", "target": "missing"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wfID := createUnifiedWorkflow(t, app, "u1", "acme", "", "cyclic-"+tc.name, tc.graph)
			st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+wfID+`","input":"go"}`)
			if st != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400; body=%s", tc.name, st, body)
			}
		})
	}
	if n := aiHits.Load(); n != 0 {
		t.Fatalf("AI gateway was hit %d times on cyclic graphs; want 0", n)
	}
}

// TestUnifiedDAGSSRFRefused proves tenant-supplied http/connector steps dial
// strictly through hanzoai/outbound and refuse loopback, RFC1918, cloud metadata,
// and NAT64 private addresses with HTTP 400.
func TestUnifiedDAGSSRFRefused(t *testing.T) {
	app, _ := harness(t)

	var secretHits atomic.Int32
	internalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secretHits.Add(1)
		_, _ = w.Write([]byte(`{"secret":"must-never-leak"}`))
	}))
	t.Cleanup(internalSrv.Close)

	targets := []string{
		internalSrv.URL + "/internal",
		"http://127.0.0.1:9/secret",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/cluster",
		"http://[64:ff9b::a9fe:a9fe]/",
	}

	for _, target := range targets {
		graph := map[string]any{
			"schema": "hanzo.flow/v1",
			"nodes": []any{
				map[string]any{
					"id":   "n_http",
					"type": "http",
					"data": map[string]any{
						"kind":   "http",
						"method": "GET",
						"url":    target,
					},
				},
			},
			"edges": []any{},
		}
		wfID := createUnifiedWorkflow(t, app, "u1", "acme", "", "ssrf-check", graph)
		st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+wfID+`"}`)
		if st != http.StatusBadRequest {
			t.Errorf("target %q: status = %d, want 400; body=%s", target, st, body)
		}
		if strings.Contains(body, "must-never-leak") {
			t.Fatalf("target %q leaked internal response body", target)
		}
	}
	if n := secretHits.Load(); n != 0 {
		t.Fatalf("loopback internal server received %d hit(s); SSRF guard must block all", n)
	}
}

// TestUnifiedDAGPlatformActionsAndMediaAndMCP proves platform actions (/v1/notify,
// /v1/decisions), Studio/Comfy media (/v1/images/generations, /v1/prompt), MCP
// tools (/mcp), and wait metadata execute cleanly while blocking disallowed
// privileged paths (/v1/admin, /v1/kms).
func TestUnifiedDAGPlatformActionsAndMediaAndMCP(t *testing.T) {
	app, _ := harness(t)

	var mu sync.Mutex
	seenPaths := map[string]string{} // path -> X-Org-Id
	platSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPaths[r.URL.Path] = r.Header.Get("X-Org-Id")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/notify/send":
			_, _ = w.Write([]byte(`{"sent":true,"id":"msg-1"}`))
		case "/v1/decisions":
			_, _ = w.Write([]byte(`{"decision":"approve","confidence":0.94}`))
		case "/v1/images/generations":
			_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.hanzo.ai/img1.png"}]}`))
		case "/v1/prompt":
			_, _ = w.Write([]byte(`{"prompt_id":"comfy-job-9"}`))
		case "/mcp":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"text":"crm_contact_42"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(platSrv.Close)
	t.Setenv("HANZO_API_URL", platSrv.URL)
	t.Setenv("HANZO_STUDIO_URL", platSrv.URL)

	graph := map[string]any{
		"schema": "hanzo.flow/v1",
		"nodes": []any{
			map[string]any{"id": "n_wait", "type": "wait", "data": map[string]any{"schedule": "0 9 * * *"}},
			map[string]any{"id": "n_mcp", "type": "mcp", "data": map[string]any{"tool": "crm.lookup", "arguments": map[string]any{"email": "a@b.co"}}},
			map[string]any{"id": "n_kai", "type": "kai", "data": map[string]any{"choices": []any{"approve", "reject"}, "prompt": "Check {{n_mcp.text}}"}},
			map[string]any{"id": "n_notify", "type": "action", "data": map[string]any{"endpoint": "/v1/notify/send", "body": map[string]any{"to": "ops@acme.io"}}},
			map[string]any{"id": "n_img", "type": "media", "data": map[string]any{"model": "iro", "prompt": "solar house"}},
			map[string]any{"id": "n_comfy", "type": "comfy", "data": map[string]any{"prompt_graph": map[string]any{"1": map[string]any{"class_type": "KSampler"}}}},
			map[string]any{"id": "n_out", "type": "output", "data": map[string]any{"template": "done:{{n_comfy.prompt_id}}"}},
		},
		"edges": []any{
			map[string]any{"source": "n_wait", "target": "n_mcp"},
			map[string]any{"source": "n_mcp", "target": "n_kai"},
			map[string]any{"source": "n_kai", "target": "n_notify"},
			map[string]any{"source": "n_notify", "target": "n_img"},
			map[string]any{"source": "n_img", "target": "n_comfy"},
			map[string]any{"source": "n_comfy", "target": "n_out"},
		},
	}

	wfID := createUnifiedWorkflow(t, app, "u1", "acme", "hdr.u1.sig", "full-platform-dag", graph)
	st, body := doWithAuth(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", "hdr.u1.sig", `{"workflow":"`+wfID+`"}`)
	if st != http.StatusOK {
		t.Fatalf("platform DAG run = %d body=%s", st, body)
	}
	var res dagRunResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Status != "completed" || res.Summary != "done:comfy-job-9" {
		t.Fatalf("unexpected result: status=%s summary=%q", res.Status, res.Summary)
	}

	mu.Lock()
	for _, wantPath := range []string{"/mcp", "/v1/decisions", "/v1/notify/send", "/v1/images/generations", "/v1/prompt"} {
		if gotOrg := seenPaths[wantPath]; gotOrg != "acme" {
			t.Errorf("platform path %s saw X-Org-Id=%q, want acme", wantPath, gotOrg)
		}
	}
	mu.Unlock()

	// Privileged / traversal platform endpoints must be refused 400 and never reach server.
	for _, forbidden := range []string{"/v1/admin/orgs", "/v1/kms/secrets/prod/master", "/v1/notify/../../v1/admin/orgs"} {
		badGraph := map[string]any{
			"schema": "hanzo.flow/v1",
			"nodes": []any{
				map[string]any{"id": "bad", "type": "action", "data": map[string]any{"endpoint": forbidden}},
			},
			"edges": []any{},
		}
		badID := createUnifiedWorkflow(t, app, "u1", "acme", "hdr.u1.sig", "bad-action", badGraph)
		st, body := doWithAuth(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", "hdr.u1.sig", `{"workflow":"`+badID+`"}`)
		if st != http.StatusBadRequest {
			t.Errorf("forbidden platform endpoint %q = %d, want 400; body=%s", forbidden, st, body)
		}
	}
}

// TestUnifiedDAGConditionOperators proves all pure-Go comparison and boolean
// operators (==, !=, >, >=, <, <=, contains, truthy) and false-branch routing.
func TestUnifiedDAGConditionOperators(t *testing.T) {
	exec := &dagExec{
		vars: map[string]any{
			"score":  float64(75),
			"tier":   "pro",
			"notes":  "solar battery retrofit",
			"active": true,
			"empty":  "",
			"tags":   []any{"residential", "solar"},
		},
		outputs: map[string]any{},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{"tier == 'pro'", true},
		{"tier != 'enterprise'", true},
		{"tier == 'enterprise'", false},
		{"score > 70", true},
		{"score > 75", false},
		{"score >= 75", true},
		{"score < 80", true},
		{"score <= 75", true},
		{"score <= 74", false},
		{"notes contains 'battery'", true},
		{"notes contains 'wind'", false},
		{"tags contains 'solar'", true},
		{"active", true},
		{"empty", false},
		{"!empty && score >= 75", true},
		{"tier == 'free' || score == 75", true},
		{"(tier == 'free' || tier == 'pro') && score > 70", true},
	}
	for _, tc := range cases {
		if got := exec.evalExpression(tc.expr); got != tc.want {
			t.Errorf("evalExpression(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}

	// Structured operator mode + false branch routing through full HTTP endpoint.
	app, _ := harness(t)
	graph := map[string]any{
		"schema": "hanzo.flow/v1",
		"nodes": []any{
			map[string]any{"id": "n_set", "type": "set", "data": map[string]any{"key": "amount", "value": 25}},
			map[string]any{"id": "n_cond", "type": "condition", "data": map[string]any{"field": "amount", "operator": ">=", "right": 100}},
			map[string]any{"id": "n_hi", "type": "output", "data": map[string]any{"value": "high"}},
			map[string]any{"id": "n_lo", "type": "output", "data": map[string]any{"value": "low"}},
		},
		"edges": []any{
			map[string]any{"source": "n_set", "target": "n_cond"},
			map[string]any{"source": "n_cond", "target": "n_hi", "sourceHandle": "true"},
			map[string]any{"source": "n_cond", "target": "n_lo", "sourceHandle": "false"},
		},
	}
	wfID := createUnifiedWorkflow(t, app, "u1", "acme", "", "branch-false", graph)
	st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+wfID+`"}`)
	if st != http.StatusOK {
		t.Fatalf("run = %d body=%s", st, body)
	}
	var res dagRunResponse
	_ = json.Unmarshal([]byte(body), &res)
	if res.Summary != "low" {
		t.Fatalf("false branch summary = %q, want low", res.Summary)
	}
}

// TestUnifiedDAGOrgIsolationAndLegacyParity verifies:
//   - foreign org cannot execute or inspect another org's hanzo.flow/v1 workflow (404),
//   - deleting a workflow purges its native run ring,
//   - legacy Python Langflow workflows (without data.schema == "hanzo.flow/v1")
//     still proxy verbatim to upstream POST /v1/run/{id}?stream=false.
func TestUnifiedDAGOrgIsolationAndLegacyParity(t *testing.T) {
	app, f := harness(t)

	// 1. Unified workflow in acme.
	unifiedGraph := map[string]any{
		"schema": "hanzo.flow/v1",
		"nodes": []any{
			map[string]any{"id": "n1", "type": "trigger"},
			map[string]any{"id": "n2", "type": "output", "data": map[string]any{"template": "org={{org}} in={{input}}"}},
		},
		"edges": []any{
			map[string]any{"source": "n1", "target": "n2"},
		},
	}
	acmeWF := createUnifiedWorkflow(t, app, "u1", "acme", "", "acme-unified", unifiedGraph)

	st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+acmeWF+`","input":"hello"}`)
	if st != http.StatusOK || !strings.Contains(body, "org=acme in=hello") {
		t.Fatalf("acme unified run = %d body=%s", st, body)
	}

	// globex gets 404 on run and run history — never leaks acme's runs.
	if st, _ := do(t, app, http.MethodPost, "/v1/flow/runs", "u2", "globex", `{"workflow":"`+acmeWF+`","input":"steal"}`); st != http.StatusNotFound {
		t.Fatalf("globex run acme workflow = %d, want 404", st)
	}
	if st, _ := do(t, app, http.MethodGet, "/v1/flow/runs?workflow="+acmeWF, "u2", "globex", ""); st != http.StatusNotFound {
		t.Fatalf("globex runs list for acme workflow = %d, want 404", st)
	}

	// Delete clears acme's workflow and run traces.
	if st, _ := do(t, app, http.MethodDelete, "/v1/flow/workflows/"+acmeWF, "u1", "acme", ""); st != http.StatusOK {
		t.Fatalf("acme delete = %d, want 200", st)
	}

	// 2. Legacy Python Langflow workflow (no hanzo.flow/v1 schema) keeps verbatim upstream proxy.
	st, createBody := do(t, app, http.MethodPost, "/v1/flow/workflows", "u1", "acme", `{"name":"legacy-langflow","data":{"nodes":[{"id":"ChatInput-1"}],"edges":[]}}`)
	if st != http.StatusOK {
		t.Fatalf("create legacy = %d %s", st, createBody)
	}
	var legacy struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(createBody), &legacy)

	st, runBody := do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "acme", `{"workflow":"`+legacy.ID+`","input":"ping"}`)
	if st != http.StatusOK || !strings.Contains(runBody, `"session_id":"sess-1"`) {
		t.Fatalf("legacy run did not proxy verbatim: status=%d body=%s", st, runBody)
	}
	f.mu.Lock()
	var sawUpstreamRun bool
	for _, c := range f.calls {
		if c.method == http.MethodPost && c.path == "/v1/run/"+legacy.ID {
			sawUpstreamRun = true
		}
	}
	f.mu.Unlock()
	if !sawUpstreamRun {
		t.Fatalf("legacy workflow did not hit upstream POST /v1/run/%s", legacy.ID)
	}
}

// TestUnifiedDAGConcurrentRaceFree exercises concurrent native Go DAG runs and
// GET /v1/flow/runs reads across two orgs under -race.
func TestUnifiedDAGConcurrentRaceFree(t *testing.T) {
	app, _ := harness(t)

	graph := map[string]any{
		"schema": "hanzo.flow/v1",
		"nodes": []any{
			map[string]any{"id": "n_set", "type": "set", "data": map[string]any{"key": "echo", "value": "{{input}}"}},
			map[string]any{"id": "n_out", "type": "output", "data": map[string]any{"template": "{{org}}:{{vars.echo}}"}},
		},
		"edges": []any{
			map[string]any{"source": "n_set", "target": "n_out"},
		},
	}
	wfA := createUnifiedWorkflow(t, app, "ua", "org-a", "", "race-a", graph)
	wfB := createUnifiedWorkflow(t, app, "ub", "org-b", "", "race-b", graph)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "ua", "org-a", `{"workflow":"`+wfA+`","input":"a"}`)
			if st != http.StatusOK || !strings.Contains(body, "org-a:a") {
				t.Errorf("org-a run %d failed: %d %s", idx, st, body)
			}
			st, _ = do(t, app, http.MethodGet, "/v1/flow/runs?workflow="+wfA, "ua", "org-a", "")
			if st != http.StatusOK {
				t.Errorf("org-a runs %d = %d", idx, st)
			}
		}(i)
		go func(idx int) {
			defer wg.Done()
			st, body := do(t, app, http.MethodPost, "/v1/flow/runs", "ub", "org-b", `{"workflow":"`+wfB+`","input":"b"}`)
			if st != http.StatusOK || !strings.Contains(body, "org-b:b") {
				t.Errorf("org-b run %d failed: %d %s", idx, st, body)
			}
			st, _ = do(t, app, http.MethodGet, "/v1/flow/runs?workflow="+wfB, "ub", "org-b", "")
			if st != http.StatusOK {
				t.Errorf("org-b runs %d = %d", idx, st)
			}
		}(i)
	}
	wg.Wait()
}
