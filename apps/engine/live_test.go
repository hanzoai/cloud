package engine

// live_test.go — the same mounted app driven against a REAL hanzo-server.
//
// The fake in typed_wire_test.go pins the measured wire; this proves the
// measurement stays true: status, the model table, one model's state, and the
// host inventory — through the very routes the fleet serves, against a live
// hanzoai/engine server. Opt-in by env so CI needs no GPU box:
//
//	ENGINE_E2E_UPSTREAM=http://127.0.0.1:1234 make -C apps/engine test
//
// ENGINE_E2E_KEY carries the platform credential when the deployment is
// locked; a bare `hanzo-engine serve` needs none.

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
	up := os.Getenv("ENGINE_E2E_UPSTREAM")
	if up == "" {
		t.Skip("ENGINE_E2E_UPSTREAM unset — live end-to-end skipped")
	}
	t.Setenv("ENGINE_UPSTREAM", up)
	t.Setenv("ENGINE_API_KEY", os.Getenv("ENGINE_E2E_KEY"))
	app := zip.New(zip.Config{Logger: luxlog.New("enginelive"), DisableStartupMessage: true})
	compose(app)
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

func TestLiveEngineLens(t *testing.T) {
	app := liveApp(t)

	status, body := do(t, app, http.MethodGet, "/v1/engine/status", "u1", "e2e-org", "")
	if status != http.StatusOK || !strings.Contains(body, `"reachable":true`) {
		t.Fatalf("status = %d %s — is the engine up?", status, body)
	}

	// The model table answers, and its first named row's state reads back
	// through the one-model lens exactly as the table reported it.
	status, body = do(t, app, http.MethodGet, "/v1/engine/models", "u1", "e2e-org", "")
	if status != http.StatusOK {
		t.Fatalf("models = %d %s", status, body)
	}
	var table struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &table); err != nil || len(table.Data) == 0 {
		t.Fatalf("model table unparseable: %s", body)
	}
	probed := false
	for _, m := range table.Data {
		if m.Status == "" {
			continue // the aggregate "default" row carries no state
		}
		status, body = do(t, app, http.MethodGet, "/v1/engine/model?model="+m.ID, "u1", "e2e-org", "")
		if status != http.StatusOK || !strings.Contains(body, `"status":"`+m.Status+`"`) {
			t.Fatalf("model(%s) = %d %s, want the table's own state %q", m.ID, status, body, m.Status)
		}
		probed = true
	}
	if !probed {
		t.Fatal("no model row carried a load state — the live lens proved nothing")
	}

	// The host inventory names at least one device.
	status, body = do(t, app, http.MethodGet, "/v1/engine/system", "u1", "e2e-org", "")
	if status != http.StatusOK || !strings.Contains(body, `"devices"`) {
		t.Fatalf("system = %d %s", status, body)
	}

	// The gate holds against the real backend too.
	if status, _ = do(t, app, http.MethodGet, "/v1/engine/models", "", "", ""); status != http.StatusForbidden {
		t.Fatalf("unauthenticated live read = %d, want 403", status)
	}
}
