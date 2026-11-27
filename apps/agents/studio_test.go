package agents

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestFirstSessionResearchBriefAndSelectiveRetry exercises Order 3
// and the operator's first session end-to-end:
//  1. Verify the seven studio functions and versioned shared templates.
//  2. Approve and apply the 7-function roster (editing a specialist role), and
//     verify idempotent re-application without duplicating agents or budgets.
//  3. Submit the autonomous lab scheduling research brief to the venture lead
//     with two specialists, where one specialist fails on the first pass.
//  4. Verify completed specialist work is preserved and RerunBrief re-executes
//     ONLY the failed specialist, producing the lead synthesis with citations,
//     unsupported claims, open questions, and per-agent attribution.
func TestFirstSessionResearchBriefAndSelectiveRetry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &fakeAI{
			content: "Laboratory time-motion studies support 15-minute block scheduling for automated assay runs [cite:doi:10.2460/javma.258.4.391] [cite:pmid:34198712]. " +
				"[unsupported:Vendor claim of 60% reduction in DVM overtime lacks a peer-reviewed control group]. " +
				"Open question: How should urgent walk-in triage slots be sized across 2-DVM vs 6-DVM clinics?",
		}
		app := mountApp(t, ai)
		const ventureOrg = "venture-research-workflow"

		// 1. Inspect the 7 studio functions & versioned templates.
		code, body := do(t, app, http.MethodGet, "/v1/agent/roster/templates", ventureOrg, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/agent/roster/templates: %d (%s)", code, body)
		}
		var tplOut rosterTemplatesOut
		if err := json.Unmarshal(body, &tplOut); err != nil {
			t.Fatalf("decode templates: %v", err)
		}
		if tplOut.Version != StudioTemplateVersion || len(tplOut.Functions) != 7 || len(tplOut.Templates) != 8 {
			t.Fatalf("want 7 functions and 8 templates (1 lead + 7 specialists), got functions=%d templates=%d", len(tplOut.Functions), len(tplOut.Templates))
		}

		// 2. Ben edits the regulatory-clinical specialist role and approves the 7-function roster.
		code, body = do(t, app, http.MethodPost, "/v1/agent/roster", ventureOrg, map[string]any{
			"venture":          "vet-clinic-scheduling",
			"seedDefaultSeven": true,
			"roles": []map[string]any{
				{
					"name":      "specialist-regulatory-clinical",
					"function":  FuncRegulatoryClinical,
					"manager":   "venture-lead",
					"expertise": "Experimental workflow trials, reproducibility guidelines, and instrument scheduling evidence.",
				},
			},
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/agent/roster: %d (%s)", code, body)
		}
		var roster1 applyRosterOut
		if err := json.Unmarshal(body, &roster1); err != nil {
			t.Fatalf("decode roster1: %v", err)
		}
		if roster1.Created != 8 || roster1.Updated != 0 || len(roster1.Agents) != 8 {
			t.Fatalf("initial roster want created=8 updated=0, got created=%d updated=%d", roster1.Created, roster1.Updated)
		}

		// Re-applying the roster (e.g. after an interrupted launch) updates in place without duplicating agents.
		code, body = do(t, app, http.MethodPost, "/v1/agent/roster", ventureOrg, map[string]any{
			"venture":          "vet-clinic-scheduling",
			"seedDefaultSeven": true,
		})
		if code != http.StatusOK {
			t.Fatalf("repeat POST /v1/agent/roster: %d (%s)", code, body)
		}
		var roster2 applyRosterOut
		_ = json.Unmarshal(body, &roster2)
		if roster2.Created != 0 || roster2.Updated != 8 {
			t.Fatalf("idempotent roster re-apply want created=0 updated=8, got created=%d updated=%d", roster2.Created, roster2.Updated)
		}

		// 3. Submit the operator's autonomous lab scheduling brief, injecting a transient failure on the 2nd specialist.
		code, body = do(t, app, http.MethodPost, "/v1/agent/briefs", ventureOrg, map[string]any{
			"venture":     "vet-clinic-scheduling",
			"title":       "Autonomous Lab Scheduling Workflow Evidence",
			"question":    "Which published evidence supports a proposed time-saving change to an experimental laboratory's scheduling workflow?",
			"lead":        "venture-lead",
			"specialists": []string{"specialist-regulatory-clinical", "specialist-market-competitive"},
			"failOnce":    []string{"specialist-market-competitive"},
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/agent/briefs: %d (%s)", code, body)
		}
		var brief1 BriefRecord
		if err := json.Unmarshal(body, &brief1); err != nil {
			t.Fatalf("decode brief1: %v", err)
		}
		if brief1.Status != "partial_failure" {
			t.Fatalf("brief with failed specialist want status=partial_failure, got %q", brief1.Status)
		}
		if len(brief1.Tasks) != 2 || brief1.Tasks[0].Status != "done" || brief1.Tasks[1].Status != "error" {
			t.Fatalf("tasks state want [done, error], got %+v", brief1.Tasks)
		}
		firstSpecialistRunID := brief1.Tasks[0].RunID

		// 4. Rerun the brief: only the failed specialist task must run; the completed one is preserved.
		code, body = do(t, app, http.MethodPost, "/v1/agent/briefs/"+brief1.ID+"/rerun", ventureOrg, nil)
		if code != http.StatusOK {
			t.Fatalf("POST /v1/agent/briefs/%s/rerun: %d (%s)", brief1.ID, code, body)
		}
		var brief2 BriefRecord
		if err := json.Unmarshal(body, &brief2); err != nil {
			t.Fatalf("decode brief2: %v", err)
		}
		if brief2.Status != "completed" {
			t.Fatalf("rerun brief want status=completed, got %q", brief2.Status)
		}
		if brief2.SkippedCompleted != 1 || brief2.RerunsPerformed != 1 {
			t.Fatalf("selective rerun want skippedCompleted=1 rerunsPerformed=1, got skipped=%d reruns=%d",
				brief2.SkippedCompleted, brief2.RerunsPerformed)
		}
		if brief2.Tasks[0].Attempts != 1 || brief2.Tasks[0].RunID != firstSpecialistRunID {
			t.Fatalf("completed specialist must not be re-executed: attempts=%d runID=%q (want %q)",
				brief2.Tasks[0].Attempts, brief2.Tasks[0].RunID, firstSpecialistRunID)
		}
		if brief2.Tasks[1].Attempts != 2 || brief2.Tasks[1].Status != "done" {
			t.Fatalf("failed specialist must succeed on attempt 2: %+v", brief2.Tasks[1])
		}
		if len(brief2.Citations) == 0 || len(brief2.UnsupportedClaims) == 0 || len(brief2.OpenQuestions) == 0 {
			t.Fatalf("completed brief must carry citations, unsupportedClaims, and openQuestions: %+v", brief2)
		}
		if len(brief2.Attribution) != 3 {
			t.Fatalf("attribution must record both specialists + lead (3 entries), got %d", len(brief2.Attribution))
		}

	})
}

// TestNightlyRoleProposalsApproveOnceAndRejectWithoutAgent verifies Order 4:
// nightly review generates role candidates with examples, cost estimate, and
// reporting line; approving a candidate creates the agent once (idempotent on
// repeat approval); rejecting records the decision without creating an agent.
func TestNightlyRoleProposalsApproveOnceAndRejectWithoutAgent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "ok"})
		const ventureOrg = "venture-research-workflow"

		// Seed venture-lead so approved roles link to their manager's lineage.
		do(t, app, http.MethodPost, "/v1/agent/roster", ventureOrg, map[string]any{
			"venture":          "vet-clinic-scheduling",
			"seedDefaultSeven": true,
		})

		code, body := do(t, app, http.MethodPost, "/v1/agent/proposals/review", ventureOrg, map[string]any{
			"venture": "vet-clinic-scheduling",
			"observedGaps": []map[string]any{
				{
					"function":          FuncProductEngineering,
					"roleName":          "specialist-vet-pims-hl7",
					"title":             "Laboratory LIMS & Telemetry Integration Specialist",
					"topic":             "laboratory-lims-telemetry-sync",
					"reportingLine":     "venture-lead",
					"examples":          []string{"Cornerstone/ezyVet writeback latency gap in brief_01", "Reviewer spent 1,420s correcting appointment slot locks"},
					"failureCount":      3,
					"reviewSeconds":     1420,
					"estimatedMicroUsd": 18_000_000,
				},
				{
					"function":          FuncFinanceFundraising,
					"roleName":          "specialist-equine-insurance",
					"title":             "Equine Insurance Underwriting Specialist",
					"topic":             "equine-underwriting",
					"reportingLine":     "venture-lead",
					"examples":          []string{"Out-of-scope equine claim in small-animal clinic brief"},
					"failureCount":      2,
					"reviewSeconds":     300,
					"estimatedMicroUsd": 12_000_000,
				},
			},
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/agent/proposals/review: %d (%s)", code, body)
		}
		var revOut reviewProposalsOut
		if err := json.Unmarshal(body, &revOut); err != nil {
			t.Fatalf("decode reviewProposalsOut: %v", err)
		}
		if revOut.Created != 2 || len(revOut.Proposals) != 2 {
			t.Fatalf("want 2 proposals created, got %+v", revOut)
		}
		pApprove := revOut.Proposals[0]
		pReject := revOut.Proposals[1]
		if len(pApprove.Examples) == 0 || pApprove.EstimatedMicroUSD != 18_000_000 || pApprove.ReportingLine != "venture-lead" {
			t.Fatalf("proposal missing required examples, cost estimate, or reporting line: %+v", pApprove)
		}

		// Approve pApprove once -> creates agent.
		code, body = do(t, app, http.MethodPost, "/v1/agent/proposals/"+pApprove.ID+"/decide", ventureOrg, map[string]any{
			"decision": "approve",
			"reason":   "Repeated PIMS integration review bottleneck confirmed by the studio operator.",
		})
		if code != http.StatusOK {
			t.Fatalf("approve proposal: %d (%s)", code, body)
		}
		var decided1 RoleProposal
		_ = json.Unmarshal(body, &decided1)
		if decided1.Status != "approved" || decided1.AgentID == "" {
			t.Fatalf("approved proposal must set status=approved and agentId, got %+v", decided1)
		}

		// Approving the same proposal a second time is idempotent and returns the same AgentID.
		code, body = do(t, app, http.MethodPost, "/v1/agent/proposals/"+pApprove.ID+"/decide", ventureOrg, map[string]any{
			"decision": "approve",
		})
		if code != http.StatusOK {
			t.Fatalf("repeat approve proposal: %d (%s)", code, body)
		}
		var decidedAgain RoleProposal
		_ = json.Unmarshal(body, &decidedAgain)
		if decidedAgain.AgentID != decided1.AgentID {
			t.Fatalf("repeat approval must not create a second agent: first=%q second=%q", decided1.AgentID, decidedAgain.AgentID)
		}

		// Reject pReject -> records rejection without creating an agent.
		code, body = do(t, app, http.MethodPost, "/v1/agent/proposals/"+pReject.ID+"/decide", ventureOrg, map[string]any{
			"decision": "reject",
			"reason":   "Out of scope for small-animal scheduling pilot.",
		})
		if code != http.StatusOK {
			t.Fatalf("reject proposal: %d (%s)", code, body)
		}
		var rejected RoleProposal
		_ = json.Unmarshal(body, &rejected)
		if rejected.Status != "rejected" || rejected.AgentID != "" {
			t.Fatalf("rejected proposal must have status=rejected and empty agentId: %+v", rejected)
		}
		if getCode, _ := do(t, app, http.MethodGet, "/v1/agent/"+pReject.Name, ventureOrg, nil); getCode != http.StatusNotFound {
			t.Fatalf("rejected proposal must NOT create an agent, GET /v1/agent/%s returned %d", pReject.Name, getCode)
		}

	})
}

// TestPortfolioScaleNineVentures1133RoleDefinitions verifies Order 5 scale
// acceptance: 9 sample venture workspaces with 1,133 role definitions distributed
// among them (8 ventures x 126 roles + 1 venture x 125 roles = 1,133 roles),
// exercising role enrollment and brief execution across all 9 workspaces.
func TestPortfolioScaleNineVentures1133RoleDefinitions(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{
			content: "Cited finding [cite:doi:10.2460/javma.258.4.391]. [unsupported:Unverified vendor claim]. Open question: Clinic staffing ratio?",
		})
		funcs := studioFunctionsCatalog()
		totalRoles := 0

		for v := 1; v <= 9; v++ {
			ventureOrg := fmt.Sprintf("studio-venture-%d", v)
			targetCount := 126
			if v == 9 {
				targetCount = 125 // 8*126 + 125 = 1,133 role definitions across 9 ventures
			}

			// 1 lead + 7 function leads + (targetCount - 8) sub-specialists reporting to the 7 function leads.
			roles := make([]map[string]any, 0, targetCount-8)
			for i := 0; i < targetCount-8; i++ {
				fn := funcs[i%len(funcs)]
				roles = append(roles, map[string]any{
					"name":     fmt.Sprintf("role-%s-%03d", fn.ID[:6], i+1),
					"function": fn.ID,
					"manager":  "specialist-" + fn.ID,
					"duties":   fmt.Sprintf("Sub-specialist #%d in %s.", i+1, fn.Title),
				})
			}

			code, body := do(t, app, http.MethodPost, "/v1/agent/roster", ventureOrg, map[string]any{
				"venture":          ventureOrg,
				"seedDefaultSeven": true,
				"roles":            roles,
			})
			if code != http.StatusOK {
				t.Fatalf("venture %d roster failed: %d (%s)", v, code, body)
			}
			var out applyRosterOut
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("venture %d decode roster: %v", v, err)
			}
			if out.Created != targetCount {
				t.Fatalf("venture %d want %d created roles, got %d", v, targetCount, out.Created)
			}
			totalRoles += out.Created

			// Execute a research brief in each venture workspace across its lead and two specialists.
			bCode, bBody := do(t, app, http.MethodPost, "/v1/agent/briefs", ventureOrg, map[string]any{
				"venture":     ventureOrg,
				"title":       fmt.Sprintf("Venture %d Evidence Brief", v),
				"question":    "Which published evidence supports the proposed workflow change?",
				"lead":        "venture-lead",
				"specialists": []string{"specialist-regulatory-clinical", "specialist-market-competitive"},
			})
			if bCode != http.StatusOK {
				t.Fatalf("venture %d brief failed: %d (%s)", v, bCode, bBody)
			}
			var bRec BriefRecord
			_ = json.Unmarshal(bBody, &bRec)
			if bRec.Status != "completed" || bRec.TotalMicroUSD <= 0 {
				t.Fatalf("venture %d brief did not complete cleanly: %+v", v, bRec)
			}
		}

		if totalRoles != 1133 {
			t.Fatalf("total enrolled roles across 9 ventures want 1133, got %d", totalRoles)
		}

	})
}
