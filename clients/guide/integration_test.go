package guide

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/content"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// integration_test.go is the end-to-end proof of the module-install wiring fix: a
// Guide "do it for me" step whose action needs the marketing module COMPLETES on an
// org that never installed it. It mounts the REAL plane — framework + content +
// automations + guide — with the real invoke seam (automations.InvokeTool), so the
// positioning step runs content_generate through the per-principal MCP plane exactly
// as in production. Before the always-on fix this returned
// "content: marketing module not installed for org"; now it drafts, generates a real
// Campaign, and marks the step done.

// mountFullPlane wires the four real subsystems that a Guide content step traverses,
// with a fake AI for both the agent's draft and content's generator.
func mountFullPlane(t *testing.T, ai cloud.AIClient) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	log := luxlog.New("test")
	if err := framework.Mount(app, cloud.Deps{Logger: log, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("framework.Mount: %v", err)
	}
	if err := content.Mount(app, cloud.Deps{Logger: log, AI: ai, Domain: "api.test", Env: "testnet"}); err != nil {
		t.Fatalf("content.Mount: %v", err)
	}
	if err := automations.Mount(app, cloud.Deps{Logger: log, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("automations.Mount: %v", err)
	}
	// guide with the REAL invoke seam (automations.InvokeTool) — no fake.
	if err := Mount(app, cloud.Deps{Logger: log, DataDir: t.TempDir(), AI: ai, AIDefaultModel: "zen"}); err != nil {
		t.Fatalf("guide.Mount: %v", err)
	}
	// Keep only the store-backed "acted" detector so the read never hits a live warehouse.
	mounted.State.detectors = map[string]Detector{"acted": mounted.State.detectors["acted"]}
	t.Cleanup(func() {
		_ = Shutdown()
		_ = automations.Shutdown(context.Background())
		_ = content.Shutdown()
		_ = framework.Shutdown()
	})
	return app
}

// TestDoStepCompletesWithoutModuleInstall: the positioning step (tool
// content_generate, doctype Campaign) COMPLETES for a fresh org that never installed
// marketing — the fix for the live "module not installed for org" gap. The real
// content.Generate runs through the always-on marketing module and writes a Campaign.
func TestDoStepCompletesWithoutModuleInstall(t *testing.T) {
	const org = "freshco" // never runs POST /v1/framework/modules/marketing/install
	ai := &fakeAI{content: "The one-line positioning that seeds every asset.\n\n- point a\n- point b\n- point c"}
	app := mountFullPlane(t, ai)

	// positioning is gated by the journey's first quest (company) — complete it.
	if r := req(t, app, http.MethodPost, "/v1/guide/steps/company/done", org, nil); r.Code != http.StatusOK {
		t.Fatalf("done company want 200, got %d (%s)", r.Code, r.Body)
	}

	// "Do it for me" on positioning — the exact path the user hit.
	r := req(t, app, http.MethodPost, "/v1/guide/steps/positioning/do", org, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("do positioning want 200, got %d (%s)", r.Code, r.Body)
	}
	var resp struct {
		State  string  `json:"state"`
		Events []event `json:"events"`
		Error  string  `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode do: %v (%s)", err, r.Body)
	}
	if resp.Error != "" {
		t.Fatalf("do must not error (was: %q) — the module gate should be gone", resp.Error)
	}
	if resp.State != string(StateDone) {
		t.Fatalf("do must complete the step, got state %q (events: %+v)", resp.State, resp.Events)
	}
	// No event may carry the module-not-installed error.
	for _, e := range resp.Events {
		if e.Type == "error" {
			t.Fatalf("do produced an error event: %q", e.Error)
		}
	}

	// The real effect landed: a Campaign document now exists in the org's framework
	// store — created WITHOUT any explicit marketing install.
	docs, err := framework.Search(context.Background(), org, content.DocTypeCampaign, nil, 10)
	if err != nil {
		t.Fatalf("search campaigns: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("a Campaign must have been created by the Guide's do-action (module auto-available)")
	}

	// The overview now shows positioning done, and the acted signal keeps it done.
	v := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", org, nil).Body)
	for _, s := range v.Steps {
		if s.ID == "positioning" && s.State != StateDone {
			t.Fatalf("positioning must be done after do, got %q", s.State)
		}
	}
}
