package guide

import (
	"github.com/hanzoai/cloud/internal/sqltest"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestOneHealthGuidePlaybookOrderAndBlocking verifies that PUT /v1/guide/curriculum
// accepts one-health-guide.yaml, exposes all four phases and ten steps, blocks the
// final portfolio-decision step at the start, and resolves all ten steps in exact
// dependency order.
func TestOneHealthGuidePlaybookOrderAndBlocking(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		const ventureOrg = "one-health-vet-scheduling"

		raw, err := os.ReadFile(filepath.Join("brands", "one-health.yaml"))
		if err != nil {
			t.Fatalf("read brands/one-health.yaml: %v", err)
		}

		// Submit one-health-guide.yaml as the per-org curriculum override.
		putResp := reqRaw(t, app, http.MethodPut, "/v1/guide/curriculum", map[string]string{
			"X-Org-Id":     ventureOrg,
			"X-User-Id":    "u-" + ventureOrg,
			"Content-Type": "application/yaml",
		}, raw)
		if putResp.Code != http.StatusOK {
			t.Fatalf("PUT /v1/guide/curriculum want 200, got %d (%s)", putResp.Code, putResp.Body)
		}
		cv := decode[curriculumView](t, putResp.Body)
		if !cv.Custom {
			t.Fatal("PUT /v1/guide/curriculum must mark custom=true")
		}
		if cv.Curriculum.Version != "2026.11-one-health.1" {
			t.Fatalf("version want 2026.11-one-health.1, got %q", cv.Curriculum.Version)
		}
		if len(cv.Curriculum.Steps) != 10 {
			t.Fatalf("steps count want 10, got %d", len(cv.Curriculum.Steps))
		}

		// Final review step (portfolio-decision) must be blocked at the start.
		blockResp := req(t, app, http.MethodPost, "/v1/guide/steps/portfolio-decision/done", ventureOrg, nil)
		if blockResp.Code != http.StatusConflict {
			t.Fatalf("portfolio-decision at start want 409 Conflict, got %d (%s)", blockResp.Code, blockResp.Body)
		}

		wantOrder := []string{
			"studio-org",
			"venture-intake",
			"venture-playbook",
			"team-roster",
			"research-brief",
			"specialist-evidence",
			"lead-synthesis",
			"domain-review",
			"action-approval",
			"portfolio-decision",
		}

		for i, stepID := range wantOrder {
			cur := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", ventureOrg, nil).Body)
			if cur.Progress.Next != stepID {
				t.Fatalf("step %d: Progress.Next want %q, got %q", i+1, stepID, cur.Progress.Next)
			}
			if cur.Progress.Done != i {
				t.Fatalf("step %d: Progress.Done want %d, got %d", i+1, i, cur.Progress.Done)
			}
			doneResp := req(t, app, http.MethodPost, "/v1/guide/steps/"+stepID+"/done", ventureOrg, nil)
			if doneResp.Code != http.StatusOK {
				t.Fatalf("step %d (%s) done want 200, got %d (%s)", i+1, stepID, doneResp.Code, doneResp.Body)
			}
		}

		final := decode[overviewView](t, req(t, app, http.MethodGet, "/v1/guide", ventureOrg, nil).Body)
		if final.Progress.Done != 10 || final.Progress.Total != 10 || final.Progress.Percent != 100 || final.Progress.Next != "" {
			t.Fatalf("all 10 steps resolved: got %+v", final.Progress)
		}

		// Also verify the canonical testdata one-health-guide.yaml (oh-brief .. oh-review).
		canonicalRaw, err := os.ReadFile(filepath.Join("testdata", "one-health-guide.yaml"))
		if err != nil {
			t.Fatalf("read one-health-guide.yaml: %v", err)
		}
		const canonicalOrg = "one-health-canonical"
		putCanon := reqRaw(t, app, http.MethodPut, "/v1/guide/curriculum", map[string]string{
			"X-Org-Id":     canonicalOrg,
			"X-User-Id":    "u-" + canonicalOrg,
			"Content-Type": "application/yaml",
		}, canonicalRaw)
		if putCanon.Code != http.StatusOK {
			t.Fatalf("PUT canonical one-health-guide.yaml want 200, got %d (%s)", putCanon.Code, putCanon.Body)
		}
		if block := req(t, app, http.MethodPost, "/v1/guide/steps/oh-review/done", canonicalOrg, nil); block.Code != http.StatusConflict {
			t.Fatalf("oh-review at start want 409 Conflict, got %d", block.Code)
		}
		for _, id := range []string{"oh-brief", "oh-boundary", "oh-chart", "oh-role-specs", "oh-roster", "oh-run", "oh-evidence", "oh-decision", "oh-gap", "oh-review"} {
			if r := req(t, app, http.MethodPost, "/v1/guide/steps/"+id+"/done", canonicalOrg, nil); r.Code != http.StatusOK {
				t.Fatalf("step %s done want 200, got %d (%s)", id, r.Code, r.Body)
			}
		}
	})
}

// TestOneHealthVentureCreationIdempotentResumeAndIsolation verifies Order 2 acceptance:
// starting two ventures, resuming an interrupted/repeated launch without duplicating
// a team or budget, surfacing shared studio templates in both, and keeping each
// venture's progress and actions isolated inside its own organization store.
func TestOneHealthVentureCreationIdempotentResumeAndIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		const studioOrg = "one-health"

		v1Body := map[string]any{
			"slug":                  "vet-clinic-scheduling",
			"title":                 "Veterinary Clinic Scheduling Workflow",
			"thesis":                "AI triage + block scheduling cuts clinic wait times by >=15%.",
			"sponsor":               "Ben Lewis",
			"owner":                 "venture-lead",
			"initialBudgetMicroUSD": int64(500_000_000),
			"evidence":              []string{"doi:10.2460/javma.258.4.391"},
			"playbook":              "one-health",
			"teamSeeded":            true,
		}
		r1 := req(t, app, http.MethodPost, "/v1/guide/ventures", studioOrg, v1Body)
		if r1.Code != http.StatusOK {
			t.Fatalf("create venture 1 want 200, got %d (%s)", r1.Code, r1.Body)
		}
		v1 := decode[VentureRecord](t, r1.Body)
		if v1.Resumed {
			t.Fatal("first create must not report resumed=true")
		}
		if !v1.Setup.OrgProvisioned || !v1.Setup.PlaybookEnrolled || !v1.Setup.BudgetAllocated || !v1.Setup.TeamSeeded {
			t.Fatalf("venture 1 setup checkpoints incomplete: %+v", v1.Setup)
		}
		if len(v1.Templates) < 4 {
			t.Fatalf("shared studio templates must be visible on venture 1, got %d", len(v1.Templates))
		}

		// Repeating the launch with a different budget must resume without duplicating the initial budget.
		v1Repeat := map[string]any{
			"slug":                  "vet-clinic-scheduling",
			"title":                 "Veterinary Clinic Scheduling Workflow",
			"initialBudgetMicroUSD": int64(999_000_000),
			"evidence":              []string{"pmid:34198712"},
		}
		r1Again := req(t, app, http.MethodPost, "/v1/guide/ventures", studioOrg, v1Repeat)
		if r1Again.Code != http.StatusOK {
			t.Fatalf("repeat venture 1 want 200, got %d (%s)", r1Again.Code, r1Again.Body)
		}
		v1Resumed := decode[VentureRecord](t, r1Again.Body)
		if !v1Resumed.Resumed {
			t.Fatal("repeated launch must report resumed=true")
		}
		if v1Resumed.InitialBudgetMicroUSD != 500_000_000 {
			t.Fatalf("repeated launch must not overwrite/duplicate initialBudgetMicroUSD: got %d, want 500000000", v1Resumed.InitialBudgetMicroUSD)
		}
		if len(v1Resumed.Evidence) != 2 {
			t.Fatalf("repeated launch must merge seed evidence deduplicated: got %v", v1Resumed.Evidence)
		}

		// Start a second venture in the same studio portfolio.
		v2Body := map[string]any{
			"slug":                  "bovine-rapid-diagnostics",
			"title":                 "Bovine Point-of-Care Diagnostics",
			"thesis":                "Pen-side lateral flow + reader reduces herd antibiotic overuse.",
			"sponsor":               "Ben Lewis",
			"owner":                 "venture-lead",
			"initialBudgetMicroUSD": int64(300_000_000),
			"playbook":              "one-health",
		}
		r2 := req(t, app, http.MethodPost, "/v1/guide/ventures", studioOrg, v2Body)
		if r2.Code != http.StatusOK {
			t.Fatalf("create venture 2 want 200, got %d (%s)", r2.Code, r2.Body)
		}
		v2 := decode[VentureRecord](t, r2.Body)
		if len(v2.Templates) != len(v1.Templates) {
			t.Fatalf("shared studio templates must be visible in both ventures: v1=%d v2=%d", len(v1.Templates), len(v2.Templates))
		}

		// Complete step 1 ("studio-org") inside venture 1's isolated org and verify venture 2 is untouched.
		if resp := req(t, app, http.MethodPost, "/v1/guide/steps/studio-org/done", v1.VentureOrg, nil); resp.Code != http.StatusOK {
			t.Fatalf("complete studio-org in v1 want 200, got %d (%s)", resp.Code, resp.Body)
		}
		v1Check := decode[VentureRecord](t, req(t, app, http.MethodGet, "/v1/guide/ventures/vet-clinic-scheduling", studioOrg, nil).Body)
		v2Check := decode[VentureRecord](t, req(t, app, http.MethodGet, "/v1/guide/ventures/bovine-rapid-diagnostics", studioOrg, nil).Body)
		if v1Check.Progress.Done != 1 || v1Check.Progress.Next != "venture-intake" {
			t.Fatalf("v1 progress want done=1 next=venture-intake, got %+v", v1Check.Progress)
		}
		if v2Check.Progress.Done != 0 || v2Check.Progress.Next != "studio-org" {
			t.Fatalf("v2 progress must remain isolated at done=0 next=studio-org, got %+v", v2Check.Progress)
		}
	})
}

// TestOneHealthPortfolioOperationsPauseExportOffboardAndForm verifies Order 5 portfolio
// operations: telemetry sync, pause/resume, export bundle, entity formation link,
// offboarding, and the studio-wide GET /v1/guide/portfolio rollup.
func TestOneHealthPortfolioOperationsPauseExportOffboardAndForm(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		const studioOrg = "one-health"

		for _, slug := range []string{"vet-scheduling", "canine-oncology"} {
			r := req(t, app, http.MethodPost, "/v1/guide/ventures", studioOrg, map[string]any{
				"slug":                  slug,
				"title":                 slug,
				"initialBudgetMicroUSD": int64(250_000_000),
			})
			if r.Code != http.StatusOK {
				t.Fatalf("create %s: %d (%s)", slug, r.Code, r.Body)
			}
		}

		// Sync telemetry & advance vet-scheduling, then export, then form.
		syncResp := req(t, app, http.MethodPost, "/v1/guide/ventures/vet-scheduling/lifecycle", studioOrg, map[string]any{
			"action":           "advance",
			"spendMicroUSD":    int64(42_500_000),
			"pendingDecisions": 2,
			"failedRuns":       1,
			"latestEvidence":   "doi:10.2460/javma.258.4.391",
		})
		if syncResp.Code != http.StatusOK {
			t.Fatalf("advance vet-scheduling: %d (%s)", syncResp.Code, syncResp.Body)
		}

		expResp := req(t, app, http.MethodPost, "/v1/guide/ventures/vet-scheduling/lifecycle", studioOrg, map[string]any{
			"action": "export",
		})
		if expResp.Code != http.StatusOK {
			t.Fatalf("export vet-scheduling: %d (%s)", expResp.Code, expResp.Body)
		}
		expOut := decode[ventureLifecycleOut](t, expResp.Body)
		if expOut.Export == nil || len(expOut.Export.Steps) != 10 {
			t.Fatalf("export bundle must include 10 steps, got %+v", expOut.Export)
		}

		formResp := req(t, app, http.MethodPost, "/v1/guide/ventures/vet-scheduling/lifecycle", studioOrg, map[string]any{
			"action":      "form",
			"formationId": "form_delaware_c_01",
		})
		if formResp.Code != http.StatusOK {
			t.Fatalf("form vet-scheduling: %d (%s)", formResp.Code, formResp.Body)
		}

		// Pause and then offboard canine-oncology.
		if r := req(t, app, http.MethodPost, "/v1/guide/ventures/canine-oncology/lifecycle", studioOrg, map[string]any{"action": "pause"}); r.Code != http.StatusOK {
			t.Fatalf("pause canine-oncology: %d", r.Code)
		}
		if r := req(t, app, http.MethodPost, "/v1/guide/ventures/canine-oncology/lifecycle", studioOrg, map[string]any{"action": "offboard"}); r.Code != http.StatusOK {
			t.Fatalf("offboard canine-oncology: %d", r.Code)
		}

		port := decode[portfolioView](t, req(t, app, http.MethodGet, "/v1/guide/portfolio", studioOrg, nil).Body)
		if port.TotalVentures != 2 || port.FormedVentures != 1 || port.OffboardedVentures != 1 {
			t.Fatalf("portfolio counts mismatch: %+v", port)
		}
		if port.TotalBudgetMicroUSD != 500_000_000 || port.TotalSpendMicroUSD != 42_500_000 || port.PendingDecisions != 2 || port.FailedRuns != 1 {
			t.Fatalf("portfolio financial/decision rollup mismatch: %+v", port)
		}
	})
}

// TestOneHealthTeammateAssignedVenturesAndDisposableSeatRevocation verifies Order 1
// acceptance: a teammate invited to the studio can access only the ventures
// assigned to them, and revoking a disposable seat immediately removes access.
func TestOneHealthTeammateAssignedVenturesAndDisposableSeatRevocation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		const studioOrg = "one-health"
		const benUser = "ben"
		const disposableSeat = "seat-disposable"

		for _, slug := range []string{"vet-scheduling", "equine-imaging"} {
			r := req(t, app, http.MethodPost, "/v1/guide/ventures", studioOrg, map[string]any{
				"slug":    slug,
				"title":   slug,
				"sponsor": "one-health/" + benUser,
			})
			if r.Code != http.StatusOK {
				t.Fatalf("create %s: %d (%s)", slug, r.Code, r.Body)
			}
		}

		// Assign the disposable seat to vet-scheduling only.
		assignResp := req(t, app, http.MethodPost, "/v1/guide/ventures/vet-scheduling/lifecycle", studioOrg, map[string]any{
			"action": "assign_member",
			"member": "one-health/" + disposableSeat,
		})
		if assignResp.Code != http.StatusOK {
			t.Fatalf("assign_member want 200, got %d (%s)", assignResp.Code, assignResp.Body)
		}

		// Teammate lists ventures: sees only vet-scheduling, not equine-imaging.
		seatHeaders := map[string]string{
			"X-Org-Id":  studioOrg,
			"X-User-Id": disposableSeat,
		}
		listResp := reqRaw(t, app, http.MethodGet, "/v1/guide/ventures", seatHeaders, nil)
		if listResp.Code != http.StatusOK {
			t.Fatalf("teammate list ventures want 200, got %d (%s)", listResp.Code, listResp.Body)
		}
		vl := decode[ventureList](t, listResp.Body)
		if len(vl.Ventures) != 1 || vl.Ventures[0].Slug != "vet-scheduling" {
			t.Fatalf("teammate must see only assigned venture vet-scheduling, got %+v", vl.Ventures)
		}

		// Teammate cannot access unassigned equine-imaging (403 Forbidden).
		unassignedResp := reqRaw(t, app, http.MethodGet, "/v1/guide/ventures/equine-imaging", seatHeaders, nil)
		if unassignedResp.Code != http.StatusForbidden {
			t.Fatalf("teammate GET unassigned venture want 403 Forbidden, got %d (%s)", unassignedResp.Code, unassignedResp.Body)
		}

		// Revoke the disposable seat on vet-scheduling.
		revResp := req(t, app, http.MethodPost, "/v1/guide/ventures/vet-scheduling/lifecycle", studioOrg, map[string]any{
			"action": "revoke_member",
			"member": "one-health/" + disposableSeat,
		})
		if revResp.Code != http.StatusOK {
			t.Fatalf("revoke_member want 200, got %d (%s)", revResp.Code, revResp.Body)
		}

		// Disposable seat now gets 403 on vet-scheduling and sees 0 ventures.
		afterGet := reqRaw(t, app, http.MethodGet, "/v1/guide/ventures/vet-scheduling", seatHeaders, nil)
		if afterGet.Code != http.StatusForbidden {
			t.Fatalf("revoked seat GET vet-scheduling want 403 Forbidden, got %d (%s)", afterGet.Code, afterGet.Body)
		}
		afterList := decode[ventureList](t, reqRaw(t, app, http.MethodGet, "/v1/guide/ventures", seatHeaders, nil).Body)
		if len(afterList.Ventures) != 0 {
			t.Fatalf("revoked seat list ventures want 0, got %d", len(afterList.Ventures))
		}
	})
}
