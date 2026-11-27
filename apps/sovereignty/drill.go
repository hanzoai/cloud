// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// KillHanzoControlPlane shuts down every Hanzo-operated HTTP endpoint AND
// activates the HostBlocker Kill Switch so any subsequent attempt to reach
// api.hanzo.ai, *.hanzo.ai, or any Hanzo loopback node fails closed.
func KillHanzoControlPlane(hanzo *HanzoControlPlane, guard *HostBlocker) {
	if guard != nil {
		guard.ActivateKillSwitch()
	}
	if hanzo != nil {
		hanzo.Kill()
	}
}

// DeterministicModelWeights generates a reproducible open-weight binary
// payload for content-addressed SHA-256 verification across loopback mirrors.
func DeterministicModelWeights(modelID string, sizeBytes int) []byte {
	if sizeBytes < 256 {
		sizeBytes = 4096
	}
	buf := make([]byte, sizeBytes)
	seed := Keccak256Bytes([]byte("hip-1351:open-weights:v1:"), []byte(modelID))
	for off := 0; off < sizeBytes; off += 32 {
		seed = Keccak256Bytes(seed[:], []byte{byte(off >> 8), byte(off)})
		n := 32
		if off+n > sizeBytes {
			n = sizeBytes - off
		}
		copy(buf[off:off+n], seed[:n])
	}
	return buf
}

// RunNetworkExitDrill executes the complete 8-stage HIP-1351 Full Sovereignty
// and Network Exit acceptance pipeline over real loopback HTTP servers.
func RunNetworkExitDrill(domain EIP712Domain) (*NetworkExitReport, *NetworkForkBundle, error) {
	started := time.Now().UTC()
	guard := NewHostBlocker()
	defer guard.CloseIdleConnections()
	client := guard.NewHTTPClient(10 * time.Second)

	// Start Control Plane A (Hanzo convenience) and Control Plane B (Independent Community).
	hanzoPlane, err := NewHanzoControlPlane(domain, guard)
	if err != nil {
		return nil, nil, err
	}
	defer hanzoPlane.Kill()

	communityPlane, err := NewCommunityControlPlane("community-op-b", domain, client, 2)
	if err != nil {
		return nil, nil, err
	}
	defer communityPlane.Close()

	// Ephemeral runtime identities (zero hardcoded keys).
	publisherKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	tenantKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	providerAKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	govVoter2KP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	userDataKey, err := GenerateUserDataKey()
	if err != nil {
		return nil, nil, err
	}
	wrongThirdPartyKey, err := GenerateUserDataKey()
	if err != nil {
		return nil, nil, err
	}

	// Start initial Provider A (joined during Stage 1 before blackout).
	providerA := NewProviderNodeServer("provider-alpha-initial", providerAKP, domain, client)
	defer providerA.Close()

	const modelID = "zen-open-7b-deterministic-v1"
	modelBlob := DeterministicModelWeights(modelID, 16384)
	modelDigest := SHA256Hex(modelBlob)
	workspacePlaintext := []byte(`{"workspace":"sovereign-research-workspace","documents":["hip-1351-spec.md","model-eval-notes.json"],"ownerPolicy":"zero-custodial-escrow","version":1}`)
	const snapshotID = "snap-user-workspace-001"

	report := &NetworkExitReport{
		ProtocolVersion:     ProtocolVersion,
		StartedAt:           started,
		PostKillStagesTotal: 6, // Stages 3, 4, 5, 6, 7, 8 execute strictly after Kill Switch.
		Stages:              make([]DrillStageResult, 0, 8),
	}

	var providerB *ProviderNodeServer
	defer func() {
		if providerB != nil {
			providerB.Close()
		}
	}()

	// -------------------------------------------------------------------------
	// STAGE 1: Baseline with Hanzo convenience plane online
	// -------------------------------------------------------------------------
	s1Start := time.Now()
	s1HanzoBefore := guard.Metrics().HanzoCallsBeforeKill
	{
		// Verify Hanzo gateway is reachable before blackout.
		if _, err := doJSONRequest(client, http.MethodGet, hanzoPlane.Gateway.URL+"/v1/health", nil, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo gateway health: %w", err)
		}
		// Upload content-addressed weights to BOTH Hanzo mirror and Community mirror.
		if err := UploadModelBlob(client, hanzoPlane.Mirror.URL(), modelDigest, modelBlob); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo mirror upload: %w", err)
		}
		if err := UploadModelBlob(client, communityPlane.Mirror.URL(), modelDigest, modelBlob); err != nil {
			return nil, nil, fmt.Errorf("stage 1 community mirror upload: %w", err)
		}
		// Sign and publish ModelManifest (with portable community mirror + Hanzo mirror).
		manifest := ModelManifest{
			ModelID:      modelID,
			Version:      "1.0.0",
			License:      "Apache-2.0",
			DigestSHA256: modelDigest,
			SizeBytes:    int64(len(modelBlob)),
			Mirrors:      []string{communityPlane.Mirror.URL(), hanzoPlane.Mirror.URL()},
			IssuedAt:     time.Now().UTC().Unix(),
		}
		if err := SignModelManifest(domain, publisherKP, &manifest); err != nil {
			return nil, nil, err
		}
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Registry.URL()+"/v1/models", manifest, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo register model: %w", err)
		}
		for _, b := range communityPlane.BootstrapNodes {
			if _, err := doJSONRequest(client, http.MethodPost, b.URL()+"/v1/models", manifest, nil); err != nil {
				return nil, nil, fmt.Errorf("stage 1 community register model: %w", err)
			}
		}
		// Provider A loads verified weights and registers on Hanzo registry.
		if err := providerA.FetchAndVerifyModel(hanzoPlane.Mirror.URL(), modelDigest); err != nil {
			return nil, nil, fmt.Errorf("stage 1 providerA fetch model: %w", err)
		}
		adA, err := providerA.BuildSignedAdvertisement([]string{modelID}, 500, 600, time.Now().UTC())
		if err != nil {
			return nil, nil, err
		}
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Registry.URL()+"/v1/peers/register", adA, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo register providerA: %w", err)
		}
		// Fund tenant on both Hanzo facilitator and independent Community facilitator.
		mintPayload := MintRequest{Address: tenantKP.Address(), Amount: 10_000_000}
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Facilitator.URL()+"/v1/payments/mint", mintPayload, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo mint: %w", err)
		}
		if _, err := doJSONRequest(client, http.MethodPost, communityPlane.Facilitator.URL()+"/v1/payments/mint", mintPayload, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 community mint: %w", err)
		}
		// Execute & settle one baseline job through Hanzo convenience plane.
		baseReq := InferenceRequest{
			JobID:             "job-baseline-hanzo-001",
			TenantAddress:     tenantKP.Address(),
			ModelID:           modelID,
			ModelDigestSHA256: modelDigest,
			Prompt:            "Verify baseline execution before network exit drill",
			Seed:              1351,
			MaxTokens:         64,
			Nonce:             1,
			Timestamp:         time.Now().UTC().Unix(),
		}
		var baseReceipt InferenceExecutionReceipt
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Scheduler.URL()+"/v1/schedule", scheduleDispatchRequest{Request: baseReq}, &baseReceipt); err != nil {
			return nil, nil, fmt.Errorf("stage 1 baseline schedule: %w", err)
		}
		if err := VerifyExecutionReceiptOffline(domain, baseReq, modelBlob, baseReceipt); err != nil {
			return nil, nil, fmt.Errorf("stage 1 baseline verify: %w", err)
		}
		basePay := PaymentAuthorization{
			PaymentID:    "pay-baseline-hanzo-001",
			JobID:        baseReq.JobID,
			PayeeAddress: providerA.KeyPair.Address(),
			AmountAtomic: 500,
			Nonce:        1,
			ValidBefore:  time.Now().UTC().Add(10 * time.Minute).Unix(),
			ChainID:      domain.ChainID,
		}
		if err := SignPaymentAuthorization(domain, tenantKP, &basePay); err != nil {
			return nil, nil, err
		}
		var baseSettle SettlementReceipt
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Facilitator.URL()+"/v1/payments/settle", basePay, &baseSettle); err != nil {
			return nil, nil, fmt.Errorf("stage 1 baseline settle: %w", err)
		}
		// User encrypts workspace data client-side (zero custodial escrow) and stores portable envelope.
		env, err := EncryptUserData(
			domain,
			tenantKP,
			userDataKey,
			snapshotID,
			workspacePlaintext,
			[]string{communityPlane.Vault.URL(), hanzoPlane.Vault.URL()},
			[]string{"hanzo-convenience", communityPlane.OperatorID},
			time.Now().UTC().Unix(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("stage 1 encrypt user data: %w", err)
		}
		if _, err := doJSONRequest(client, http.MethodPost, hanzoPlane.Vault.URL()+"/v1/vault/snapshots", env, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 hanzo vault store: %w", err)
		}
		if _, err := doJSONRequest(client, http.MethodPost, communityPlane.Vault.URL()+"/v1/vault/snapshots", env, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 1 community vault replicate: %w", err)
		}
	}
	s1HanzoDelta := guard.Metrics().HanzoCallsBeforeKill - s1HanzoBefore
	report.Stages = append(report.Stages, DrillStageResult{
		Stage:                1,
		Name:                 "baseline_hanzo_online",
		Plane:                string(PlaneHanzo),
		Passed:               s1HanzoDelta > 0,
		DurationMs:           float64(time.Since(s1Start).Microseconds()) / 1000.0,
		HanzoCallsDuringStep: s1HanzoDelta,
		Detail:               fmt.Sprintf("baseline job settled & encrypted workspace stored (%d pre-kill Hanzo calls)", s1HanzoDelta),
	})

	// -------------------------------------------------------------------------
	// STAGE 2: Kill Switch — Total Hanzo Control Plane Blackout + DNS/Host Block
	// -------------------------------------------------------------------------
	s2Start := time.Now()
	KillHanzoControlPlane(hanzoPlane, guard)
	// Verify api.hanzo.ai AND the killed Hanzo loopback gateway fail closed.
	errAPI := guard.ProbeBlackoutDirect("https://api.hanzo.ai/v1/health")
	errGW := guard.ProbeBlackoutDirect(hanzoPlane.Gateway.URL + "/v1/health")
	blackoutProbePassed := errors.Is(errAPI, ErrHanzoHostBlocked) && errors.Is(errGW, ErrHanzoHostBlocked)
	// Measure Failover RTO: verify Community Bootstrap B is immediately ready with 0 Hanzo calls.
	if _, err := doJSONRequest(client, http.MethodGet, communityPlane.BootstrapNodes[0].URL()+"/v1/health", nil, nil); err != nil {
		return nil, nil, fmt.Errorf("stage 2 community readiness probe: %w", err)
	}
	rtoMs := float64(time.Since(s2Start).Nanoseconds()) / 1e6
	report.FailoverRTOMs = rtoMs
	report.HanzoBlackoutProbePass = blackoutProbePassed
	report.Stages = append(report.Stages, DrillStageResult{
		Stage:                2,
		Name:                 "kill_switch_hanzo_blackout",
		Plane:                "blackout_transition",
		Passed:               blackoutProbePassed && guard.Metrics().HanzoCallsAfterKill == 0,
		DurationMs:           rtoMs,
		HanzoCallsDuringStep: guard.Metrics().HanzoCallsAfterKill,
		Detail:               "all 6 Hanzo servers terminated; api.hanzo.ai & *.hanzo.ai hard-blocked; community plane live",
	})

	// Helper to record post-kill stages and verify 0 Hanzo calls during each.
	recordPostKillStage := func(num int, name string, start time.Time, hanzoBefore int64, passed bool, detail string) {
		delta := guard.Metrics().HanzoCallsAfterKill - hanzoBefore
		if delta != 0 {
			passed = false
		}
		if passed {
			report.PostKillStagesPassed++
		}
		report.Stages = append(report.Stages, DrillStageResult{
			Stage:                num,
			Name:                 name,
			Plane:                string(PlaneCommunity),
			Passed:               passed,
			DurationMs:           float64(time.Since(start).Nanoseconds()) / 1e6,
			HanzoCallsDuringStep: delta,
			Detail:               detail,
		})
	}

	// -------------------------------------------------------------------------
	// STAGE 3: Brand-New Provider Onboarding Under Total Hanzo Blackout
	// -------------------------------------------------------------------------
	s3Start := time.Now()
	s3Hanzo := guard.Metrics().HanzoCallsAfterKill
	providerBKP, err := GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	providerB = NewProviderNodeServer("provider-bravo-post-blackout", providerBKP, domain, client)
	if err := providerB.FetchAndVerifyModel(communityPlane.Mirror.URL(), modelDigest); err != nil {
		return nil, nil, fmt.Errorf("stage 3 providerB fetch model from community mirror: %w", err)
	}
	adB, err := providerB.BuildSignedAdvertisement([]string{modelID}, 450, 1200, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	for _, b := range communityPlane.BootstrapNodes {
		if _, err := doJSONRequest(client, http.MethodPost, b.URL()+"/v1/peers/register", adB, nil); err != nil {
			return nil, nil, fmt.Errorf("stage 3 register providerB on %s: %w", b.ID, err)
		}
	}
	recordPostKillStage(3, "new_provider_onboarding_under_blackout", s3Start, s3Hanzo, true,
		fmt.Sprintf("new provider %s (%s) joined Community Plane B after blackout", providerB.ProviderID, providerB.KeyPair.Address()))

	// -------------------------------------------------------------------------
	// STAGE 4: Decentralized Peer & Content-Addressed Model Discovery
	// -------------------------------------------------------------------------
	s4Start := time.Now()
	s4Hanzo := guard.Metrics().HanzoCallsAfterKill
	discoveredPeers, err := DiscoverPeersFromBootstraps(client, domain, communityPlane.BootstrapURLs(), modelID)
	if err != nil {
		return nil, nil, fmt.Errorf("stage 4 discover peers: %w", err)
	}
	foundProviderB := false
	for _, p := range discoveredPeers {
		if p.ProviderID == providerB.ProviderID {
			foundProviderB = true
			break
		}
	}
	discoveredManifest, err := DiscoverModelManifest(client, domain, communityPlane.BootstrapURLs(), modelID)
	if err != nil {
		return nil, nil, fmt.Errorf("stage 4 discover model manifest: %w", err)
	}
	// Select first non-Hanzo mirror from the manifest and verify SHA-256 weights.
	var verifiedModelBlob []byte
	for _, mirrorURL := range discoveredManifest.Mirrors {
		if strings.Contains(mirrorURL, hanzoPlane.Mirror.URL()) {
			continue
		}
		blob, dlErr := DownloadAndVerifyModelBlob(client, mirrorURL, discoveredManifest.DigestSHA256)
		if dlErr == nil {
			verifiedModelBlob = blob
			break
		}
	}
	s4Pass := foundProviderB && len(verifiedModelBlob) == len(modelBlob) && SHA256Hex(verifiedModelBlob) == modelDigest
	recordPostKillStage(4, "decentralized_peer_and_model_discovery", s4Start, s4Hanzo, s4Pass,
		fmt.Sprintf("discovered %d peer(s) including %s and SHA-256 verified %dB model weights", len(discoveredPeers), providerB.ProviderID, len(verifiedModelBlob)))

	// -------------------------------------------------------------------------
	// STAGE 5: Stateless Scheduling & Inference Execution
	// -------------------------------------------------------------------------
	s5Start := time.Now()
	s5Hanzo := guard.Metrics().HanzoCallsAfterKill
	exitReq := InferenceRequest{
		JobID:             "job-sovereign-exit-001",
		TenantAddress:     tenantKP.Address(),
		ModelID:           modelID,
		ModelDigestSHA256: discoveredManifest.DigestSHA256,
		Prompt:            "Execute verifiable inference under total Hanzo control-plane blackout",
		Seed:              20261351,
		MaxTokens:         128,
		Nonce:             2,
		Timestamp:         time.Now().UTC().Unix(),
	}
	var exitReceipt InferenceExecutionReceipt
	if _, err := doJSONRequest(client, http.MethodPost, communityPlane.Scheduler.URL()+"/v1/schedule", scheduleDispatchRequest{
		Request:           exitReq,
		PreferredProvider: providerB.ProviderID,
	}, &exitReceipt); err != nil {
		return nil, nil, fmt.Errorf("stage 5 community schedule: %w", err)
	}
	report.TokensGenerated = exitReceipt.TokensGenerated
	s5Pass := exitReceipt.ProviderID == providerB.ProviderID && exitReceipt.TokensGenerated == exitReq.MaxTokens
	recordPostKillStage(5, "stateless_scheduling_and_inference", s5Start, s5Hanzo, s5Pass,
		fmt.Sprintf("scheduled job %s on %s -> %d deterministic tokens", exitReq.JobID, exitReceipt.ProviderID, exitReceipt.TokensGenerated))

	// -------------------------------------------------------------------------
	// STAGE 6: Independent Offline Verification
	// -------------------------------------------------------------------------
	s6Start := time.Now()
	s6Hanzo := guard.Metrics().HanzoCallsAfterKill
	verifyErr := VerifyExecutionReceiptOffline(domain, exitReq, verifiedModelBlob, exitReceipt)
	recordPostKillStage(6, "independent_offline_verification", s6Start, s6Hanzo, verifyErr == nil,
		fmt.Sprintf("verified EIP-712 provider signature + deterministic SHA-256 digest %s offline", exitReceipt.OutputDigestSHA256[:16]))

	// -------------------------------------------------------------------------
	// STAGE 7: Independent Payment Settlement (Community Facilitator B)
	// -------------------------------------------------------------------------
	s7Start := time.Now()
	s7Hanzo := guard.Metrics().HanzoCallsAfterKill
	exitPay := PaymentAuthorization{
		PaymentID:    "pay-sovereign-exit-001",
		JobID:        exitReq.JobID,
		PayeeAddress: providerB.KeyPair.Address(),
		AmountAtomic: 450,
		Nonce:        2,
		ValidBefore:  time.Now().UTC().Add(10 * time.Minute).Unix(),
		ChainID:      domain.ChainID,
	}
	if err := SignPaymentAuthorization(domain, tenantKP, &exitPay); err != nil {
		return nil, nil, err
	}
	var exitSettle SettlementReceipt
	if _, err := doJSONRequest(client, http.MethodPost, communityPlane.Facilitator.URL()+"/v1/payments/settle", exitPay, &exitSettle); err != nil {
		return nil, nil, fmt.Errorf("stage 7 community settlement: %w", err)
	}
	settleVerifyErr := VerifySettlementReceipt(domain, exitSettle)
	drift := communityPlane.Facilitator.ConservationDrift()
	report.SettledAmountAtomic = exitSettle.AmountAtomic
	report.ConservationDrift = drift
	s7Pass := settleVerifyErr == nil && exitSettle.FacilitatorID == communityPlane.Facilitator.ID && drift == 0
	recordPostKillStage(7, "independent_payment_settlement", s7Start, s7Hanzo, s7Pass,
		fmt.Sprintf("settled %d atomic units to %s via %s (drift=%d)", exitSettle.AmountAtomic, providerB.ProviderID, exitSettle.FacilitatorID, drift))

	// -------------------------------------------------------------------------
	// STAGE 8: Authorized User Data Recovery, Governance, & Operator Fork
	// -------------------------------------------------------------------------
	s8Start := time.Now()
	s8Hanzo := guard.Metrics().HanzoCallsAfterKill
	var fetchedEnv EncryptedDataEnvelope
	if _, err := doJSONRequest(client, http.MethodGet, communityPlane.Vault.URL()+"/v1/vault/snapshots/"+snapshotID, nil, &fetchedEnv); err != nil {
		return nil, nil, fmt.Errorf("stage 8 fetch encrypted snapshot: %w", err)
	}
	recoveredBytes, err := DecryptUserData(domain, userDataKey, fetchedEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("stage 8 authorized decrypt: %w", err)
	}
	authorizedMatch := bytes.Equal(recoveredBytes, workspacePlaintext)

	// Verify unauthorized third-party key is fail-closed rejected.
	_, unauthErr := DecryptUserData(domain, wrongThirdPartyKey, fetchedEnv)
	unauthBlocked := errors.Is(unauthErr, ErrUnauthorizedDataAccess)
	report.UnauthorizedKeyBlocked = unauthBlocked

	// Independent community governance vote (2-of-2 community signers, 0 Hanzo calls).
	vote1 := GovernanceVote{
		ProposalID: "gov-add-bootstrap-3",
		Action:     "add_bootstrap_peer",
		Payload:    "community-bootstrap-3.mesh.local",
		Approve:    true,
		VotedAt:    time.Now().UTC().Unix(),
	}
	if err := SignGovernanceVote(domain, communityPlane.OperatorKP, &vote1); err != nil {
		return nil, nil, err
	}
	vote2 := GovernanceVote{
		ProposalID: "gov-add-bootstrap-3",
		Action:     "add_bootstrap_peer",
		Payload:    "community-bootstrap-3.mesh.local",
		Approve:    true,
		VotedAt:    time.Now().UTC().Unix(),
	}
	if err := SignGovernanceVote(domain, govVoter2KP, &vote2); err != nil {
		return nil, nil, err
	}
	var govDec GovernanceDecision
	if _, err := doJSONRequest(client, http.MethodPost, communityPlane.BootstrapNodes[0].URL()+"/v1/governance/vote", vote1, &govDec); err != nil {
		return nil, nil, fmt.Errorf("stage 8 gov vote 1: %w", err)
	}
	if _, err := doJSONRequest(client, http.MethodPost, communityPlane.BootstrapNodes[0].URL()+"/v1/governance/vote", vote2, &govDec); err != nil {
		return nil, nil, fmt.Errorf("stage 8 gov vote 2: %w", err)
	}
	report.GovernanceRatified = govDec.Ratified

	// Export signed NetworkForkBundle and hydrate a fresh independent Operator C plane.
	forkBundle, err := communityPlane.ExportForkBundle()
	if err != nil {
		return nil, nil, fmt.Errorf("stage 8 export fork bundle: %w", err)
	}
	forkRaw, err := json.Marshal(forkBundle)
	if err != nil {
		return nil, nil, err
	}
	forkVerifyErr := VerifyForkBundleOffline(domain, forkBundle)
	operatorC, err := NewCommunityControlPlane("community-op-c-fork", domain, client, 1)
	if err != nil {
		return nil, nil, err
	}
	defer operatorC.Close()
	hydrateErr := operatorC.HydrateFromFork(forkBundle)
	var forkVaultEnv EncryptedDataEnvelope
	var forkDecryptMatch bool
	var forkModelOK bool
	var forkWorkloadOK bool
	if hydrateErr == nil {
		if _, getErr := doJSONRequest(client, http.MethodGet, operatorC.Vault.URL()+"/v1/vault/snapshots/"+snapshotID, nil, &forkVaultEnv); getErr == nil {
			if dec, dErr := DecryptUserData(domain, userDataKey, forkVaultEnv); dErr == nil {
				forkDecryptMatch = bytes.Equal(dec, workspacePlaintext)
			}
		}
		// Verify model artifact survives fork migration on Operator C's mirror
		// and serves a real end-to-end inference workload with offline verification.
		if forkManifest, mErr := DiscoverModelManifest(client, domain, operatorC.BootstrapURLs(), modelID); mErr == nil {
			if forkBlob, bErr := DownloadAndVerifyModelBlob(client, operatorC.Mirror.URL(), forkManifest.DigestSHA256); bErr == nil && bytes.Equal(forkBlob, verifiedModelBlob) {
				forkModelOK = true
				if provCKP, kErr := GenerateKeyPair(); kErr == nil {
					providerC := NewProviderNodeServer("provider-op-c-fork", provCKP, domain, client)
					defer providerC.Close()
					if fErr := providerC.FetchAndVerifyModel(operatorC.Mirror.URL(), forkManifest.DigestSHA256); fErr == nil {
						if adC, aErr := providerC.BuildSignedAdvertisement([]string{modelID}, 450, 1000, time.Now().UTC()); aErr == nil {
							if _, rErr := doJSONRequest(client, http.MethodPost, operatorC.BootstrapNodes[0].URL()+"/v1/peers/register", adC, nil); rErr == nil {
								forkReq := InferenceRequest{
									JobID:             "job-sovereign-fork-op-c-001",
									TenantAddress:     tenantKP.Address(),
									ModelID:           modelID,
									ModelDigestSHA256: forkManifest.DigestSHA256,
									Prompt:            "Verify post-fork model artifact and inference execution on Operator C",
									Seed:              20261352,
									MaxTokens:         64,
									Nonce:             3,
									Timestamp:         time.Now().UTC().Unix(),
								}
								var forkReceipt InferenceExecutionReceipt
								if _, sErr := doJSONRequest(client, http.MethodPost, operatorC.Scheduler.URL()+"/v1/schedule", scheduleDispatchRequest{
									Request:           forkReq,
									PreferredProvider: providerC.ProviderID,
								}, &forkReceipt); sErr == nil {
									if vErr := VerifyExecutionReceiptOffline(domain, forkReq, forkBlob, forkReceipt); vErr == nil {
										forkWorkloadOK = forkReceipt.ProviderID == providerC.ProviderID && forkReceipt.TokensGenerated == forkReq.MaxTokens
									}
								}
							}
						}
					}
				}
			}
		}
	}
	forkPeers, _ := DiscoverPeersFromBootstraps(client, domain, operatorC.BootstrapURLs(), modelID)
	report.ForkBundleVerified = forkVerifyErr == nil && hydrateErr == nil && forkDecryptMatch && forkModelOK && forkWorkloadOK
	report.ForkBundleBytes = len(forkRaw)
	report.ForkHydratedProviders = len(forkPeers)
	report.ForkHydratedVaultItems = len(operatorC.Vault.SnapshotState())

	s8Pass := authorizedMatch && unauthBlocked && govDec.Ratified && report.ForkBundleVerified
	recordPostKillStage(8, "authorized_data_recovery_and_fork_migration", s8Start, s8Hanzo, s8Pass,
		fmt.Sprintf("decrypted %dB workspace, rejected unauthorized key, ratified governance (%d/%d), forked to Operator C (%dB bundle, mirror+workload verified=%v)",
			len(recoveredBytes), govDec.Approvals, govDec.QuorumRequired, len(forkRaw), forkModelOK && forkWorkloadOK))

	// Finalize report metrics.
	report.GuardMetrics = guard.Metrics()
	report.TotalDurationMs = float64(time.Since(started).Microseconds()) / 1000.0

	allPassed := true
	for _, st := range report.Stages {
		if !st.Passed {
			allPassed = false
			break
		}
	}
	report.AllStagesPassed = allPassed && report.GuardMetrics.HanzoCallsAfterKill == 0
	if report.PostKillStagesTotal > 0 {
		report.DecentralizationRatio = float64(report.PostKillStagesPassed) / float64(report.PostKillStagesTotal)
	}
	if report.GuardMetrics.HanzoCallsAfterKill > 0 {
		report.DecentralizationRatio = 0
	}
	sovPassed := 0
	if authorizedMatch {
		sovPassed++
	}
	if unauthBlocked {
		sovPassed++
	}
	if report.ForkBundleVerified {
		sovPassed++
	}
	if govDec.Ratified {
		sovPassed++
	}
	report.SovereigntyRatio = float64(sovPassed) / 4.0

	return report, &forkBundle, nil
}

// RunNegativeAndBoundaryRegimes executes the three honest negative and boundary
// experiments documenting failure modes and architectural trade-offs.
func RunNegativeAndBoundaryRegimes(domain EIP712Domain, trials int) ([]NegativeRegimeResult, error) {
	if trials <= 0 {
		trials = 25
	}
	results := make([]NegativeRegimeResult, 0, 3)

	// -------------------------------------------------------------------------
	// Regime 1: centralized_hanzo_coupled_negative
	// Proves a client/scheduler hard-coupled to api.hanzo.ai / Hanzo plane
	// collapses to 0.0% completion after KillHanzoControlPlane().
	// -------------------------------------------------------------------------
	{
		guard := NewHostBlocker()
		defer guard.CloseIdleConnections()
		client := guard.NewHTTPClient(5 * time.Second)
		hanzoPlane, err := NewHanzoControlPlane(domain, guard)
		if err != nil {
			return nil, err
		}
		defer hanzoPlane.Kill()

		// Confirm Hanzo plane works before Kill Switch.
		if _, err := doJSONRequest(client, http.MethodGet, hanzoPlane.Gateway.URL+"/v1/health", nil, nil); err != nil {
			return nil, fmt.Errorf("regime 1 pre-kill health failed: %w", err)
		}
		// Engage Kill Switch.
		KillHanzoControlPlane(hanzoPlane, guard)

		succeeded := 0
		failedClosed := 0
		for i := 0; i < trials; i++ {
			target := "https://api.hanzo.ai/v1/schedule"
			if i%2 == 1 {
				target = hanzoPlane.Scheduler.URL() + "/v1/schedule"
			}
			_, callErr := doJSONRequest(client, http.MethodPost, target, map[string]string{"jobId": fmt.Sprintf("coupled-%d", i)}, nil)
			if callErr == nil {
				succeeded++
			} else if errors.Is(callErr, ErrHanzoHostBlocked) {
				failedClosed++
			}
		}
		gm := guard.Metrics()
		results = append(results, NegativeRegimeResult{
			Name:              "centralized_hanzo_coupled_negative",
			Category:          "negative_control",
			Trials:            trials,
			Succeeded:         succeeded,
			FailedClosed:      failedClosed,
			CompletionRate:    float64(succeeded) / float64(trials),
			BlockedHanzoCalls: gm.HanzoBlockedAttempts,
			ExpectedBehavior:  "0.0% completion (total outage) once Hanzo endpoints and api.hanzo.ai are killed",
			ObservedBehavior:  fmt.Sprintf("%d/%d succeeded (%.1f%%); %d calls blocked by HostBlocker", succeeded, trials, 100*float64(succeeded)/float64(trials), gm.HanzoBlockedAttempts),
			Validated:         succeeded == 0 && failedClosed == trials && gm.HanzoBlockedAttempts == int64(trials),
		})
	}

	// -------------------------------------------------------------------------
	// Regime 2: missing_user_key_unrecoverable_boundary
	// Honest non-custodial key-management boundary: without the user's sovereign
	// decryption key (zero custodial escrow), workspace data cannot be recovered.
	// -------------------------------------------------------------------------
	{
		ownerKP, err := GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		realKey, err := GenerateUserDataKey()
		if err != nil {
			return nil, err
		}
		env, err := EncryptUserData(
			domain,
			ownerKP,
			realKey,
			"snap-noncustodial-boundary",
			[]byte("sensitive-sovereign-workspace-payload"),
			[]string{"https://community-vault.example"},
			[]string{"community-op-b"},
			time.Now().UTC().Unix(),
		)
		if err != nil {
			return nil, err
		}
		succeeded := 0
		failedClosed := 0
		for i := 0; i < trials; i++ {
			var attemptKey []byte
			if i%2 == 0 {
				attemptKey = nil // user lost key completely
			} else {
				attemptKey, _ = GenerateUserDataKey() // wrong replacement key
			}
			_, decErr := DecryptUserData(domain, attemptKey, env)
			if decErr == nil {
				succeeded++
			} else if errors.Is(decErr, ErrUnauthorizedDataAccess) {
				failedClosed++
			}
		}
		results = append(results, NegativeRegimeResult{
			Name:             "missing_user_key_unrecoverable_boundary",
			Category:         "boundary_tradeoff",
			Trials:           trials,
			Succeeded:        succeeded,
			FailedClosed:     failedClosed,
			CompletionRate:   float64(succeeded) / float64(trials),
			ExpectedBehavior: "0.0% recovery without user's sovereign key (honest non-custodial zero-escrow boundary)",
			ObservedBehavior: fmt.Sprintf("%d/%d recovered; %d/%d fail-closed with ErrUnauthorizedDataAccess", succeeded, trials, failedClosed, trials),
			Validated:        succeeded == 0 && failedClosed == trials && !env.CapabilityManifest.CustodialEscrow,
		})
	}

	// -------------------------------------------------------------------------
	// Regime 3: partitioned_bootstrap_quorum_loss
	// When ALL independent community bootstrap peers are offline (0 reachable),
	// peer discovery MUST fail closed with ErrBootstrapQuorumLost instead of
	// silently falling back to Hanzo or unverified state.
	// -------------------------------------------------------------------------
	{
		guard := NewHostBlocker()
		defer guard.CloseIdleConnections()
		guard.ActivateKillSwitch()
		client := guard.NewHTTPClient(2 * time.Second)

		plane, err := NewCommunityControlPlane("community-partitioned", domain, client, 2)
		if err != nil {
			return nil, err
		}
		bootURLs := plane.BootstrapURLs()
		// Take down ALL independent community bootstrap peers simultaneously.
		plane.Close()

		succeeded := 0
		failedClosed := 0
		for i := 0; i < trials; i++ {
			_, discErr := DiscoverPeersFromBootstraps(client, domain, bootURLs, "zen-open-7b-deterministic-v1")
			if discErr == nil {
				succeeded++
			} else if errors.Is(discErr, ErrBootstrapQuorumLost) {
				failedClosed++
			}
		}
		results = append(results, NegativeRegimeResult{
			Name:              "partitioned_bootstrap_quorum_loss",
			Category:          "boundary_tradeoff",
			Trials:            trials,
			Succeeded:         succeeded,
			FailedClosed:      failedClosed,
			CompletionRate:    float64(succeeded) / float64(trials),
			BlockedHanzoCalls: guard.Metrics().HanzoCallsAfterKill,
			ExpectedBehavior:  "0.0% discovery when all community bootstrap peers are offline; fail-closed ErrBootstrapQuorumLost with 0 Hanzo fallback calls",
			ObservedBehavior:  fmt.Sprintf("%d/%d discovered; %d/%d fail-closed ErrBootstrapQuorumLost (Hanzo calls=%d)", succeeded, trials, failedClosed, trials, guard.Metrics().HanzoCallsAfterKill),
			Validated:         succeeded == 0 && failedClosed == trials && guard.Metrics().HanzoCallsAfterKill == 0,
		})
	}

	return results, nil
}

// Coordinator manages on-demand execution and caching of network exit drills
// and fork bundle exports for the standalone zip HTTP surface.
type Coordinator struct {
	domain       EIP712Domain
	mu           sync.RWMutex
	latestReport *NetworkExitReport
	latestFork   *NetworkForkBundle
	runsCount    int64
}

// NewCoordinator constructs a Coordinator for the given EIP-712 domain.
func NewCoordinator(domain EIP712Domain) *Coordinator {
	if domain.Name == "" {
		domain = DefaultDomain()
	}
	return &Coordinator{domain: domain}
}

// Domain returns the configured EIP-712 domain.
func (c *Coordinator) Domain() EIP712Domain { return c.domain }

// RunDrill runs a fresh 8-stage blackout exit drill and stores the latest report & fork bundle.
func (c *Coordinator) RunDrill() (*NetworkExitReport, *NetworkForkBundle, error) {
	rep, fork, err := RunNetworkExitDrill(c.domain)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.latestReport = rep
	c.latestFork = fork
	c.runsCount++
	c.mu.Unlock()
	return rep, fork, nil
}

// LatestReport returns the most recent NetworkExitReport, if any.
func (c *Coordinator) LatestReport() (*NetworkExitReport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.latestReport == nil {
		return nil, false
	}
	cp := *c.latestReport
	cp.Stages = append([]DrillStageResult(nil), c.latestReport.Stages...)
	return &cp, true
}

// LatestForkBundle returns the most recent signed NetworkForkBundle, if any.
func (c *Coordinator) LatestForkBundle() (*NetworkForkBundle, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.latestFork == nil {
		return nil, false
	}
	cp := *c.latestFork
	return &cp, true
}

// RunsCount returns the total number of completed network exit drills.
func (c *Coordinator) RunsCount() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runsCount
}
