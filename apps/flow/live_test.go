package flow

// live_test.go — the same mounted app driven against a REAL flow service.
//
// The fake in typed_wire_test.go pins the measured wire; this proves the
// measurement stays true: the full loop — create, list, get, patch, run (a
// real graph execution), run records, delete — through the very routes the
// fleet serves, against a live hanzoai/flow server. Opt-in by env so CI needs
// no Python service:
//
//	FLOW_E2E_UPSTREAM=http://127.0.0.1:7860 FLOW_E2E_KEY=sk-… \
//	  make -C apps/flow test
//
// The run stage executes a minimal ChatInput→ChatOutput graph derived from the
// server's own basic examples, so it needs no model-provider credential.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	up := os.Getenv("FLOW_E2E_UPSTREAM")
	if up == "" {
		t.Skip("FLOW_E2E_UPSTREAM unset — live end-to-end skipped")
	}
	t.Setenv("FLOW_UPSTREAM", up)
	t.Setenv("FLOW_API_KEY", os.Getenv("FLOW_E2E_KEY"))
	app := zip.New(zip.Config{Logger: luxlog.New("flowlive"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("flowlive"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func TestLiveWorkflowLoop(t *testing.T) {
	app := liveApp(t)

	status, body := do(t, app, http.MethodGet, "/v1/flow/status", "u1", "e2e-org", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) {
		t.Fatalf("status = %d %s — is the flow server up?", status, body)
	}

	// A minimal runnable graph: the server's own ChatInput and ChatOutput
	// components wired directly, so the run needs no LLM credential.
	graph := echoGraph(t, app)

	status, body = do(t, app, http.MethodPost, "/v1/flow/workflows", "u1", "e2e-org",
		`{"name":"wf-live-e2e","description":"live loop","data":`+graph+`}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d %s", status, body)
	}
	var wf struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &wf); err != nil || wf.ID == "" {
		t.Fatalf("create relay unparseable: %s", body)
	}
	defer do(t, app, http.MethodDelete, "/v1/flow/workflows/"+wf.ID, "u1", "e2e-org", "")

	if status, body = do(t, app, http.MethodGet, "/v1/flow/workflows", "u1", "e2e-org", ""); status != http.StatusOK || !strings.Contains(body, wf.ID) {
		t.Fatalf("list = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPatch, "/v1/flow/workflows/"+wf.ID, "u1", "e2e-org", `{"description":"live loop updated"}`); status != http.StatusOK {
		t.Fatalf("patch = %d %s", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/v1/flow/runs", "u1", "e2e-org",
		`{"workflow":"`+wf.ID+`","input":"ping-live"}`)
	if status != http.StatusOK || !strings.Contains(body, "ping-live") {
		t.Fatalf("run = %d %s — the graph did not echo", status, body)
	}
	if status, body = do(t, app, http.MethodGet, "/v1/flow/runs?workflow="+wf.ID, "u1", "e2e-org", ""); status != http.StatusOK || !strings.Contains(body, "vertex_builds") {
		t.Fatalf("runs = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodDelete, "/v1/flow/workflows/"+wf.ID, "u1", "e2e-org", ""); status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, body)
	}

	// A second org cannot see or touch the first org's workflow even against
	// the real backend (create a fresh one, probe cross-org, clean up).
	status, body = do(t, app, http.MethodPost, "/v1/flow/workflows", "u1", "e2e-org", `{"name":"wf-live-iso"}`)
	if status != http.StatusOK {
		t.Fatalf("iso create = %d %s", status, body)
	}
	_ = json.Unmarshal([]byte(body), &wf)
	defer do(t, app, http.MethodDelete, "/v1/flow/workflows/"+wf.ID, "u1", "e2e-org", "")
	if status, _ = do(t, app, http.MethodGet, "/v1/flow/workflows/"+wf.ID, "u9", "e2e-other", ""); status != http.StatusNotFound {
		t.Fatalf("cross-org read = %d, want 404", status)
	}
}

// echoGraph derives a ChatInput→ChatOutput graph from the live server's own
// basic examples, wiring input straight to output. Built from the server's
// component records rather than authored here, so it stays valid across
// component-template changes.
func echoGraph(t *testing.T, app *zip.App) string {
	t.Helper()
	// Reuse the app's own upstream config to read one example flow directly.
	st, body, err := send(t.Context(), http.MethodGet, "/v1/flows/basic_examples/", nil, timeout)
	if err != nil || st != http.StatusOK {
		t.Skipf("basic_examples unavailable (%d, %v) — cannot derive an echo graph", st, err)
	}
	var examples []struct {
		Name string `json:"name"`
		Data struct {
			Nodes []json.RawMessage `json:"nodes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &examples); err != nil || len(examples) == 0 {
		t.Skip("no basic examples — cannot derive an echo graph")
	}
	var in, out json.RawMessage
	var inID, outID string
	for _, ex := range examples {
		for _, n := range ex.Data.Nodes {
			var node struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(n, &node)
			if in == nil && strings.HasPrefix(node.ID, "ChatInput") {
				in, inID = n, node.ID
			}
			if out == nil && strings.HasPrefix(node.ID, "ChatOutput") {
				out, outID = n, node.ID
			}
		}
		if in != nil && out != nil {
			break
		}
	}
	if in == nil || out == nil {
		t.Skip("no ChatInput/ChatOutput in the examples — cannot derive an echo graph")
	}
	edge := map[string]any{
		"source": inID, "target": outID,
		"data": map[string]any{
			"sourceHandle": map[string]any{"dataType": "ChatInput", "id": inID, "name": "message", "output_types": []string{"Message"}},
			"targetHandle": map[string]any{"fieldName": "input_value", "id": outID, "inputTypes": []string{"Data", "DataFrame", "Message"}, "type": "str"},
		},
	}
	g, _ := json.Marshal(map[string]any{"nodes": []json.RawMessage{in, out}, "edges": []any{edge}})
	return string(g)
}
