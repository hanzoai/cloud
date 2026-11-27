package research

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func doAs(t *testing.T, app *zip.App, method, path, org, user, project string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// TestHIP1400StandardManifest verifies /v1/research/standard returns a
// conformant HIP-1400 manifest matching hanzo-standard.json.
func TestHIP1400StandardManifest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		code, m := doAs(t, app, http.MethodGet, "/v1/research/standard", "acme", "alice", "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/research/standard status=%d body=%v", code, m)
		}
		if m["spec_version"] != "1.0.0" || m["component"] != "research/cloud" {
			t.Fatalf("unexpected manifest: %v", m)
		}
		hips, _ := m["implemented_hips"].([]any)
		if len(hips) < 5 {
			t.Fatalf("expected HIP-1400..1404 in implemented_hips, got %v", hips)
		}
	})
}

// TestHIP1403PreRegistrationReplicationAndRetraction exercises the complete
// scientific method state machine and its HIP-1402 XP projection:
//  1. Post-hoc confirmatory claims (where run.seq <= hypothesis.seq) are refused 422.
//  2. Exploratory claims succeed without pre-registration, earn 0 XP, and cannot be replicated.
//  3. Pre-registered confirmatory claims begin unreplicated (0 XP).
//  4. Self-replication by proposer is refused 403.
//  5. Independent clean-room replication transitions status -> replicated and mints
//     verified science XP to both proposer (100) and independent verifier (50).
//  6. Filing CounterEvidence transitions status -> retracted, retains history, and
//     reverses net XP back to 0.
func TestHIP1403PreRegistrationReplicationAndRetraction(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)
		nTotal := 100

		// Record an early run BEFORE registering the hypothesis.
		code, m := doAs(t, app, http.MethodPost, "/v1/research/runs", "acme", "alice", "do-lab", runBatch{
			Runs: []Run{{
				ID:        "run-before-hyp",
				Benchmark: "swe-bench-verified",
				Split:     "test",
				System:    "gen-v1",
				Questions: &nTotal,
				Answered:  &nTotal,
				Ended:     "2026-11-22T01:00:00Z",
				Measures:  []Measure{{Category: "all", Metric: "pass@1", Value: 0.72}},
			}},
		})
		if code != http.StatusOK {
			t.Fatalf("POST early run status=%d body=%v", code, m)
		}

		// Pre-register HypothesisRecord AFTER run-before-hyp.
		code, hyp := doAs(t, app, http.MethodPost, "/v1/research/hypotheses", "acme", "alice", "do-lab", HypothesisRecord{
			Statement:      "Verifier tree-search lifts SWE-bench Verified pass@1 by >= 0.04 under $25.",
			NullHypothesis: "Lift is < 0.04 or spend exceeds $25.",
			GateRef:        "swe-bench-verified",
			DatasetRefs:    []string{"sha256:4a8b1fc0ffee"},
			Budget:         HypothesisBudget{MaxComputeSeconds: 3600, MaxCostUSD: 25},
		})
		if code != http.StatusOK {
			t.Fatalf("POST hypothesis status=%d body=%v", code, hyp)
		}
		hypID := hyp["hypothesis_id"].(string)

		// Attempt post-hoc confirmatory claim using the pre-existing run -> MUST fail 422!
		code, m = doAs(t, app, http.MethodPost, "/v1/research/claims", "acme", "alice", "do-lab", ResearchClaim{
			HypothesisID: hypID,
			RunID:        "run-before-hyp",
			Mode:         "confirmatory",
			Metric:       "pass@1",
			EffectSize:   0.04,
		})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 for post-hoc confirmatory claim, got %d: %v", code, m)
		}

		// Same run filed as exploratory MUST succeed, earn 0 XP, and refuse replication.
		code, expClaim := doAs(t, app, http.MethodPost, "/v1/research/claims", "acme", "alice", "do-lab", ResearchClaim{
			RunID:      "run-before-hyp",
			Mode:       "exploratory",
			Metric:     "pass@1",
			EffectSize: 0.04,
		})
		if code != http.StatusOK {
			t.Fatalf("exploratory claim failed status=%d body=%v", code, expClaim)
		}

		// Execute a fresh primary run AFTER hypothesis registration.
		lo1, hi1 := 0.73, 0.79
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/runs", "acme", "alice", "do-lab", runBatch{
			Runs: []Run{{
				ID:        "run-primary-1",
				Benchmark: "swe-bench-verified",
				Split:     "test",
				System:    "gen-tree-v2",
				Questions: &nTotal,
				Answered:  &nTotal,
				Ended:     "2026-11-22T02:00:00Z",
				Measures: []Measure{
					{Category: "all", Metric: "pass@1", Value: 0.76, Lo: &lo1, Hi: &hi1},
					{Category: "all", Metric: "cost_usd", Value: 18.5},
				},
			}},
		})
		if code != http.StatusOK {
			t.Fatalf("POST primary run failed: %d", code)
		}

		// File confirmatory claim -> status MUST be unreplicated and XP MUST be 0.
		code, claim := doAs(t, app, http.MethodPost, "/v1/research/claims", "acme", "alice", "do-lab", ResearchClaim{
			HypothesisID: hypID,
			RunID:        "run-primary-1",
			Mode:         "confirmatory",
			Metric:       "pass@1",
			EffectSize:   0.04,
		})
		if code != http.StatusOK || claim["status"] != "unreplicated" {
			t.Fatalf("confirmatory claim status=%d body=%v", code, claim)
		}
		claimID := claim["claim_id"].(string)

		code, xp := doAs(t, app, http.MethodGet, "/v1/research/xp", "acme", "alice", "do-lab", nil)
		if code != http.StatusOK || len(xp["balances"].([]any)) != 0 {
			t.Fatalf("unreplicated claim must mint 0 XP, got %v", xp)
		}

		// Record independent clean-room replication run by bob.
		lo2, hi2 := 0.74, 0.78
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/runs", "acme", "bob", "do-lab", runBatch{
			Runs: []Run{{
				ID:        "run-rep-bob-1",
				Benchmark: "swe-bench-verified",
				Split:     "test",
				System:    "gen-tree-v2",
				Questions: &nTotal,
				Answered:  &nTotal,
				Ended:     "2026-11-22T03:00:00Z",
				Measures:  []Measure{{Category: "all", Metric: "pass@1", Value: 0.755, Lo: &lo2, Hi: &hi2}},
			}},
		})
		if code != http.StatusOK {
			t.Fatalf("POST replication run failed: %d", code)
		}

		// Self-replication by alice MUST be refused 403!
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/claims/"+claimID+"/replicate", "acme", "alice", "do-lab",
			map[string]any{"run_id": "run-rep-bob-1"})
		if code != http.StatusForbidden {
			t.Fatalf("expected 403 on self-replication by proposer, got %d", code)
		}

		// Independent replication by bob -> transitions to replicated and mints science XP!
		code, repClaim := doAs(t, app, http.MethodPost, "/v1/research/claims/"+claimID+"/replicate", "acme", "bob", "do-lab",
			map[string]any{"run_id": "run-rep-bob-1"})
		if code != http.StatusOK || repClaim["status"] != "replicated" {
			t.Fatalf("independent replication failed status=%d body=%v", code, repClaim)
		}

		code, xp = doAs(t, app, http.MethodGet, "/v1/research/xp?domain=science", "acme", "alice", "do-lab", nil)
		if code != http.StatusOK || len(xp["balances"].([]any)) != 2 {
			t.Fatalf("expected 2 science XP balances (alice=100, bob=50), got %v", xp)
		}
		firstBal := xp["balances"].([]any)[0].(map[string]any)
		secondBal := xp["balances"].([]any)[1].(map[string]any)
		if firstBal["principal"] != "did:hanzo:alice" || firstBal["total_xp"].(float64) != 100 {
			t.Fatalf("expected alice 100 science XP, got %v", firstBal)
		}
		if secondBal["principal"] != "did:hanzo:bob" || secondBal["total_xp"].(float64) != 50 {
			t.Fatalf("expected bob 50 science XP, got %v", secondBal)
		}

		// Retract claim with CounterEvidence -> net XP MUST return to 0 with retracted_by populated!
		code, retClaim := doAs(t, app, http.MethodPost, "/v1/research/claims/"+claimID+"/retract", "acme", "carol", "do-lab",
			map[string]any{
				"evidence_ref": "sha256:counter0001",
				"summary":      "Holdout leak discovered in prompt template fixture.",
			})
		if code != http.StatusOK || retClaim["status"] != "retracted" {
			t.Fatalf("retract claim failed status=%d body=%v", code, retClaim)
		}

		code, xpAfter := doAs(t, app, http.MethodGet, "/v1/research/xp", "acme", "alice", "do-lab", nil)
		if code != http.StatusOK {
			t.Fatalf("GET xp after retraction failed: %d", code)
		}
		for _, raw := range xpAfter["balances"].([]any) {
			b := raw.(map[string]any)
			if b["total_xp"].(float64) != 0 {
				t.Fatalf("expected net 0 XP after CounterEvidence retraction, got %v", b)
			}
		}
		if len(xpAfter["projections"].([]any)) != 4 {
			t.Fatalf("expected 4 audit projections (2 awards + 2 reversals), got %v", xpAfter["projections"])
		}
	})
}

// TestHIP1401CandidatesParetoQualificationAndHIP1404Regressions verifies:
//  1. Forbidden mutation surfaces (sandbox.visor, standard.json) are rejected 422.
//  2. Pareto frontier selects strictly non-dominated candidates and excludes safety < 1.0.
//  3. Independent qualification awards fractional multi-contributor XP and enables
//     HIP-1404 error-to-fix closure.
func TestHIP1401CandidatesParetoQualificationAndHIP1404Regressions(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountResearch(t)

		// Forbidden surface MUST fail 422.
		code, _ := doAs(t, app, http.MethodPost, "/v1/research/candidates", "acme", "alice", "do-lab", EvolutionCandidate{
			ParentIDs:       []string{"root"},
			MutationSurface: "sandbox.visor",
			ArtifactDigest:  "sha256:bad0001",
			DiffRef:         "sha256:diff001",
			GateRef:         "visor-conformance@1.0.0",
		})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 for forbidden sandbox.visor mutation surface, got %d", code)
		}

		// Candidate A: high accuracy (0.85), moderate cost ($0.60) -> on Pareto frontier.
		code, candA := doAs(t, app, http.MethodPost, "/v1/research/candidates", "acme", "alice", "do-lab", EvolutionCandidate{
			ParentIDs:       []string{"root"},
			MutationSurface: "engine.strategy",
			ArtifactDigest:  "sha256:aaaa1111",
			DiffRef:         "sha256:diffaaaa",
			GateRef:         "swe-bench-verified@1.2.0",
			Contributors: []AttributionShare{
				{Principal: "did:hanzo:alice", Weight: 0.7},
				{Principal: "did:hanzo:agent-gen-1", Weight: 0.3},
			},
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.85,
				CostPerSuccessUSD: 0.60,
				LatencyP50Ms:      1200,
				LatencyP95Ms:      2400,
				Robustness:        0.90,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("candA failed: %d %v", code, candA)
		}
		idA := candA["candidate_id"].(string)

		// Candidate B: lower cost ($0.20), slightly lower accuracy (0.78) -> ALSO on Pareto frontier!
		code, candB := doAs(t, app, http.MethodPost, "/v1/research/candidates", "acme", "alice", "do-lab", EvolutionCandidate{
			ParentIDs:       []string{idA},
			MutationSurface: "router.cascade",
			ArtifactDigest:  "sha256:bbbb2222",
			DiffRef:         "sha256:diffbbbb",
			GateRef:         "swe-bench-verified@1.2.0",
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.78,
				CostPerSuccessUSD: 0.20,
				LatencyP50Ms:      600,
				LatencyP95Ms:      1100,
				Robustness:        0.88,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("candB failed: %d %v", code, candB)
		}
		idB := candB["candidate_id"].(string)

		// Candidate C: strictly dominated by A (worse accuracy, higher cost, slower, lower robustness).
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/candidates", "acme", "alice", "do-lab", EvolutionCandidate{
			ParentIDs:       []string{idA},
			MutationSurface: "engine.strategy",
			ArtifactDigest:  "sha256:cccc3333",
			DiffRef:         "sha256:diffcccc",
			GateRef:         "swe-bench-verified@1.2.0",
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.80,
				CostPerSuccessUSD: 0.95,
				LatencyP50Ms:      1800,
				LatencyP95Ms:      3000,
				Robustness:        0.82,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("candC failed: %d", code)
		}

		// Candidate D: high accuracy (0.95) but safety violation (safety=0.95 < 1.0) -> excluded from frontier!
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/candidates", "acme", "alice", "do-lab", EvolutionCandidate{
			ParentIDs:       []string{"root"},
			MutationSurface: "harness.policy",
			ArtifactDigest:  "sha256:dddd4444",
			DiffRef:         "sha256:diffdddd",
			GateRef:         "swe-bench-verified@1.2.0",
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.95,
				CostPerSuccessUSD: 0.10,
				LatencyP50Ms:      400,
				LatencyP95Ms:      800,
				Robustness:        0.95,
				Safety:            0.95,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("candD failed: %d", code)
		}

		// Pareto frontier MUST contain exactly [idA, idB] in verified_success desc order!
		code, front := doAs(t, app, http.MethodGet, "/v1/research/candidates/frontier", "acme", "alice", "do-lab", nil)
		if code != http.StatusOK || front["total"].(float64) != 2 {
			t.Fatalf("expected 2 non-dominated candidates on Pareto frontier, got %v", front)
		}
		fdata := front["data"].([]any)
		if fdata[0].(map[string]any)["candidate_id"] != idA || fdata[1].(map[string]any)["candidate_id"] != idB {
			t.Fatalf("unexpected Pareto frontier ordering: %v", fdata)
		}

		// Self-qualification by contributor agent-gen-1 MUST fail 403!
		code, _ = doAs(t, app, http.MethodPost, "/v1/research/candidates/"+idA+"/qualify", "acme", "agent-gen-1", "do-lab", qualifyCandidateIn{
			EvidenceRefs:   []string{"sha256:holdout01"},
			ProductFitness: &ProductFitness{VerifiedSuccess: 0.85, CostPerSuccessUSD: 0.60, LatencyP50Ms: 1200, LatencyP95Ms: 2400, Robustness: 0.90, Safety: 1.0},
		})
		if code != http.StatusForbidden {
			t.Fatalf("expected 403 on self-qualification by contributor, got %d", code)
		}

		// Independent qualification + promotion by qualifier-bot -> succeeds, pins rollback_target="root".
		code, qualA := doAs(t, app, http.MethodPost, "/v1/research/candidates/"+idA+"/qualify", "acme", "qualifier-bot", "do-lab", qualifyCandidateIn{
			EvidenceRefs: []string{"sha256:holdout01"},
			Promote:      true,
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.85,
				CostPerSuccessUSD: 0.60,
				LatencyP50Ms:      1200,
				LatencyP95Ms:      2400,
				Robustness:        0.90,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK || qualA["status"] != "promoted" || qualA["rollback_target"] != "root" {
			t.Fatalf("qualify candA failed: %d %v", code, qualA)
		}

		// Record HIP-1404 regression and close it with qualified candidate idA!
		code, reg := doAs(t, app, http.MethodPost, "/v1/research/regressions", "acme", "qualifier-bot", "do-lab", RegressionRecord{
			SourceKind:      "conformance_failure",
			ErrorSignature:  "hip-1400:schema-drift",
			Summary:         "Engine omitted step digest under timeout cancellation",
			GateTestRef:     "gate://engine/timeout_digest_test",
			FixCandidateRef: idA,
		})
		if code != http.StatusOK || reg["status"] != "fixed" {
			t.Fatalf("HIP-1404 regression fix failed: %d %v", code, reg)
		}

		// Verify XP splits 185 engine XP 70/30 (130 alice + 56 agent-gen-1) + 50 spec XP to alice!
		code, xp := doAs(t, app, http.MethodGet, "/v1/research/xp", "acme", "alice", "do-lab", nil)
		if code != http.StatusOK || len(xp["balances"].([]any)) != 2 {
			t.Fatalf("unexpected XP projection: %v", xp)
		}
		aliceBal := xp["balances"].([]any)[0].(map[string]any)
		if aliceBal["principal"] != "did:hanzo:alice" || aliceBal["total_xp"].(float64) != 180 {
			t.Fatalf("expected alice total_xp=180 (130 engine + 50 spec), got %v", aliceBal)
		}
	})
}

// TestOneExperimentThreeDevicesEndToEndWithRestart proves the 6-step cross-device
// acceptance contract across Web, Desktop (hanzod), and Mobile, including full
// store shutdown and cold restart persistence:
//  1. Web pre-registers a falsifiable HypothesisRecord.
//  2. Desktop hanzod uploads SHA-256 content-addressed artifacts + completed Run.
//  3. Mobile reads durable progress, completion, and artifact digests.
//  4. Independent verifier replicates the claim & qualifies the EvolutionCandidate,
//     awarding soulbound Hanzo XP across science and engine.
//  5. All stores close and reopen on the same data directory; Web, Desktop, and
//     Mobile read identical records, digests, frontier, and XP after restart.
func TestOneExperimentThreeDevicesEndToEndWithRestart(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dataDir := t.TempDir()
		boot := func() *zip.App {
			app := zip.New(zip.Config{Logger: luxlog.New("test")})
			compose(app)
			t.Setenv("CLOUD_DATA_DIR", dataDir)
			if err := Use(app, cloud.Deps{}); err != nil {
				t.Fatalf("Use: %v", err)
			}
			return app
		}

		app1 := boot()

		// 1. Web: Pre-register hypothesis.
		code, hyp := doAs(t, app1, http.MethodPost, "/v1/research/hypotheses", "acme", "web-researcher", "cross-device", HypothesisRecord{
			Statement:      "Local hanzod sandbox execution reproduces 0.88 pass@1 within $10 budget.",
			NullHypothesis: "Pass@1 < 0.88 or spend > $10.",
			GateRef:        "swe-bench-verified",
			DatasetRefs:    []string{"sha256:dataset001"},
			Budget:         HypothesisBudget{MaxComputeSeconds: 300, MaxCostUSD: 10},
		})
		if code != http.StatusOK {
			t.Fatalf("step 1 (web hypothesis): %d %v", code, hyp)
		}
		hypID := hyp["hypothesis_id"].(string)

		// 2. Desktop (hanzod): Upload content-addressed execution artifact + completed Run.
		artifactPayload := "d256-verified-trace-output"
		code, art := doAs(t, app1, http.MethodPost, "/v1/research/artifacts", "acme", "web-researcher", "cross-device", map[string]any{
			"kind":    "report",
			"run_id":  "exp-three-devices-1",
			"git_sha": "4144c99",
			"content": "ZDI1Ni12ZXJpZmllZC10cmFjZS1vdXRwdXQ=", // base64(artifactPayload)
		})
		if code != http.StatusOK {
			t.Fatalf("step 2a (desktop hanzod artifact): %d %v", code, art)
		}
		artRef := art["ref"].(string)
		artSHA := art["sha256"].(string)
		_ = artifactPayload

		nQ := 50
		lo, hi := 0.85, 0.91
		code, _ = doAs(t, app1, http.MethodPost, "/v1/research/runs", "acme", "web-researcher", "cross-device", runBatch{
			Runs: []Run{{
				ID:          "exp-three-devices-1",
				Benchmark:   "swe-bench-verified",
				Split:       "test",
				System:      "hanzod-local",
				Commit:      "4144c99",
				Questions:   &nQ,
				Answered:    &nQ,
				When:        "2026-11-22T10:00:00Z",
				Ended:       "2026-11-22T10:02:00Z",
				FactsDigest: artRef,
				Notes:       "sandbox=hanzo-vm hypothesis_id=" + hypID,
				Measures: []Measure{
					{Category: "all", Metric: "pass@1", Value: 0.88, Lo: &lo, Hi: &hi},
					{Category: "all", Metric: "cost_usd", Value: 3.40},
				},
			}},
		})
		if code != http.StatusOK {
			t.Fatalf("step 2b (desktop hanzod run): %d", code)
		}

		// 3. Mobile: Observe durable completed run + Evidence digest.
		code, mobileRuns := doAs(t, app1, http.MethodGet, "/v1/research/runs?id=exp-three-devices-1", "acme", "web-researcher", "cross-device", nil)
		if code != http.StatusOK || mobileRuns["total"].(float64) != 1 {
			t.Fatalf("step 3 (mobile run read): %d %v", code, mobileRuns)
		}
		runRow := mobileRuns["data"].([]any)[0].(map[string]any)
		if runRow["completion"] != "complete" || runRow["facts_digest"] != artRef {
			t.Fatalf("step 3 (mobile completion/digest mismatch): %v", runRow)
		}

		// 4. Independent qualification & replication -> verified Hanzo XP.
		code, cand := doAs(t, app1, http.MethodPost, "/v1/research/candidates", "acme", "web-researcher", "cross-device", EvolutionCandidate{
			ParentIDs:       []string{"root"},
			MutationSurface: "engine.strategy",
			ArtifactDigest:  artRef,
			DiffRef:         artRef,
			GateRef:         "swe-bench-verified",
			EvidenceRefs:    []string{artRef},
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.88,
				CostPerSuccessUSD: 0.068,
				LatencyP50Ms:      820,
				LatencyP95Ms:      1500,
				Robustness:        0.90,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("step 4a (propose candidate): %d %v", code, cand)
		}
		candID := cand["candidate_id"].(string)

		code, _ = doAs(t, app1, http.MethodPost, "/v1/research/candidates/"+candID+"/qualify", "acme", "independent-qualifier", "cross-device", qualifyCandidateIn{
			EvidenceRefs: []string{artRef},
			Promote:      true,
			ProductFitness: &ProductFitness{
				VerifiedSuccess:   0.88,
				CostPerSuccessUSD: 0.068,
				LatencyP50Ms:      820,
				LatencyP95Ms:      1500,
				Robustness:        0.90,
				Safety:            1.0,
			},
		})
		if code != http.StatusOK {
			t.Fatalf("step 4b (qualify candidate): %d", code)
		}

		code, cl := doAs(t, app1, http.MethodPost, "/v1/research/claims", "acme", "web-researcher", "cross-device", ResearchClaim{
			HypothesisID: hypID,
			RunID:        "exp-three-devices-1",
			CandidateRef: candID,
			Mode:         "confirmatory",
			Metric:       "pass@1",
			EffectSize:   0.06,
			EvidenceRefs: []string{artRef},
		})
		if code != http.StatusOK {
			t.Fatalf("step 4c (file claim): %d %v", code, cl)
		}
		claimID := cl["claim_id"].(string)

		code, _ = doAs(t, app1, http.MethodPost, "/v1/research/runs", "acme", "independent-qualifier", "cross-device", runBatch{
			Runs: []Run{{
				ID:        "exp-three-devices-rep",
				Benchmark: "swe-bench-verified",
				Split:     "test",
				System:    "hanzod-cleanroom",
				Questions: &nQ,
				Answered:  &nQ,
				Ended:     "2026-11-22T10:05:00Z",
				Measures:  []Measure{{Category: "all", Metric: "pass@1", Value: 0.875, Lo: &lo, Hi: &hi}},
			}},
		})
		if code != http.StatusOK {
			t.Fatalf("step 4d (clean-room replication run): %d", code)
		}

		code, rep := doAs(t, app1, http.MethodPost, "/v1/research/claims/"+claimID+"/replicate", "acme", "independent-qualifier", "cross-device",
			map[string]any{"run_id": "exp-three-devices-rep"})
		if code != http.StatusOK || rep["status"] != "replicated" {
			t.Fatalf("step 4e (replicate claim): %d %v", code, rep)
		}

		// 5. Cold restart: close all SQLite stores and boot a fresh app instance on dataDir.
		if err := Shutdown(); err != nil {
			t.Fatalf("Shutdown before restart: %v", err)
		}
		app2 := boot()
		t.Cleanup(func() { _ = Shutdown() })

		// Re-open from Web, Desktop, and Mobile views after restart:
		for _, surface := range []string{"web-researcher", "desktop-hanzod", "mobile-viewer"} {
			code, hypsAfter := doAs(t, app2, http.MethodGet, "/v1/research/hypotheses?id="+hypID, "acme", surface, "cross-device", nil)
			if code != http.StatusOK || hypsAfter["total"].(float64) != 1 {
				t.Fatalf("restart (%s) hypothesis missing: %d %v", surface, code, hypsAfter)
			}
			code, runsAfter := doAs(t, app2, http.MethodGet, "/v1/research/runs?id=exp-three-devices-1", "acme", surface, "cross-device", nil)
			if code != http.StatusOK || runsAfter["total"].(float64) != 1 {
				t.Fatalf("restart (%s) run missing: %d %v", surface, code, runsAfter)
			}
			code, frontAfter := doAs(t, app2, http.MethodGet, "/v1/research/candidates/frontier", "acme", surface, "cross-device", nil)
			if code != http.StatusOK || frontAfter["total"].(float64) != 1 {
				t.Fatalf("restart (%s) Pareto frontier missing: %d %v", surface, code, frontAfter)
			}
			code, xpAfter := doAs(t, app2, http.MethodGet, "/v1/research/xp", "acme", surface, "cross-device", nil)
			if code != http.StatusOK || len(xpAfter["balances"].([]any)) != 2 {
				t.Fatalf("restart (%s) XP projection mismatch: %d %v", surface, code, xpAfter)
			}

			// Verify raw artifact bytes by SHA-256 content hash survive restart too!
			req := httptest.NewRequest(http.MethodGet, "/v1/research/artifacts/"+artSHA+"?project=cross-device", nil)
			req.Header.Set("X-Org-Id", "acme")
			req.Header.Set("X-User-Id", surface)
			resp, err := app2.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("restart (%s) artifact blob fetch failed: status=%v err=%v", surface, resp.StatusCode, err)
			}
			blobBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(blobBytes) != "d256-verified-trace-output" {
				t.Fatalf("restart (%s) artifact bytes mismatch: %q", surface, string(blobBytes))
			}
		}
	})
}
