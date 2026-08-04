package auto

// live_test.go — the same mounted app driven against a REAL auto service.
//
// The fake in typed_wire_test.go pins the measured wire; this proves the
// measurement stays true: the full loop — pieces, create, list, get, patch,
// publish, start (a real durable dispatch), poll to completed with real
// output, delete — through the very routes the fleet serves, against a live
// hanzoai/auto server whose engine is wired to a live tasksd. Opt-in by env
// so CI needs no running product:
//
//	AUTO_E2E_UPSTREAM=http://127.0.0.1:18090 make -C apps/auto test
//
// The run stage executes a webhook→set graph, so the completed run's output
// must carry the set node's binding — computed by the product's engine, not
// echoed by anything in this repo.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	up := os.Getenv("AUTO_E2E_UPSTREAM")
	if up == "" {
		t.Skip("AUTO_E2E_UPSTREAM unset — live end-to-end skipped")
	}
	t.Setenv("AUTO_UPSTREAM", up)
	app := zip.New(zip.Config{Logger: luxlog.New("autolive"), DisableStartupMessage: true})
	compose(app)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("autolive"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func TestLiveFlowLoop(t *testing.T) {
	app := liveApp(t)
	const org = "e2e-cloud-auto"

	status, body := do(t, app, http.MethodGet, "/v1/auto/status", "u1", org, "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) {
		t.Fatalf("status = %d %s — is the auto server up?", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/v1/auto/pieces", "u1", org, "")
	if status != http.StatusOK || !strings.Contains(body, `"webhook"`) {
		t.Fatalf("pieces = %d %s", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/v1/auto/flows", "u1", org,
		`{"name":"wf-live-e2e","data":{"nodes":[{"id":"t","type":"webhook"},{"id":"s","type":"set","data":{"name":"greeting","value":"hello-from-auto"}}],"edges":[{"from":"t","to":"s"}]}}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d %s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("create relay unparseable: %s", body)
	}
	defer do(t, app, http.MethodDelete, "/v1/auto/flows/"+created.ID, "u1", org, "")

	if status, body = do(t, app, http.MethodGet, "/v1/auto/flows", "u1", org, ""); status != http.StatusOK || !strings.Contains(body, created.ID) {
		t.Fatalf("list = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPatch, "/v1/auto/flows/"+created.ID, "u1", org, `{"name":"wf-live-e2e-2"}`); status != http.StatusOK || !strings.Contains(body, "wf-live-e2e-2") {
		t.Fatalf("patch = %d %s", status, body)
	}
	if status, body = do(t, app, http.MethodPost, "/v1/auto/flows/"+created.ID+"/publish", "u1", org, ""); status != http.StatusOK || !strings.Contains(body, `"published":true`) {
		t.Fatalf("publish = %d %s", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/v1/auto/runs", "u1", org,
		`{"flow":"`+created.ID+`","input":{"who":"cloud"}}`)
	if status != http.StatusOK {
		t.Fatalf("start = %d %s", status, body)
	}
	var run struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &run); err != nil || run.ID == "" {
		t.Fatalf("start relay unparseable: %s", body)
	}

	// The engine executes on the tasks plane; poll the record to terminal.
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, body = do(t, app, http.MethodGet, "/v1/auto/runs/"+run.ID, "u1", org, "")
		if status != http.StatusOK {
			t.Fatalf("run get = %d %s", status, body)
		}
		_ = json.Unmarshal([]byte(body), &run)
		if run.Status == "completed" || run.Status == "failed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if run.Status != "completed" || !strings.Contains(body, "hello-from-auto") {
		t.Fatalf("run terminal = %s — want completed with the set node's output; body=%s", run.Status, body)
	}

	if status, body = do(t, app, http.MethodGet, "/v1/auto/runs?flow="+created.ID, "u1", org, ""); status != http.StatusOK || !strings.Contains(body, run.ID) {
		t.Fatalf("runs list = %d %s", status, body)
	}
}
