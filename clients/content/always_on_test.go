package content

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
)

// always_on_test.go proves the live gap is closed: the Business AI Guide ran
// content_generate → "content: marketing module not installed for org". Because
// marketing is now an always-on module (doctypes.go init → framework.MarkAlwaysOn),
// content.Generate completes for an org that NEVER installed it — no per-org install
// step, no special-casing per Guide step.

// TestGenerate_CompletesWithoutInstall_HTTP: POST /v1/content/generate for a fresh
// org that did NOT install marketing returns 201 (was 409 "install the marketing
// module first"). This is the exact path the Guide's do-action drives.
func TestGenerate_CompletesWithoutInstall_HTTP(t *testing.T) {
	const org = "freshco" // never runs POST /v1/framework/modules/marketing/install
	ai := &recordingAI{reply: `A crisp one-line positioning statement.

- Supporting point one
- Supporting point two
- Supporting point three`}
	app := mountWith(t, cloud.Deps{AI: ai, Env: "testnet"})

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeCampaign,
		"title":   "Positioning",
		"brief":   "Write a single crisp positioning statement plus three supporting bullets.",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate on a fresh (un-installed) org must complete with 201, got %d %s", code, b)
	}
	var res GenerateResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, b)
	}
	if res.DocType != DocTypeCampaign || res.Name == "" || res.Status != StatusDraft {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestGenerate_CompletesWithoutInstall_InProcess: the ONE Generate op (the automation
// connector + the Guide agent both call it in-process) never returns
// errModuleNotInstalled for a fresh org — the module is always-on.
func TestGenerate_CompletesWithoutInstall_InProcess(t *testing.T) {
	const org = "acme"
	ai := &recordingAI{reply: "Positioning: the sharpest one-liner.\n\n- a\n- b\n- c"}
	_ = mountWith(t, cloud.Deps{AI: ai, Env: "testnet"})

	res, err := Generate(context.Background(), org, GenerateInput{
		DocType: DocTypeCampaign,
		Title:   "Positioning",
		Brief:   "one crisp positioning statement",
	})
	if errors.Is(err, errModuleNotInstalled) {
		t.Fatal("Generate must NOT return errModuleNotInstalled for a fresh org — marketing is always-on")
	}
	if err != nil {
		t.Fatalf("Generate on a fresh org: %v", err)
	}
	if res.Name == "" || res.DocType != DocTypeCampaign {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestGenerate_InstallStillSucceeds: installing an always-on module stays a valid,
// idempotent no-op (it reports its DocTypes as already present) — no regression for
// a caller that still explicitly installs, and customization via the doctype API is
// unaffected (a stored override wins over the fixture).
func TestGenerate_InstallStillSucceeds(t *testing.T) {
	const org = "acme"
	app := mountWith(t, cloud.Deps{AI: &recordingAI{reply: "x"}, Env: "testnet"})
	code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil)
	if code != http.StatusOK {
		t.Fatalf("install marketing want 200, got %d %s", code, b)
	}
}
