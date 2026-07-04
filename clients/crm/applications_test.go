package crm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// fakeAI is a stub gateway: it returns a canned reply, or an error, so the screen
// path is exercised without a live LLM.
type fakeAI struct {
	reply string
	err   error
}

func (f fakeAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &types.ChatResponse{Content: f.reply}, nil
}

// goodScreen is a realistic model reply (wrapped in prose to exercise extraction).
const goodScreen = `Sure, here is the screen:
{"score": 88, "tier1_backed": "yes", "summary": "Strong seed-stage AI infra team.\nClear fit for the credits program.", "suggested_credits": 150000, "draft_reply": "Hi Jane — thanks for applying to the Hanzo Startup Program. Your tier-1 backing and use case look like a strong fit; we'd love to set up onboarding and discuss credits."}`

// fullApplication is a complete intake body (every field populated).
func fullApplication() map[string]any {
	return map[string]any{
		"company":        "Acme AI",
		"website":        "https://acme.ai",
		"contactName":    "Jane Smith",
		"email":          "jane@acme.ai",
		"role":           "Co-founder & CEO",
		"stage":          "seed",
		"investors":      "Sequoia plus a few angels",
		"tier1Investors": []string{"Sequoia"},
		"amountRaised":   "$2M",
		"teamSize":       "5",
		"building":       "An agentic coding platform for enterprises.",
		"useCases":       []string{"ai", "deploy"},
		"infraSpend":     "$3,000/mo",
		"byoCompute":     true,
		"byoHardware":    "8x H100",
		"techstarsBatch": "NYC W17",
		"heardVia":       "A friend or colleague",
		"submittedAt":    "2026-07-04T00:00:00Z",
	}
}

// TestApplyCreatesRecords proves a public submission lands an application with all
// fields in metadata, tier-1 detected, and a CRM Company+Contact projection.
func TestApplyCreatesRecords(t *testing.T) {
	app := mountApp(t)
	mounted.screenSync = true // run screen inline; no goroutine leak

	code, body := do(t, app, http.MethodPost, "/v1/crm/applications", "", fullApplication())
	if code != http.StatusCreated {
		t.Fatalf("apply want 201, got %d (%s)", code, body)
	}
	var created struct{ ID, Stage, Status string }
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		t.Fatalf("apply response: %v (%s)", err, body)
	}
	if created.Stage != StageApplied {
		t.Fatalf("intake stage want applied, got %q", created.Stage)
	}

	// Staff read (org=hanzo, validated principal).
	code, body = do(t, app, http.MethodGet, "/v1/crm/applications", "hanzo", nil)
	var list struct {
		Data []Application `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Data) != 1 {
		t.Fatalf("list want 1 application, got %d %+v", code, list.Data)
	}
	a := list.Data[0]
	if a.Company != "Acme AI" || a.Email != "jane@acme.ai" {
		t.Fatalf("application identity mismatch: %+v", a)
	}
	if !a.Tier1 {
		t.Fatalf("tier1 should be detected from selected Sequoia")
	}
	if a.Metadata["building"] != "An agentic coding platform for enterprises." {
		t.Fatalf("metadata missing building: %+v", a.Metadata)
	}
	if a.CompanyID == "" || a.ContactID == "" {
		t.Fatalf("CRM projection ids should be set: %+v", a)
	}

	// The CRM Company + Contact projections exist in the standard CRM tabs.
	code, body = do(t, app, http.MethodGet, "/v1/crm/companies", "hanzo", nil)
	var comps struct {
		Data []Company `json:"data"`
	}
	_ = json.Unmarshal(body, &comps)
	if code != http.StatusOK || len(comps.Data) != 1 || comps.Data[0].Name != "Acme AI" {
		t.Fatalf("company projection want [Acme AI], got %d %+v", code, comps.Data)
	}
	code, body = do(t, app, http.MethodGet, "/v1/crm/contacts", "hanzo", nil)
	var conts struct {
		Data []Contact `json:"data"`
	}
	_ = json.Unmarshal(body, &conts)
	if code != http.StatusOK || len(conts.Data) != 1 || conts.Data[0].Email != "jane@acme.ai" {
		t.Fatalf("contact projection want [jane@acme.ai], got %d %+v", code, conts.Data)
	}
}

// TestApplyHoneypot proves a filled honeypot is silently dropped (200, nothing
// stored).
func TestApplyHoneypot(t *testing.T) {
	app := mountApp(t)
	mounted.screenSync = true
	body := fullApplication()
	body["hp"] = "http://spam.example"
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", body); code != http.StatusOK {
		t.Fatalf("honeypot want 200, got %d", code)
	}
	_, lb := do(t, app, http.MethodGet, "/v1/crm/applications", "hanzo", nil)
	var list struct {
		Data []Application `json:"data"`
	}
	_ = json.Unmarshal(lb, &list)
	if len(list.Data) != 0 {
		t.Fatalf("honeypot submission must not store; got %d", len(list.Data))
	}
}

// TestApplyValidation covers the intake rejections.
func TestApplyValidation(t *testing.T) {
	app := mountApp(t)
	mounted.screenSync = true

	cases := []struct {
		name string
		mut  func(m map[string]any)
	}{
		{"missing company", func(m map[string]any) { delete(m, "company") }},
		{"missing contact", func(m map[string]any) { delete(m, "contactName") }},
		{"bad email", func(m map[string]any) { m["email"] = "not-an-email" }},
	}
	for _, tc := range cases {
		b := fullApplication()
		tc.mut(b)
		if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", b); code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", tc.name, code)
		}
	}
}

// TestApplyIdempotent proves a resubmission of the same (email, company) updates
// rather than duplicates.
func TestApplyIdempotent(t *testing.T) {
	app := mountApp(t)
	mounted.screenSync = true

	first := fullApplication()
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", first); code != http.StatusCreated {
		t.Fatalf("first apply want 201, got %d", code)
	}
	second := fullApplication()
	second["teamSize"] = "9" // updated detail
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", second); code != http.StatusOK {
		t.Fatalf("resubmit want 200, got %d", code)
	}
	_, lb := do(t, app, http.MethodGet, "/v1/crm/applications", "hanzo", nil)
	var list struct {
		Data []Application `json:"data"`
	}
	_ = json.Unmarshal(lb, &list)
	if len(list.Data) != 1 {
		t.Fatalf("idempotent intake want 1 application, got %d", len(list.Data))
	}
	if list.Data[0].Metadata["teamSize"] != "9" {
		t.Fatalf("resubmit should refresh metadata: %+v", list.Data[0].Metadata)
	}
}

// TestApplyScreenEndToEnd is the full intake→screen→auto-advance proof with a
// fake gateway: submit → the application is scored and moves applied→screened.
func TestApplyScreenEndToEnd(t *testing.T) {
	app := mountApp(t)
	mounted.ai = fakeAI{reply: goodScreen}
	mounted.screenSync = true

	if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", fullApplication()); code != http.StatusCreated {
		t.Fatalf("apply want 201, got %d", code)
	}
	_, lb := do(t, app, http.MethodGet, "/v1/crm/applications", "hanzo", nil)
	var list struct {
		Data []Application `json:"data"`
	}
	_ = json.Unmarshal(lb, &list)
	if len(list.Data) != 1 {
		t.Fatalf("want 1 application, got %d", len(list.Data))
	}
	a := list.Data[0]
	if a.Stage != StageScreened {
		t.Fatalf("screened application should auto-advance to screened, got %q", a.Stage)
	}
	if a.Screen.Status != "done" {
		t.Fatalf("screen status want done, got %q (%+v)", a.Screen.Status, a.Screen)
	}
	if a.Screen.Score != 88 || a.Screen.Tier1Backed != "yes" || a.Screen.SuggestedCredits != 150000 {
		t.Fatalf("screen fields mismatch: %+v", a.Screen)
	}
	if a.Screen.DraftReply == "" || a.Screen.Summary == "" {
		t.Fatalf("screen should carry a draft reply + summary: %+v", a.Screen)
	}
	// The auto-advance is recorded on the timeline.
	if len(a.Events) < 2 || a.Events[len(a.Events)-1].To != StageScreened {
		t.Fatalf("expected an applied→screened event, got %+v", a.Events)
	}
}

// TestScreenNonFatal proves the intake still lands when the gateway errors: the
// screen is marked failed and the application stays at applied.
func TestScreenNonFatal(t *testing.T) {
	app := mountApp(t)
	mounted.ai = fakeAI{err: errors.New("gateway unavailable")}
	mounted.screenSync = true

	if code, _ := do(t, app, http.MethodPost, "/v1/crm/applications", "", fullApplication()); code != http.StatusCreated {
		t.Fatalf("apply want 201, got %d", code)
	}
	_, lb := do(t, app, http.MethodGet, "/v1/crm/applications", "hanzo", nil)
	var list struct {
		Data []Application `json:"data"`
	}
	_ = json.Unmarshal(lb, &list)
	if len(list.Data) != 1 {
		t.Fatalf("want 1 application, got %d", len(list.Data))
	}
	if list.Data[0].Stage != StageApplied {
		t.Fatalf("failed screen must not advance; got %q", list.Data[0].Stage)
	}
	if list.Data[0].Screen.Status != "failed" {
		t.Fatalf("screen status want failed, got %q", list.Data[0].Screen.Status)
	}
}

// TestPatchStageMachine drives the staff pipeline: advance one step, block a
// skip, require a reason to reject, and reject with reason.
func TestPatchStageMachine(t *testing.T) {
	app := mountApp(t)
	mounted.ai = nil // no auto-advance; keep it at applied
	mounted.screenSync = true

	_, body := do(t, app, http.MethodPost, "/v1/crm/applications", "", fullApplication())
	var created struct{ ID string }
	_ = json.Unmarshal(body, &created)
	id := created.ID
	path := "/v1/crm/applications/" + id

	// applied → screened (advance one): OK.
	if code, _ := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": StageScreened}); code != http.StatusOK {
		t.Fatalf("advance to screened want 200, got %d", code)
	}
	// screened → onboarded (skip): blocked.
	if code, _ := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": StageOnboarded}); code != http.StatusBadRequest {
		t.Fatalf("skip to onboarded want 400, got %d", code)
	}
	// screened → qualified: OK.
	if code, _ := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": StageQualified}); code != http.StatusOK {
		t.Fatalf("advance to qualified want 200, got %d", code)
	}
	// → rejected without reason: blocked.
	if code, _ := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": StageRejected}); code != http.StatusBadRequest {
		t.Fatalf("reject without reason want 400, got %d", code)
	}
	// → rejected with reason: OK and reason stored.
	code, rb := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": StageRejected, "reason": "not a fit this round"})
	if code != http.StatusOK {
		t.Fatalf("reject with reason want 200, got %d (%s)", code, rb)
	}
	var rejected Application
	_ = json.Unmarshal(rb, &rejected)
	if rejected.Stage != StageRejected || rejected.Reason != "not a fit this round" {
		t.Fatalf("reject state mismatch: %+v", rejected)
	}
	// Unknown stage → 400.
	if code, _ := do(t, app, http.MethodPatch, path, "hanzo", map[string]any{"stage": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("bogus stage want 400, got %d", code)
	}
	// Staff read requires a principal: no org → 403.
	if code, _ := do(t, app, http.MethodGet, path, "", nil); code != http.StatusForbidden {
		t.Fatalf("unauth application read want 403, got %d", code)
	}
}

// TestStageTransitions unit-tests the pure stage machine.
func TestStageTransitions(t *testing.T) {
	ok := func(from, to string) {
		if allowed, why := canTransition(from, to); !allowed {
			t.Fatalf("%s→%s should be allowed (%s)", from, to, why)
		}
	}
	no := func(from, to string) {
		if allowed, _ := canTransition(from, to); allowed {
			t.Fatalf("%s→%s should be blocked", from, to)
		}
	}
	ok(StageApplied, StageScreened)              // advance one
	ok(StageScreened, StageQualified)            // advance one
	ok(StageQualified, StageCreditsOffered)      // advance one
	ok(StageCreditsOffered, StageOnboarded)      // advance one
	ok(StageQualified, StageApplied)             // move back (correction)
	ok(StageApplied, StageRejected)              // reject from any
	ok(StageOnboarded, StageRejected)            // reject from any
	ok(StageRejected, StageApplied)              // reopen
	ok(StageApplied, StageApplied)               // no-op
	no(StageApplied, StageQualified)             // skip forward
	no(StageApplied, StageOnboarded)             // skip forward
	no(StageRejected, StageScreened)             // reopen only to applied
	no(StageApplied, "bogus")                    // unknown
}

// TestParseScreen covers extraction from prose/fences, clamping, and snapping.
func TestParseScreen(t *testing.T) {
	res, err := parseScreen("```json\n" + `{"score": 250, "tier1_backed": "YES", "suggested_credits": 40000, "summary": "x", "draft_reply": "y"}` + "\n```")
	if err != nil {
		t.Fatalf("parseScreen: %v", err)
	}
	if res.Score != 100 { // clamped to [0,100]
		t.Fatalf("score clamp want 100, got %d", res.Score)
	}
	if res.Tier1Backed != "yes" {
		t.Fatalf("tier1 normalize want yes, got %q", res.Tier1Backed)
	}
	if res.SuggestedCredits != 50000 { // 40000 snaps to nearest tier
		t.Fatalf("credits snap want 50000, got %d", res.SuggestedCredits)
	}
	if _, err := parseScreen("no json here"); err == nil {
		t.Fatalf("parseScreen should error on missing JSON")
	}
}

// TestDetectTier1 covers deterministic tier-1 detection.
func TestDetectTier1(t *testing.T) {
	if ok, m := detectTier1([]string{"Sequoia"}, ""); !ok || len(m) != 1 {
		t.Fatalf("selected fund should detect: %v %v", ok, m)
	}
	if ok, m := detectTier1(nil, "we are backed by Founders Fund and some angels"); !ok || len(m) == 0 {
		t.Fatalf("free-text fund should detect: %v %v", ok, m)
	}
	if ok, _ := detectTier1(nil, "fully bootstrapped, no investors"); ok {
		t.Fatalf("bootstrapped should not detect tier-1")
	}
}
