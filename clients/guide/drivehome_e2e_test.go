package guide

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/company"
	"github.com/hanzoai/cloud/clients/content"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// drivehome_e2e_test.go is the DRIVE-IT-HOME proof: a cross-subsystem harness that
// mounts the REAL app wire (framework + content + automations + guide + company) and
// walks a FRESH org through the whole agentic-company + Guide loop, asserting each of
// the seven seams against the real, in-process subsystems with a validated test
// principal. Nothing is stubbed at the seam boundary that production wires: the Guide's
// "do it for me" runs content_generate through the real automations.InvokeTool onto the
// real framework DocType store; the company machine runs its real transition guards; the
// blueprint admin plane runs the real SuperAdmin predicate. The only fakes are the AI
// completion (a deterministic draft) and the growth-observe seams, which are bound to
// provably org-scoped reads exactly as the composition root (apps/wire_seams.go) binds
// framework.ModuleInstalled / integrations.Connected in prod.

// mountAgenticStack wires the five real subsystems a fresh org traverses on the
// agentic-company + Guide journey, sharing ONE zip.App (one request plane), with a
// deterministic fake AI for both the guide agent's draft and content's generator. It is
// the harness twin of the production Wire() slice for exactly this slice of subsystems.
func mountAgenticStack(t *testing.T, ai cloud.AIClient) *zip.App {
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
	// guide with the REAL invoke seam (automations.InvokeTool) — no fake tool plane.
	if err := Mount(app, cloud.Deps{Logger: log, DataDir: t.TempDir(), AI: ai, AIDefaultModel: "zen"}); err != nil {
		t.Fatalf("guide.Mount: %v", err)
	}
	// company: the incorporation state machine on its own per-org store, manual KYC
	// provider (the honest default — a pass requires a reviewer decision, never a
	// client assertion).
	if err := company.Mount(app, cloud.Deps{Logger: log, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("company.Mount: %v", err)
	}
	// Keep only the store-backed "acted" detector so a checklist read never blocks on a
	// live analytics warehouse (the growth STAGE is driven by observe/signals, not by
	// these checklist detectors, so this does not weaken the observe proof).
	mounted.State.detectors = map[string]Detector{"acted": mounted.State.detectors["acted"]}
	t.Cleanup(func() {
		_ = company.Shutdown(context.Background())
		_ = Shutdown()
		_ = automations.Shutdown(context.Background())
		_ = content.Shutdown()
		_ = framework.Shutdown()
	})
	return app
}

// superForOrg is a platform SuperAdmin acting IN a named org: X-User-Owner=="admin"
// (the cross-tenant predicate, so IsSuperAdmin holds) while X-Org-Id is the org whose
// record is being acted on (so principal.Org resolves that org). This is the exact
// admin-org-switch identity SanitizeIdentity mints, and the only identity allowed to
// record a founder-KYC reviewer decision.
func superForOrg(org string) map[string]string {
	return map[string]string{
		"X-Org-Id": org, "X-User-Id": "admin/reviewer",
		"X-User-IsAdmin": "true", "X-User-Owner": "admin",
	}
}

// ── Seam 1 — company formation + the KYC gate ────────────────────────────────

// TestDrive_Seam1_CompanyFormationAndKYCGate proves the incorporation entrypoint: POST
// /v1/company begins ONE formation for the org; the machine advances structure→founders
// once a structure is chosen; and the KYC gate HOLDS — an unattributed founder can never
// cross founders→payment, and only an ATTRIBUTED reviewer confirmation opens it.
func TestDrive_Seam1_CompanyFormationAndKYCGate(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "draft"})
	const org = "foundco"

	// One call begins the formation. The initial machine state is StageStructure (the
	// true initial stage); founders is the next rung once a structure is chosen.
	begin := reqJSON(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "c-corp", "jurisdiction": "DE", "name": "Foundco Inc",
	})
	if begin.Code != http.StatusCreated {
		t.Fatalf("POST /v1/company want 201, got %d (%s)", begin.Code, begin.Body)
	}
	if got := formationStage(t, begin.Body); got != string(company.StageStructure) {
		t.Fatalf("a fresh formation starts at structure, got %q", got)
	}
	// Idempotent: a second begin returns the same formation, not a duplicate.
	if again := reqJSON(t, app, http.MethodPost, "/v1/company", org, map[string]any{}); again.Code != http.StatusOK {
		t.Fatalf("second POST /v1/company want 200 (idempotent), got %d (%s)", again.Code, again.Body)
	}

	// Advance to founders (guardStructureChosen is satisfied by the begin body).
	adv := reqJSON(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "founders"})
	if adv.Code != http.StatusOK || formationStage(t, adv.Body) != string(company.StageFounders) {
		t.Fatalf("advance→founders want 200@founders, got %d @%q (%s)", adv.Code, formationStage(t, adv.Body), adv.Body)
	}

	// Add a founder — no attributed KYC yet.
	setF := reqJSON(t, app, http.MethodPost, "/v1/company/founders", org, map[string]any{
		"founders": []map[string]any{{"name": "Ada", "email": "ada@foundco.com", "equityBps": 10000}},
	})
	if setF.Code != http.StatusOK {
		t.Fatalf("set founders want 200, got %d (%s)", setF.Code, setF.Body)
	}

	// THE GATE HOLDS: an unattributed founder cannot cross founders→payment (422).
	blocked := reqJSON(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"})
	if blocked.Code != http.StatusUnprocessableEntity {
		t.Fatalf("KYC gate must hold: advance→payment before KYC want 422, got %d (%s)", blocked.Code, blocked.Body)
	}

	// A client cannot forge a pass: only a platform SuperAdmin reviewer decision settles
	// a founder, and it is ATTRIBUTED (DecidedBy = the reviewer). Start KYC (pending
	// refs), then confirm.
	if r := reqJSON(t, app, http.MethodPost, "/v1/company/kyc", org, nil); r.Code != http.StatusOK {
		t.Fatalf("start KYC want 200, got %d (%s)", r.Code, r.Body)
	}
	// A NON-SuperAdmin decision is refused (the privilege boundary).
	if r := reqJSON(t, app, http.MethodPost, "/v1/company/kyc/decision", org, map[string]any{
		"email": "ada@foundco.com", "status": "reviewer_confirmed",
	}); r.Code != http.StatusForbidden {
		t.Fatalf("non-SuperAdmin KYC decision want 403, got %d (%s)", r.Code, r.Body)
	}
	// The SuperAdmin reviewer confirms (acting IN foundco, owner==admin).
	if r := reqH(t, app, http.MethodPost, "/v1/company/kyc/decision", superForOrg(org), map[string]any{
		"email": "ada@foundco.com", "status": "reviewer_confirmed",
	}); r.Code != http.StatusOK {
		t.Fatalf("SuperAdmin KYC decision want 200, got %d (%s)", r.Code, r.Body)
	}

	// NOW the gate opens: founders→payment succeeds on an attributed pass.
	opened := reqJSON(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"})
	if opened.Code != http.StatusOK || formationStage(t, opened.Body) != string(company.StagePayment) {
		t.Fatalf("attributed KYC must open the gate: want 200@payment, got %d @%q (%s)",
			opened.Code, formationStage(t, opened.Body), opened.Body)
	}
	t.Logf("SEAM 1 PROVEN: formation begins@structure → advances@founders → KYC gate 422 (unattributed) → SuperAdmin-attributed pass → advances@payment")
}

// ── Seam 2 — Guide default-on: the marketing/content module resolves with zero install ──

// TestDrive_Seam2_GuideDefaultOnZeroInstall proves the "module not installed for org"
// fix: a brand-new org that NEVER ran POST /v1/framework/modules/marketing/install still
// resolves the marketing module and its Campaign DocType — framework.ModuleInstalled and
// framework.Installed (both call GetDocType) satisfy the always-on fixture for every org.
func TestDrive_Seam2_GuideDefaultOnZeroInstall(t *testing.T) {
	mountAgenticStack(t, &fakeAI{content: "draft"})
	ctx := context.Background()
	const org = "neverinstalled" // this org runs NO install, ever

	if !framework.ModuleInstalled(ctx, org, content.Module) {
		t.Fatalf("marketing module must resolve for a fresh org with zero install (the default-on fix)")
	}
	if !framework.Installed(ctx, org, content.DocTypeCampaign) {
		t.Fatalf("the Campaign DocType (GetDocType) must resolve for a fresh org with zero install")
	}
	// A truly-unknown module still honest-degrades to false (no spurious true).
	if framework.ModuleInstalled(ctx, org, "no-such-module") {
		t.Fatalf("an unknown module must resolve false, never a spurious true")
	}
	t.Logf("SEAM 2 PROVEN: fresh org %q resolves marketing module + Campaign DocType with zero install; unknown module stays false", org)
}

// ── Seam 3 — observe: org-scoped growth profile ──────────────────────────────

// TestDrive_Seam3_ObserveOrgScoped proves the observe layer serves each org its OWN
// growth stage + signals + keyMetrics, and never another org's. A signal seam true only
// for "alpha" must leave "beta" formed with zeroed metrics, and every probe reads only
// the caller's own org.
func TestDrive_Seam3_ObserveOrgScoped(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "draft"})

	mounted.State.signals = Signals{
		ModuleInstalled:  func(_ context.Context, org, m string) bool { return org == "alpha" && m == "cms" },
		ConnectorPresent: func(_ context.Context, org, p string) bool { return org == "alpha" && p == "stripe" },
		HasDeployment:    func(_ context.Context, org string) (bool, error) { return org == "alpha", nil },
		RevenueCents: func(_ context.Context, org string) (int64, error) {
			if org == "alpha" {
				return 424242, nil
			}
			return 0, nil
		},
		RecordCount: func(_ context.Context, org string) (int64, error) {
			if org == "alpha" {
				return 500, nil
			}
			return 0, nil
		},
	}

	alpha := profileOf(t, app, "alpha")
	beta := profileOf(t, app, "beta")

	// alpha observes its OWN signals + numbers → money-of-record → scaling.
	if alpha.Stage != StageScaling {
		t.Fatalf("alpha has revenue → scaling, got %q", alpha.Stage)
	}
	if alpha.KeyMetrics.RevenueCents != 424242 || alpha.KeyMetrics.Records != 500 {
		t.Fatalf("alpha keyMetrics must be its OWN numbers, got %+v", alpha.KeyMetrics)
	}
	for _, sig := range []string{"module:cms", "connected:stripe", SignalDeployed, SignalRevenue, SignalCustomers} {
		if !alpha.Signals[sig] {
			t.Fatalf("alpha must observe its own signal %q", sig)
		}
	}
	// beta sees NONE of alpha's signals — zero leakage — and stays formed.
	if beta.Stage != StageFormed {
		t.Fatalf("beta observed nothing → formed, got %q", beta.Stage)
	}
	for name, present := range beta.Signals {
		if present {
			t.Fatalf("cross-tenant leak: org beta observed %q=true (belongs to alpha)", name)
		}
	}
	if beta.KeyMetrics.RevenueCents != 0 || beta.KeyMetrics.Records != 0 {
		t.Fatalf("beta keyMetrics must be zero, got %+v", beta.KeyMetrics)
	}
	t.Logf("SEAM 3 PROVEN: alpha stage=%s rev=%d records=%d | beta stage=%s rev=%d records=%d (org-scoped, zero leak)",
		alpha.Stage, alpha.KeyMetrics.RevenueCents, alpha.KeyMetrics.Records,
		beta.Stage, beta.KeyMetrics.RevenueCents, beta.KeyMetrics.Records)
}

// ── Seam 4 — suggest: the ranked next-best move ──────────────────────────────

// TestDrive_Seam4_SuggestNextBestMove proves the recommendation surface returns the
// ranked next-best quests for the org's reconciled state — a fresh org's leading move is
// the journey root (incorporate), which is automatable (the Business AI can run it).
func TestDrive_Seam4_SuggestNextBestMove(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "draft"})
	var resp suggestResponse
	r := reqJSON(t, app, http.MethodGet, "/v1/guide/suggest", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("suggest want 200, got %d (%s)", r.Code, r.Body)
	}
	if err := json.Unmarshal(r.Body, &resp); err != nil {
		t.Fatalf("decode suggest: %v (%s)", err, r.Body)
	}
	if resp.Next != "incorporate" {
		t.Fatalf("fresh-org next-best want incorporate, got %q", resp.Next)
	}
	if len(resp.Suggestions) == 0 || resp.Suggestions[0].StepID != "incorporate" {
		t.Fatalf("incorporate must lead the ranked suggestions, got %v", ids(resp.Suggestions))
	}
	if !resp.Suggestions[0].Automatable {
		t.Fatalf("the leading quest must be automatable (the Business AI can do it)")
	}
	// Fail-closed without a principal.
	if r := reqJSON(t, app, http.MethodGet, "/v1/guide/suggest", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("suggest without principal want 403, got %d", r.Code)
	}
	t.Logf("SEAM 4 PROVEN: fresh org next-best=%q automatable=%v unlocks=%d (%d ranked candidates)",
		resp.Next, resp.Suggestions[0].Automatable, resp.Suggestions[0].Unlocks, len(resp.Suggestions))
}

// ── Seam 5 — strategies corpus, stage-filtered, signal-gated ─────────────────

// TestDrive_Seam5_StrategiesStageFiltered proves the tactics corpus is org-scoped and
// filtered by the observed stage — and, the load-bearing part, that an explicit ?stage=
// override can lift STAGE-gated tactics but can NEVER unlock a SIGNAL-gated tactic (a
// has:<capability> tag whose observed signal is absent stays hidden).
func TestDrive_Seam5_StrategiesStageFiltered(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "draft"})
	// No signals bound → the org observes nothing (stage formed, every has:* signal absent).

	// Observed (formed): stage:research tactics surface; stage:scaling tactics do not;
	// has:analytics tactics do not.
	base := strategiesOf(t, app, "acme", "", "")
	if base.Stage != string(StageFormed) {
		t.Fatalf("unobserved org strategies stage want formed, got %q", base.Stage)
	}
	if !base.has("buyer-personas") { // tags: [stage:research] → rank 0, always satisfied
		t.Fatalf("a stage:research tactic must surface at formed")
	}
	if base.has("churn-audit") { // tags: [stage:scaling]
		t.Fatalf("a stage:scaling tactic must be hidden at observed formed")
	}
	if base.has("clv-cac") { // tags: [has:analytics]
		t.Fatalf("a has:analytics tactic must be hidden when analytics is not observed")
	}

	// ?stage=scaling lifts the STAGE floor: stage:scaling tactics now surface...
	scaling := strategiesOf(t, app, "acme", "scaling", "")
	if scaling.Stage != string(StageScaling) {
		t.Fatalf("?stage=scaling must preview scaling, got %q", scaling.Stage)
	}
	if !scaling.has("churn-audit") {
		t.Fatalf("?stage=scaling must surface a stage:scaling tactic")
	}
	// ...but a SIGNAL-gated tactic stays hidden — the stage override cannot forge a
	// capability the org has not connected.
	if scaling.has("clv-cac") {
		t.Fatalf("SIGNAL GATE BREACH: ?stage=scaling must NOT unlock a has:analytics tactic")
	}

	// The ?category= filter narrows EXACTLY, server-side. Asserted against a category
	// that genuinely surfaces at the observed stage, so a non-empty result is required
	// first — an empty result would make every "no leak" claim below vacuously true.
	const cat = "business-market-insight"
	filtered := strategiesOf(t, app, "acme", "", cat)
	if filtered.Count == 0 {
		t.Fatalf("?category=%s must return tactics at observed formed (an empty read proves nothing)", cat)
	}
	for _, s := range filtered.Strategies {
		if s.Category != cat {
			t.Fatalf("?category=%s leaked a %q tactic", cat, s.Category)
		}
	}
	// ...and it is a strict SUBSET of the unfiltered read — a narrowing, never a
	// different or wider set (a filter that changed WHICH tactics are gated would be a
	// gate bypass, not a filter).
	if filtered.Count >= base.Count {
		t.Fatalf("?category= must narrow: filtered=%d unfiltered=%d", filtered.Count, base.Count)
	}
	for _, s := range filtered.Strategies {
		if !base.has(s.ID) {
			t.Fatalf("?category= surfaced %q, which the unfiltered read at the same stage did not", s.ID)
		}
	}
	t.Logf("SEAM 5 PROVEN: observed(formed)=%d tactics [research✓ scaling✗ analytics✗]; ?stage=scaling=%d tactics [scaling✓ but analytics-gate STILL✗]; ?category=%s=%d tactics (strict subset, no leak)",
		base.Count, scaling.Count, cat, filtered.Count)
}

// ── Seam 6 — execute a step: the effect lands, detect reflects it ────────────

// TestDrive_Seam6_ExecuteStepEffectLandsDetectReflects proves the executing seam. A Guide
// content_generate step runs through the REAL automations.InvokeTool → content.Generate →
// framework store: a Campaign document actually lands for the org, the step auto-marks
// done, AND the "acted" auto-detect signal INDEPENDENTLY reflects the landed effect (a
// reset step re-detects done on the next read, driven only by the recorded action).
func TestDrive_Seam6_ExecuteStepEffectLandsDetectReflects(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "One-line positioning.\n\n- a\n- b\n- c"})
	const org = "shipco"
	ctx := context.Background()

	// A single automatable step, signal:acted (so the detector governs it), tool
	// content_generate → a real Campaign.
	override := map[string]any{"version": "org-1", "steps": []map[string]any{{
		"id": "positioning", "title": "Nail positioning",
		"tool": "content_generate", "signal": "acted", "draftInto": "brief",
		"draft": "Write a one-line positioning statement plus three bullets.",
		"args":  map[string]any{"doctype": "Campaign", "title": "Positioning"},
	}}}
	if r := reqJSON(t, app, http.MethodPut, "/v1/guide/curriculum", org, override); r.Code != http.StatusOK {
		t.Fatalf("put override want 200, got %d (%s)", r.Code, r.Body)
	}

	// Before: no Campaign docs.
	if docs, _ := framework.Search(ctx, org, content.DocTypeCampaign, nil, 10); len(docs) != 0 {
		t.Fatalf("precondition: fresh org must have 0 campaigns, got %d", len(docs))
	}

	// Execute "do it for me".
	do := reqJSON(t, app, http.MethodPost, "/v1/guide/steps/positioning/do", org, nil)
	if do.Code != http.StatusOK {
		t.Fatalf("do step want 200, got %d (%s)", do.Code, do.Body)
	}
	var doResp struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(do.Body, &doResp)
	if doResp.Error != "" {
		t.Fatalf("do step must not error (the module gate is gone): %q", doResp.Error)
	}
	if doResp.State != string(StateDone) {
		t.Fatalf("do step must complete, got state %q", doResp.State)
	}

	// The REAL effect landed: a Campaign now exists in the org's framework store.
	docs, err := framework.Search(ctx, org, content.DocTypeCampaign, nil, 10)
	if err != nil {
		t.Fatalf("search campaigns: %v", err)
	}
	if len(docs) == 0 {
		t.Fatalf("a Campaign must have landed in the framework store (real effect)")
	}

	// DETECT REFLECTS IT: reset the step to todo, then a fresh read reconciles it back to
	// done via the "acted" detector alone (the recorded successful action) — no re-do.
	if r := reqJSON(t, app, http.MethodPost, "/v1/guide/steps/positioning/reset", org, nil); r.Code != http.StatusOK {
		t.Fatalf("reset want 200, got %d (%s)", r.Code, r.Body)
	}
	after := decode[overviewView](t, reqJSON(t, app, http.MethodGet, "/v1/guide", org, nil).Body)
	var reflected bool
	for _, s := range after.Steps {
		if s.ID == "positioning" {
			reflected = s.State == StateDone && s.Source == "auto"
		}
	}
	if !reflected {
		t.Fatalf("the acted detector must re-mark the step done (source auto) after reset — detect reflects the landed effect")
	}
	t.Logf("SEAM 6 PROVEN: content_generate ran the real invoke→content→framework path; %d Campaign doc(s) landed; step auto-done; acted-detector re-marked done after reset", len(docs))
}

// ── Seam 7 — blueprint admin plane: SuperAdmin-gated ─────────────────────────

// TestDrive_Seam7_BlueprintAdminPlane proves the authoring plane: a SuperAdmin can GET
// and PATCH the shared brand blueprint live (a disable takes effect on the next org
// resolve), while a normal org member is refused 403 (trusting per-org isAdmin here would
// be a privilege escalation).
func TestDrive_Seam7_BlueprintAdminPlane(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "draft"})

	// A normal org is refused the whole plane.
	if r := reqJSON(t, app, http.MethodGet, "/v1/guide/blueprint", "acme", nil); r.Code != http.StatusForbidden {
		t.Fatalf("non-SuperAdmin GET blueprint want 403, got %d", r.Code)
	}
	if r := reqJSON(t, app, http.MethodPatch, "/v1/guide/blueprint/steps/gsuite", "acme", map[string]any{"enabled": false}); r.Code != http.StatusForbidden {
		t.Fatalf("non-SuperAdmin PATCH blueprint want 403, got %d", r.Code)
	}

	// A SuperAdmin reads the seeded base blueprint.
	got := reqH(t, app, http.MethodGet, "/v1/guide/blueprint", superHdr, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("SuperAdmin GET blueprint want 200, got %d (%s)", got.Code, got.Body)
	}

	// Baseline: a normal org sees gsuite in the journey.
	before := decode[overviewView](t, reqJSON(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if !hasStep(before, "gsuite") {
		t.Fatalf("baseline journey must contain gsuite")
	}

	// SuperAdmin disables gsuite — LIVE.
	patch := reqH(t, app, http.MethodPatch, "/v1/guide/blueprint/steps/gsuite", superHdr, map[string]any{"enabled": false})
	if patch.Code != http.StatusOK {
		t.Fatalf("SuperAdmin PATCH disable want 200, got %d (%s)", patch.Code, patch.Body)
	}

	// The next resolve for a normal org drops gsuite.
	after := decode[overviewView](t, reqJSON(t, app, http.MethodGet, "/v1/guide", "acme", nil).Body)
	if hasStep(after, "gsuite") {
		t.Fatalf("a SuperAdmin disable must drop the step from the next org resolve")
	}
	t.Logf("SEAM 7 PROVEN: SuperAdmin GET/PATCH ok (gsuite disabled LIVE: journey %d→%d steps); normal org 403", len(before.Steps), len(after.Steps))
}

// ── Part 2 — the autonomous loop ─────────────────────────────────────────────

// TestDrive_AutonomousLoop RUNS the closed loop for several iterations and proves it
// ADVANCES autonomously: observe stage → suggest next move → execute the automatable step
// → the effect lands + state reflects it → next suggest advances. Both axes move: the
// next-step pointer walks the milestone chain AND the observed growth STAGE climbs
// formed→launched→activated→scaling as each milestone's real effect comes online.
//
// The growth-observe seams are bound to read the org's OWN real, persisted milestone
// state from the guide store — the honest in-harness stand-in for the prod seams
// (deploy.HasDeployment / commerce.RevenueCents / crm.RecordCount) the composition root
// leaves nil today. As the machine drives each milestone to done, those reads flip and
// the classifier reclassifies — the machine genuinely steering its own observed stage.
func TestDrive_AutonomousLoop(t *testing.T) {
	app := mountAgenticStack(t, &fakeAI{content: "Generated work product.\n\n- point a\n- point b"})
	const org = "loopco"

	// A three-milestone growth chain (each an automatable content_generate step, each
	// blocked by the prior) — the machine must do them in order.
	step := func(id, title string, deps []string) map[string]any {
		return map[string]any{
			"id": id, "title": title, "deps": deps,
			"tool": "content_generate", "draftInto": "brief",
			"draft": "Draft the work product for: " + title,
			"args":  map[string]any{"doctype": "Campaign", "title": title},
		}
	}
	chain := map[string]any{"version": "growth-1", "steps": []map[string]any{
		step("go-live", "Launch the site", nil),
		step("acquire", "Acquire first customers", []string{"go-live"}),
		step("monetize", "Turn on revenue", []string{"acquire"}),
	}}
	if r := reqJSON(t, app, http.MethodPut, "/v1/guide/curriculum", org, chain); r.Code != http.StatusOK {
		t.Fatalf("put growth chain want 200, got %d (%s)", r.Code, r.Body)
	}

	// stepDone reads the org's REAL persisted step state from the guide store.
	stepDone := func(id string) bool {
		st, err := mounted.State.stores.For(org, "")
		if err != nil {
			return false
		}
		rows, err := st.States(context.Background())
		if err != nil {
			return false
		}
		return rows[id].State == StateDone
	}
	// Bind the observe seams to the org's own achieved milestones — a live deployment
	// once go-live lands, a book of customers once acquire lands, revenue once monetize
	// lands. Real reads of real state, org-scoped (only loopco has this chain).
	mounted.State.signals = Signals{
		HasDeployment: func(_ context.Context, o string) (bool, error) { return o == org && stepDone("go-live"), nil },
		RecordCount: func(_ context.Context, o string) (int64, error) {
			if o == org && stepDone("acquire") {
				return 100, nil // ≥ customersThreshold(25)
			}
			return 0, nil
		},
		RevenueCents: func(_ context.Context, o string) (int64, error) {
			if o == org && stepDone("monetize") {
				return 5000, nil
			}
			return 0, nil
		},
	}

	type snap struct {
		iter    int
		stage   Stage
		next    string
		done    int
		total   int
		docs    int
		didStep string
	}
	var journey []snap
	wantStage := []Stage{StageFormed, StageLaunched, StageActivated, StageScaling}
	wantNext := []string{"go-live", "acquire", "monetize", ""}

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		// OBSERVE — the org's current growth stage (real-time, recomputed from state).
		prof := profileOf(t, app, org)
		// SUGGEST — the ranked next-best move for the reconciled state.
		var sg suggestResponse
		_ = json.Unmarshal(reqJSON(t, app, http.MethodGet, "/v1/guide/suggest", org, nil).Body, &sg)
		docs, _ := framework.Search(ctx, org, content.DocTypeCampaign, nil, 20)

		s := snap{iter: i, stage: prof.Stage, next: sg.Next,
			done: prof.KeyMetrics.LaunchProgress.Done, total: prof.KeyMetrics.LaunchProgress.Total, docs: len(docs)}

		// EXECUTE — do the suggested next move (if any remains).
		if sg.Next != "" {
			do := reqJSON(t, app, http.MethodPost, "/v1/guide/steps/"+sg.Next+"/do", org, nil)
			if do.Code != http.StatusOK {
				t.Fatalf("iter %d: do %q want 200, got %d (%s)", i, sg.Next, do.Code, do.Body)
			}
			var dr struct {
				State string `json:"state"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(do.Body, &dr)
			if dr.Error != "" || dr.State != string(StateDone) {
				t.Fatalf("iter %d: do %q must complete cleanly, got state=%q err=%q", i, sg.Next, dr.State, dr.Error)
			}
			s.didStep = sg.Next
		}
		journey = append(journey, s)

		// Assert the loop is at the expected rung on BOTH axes.
		if s.stage != wantStage[i] {
			t.Fatalf("iter %d: observed stage want %q, got %q", i, wantStage[i], s.stage)
		}
		if s.next != wantNext[i] {
			t.Fatalf("iter %d: next-best want %q, got %q", i, wantNext[i], s.next)
		}
		if s.done != i {
			t.Fatalf("iter %d: launch progress done want %d, got %d", i, i, s.done)
		}
	}

	// The journey CONVERGED: stage climbed formed→launched→activated→scaling, next-step
	// walked go-live→acquire→monetize→done, progress 0→3, and 3 real Campaign docs landed.
	final := journey[len(journey)-1]
	if final.stage != StageScaling || final.next != "" || final.done != 3 {
		t.Fatalf("loop must converge to scaling/complete/3-done, got %+v", final)
	}
	if docs, _ := framework.Search(ctx, org, content.DocTypeCampaign, nil, 20); len(docs) != 3 {
		t.Fatalf("3 real work-product docs must have landed, got %d", len(docs))
	}
	for _, s := range journey {
		t.Logf("LOOP iter %d | observe stage=%-9s | suggest next=%-9q | progress %d/%d | docs=%d | executed=%q",
			s.iter, s.stage, s.next, s.done, s.total, s.docs, s.didStep)
	}
	t.Logf("AUTONOMOUS LOOP CONVERGED: stage formed→launched→activated→scaling; next go-live→acquire→monetize→∅; 3 real Campaign docs; machine self-steered across 4 iterations")
}

// ── helpers ──────────────────────────────────────────────────────────────────

// reqJSON is req() renamed for readability at the call sites here; it is the org-scoped
// request helper (validated X-Org-Id/X-User-Id) from harness_test.go.
func reqJSON(t *testing.T, app *zip.App, method, path, org string, body any) httpResult {
	return req(t, app, method, path, org, body)
}

func formationStage(t *testing.T, body []byte) string {
	t.Helper()
	var v struct {
		Formation struct {
			Stage string `json:"stage"`
		} `json:"formation"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode formation: %v (%s)", err, body)
	}
	return v.Formation.Stage
}

func profileOf(t *testing.T, app *zip.App, org string) profileResponse {
	t.Helper()
	r := reqJSON(t, app, http.MethodGet, "/v1/guide/profile", org, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("profile %s want 200, got %d (%s)", org, r.Code, r.Body)
	}
	return decode[profileResponse](t, r.Body)
}

// strategiesResult is the decoded /v1/guide/strategies body plus test conveniences.
type strategiesResult struct {
	Stage      string         `json:"stage"`
	Count      int            `json:"count"`
	Strategies []strategyView `json:"strategies"`
}

func (s strategiesResult) has(id string) bool {
	for _, st := range s.Strategies {
		if st.ID == id {
			return true
		}
	}
	return false
}

// strategiesOf reads GET /v1/guide/strategies for org on the two filter axes the
// endpoint exposes. An empty stage/category omits that query param entirely, so the
// server applies its own observed default rather than an empty-string filter.
func strategiesOf(t *testing.T, app *zip.App, org, stage, category string) strategiesResult {
	t.Helper()
	q := url.Values{}
	if stage != "" {
		q.Set("stage", stage)
	}
	if category != "" {
		q.Set("category", category)
	}
	path := "/v1/guide/strategies"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	r := reqJSON(t, app, http.MethodGet, path, org, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("strategies %s want 200, got %d (%s)", org, r.Code, r.Body)
	}
	return decode[strategiesResult](t, r.Body)
}
