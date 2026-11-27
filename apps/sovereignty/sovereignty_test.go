// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestFullNetworkExitUnderTotalHanzoBlackout(t *testing.T) {
	domain := DefaultDomain()
	report, fork, err := RunNetworkExitDrill(domain)
	if err != nil {
		t.Fatalf("RunNetworkExitDrill failed: %v", err)
	}
	if !report.AllStagesPassed {
		t.Fatalf("expected all 8 stages to pass, got report=%+v", report)
	}
	if len(report.Stages) != 8 {
		t.Fatalf("expected 8 stages, got %d", len(report.Stages))
	}
	for _, st := range report.Stages {
		if !st.Passed {
			t.Errorf("stage %d (%s) failed: %s", st.Stage, st.Name, st.Detail)
		}
		if st.Stage >= 3 && st.HanzoCallsDuringStep != 0 {
			t.Errorf("stage %d (%s) made %d Hanzo calls after Kill Switch", st.Stage, st.Name, st.HanzoCallsDuringStep)
		}
	}
	if report.GuardMetrics.HanzoCallsBeforeKill == 0 {
		t.Fatalf("expected baseline stage 1 to exercise Hanzo plane before Kill Switch")
	}
	if report.GuardMetrics.HanzoCallsAfterKill != 0 {
		t.Fatalf("INVARIANT VIOLATION: HanzoCallsAfterKill = %d, want 0", report.GuardMetrics.HanzoCallsAfterKill)
	}
	if report.GuardMetrics.CommunityCallsAfterKill == 0 {
		t.Fatalf("expected non-zero Community Plane B HTTP calls after Kill Switch")
	}
	if !report.HanzoBlackoutProbePass {
		t.Fatalf("expected api.hanzo.ai and Hanzo gateway blackout probe to fail closed")
	}
	if !report.UnauthorizedKeyBlocked {
		t.Fatalf("expected unauthorized third-party data key to be rejected")
	}
	if !report.GovernanceRatified {
		t.Fatalf("expected independent community governance proposal to ratify")
	}
	if !report.ForkBundleVerified || fork == nil {
		t.Fatalf("expected portable fork bundle to verify and hydrate Operator C")
	}
	if report.DecentralizationRatio != 1.0 {
		t.Fatalf("expected DecentralizationRatio=1.0, got %.4f", report.DecentralizationRatio)
	}
	if report.SovereigntyRatio != 1.0 {
		t.Fatalf("expected SovereigntyRatio=1.0, got %.4f", report.SovereigntyRatio)
	}
	if report.ConservationDrift != 0 {
		t.Fatalf("expected zero settlement conservation drift, got %d", report.ConservationDrift)
	}
	if err := VerifyForkBundleOffline(domain, *fork); err != nil {
		t.Fatalf("offline fork bundle verification failed: %v", err)
	}
}

func TestCentralizedNegativeControlFailsUnderBlackout(t *testing.T) {
	domain := DefaultDomain()
	regimes, err := RunNegativeAndBoundaryRegimes(domain, 20)
	if err != nil {
		t.Fatalf("RunNegativeAndBoundaryRegimes failed: %v", err)
	}
	if len(regimes) != 3 {
		t.Fatalf("expected 3 regimes, got %d", len(regimes))
	}
	for _, r := range regimes {
		if !r.Validated {
			t.Errorf("regime %s not validated: %+v", r.Name, r)
		}
		if r.CompletionRate != 0.0 {
			t.Errorf("regime %s expected 0.0 completion rate, got %.4f", r.Name, r.CompletionRate)
		}
		if r.FailedClosed != 20 {
			t.Errorf("regime %s expected 20 fail-closed trials, got %d", r.Name, r.FailedClosed)
		}
	}
	coupled := regimes[0]
	if coupled.Name != "centralized_hanzo_coupled_negative" {
		t.Fatalf("unexpected first regime: %s", coupled.Name)
	}
	if coupled.BlockedHanzoCalls != 20 {
		t.Fatalf("expected 20 blocked Hanzo calls on coupled negative control, got %d", coupled.BlockedHanzoCalls)
	}
}

func TestModelIntegrityAndUnauthorizedDataRecoveryRejected(t *testing.T) {
	domain := DefaultDomain()
	guard := NewHostBlocker()
	defer guard.CloseIdleConnections()
	guard.ActivateKillSwitch()
	client := guard.NewHTTPClient(5 * time.Second)

	community, err := NewCommunityControlPlane("community-integrity-test", domain, client, 2)
	if err != nil {
		t.Fatalf("NewCommunityControlPlane: %v", err)
	}
	defer community.Close()

	// 1. Tampered model weight blob MUST fail closed on SHA-256 verification.
	const modelID = "zen-integrity-test-v1"
	blob := DeterministicModelWeights(modelID, 8192)
	digest := SHA256Hex(blob)
	if err := UploadModelBlob(client, community.Mirror.URL(), digest, blob); err != nil {
		t.Fatalf("UploadModelBlob: %v", err)
	}
	community.Mirror.InjectTamperedBlob(digest, true)
	if _, err := DownloadAndVerifyModelBlob(client, community.Mirror.URL(), digest); !errors.Is(err, ErrModelDigestMismatch) {
		t.Fatalf("expected ErrModelDigestMismatch for tampered model blob, got %v", err)
	}
	community.Mirror.InjectTamperedBlob(digest, false)
	verifiedBlob, err := DownloadAndVerifyModelBlob(client, community.Mirror.URL(), digest)
	if err != nil || !bytes.Equal(verifiedBlob, blob) {
		t.Fatalf("expected clean download after clearing tamper toggle, err=%v", err)
	}

	// 2. Tampered inference execution receipt MUST fail offline verification.
	provKP, _ := GenerateKeyPair()
	tenantKP, _ := GenerateKeyPair()
	prov := NewProviderNodeServer("prov-dishonest", provKP, domain, client)
	defer prov.Close()
	if err := prov.FetchAndVerifyModel(community.Mirror.URL(), digest); err != nil {
		t.Fatalf("prov FetchAndVerifyModel: %v", err)
	}
	prov.SetTamperOutput(true)
	req := InferenceRequest{
		JobID:             "job-tamper-check",
		TenantAddress:     tenantKP.Address(),
		ModelID:           modelID,
		ModelDigestSHA256: digest,
		Prompt:            "detect tampered provider execution",
		Seed:              42,
		MaxTokens:         32,
		Nonce:             1,
		Timestamp:         time.Now().UTC().Unix(),
	}
	var badReceipt InferenceExecutionReceipt
	if _, err := doJSONRequest(client, http.MethodPost, prov.URL()+"/v1/infer", req, &badReceipt); err != nil {
		t.Fatalf("prov infer: %v", err)
	}
	if err := VerifyExecutionReceiptOffline(domain, req, verifiedBlob, badReceipt); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("expected ErrInvalidReceipt on tampered output, got %v", err)
	}

	// 3. Payment replay attack MUST be rejected with HTTP 409 / ErrReplayDetected.
	_, _ = doJSONRequest(client, http.MethodPost, community.Facilitator.URL()+"/v1/payments/mint", MintRequest{
		Address: tenantKP.Address(),
		Amount:  10_000,
	}, nil)
	pay := PaymentAuthorization{
		PaymentID:    "pay-replay-001",
		JobID:        req.JobID,
		PayeeAddress: provKP.Address(),
		AmountAtomic: 1_000,
		Nonce:        1,
		ValidBefore:  time.Now().UTC().Add(5 * time.Minute).Unix(),
		ChainID:      domain.ChainID,
	}
	if err := SignPaymentAuthorization(domain, tenantKP, &pay); err != nil {
		t.Fatalf("SignPaymentAuthorization: %v", err)
	}
	if _, err := doJSONRequest(client, http.MethodPost, community.Facilitator.URL()+"/v1/payments/settle", pay, nil); err != nil {
		t.Fatalf("first settlement failed: %v", err)
	}
	status, err := doJSONRequest(client, http.MethodPost, community.Facilitator.URL()+"/v1/payments/settle", pay, nil)
	if err == nil || status != http.StatusConflict {
		t.Fatalf("expected HTTP 409 on payment replay, got status=%d err=%v", status, err)
	}

	// 4. Unauthorized key AND tampered ciphertext MUST fail closed on user data recovery.
	userKey, _ := GenerateUserDataKey()
	wrongKey, _ := GenerateUserDataKey()
	env, err := EncryptUserData(domain, tenantKP, userKey, "snap-auth-test", []byte("top-secret-user-state"), []string{community.Vault.URL()}, []string{community.OperatorID}, time.Now().UTC().Unix())
	if err != nil {
		t.Fatalf("EncryptUserData: %v", err)
	}
	if _, err := DecryptUserData(domain, wrongKey, env); !errors.Is(err, ErrUnauthorizedDataAccess) {
		t.Fatalf("expected ErrUnauthorizedDataAccess for wrong key, got %v", err)
	}
	if _, err := DecryptUserData(domain, nil, env); !errors.Is(err, ErrUnauthorizedDataAccess) {
		t.Fatalf("expected ErrUnauthorizedDataAccess for missing key, got %v", err)
	}
	tamperedEnv := env
	ctBytes, err := hex.DecodeString(env.CiphertextHex)
	if err != nil || len(ctBytes) == 0 {
		t.Fatalf("decode ciphertext: %v", err)
	}
	ctBytes[0] ^= 0x01
	tamperedEnv.CiphertextHex = hex.EncodeToString(ctBytes)
	if _, err := DecryptUserData(domain, userKey, tamperedEnv); !errors.Is(err, ErrUnauthorizedDataAccess) {
		t.Fatalf("expected ErrUnauthorizedDataAccess for tampered ciphertext, got %v", err)
	}
	if guard.Metrics().HanzoCallsAfterKill != 0 {
		t.Fatalf("expected 0 Hanzo calls, got %d", guard.Metrics().HanzoCallsAfterKill)
	}
}

func TestConcurrentMultiTenantBlackoutExitRace(t *testing.T) {
	domain := DefaultDomain()
	guard := NewHostBlocker()
	defer guard.CloseIdleConnections()
	client := guard.NewHTTPClient(15 * time.Second)

	// Bring up Hanzo plane and immediately kill it to enforce total blackout.
	hanzoPlane, err := NewHanzoControlPlane(domain, guard)
	if err != nil {
		t.Fatalf("NewHanzoControlPlane: %v", err)
	}
	KillHanzoControlPlane(hanzoPlane, guard)

	community, err := NewCommunityControlPlane("community-race-op", domain, client, 2)
	if err != nil {
		t.Fatalf("NewCommunityControlPlane: %v", err)
	}
	defer community.Close()

	publisherKP, _ := GenerateKeyPair()
	const modelID = "zen-race-model-v1"
	modelBlob := DeterministicModelWeights(modelID, 8192)
	modelDigest := SHA256Hex(modelBlob)

	if err := UploadModelBlob(client, community.Mirror.URL(), modelDigest, modelBlob); err != nil {
		t.Fatalf("UploadModelBlob: %v", err)
	}
	manifest := ModelManifest{
		ModelID:      modelID,
		Version:      "1.0.0",
		License:      "MIT",
		DigestSHA256: modelDigest,
		SizeBytes:    int64(len(modelBlob)),
		Mirrors:      []string{community.Mirror.URL()},
		IssuedAt:     time.Now().UTC().Unix(),
	}
	if err := SignModelManifest(domain, publisherKP, &manifest); err != nil {
		t.Fatalf("SignModelManifest: %v", err)
	}
	for _, b := range community.BootstrapNodes {
		if _, err := doJSONRequest(client, http.MethodPost, b.URL()+"/v1/models", manifest, nil); err != nil {
			t.Fatalf("register model: %v", err)
		}
	}

	// Spin up 2 post-blackout independent providers.
	providers := make([]*ProviderNodeServer, 2)
	for i := 0; i < 2; i++ {
		kp, _ := GenerateKeyPair()
		p := NewProviderNodeServer(fmt.Sprintf("prov-race-%d", i+1), kp, domain, client)
		defer p.Close()
		if err := p.FetchAndVerifyModel(community.Mirror.URL(), modelDigest); err != nil {
			t.Fatalf("provider %d fetch model: %v", i, err)
		}
		ad, err := p.BuildSignedAdvertisement([]string{modelID}, 250, 2000, time.Now().UTC())
		if err != nil {
			t.Fatalf("BuildSignedAdvertisement: %v", err)
		}
		for _, b := range community.BootstrapNodes {
			if _, err := doJSONRequest(client, http.MethodPost, b.URL()+"/v1/peers/register", ad, nil); err != nil {
				t.Fatalf("register provider: %v", err)
			}
		}
		providers[i] = p
	}

	const numTenants = 8
	var wg sync.WaitGroup
	errCh := make(chan error, numTenants)

	for i := 0; i < numTenants; i++ {
		wg.Add(1)
		go func(tenantIdx int) {
			defer wg.Done()
			tenantKP, err := GenerateKeyPair()
			if err != nil {
				errCh <- fmt.Errorf("tenant %d keygen: %w", tenantIdx, err)
				return
			}
			userKey, err := GenerateUserDataKey()
			if err != nil {
				errCh <- fmt.Errorf("tenant %d data key: %w", tenantIdx, err)
				return
			}
			// Mint balance on community facilitator.
			if _, err := doJSONRequest(client, http.MethodPost, community.Facilitator.URL()+"/v1/payments/mint", MintRequest{
				Address: tenantKP.Address(),
				Amount:  100_000,
			}, nil); err != nil {
				errCh <- fmt.Errorf("tenant %d mint: %w", tenantIdx, err)
				return
			}
			// Discover peers & model manifest.
			peers, err := DiscoverPeersFromBootstraps(client, domain, community.BootstrapURLs(), modelID)
			if err != nil || len(peers) < 2 {
				errCh <- fmt.Errorf("tenant %d discover peers: len=%d err=%v", tenantIdx, len(peers), err)
				return
			}
			m, err := DiscoverModelManifest(client, domain, community.BootstrapURLs(), modelID)
			if err != nil {
				errCh <- fmt.Errorf("tenant %d discover model: %w", tenantIdx, err)
				return
			}
			blob, err := DownloadAndVerifyModelBlob(client, m.Mirrors[0], m.DigestSHA256)
			if err != nil {
				errCh <- fmt.Errorf("tenant %d download model: %w", tenantIdx, err)
				return
			}
			// Schedule inference via Community Scheduler B.
			req := InferenceRequest{
				JobID:             fmt.Sprintf("job-tenant-%d", tenantIdx),
				TenantAddress:     tenantKP.Address(),
				ModelID:           modelID,
				ModelDigestSHA256: m.DigestSHA256,
				Prompt:            fmt.Sprintf("concurrent sovereign exit prompt from tenant %d", tenantIdx),
				Seed:              uint64(1000 + tenantIdx),
				MaxTokens:         48,
				Nonce:             uint64(tenantIdx + 1),
				Timestamp:         time.Now().UTC().Unix(),
			}
			var receipt InferenceExecutionReceipt
			if _, err := doJSONRequest(client, http.MethodPost, community.Scheduler.URL()+"/v1/schedule", scheduleDispatchRequest{Request: req}, &receipt); err != nil {
				errCh <- fmt.Errorf("tenant %d schedule: %w", tenantIdx, err)
				return
			}
			if err := VerifyExecutionReceiptOffline(domain, req, blob, receipt); err != nil {
				errCh <- fmt.Errorf("tenant %d verify receipt: %w", tenantIdx, err)
				return
			}
			// Settle payment via Community Facilitator B.
			pay := PaymentAuthorization{
				PaymentID:    fmt.Sprintf("pay-tenant-%d", tenantIdx),
				JobID:        req.JobID,
				PayeeAddress: receipt.ProviderAddress,
				AmountAtomic: 250,
				Nonce:        uint64(tenantIdx + 1),
				ValidBefore:  time.Now().UTC().Add(5 * time.Minute).Unix(),
				ChainID:      domain.ChainID,
			}
			if err := SignPaymentAuthorization(domain, tenantKP, &pay); err != nil {
				errCh <- fmt.Errorf("tenant %d sign payment: %w", tenantIdx, err)
				return
			}
			var settle SettlementReceipt
			if _, err := doJSONRequest(client, http.MethodPost, community.Facilitator.URL()+"/v1/payments/settle", pay, &settle); err != nil {
				errCh <- fmt.Errorf("tenant %d settle: %w", tenantIdx, err)
				return
			}
			if err := VerifySettlementReceipt(domain, settle); err != nil {
				errCh <- fmt.Errorf("tenant %d verify settle: %w", tenantIdx, err)
				return
			}
			// Store & recover user-sovereign encrypted data.
			payload := []byte(fmt.Sprintf(`{"tenant":%d,"state":"sovereign-ok"}`, tenantIdx))
			snapID := fmt.Sprintf("snap-tenant-%d", tenantIdx)
			env, err := EncryptUserData(domain, tenantKP, userKey, snapID, payload, []string{community.Vault.URL()}, []string{community.OperatorID}, time.Now().UTC().Unix())
			if err != nil {
				errCh <- fmt.Errorf("tenant %d encrypt: %w", tenantIdx, err)
				return
			}
			if _, err := doJSONRequest(client, http.MethodPost, community.Vault.URL()+"/v1/vault/snapshots", env, nil); err != nil {
				errCh <- fmt.Errorf("tenant %d store vault: %w", tenantIdx, err)
				return
			}
			var fetched EncryptedDataEnvelope
			if _, err := doJSONRequest(client, http.MethodGet, community.Vault.URL()+"/v1/vault/snapshots/"+snapID, nil, &fetched); err != nil {
				errCh <- fmt.Errorf("tenant %d fetch vault: %w", tenantIdx, err)
				return
			}
			plain, err := DecryptUserData(domain, userKey, fetched)
			if err != nil || !bytes.Equal(plain, payload) {
				errCh <- fmt.Errorf("tenant %d decrypt mismatch: err=%v", tenantIdx, err)
				return
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent tenant error: %v", err)
	}
	if drift := community.Facilitator.ConservationDrift(); drift != 0 {
		t.Fatalf("expected 0 conservation drift across 8 concurrent tenants, got %d", drift)
	}
	if gm := guard.Metrics(); gm.HanzoCallsAfterKill != 0 {
		t.Fatalf("expected 0 Hanzo calls after kill across 8 concurrent tenants, got %d", gm.HanzoCallsAfterKill)
	}
}

func TestZIPHTTPAndSovereignForkExport(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("sovereignty-test")})
	coord := NewCoordinator(DefaultDomain())
	if err := Mount(app, luxlog.New("sovereignty-test"), coord); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	callZip := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var r io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			r = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("app.Test %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}

	// 1. Health check before drill.
	status, raw := callZip(http.MethodGet, "/v1/sovereignty/health", nil)
	if status != http.StatusOK {
		t.Fatalf("health status=%d body=%s", status, raw)
	}
	var health HealthOut
	if err := json.Unmarshal(raw, &health); err != nil || health.Status != "ok" || health.ProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected health payload: %+v err=%v", health, err)
	}

	// 2. Latest drill returns 404 before first run.
	status, _ = callZip(http.MethodGet, "/v1/sovereignty/drill/latest", nil)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 before first drill, got %d", status)
	}

	// 3. Run full 8-stage blackout exit drill over /v1/sovereignty/drill/run.
	status, raw = callZip(http.MethodPost, "/v1/sovereignty/drill/run", nil)
	if status != http.StatusOK {
		t.Fatalf("drill/run status=%d body=%s", status, raw)
	}
	var report NetworkExitReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode drill report: %v", err)
	}
	if !report.AllStagesPassed || report.GuardMetrics.HanzoCallsAfterKill != 0 {
		t.Fatalf("expected drill pass with 0 post-kill Hanzo calls, got %+v", report)
	}

	// 4. Latest drill now returns 200.
	status, _ = callZip(http.MethodGet, "/v1/sovereignty/drill/latest", nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 on drill/latest, got %d", status)
	}

	// 5. Run negative & boundary regimes over /v1/sovereignty/regimes/run.
	status, raw = callZip(http.MethodPost, "/v1/sovereignty/regimes/run", RegimesIn{Trials: 10})
	if status != http.StatusOK {
		t.Fatalf("regimes/run status=%d body=%s", status, raw)
	}
	var regOut RegimesOut
	if err := json.Unmarshal(raw, &regOut); err != nil || len(regOut.Regimes) != 3 {
		t.Fatalf("unexpected regimes output: %+v err=%v", regOut, err)
	}
	for _, r := range regOut.Regimes {
		if !r.Validated {
			t.Fatalf("regime %s failed validation via HTTP", r.Name)
		}
	}

	// 6. Export fork bundle & verify valid + tampered fork bundle detection.
	status, raw = callZip(http.MethodGet, "/v1/sovereignty/fork/export", nil)
	if status != http.StatusOK {
		t.Fatalf("fork/export status=%d body=%s", status, raw)
	}
	var fork NetworkForkBundle
	if err := json.Unmarshal(raw, &fork); err != nil {
		t.Fatalf("decode fork bundle: %v", err)
	}
	status, raw = callZip(http.MethodPost, "/v1/sovereignty/fork/verify", fork)
	if status != http.StatusOK {
		t.Fatalf("fork/verify status=%d body=%s", status, raw)
	}
	var verifyOut ForkVerifyOut
	if err := json.Unmarshal(raw, &verifyOut); err != nil || !verifyOut.Valid || verifyOut.ProviderCount == 0 {
		t.Fatalf("unexpected verifyOut: %+v err=%v", verifyOut, err)
	}

	// Tamper with fork bundle and assert fail-closed HTTP 422 rejection.
	tampered := fork
	tampered.OperatorID = "tampered-operator"
	status, _ = callZip(http.MethodPost, "/v1/sovereignty/fork/verify", tampered)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422 on tampered fork bundle, got %d", status)
	}
}

type spyRoundTripper struct {
	called int
}

func (s *spyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	s.called++
	return nil, errors.New("unexpected upstream transport call")
}

func TestHostBlockerNormalizesAbsoluteDNSNames(t *testing.T) {
	hb := NewHostBlocker()
	spy := &spyRoundTripper{}
	hb.base = spy
	hb.ActivateKillSwitch()

	for _, rawURL := range []string{
		"https://api.hanzo.ai./v1/health",
		"https://api.hanzo.ai.:443/v1/health",
		"https://API.HANZO.AI./v1/schedule",
		"https://sub.hanzo.ai./v1/models",
		"https://hanzo.ai./",
	} {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hb.RoundTrip(req); !errors.Is(err, ErrHanzoHostBlocked) {
			t.Fatalf("expected ErrHanzoHostBlocked for %s, got %v", rawURL, err)
		}
	}
	if spy.called != 0 {
		t.Fatalf("expected 0 underlying transport calls for dotted Hanzo hostnames, got %d", spy.called)
	}
	m := hb.Metrics()
	if m.CommunityCallsAfterKill != 0 || m.HanzoBlockedAttempts != 5 {
		t.Fatalf("unexpected metrics after dotted blackout probes: %+v", m)
	}
}

func TestVaultRejectsCrossOwnerSnapshotOverwrite(t *testing.T) {
	domain := DefaultDomain()
	vault := NewSovereignVaultServer("vault-owner-guard", PlaneCommunity, domain)
	defer vault.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	ownerA, _ := GenerateKeyPair()
	ownerB, _ := GenerateKeyPair()
	keyA, _ := GenerateUserDataKey()
	keyB, _ := GenerateUserDataKey()
	plainA := []byte("owner-A-confidential-state")
	plainB := []byte("owner-B-replacement-attempt")

	envA, err := EncryptUserData(domain, ownerA, keyA, "shared-snap-id", plainA, []string{vault.URL()}, []string{"op"}, time.Now().UTC().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if status, err := doJSONRequest(client, http.MethodPost, vault.URL()+"/v1/vault/snapshots", envA, nil); err != nil || status != http.StatusOK {
		t.Fatalf("owner A put snapshot failed: status=%d err=%v", status, err)
	}

	// Owner B attempts to overwrite Owner A's snapshot ID with a validly signed envelope of its own.
	envB, err := EncryptUserData(domain, ownerB, keyB, "shared-snap-id", plainB, []string{vault.URL()}, []string{"op"}, time.Now().UTC().Unix())
	if err != nil {
		t.Fatal(err)
	}
	status, err := doJSONRequest(client, http.MethodPost, vault.URL()+"/v1/vault/snapshots", envB, nil)
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 rejecting cross-owner snapshot overwrite, got status=%d err=%v", status, err)
	}

	// Owner A's original ciphertext must remain intact and decryptable with keyA.
	var fetched EncryptedDataEnvelope
	if _, err := doJSONRequest(client, http.MethodGet, vault.URL()+"/v1/vault/snapshots/shared-snap-id", nil, &fetched); err != nil {
		t.Fatal(err)
	}
	dec, err := DecryptUserData(domain, keyA, fetched)
	if err != nil || !bytes.Equal(dec, plainA) {
		t.Fatalf("owner A snapshot corrupted after rejected cross-owner overwrite: err=%v", err)
	}
}

func TestForkBundleHydratesModelArtifactsAfterOriginShutdown(t *testing.T) {
	domain := DefaultDomain()
	_, fork, err := RunNetworkExitDrill(domain)
	if err != nil {
		t.Fatalf("RunNetworkExitDrill: %v", err)
	}
	if len(fork.Models) == 0 || len(fork.ModelBlobs) == 0 {
		t.Fatalf("expected fork bundle to include model manifests and verified blobs")
	}

	// Both origin mirrors (Hanzo and Community B) are already closed by RunNetworkExitDrill.
	// Hydrate a brand-new Operator D plane solely from the returned fork bundle and download
	// the model artifact from Operator D's own mirror!
	client := &http.Client{Timeout: 5 * time.Second}
	opD, err := NewCommunityControlPlane("community-op-d-external-fork", domain, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer opD.Close()
	if err := opD.HydrateFromFork(*fork); err != nil {
		t.Fatalf("HydrateFromFork on fresh Operator D failed: %v", err)
	}
	m := fork.Models[0]
	blob, err := DownloadAndVerifyModelBlob(client, opD.Mirror.URL(), m.DigestSHA256)
	if err != nil {
		t.Fatalf("post-fork model artifact download from Operator D mirror failed: %v", err)
	}
	if int64(len(blob)) != m.SizeBytes {
		t.Fatalf("downloaded blob size=%d want %d", len(blob), m.SizeBytes)
	}
}

func TestControlPlaneServersExposeTypedOpenAPIAndMCP(t *testing.T) {
	domain := DefaultDomain()
	guard := NewHostBlocker()
	defer guard.CloseIdleConnections()
	client := guard.NewHTTPClient(5 * time.Second)

	community, err := NewCommunityControlPlane("typed-ops-op", domain, client, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer community.Close()
	provKP, _ := GenerateKeyPair()
	prov := NewProviderNodeServer("typed-ops-prov", provKP, domain, client)
	defer prov.Close()

	checkSurface := func(name, baseURL string, wantPaths []string, minTools int) {
		t.Helper()
		resp, err := http.Get(baseURL + "/.well-known/openapi.json")
		if err != nil {
			t.Fatalf("%s openapi GET: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s openapi status=%d body=%s", name, resp.StatusCode, string(raw))
		}
		var spec struct {
			Paths map[string]any `json:"paths"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
			t.Fatalf("%s decode openapi: %v", name, err)
		}
		for _, p := range wantPaths {
			if _, ok := spec.Paths[p]; !ok {
				t.Errorf("%s OpenAPI missing typed path %s (have=%v)", name, p, spec.Paths)
			}
		}

		mcpResp, err := http.Post(baseURL+"/mcp", "application/json", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		if err != nil {
			t.Fatalf("%s POST /mcp: %v", name, err)
		}
		defer mcpResp.Body.Close()
		raw, _ := io.ReadAll(mcpResp.Body)
		if mcpResp.StatusCode != http.StatusOK {
			t.Fatalf("%s /mcp status=%d body=%s", name, mcpResp.StatusCode, string(raw))
		}
		var mcpOut struct {
			Result struct {
				Tools []map[string]any `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &mcpOut); err != nil || len(mcpOut.Result.Tools) < minTools {
			t.Fatalf("%s expected >=%d MCP tools, got %d (err=%v)", name, minTools, len(mcpOut.Result.Tools), err)
		}
	}

	checkSurface("bootstrap", community.BootstrapNodes[0].URL(), []string{
		"/v1/health", "/v1/peers/register", "/v1/peers", "/v1/models", "/v1/governance/vote",
	}, 7)
	checkSurface("provider", prov.URL(), []string{"/v1/health", "/v1/infer"}, 2)
	checkSurface("scheduler", community.Scheduler.URL(), []string{"/v1/health", "/v1/schedule"}, 2)
	checkSurface("facilitator", community.Facilitator.URL(), []string{
		"/v1/health", "/v1/payments/mint", "/v1/payments/settle", "/v1/payments/ledger",
	}, 5)
	checkSurface("vault", community.Vault.URL(), []string{
		"/v1/health", "/v1/vault/snapshots", "/v1/vault/export",
	}, 4)
}

func TestGovernanceVoteRejectsConflictingProposalContents(t *testing.T) {
	domain := DefaultDomain()
	boot := NewBootstrapServer("gov-conflict-boot", PlaneCommunity, domain, 2)
	defer boot.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	voter1, _ := GenerateKeyPair()
	voter2, _ := GenerateKeyPair()

	v1 := GovernanceVote{
		ProposalID: "prop-conflict-1",
		Action:     "add_bootstrap_peer",
		Payload:    "peer-good.mesh.local",
		Approve:    true,
		VotedAt:    time.Now().UTC().Unix(),
	}
	if err := SignGovernanceVote(domain, voter1, &v1); err != nil {
		t.Fatal(err)
	}
	var dec GovernanceDecision
	if status, err := doJSONRequest(client, http.MethodPost, boot.URL()+"/v1/governance/vote", v1, &dec); err != nil || status != http.StatusOK {
		t.Fatalf("vote 1 failed: status=%d err=%v", status, err)
	}
	if dec.Ratified || dec.Approvals != 1 {
		t.Fatalf("unexpected state after vote 1: %+v", dec)
	}

	// Voter 2 signs a DIFFERENT Action/Payload under the same ProposalID.
	v2 := GovernanceVote{
		ProposalID: "prop-conflict-1",
		Action:     "remove_bootstrap_peer",
		Payload:    "peer-evil.mesh.local",
		Approve:    true,
		VotedAt:    time.Now().UTC().Unix(),
	}
	if err := SignGovernanceVote(domain, voter2, &v2); err != nil {
		t.Fatal(err)
	}
	status, err := doJSONRequest(client, http.MethodPost, boot.URL()+"/v1/governance/vote", v2, nil)
	if err == nil || status != http.StatusConflict {
		t.Fatalf("expected HTTP 409 rejecting conflicting proposal vote, got status=%d err=%v", status, err)
	}

	// Proposal must NOT be ratified with conflicting votes.
	var stored GovernanceDecision
	if _, err := doJSONRequest(client, http.MethodGet, boot.URL()+"/v1/governance/prop-conflict-1", nil, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Ratified || stored.Approvals != 1 || len(stored.Votes) != 1 {
		t.Fatalf("conflicting vote corrupted governance decision: %+v", stored)
	}
}

func TestVerifyForkBundleOfflineAuthenticatesVaultEnvelopes(t *testing.T) {
	domain := DefaultDomain()
	_, fork, err := RunNetworkExitDrill(domain)
	if err != nil {
		t.Fatalf("RunNetworkExitDrill: %v", err)
	}
	if len(fork.EncryptedVault) == 0 {
		t.Fatalf("expected non-empty EncryptedVault in fork bundle")
	}

	// Exporting operator tampers with a user's vault ciphertext and re-signs the outer bundle.
	rogueOpKP, _ := GenerateKeyPair()
	forged := *fork
	forged.EncryptedVault = append([]EncryptedDataEnvelope(nil), fork.EncryptedVault...)
	ctBytes, err := hex.DecodeString(forged.EncryptedVault[0].CiphertextHex)
	if err != nil || len(ctBytes) == 0 {
		t.Fatal(err)
	}
	ctBytes[0] ^= 0x01
	forged.EncryptedVault[0].CiphertextHex = hex.EncodeToString(ctBytes)
	if err := SignForkBundle(domain, rogueOpKP, &forged); err != nil {
		t.Fatal(err)
	}

	if err := VerifyForkBundleOffline(domain, forged); !errors.Is(err, ErrForkBundleCorrupted) {
		t.Fatalf("expected VerifyForkBundleOffline to reject operator-tampered vault envelope, got %v", err)
	}
}

