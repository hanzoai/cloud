// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type testActor struct {
	owner      *KeyPair
	controller *KeyPair
	delegate   *KeyPair
	client     *KeyPair
	attacker   *KeyPair
}

func newTestActors(t *testing.T) testActor {
	t.Helper()
	gen := func() *KeyPair {
		kp, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair: %v", err)
		}
		return kp
	}
	return testActor{
		owner:      gen(),
		controller: gen(),
		delegate:   gen(),
		client:     gen(),
		attacker:   gen(),
	}
}

func signAgent(t *testing.T, domain EIP712Domain, owner *KeyPair, ag AgentIdentity) AgentIdentity {
	t.Helper()
	if ag.ChainID == 0 {
		ag.ChainID = domain.ChainID
	}
	if ag.RegistryAddress == "" {
		ag.RegistryAddress = domain.VerifyingContract
	}
	if ag.Version == 0 {
		ag.Version = 1
	}
	if ag.Status == "" {
		ag.Status = StatusActive
	}
	sig, err := owner.SignDigest(ag.RegistrationDigest(domain))
	if err != nil {
		t.Fatalf("sign agent: %v", err)
	}
	ag.OwnerSignature = sig
	return ag
}

func signDelegation(t *testing.T, domain EIP712Domain, owner *KeyPair, g DelegationGrant) DelegationGrant {
	t.Helper()
	g.DelegationID = g.ComputeID()
	sig, err := owner.SignDigest(g.Digest(domain))
	if err != nil {
		t.Fatalf("sign delegation: %v", err)
	}
	g.OwnerSignature = sig
	return g
}

func signToolReq(t *testing.T, domain EIP712Domain, delegate *KeyPair, req ToolActionRequest) ToolActionRequest {
	t.Helper()
	sig, err := delegate.SignDigest(req.Digest(domain))
	if err != nil {
		t.Fatalf("sign tool req: %v", err)
	}
	req.DelegateSignature = sig
	return req
}

func signPayReq(t *testing.T, domain EIP712Domain, delegate *KeyPair, req PaymentAuthRequest) PaymentAuthRequest {
	t.Helper()
	sig, err := delegate.SignDigest(req.Digest(domain))
	if err != nil {
		t.Fatalf("sign pay req: %v", err)
	}
	req.DelegateSignature = sig
	return req
}

func signWorkReceipt(t *testing.T, domain EIP712Domain, worker, client *KeyPair, wr WorkReceipt) WorkReceipt {
	t.Helper()
	wr.WorkReceiptHash = wr.ComputeHash()
	digest := wr.Digest(domain)
	ws, err := worker.SignDigest(digest)
	if err != nil {
		t.Fatalf("sign work receipt (worker): %v", err)
	}
	cs, err := client.SignDigest(digest)
	if err != nil {
		t.Fatalf("sign work receipt (client): %v", err)
	}
	wr.WorkerSignature = ws
	wr.ClientAckSignature = cs
	return wr
}

func signFeedback(t *testing.T, domain EIP712Domain, client *KeyPair, fb ReputationFeedback) ReputationFeedback {
	t.Helper()
	sig, err := client.SignDigest(fb.Digest(domain))
	if err != nil {
		t.Fatalf("sign feedback: %v", err)
	}
	fb.ClientSignature = sig
	return fb
}

// TestAcceptanceLifecycleEndToEnd verifies the complete 5-step acceptance scenario
// from hanzoai/cloud#18 both programmatically and over the mounted zip HTTP API.
func TestAcceptanceLifecycleEndToEnd(t *testing.T) {
	now := int64(1760000000)
	clock := func() int64 { return now }
	domain := DefaultDomain()
	actors := newTestActors(t)

	executedTools := 0
	svc := NewService(domain, clock, func(req ToolActionRequest) (string, error) {
		executedTools++
		return fmt.Sprintf("analyzed:%s:%s", req.Tool, req.Resource), nil
	})

	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := Mount(app, luxlog.NewNoOpLogger(), svc); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	doJSON := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatalf("encode %s %s: %v", method, path, err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out bytes.Buffer
		_, _ = out.ReadFrom(resp.Body)
		return resp.StatusCode, out.Bytes()
	}

	// 1. Register portable ERC-8004 agent identity advertising MCP + A2A endpoints and capabilities.
	agentReq := signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		PaymentWallet:    actors.owner.Address(),
		MetadataURI:      "ipfs://bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
		MCPEndpoints: []EndpointDescriptor{
			{Protocol: "mcp", URL: "https://agent.example.org/mcp", Version: "2025-03-26", AuthScheme: "erc8004-delegation", Capabilities: []string{"code.analyze", "sandbox.read"}},
		},
		A2AEndpoints: []EndpointDescriptor{
			{Protocol: "a2a", URL: "https://agent.example.org/.well-known/agent.json", Version: "0.2", AuthScheme: "erc8004-delegation", Capabilities: []string{"task.delegate"}},
		},
		Capabilities: []string{"code.analyze", "sandbox.read", "x402.pay"},
	})
	code, raw := doJSON(http.MethodPost, "/v1/erc8004/agents", agentReq)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/erc8004/agents = %d: %s", code, string(raw))
	}
	var regOut RegisterAgentOut
	if err := json.Unmarshal(raw, &regOut); err != nil {
		t.Fatalf("decode register output: %v", err)
	}
	agentID := regOut.Agent.AgentID
	if agentID == "" || regOut.Tx.TxHash == "" {
		t.Fatalf("expected non-empty agentID and txHash, got %+v", regOut)
	}

	// Discover agent by MCP protocol and capability.
	code, raw = doJSON(http.MethodGet, "/v1/erc8004/agents?capability=code.analyze&protocol=mcp", nil)
	if code != http.StatusOK {
		t.Fatalf("discover MCP agent = %d: %s", code, string(raw))
	}
	var discOut DiscoverOut
	if err := json.Unmarshal(raw, &discOut); err != nil || len(discOut.Agents) != 1 || discOut.Agents[0].AgentID != agentID {
		t.Fatalf("unexpected discover result: %+v err=%v", discOut, err)
	}

	// Issue & verify offline SignedCredential without Hanzo dependency.
	cred := SignedCredential{
		CredentialID:   "cred-mcp-001",
		AgentID:        agentID,
		IssuerAddress:  actors.owner.Address(),
		SubjectAddress: actors.controller.Address(),
		ClaimType:      "mcp.endpoint",
		Capabilities:   []string{"code.analyze", "sandbox.read"},
		MCPEndpoints:   regOut.Agent.MCPEndpoints,
		A2AEndpoints:   regOut.Agent.A2AEndpoints,
		IssuedAt:       now,
		ExpiresAt:      now + 3600,
		Nonce:          1,
	}
	credSig, err := actors.owner.SignDigest(cred.Digest(domain))
	if err != nil {
		t.Fatalf("sign credential: %v", err)
	}
	cred.Signature = credSig
	if err := VerifyCredentialOffline(cred, domain, now); err != nil {
		t.Fatalf("VerifyCredentialOffline failed: %v", err)
	}
	if err := svc.Identity.RegisterCredential(cred); err != nil {
		t.Fatalf("RegisterCredential: %v", err)
	}
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/credentials/verify", cred)
	if code != http.StatusOK {
		t.Fatalf("POST /credentials/verify = %d: %s", code, string(raw))
	}

	// 2. Delegate a bounded task + strict spending limit to a delegate key.
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          agentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "audit-repo-pr-42",
		AllowedTools:     []string{"code.analyze", "sandbox.read"},
		AllowedResources: []string{"sandbox://tenant-a/workspace/*"},
		MaxSpendAtomic:   500000, // 0.50 USDC (6 decimals)
		MaxPerCallAtomic: 250000, // 0.25 USDC max per call
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 1800,
		Nonce:            1,
		RevocationEpoch:  0,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/delegations", grant)
	if code != http.StatusOK {
		t.Fatalf("POST /delegations = %d: %s", code, string(raw))
	}

	// 3. Complete paid work within delegated bounds (enforced at tool/sandbox & payment gates).
	toolReq := signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "req-tool-1",
		DelegationID:    grant.DelegationID,
		AgentID:         agentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "code.analyze",
		Resource:        "sandbox://tenant-a/workspace/main.go",
		InputHash:       Keccak256Hex([]byte("analyze:main.go")),
		Nonce:           1,
		Timestamp:       now,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/enforce/tool", toolReq)
	if code != http.StatusOK {
		t.Fatalf("POST /enforce/tool = %d: %s", code, string(raw))
	}
	var toolRec ToolExecutionRecord
	if err := json.Unmarshal(raw, &toolRec); err != nil {
		t.Fatalf("decode tool record: %v", err)
	}
	if executedTools != 1 || toolRec.OutputHash == "" {
		t.Fatalf("expected 1 executed tool with outputHash, got count=%d rec=%+v", executedTools, toolRec)
	}

	payReq := signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-1",
		DelegationID:    grant.DelegationID,
		AgentID:         agentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    200000,
		Asset:           "USDC",
		TaskRef:         "audit-repo-pr-42",
		Nonce:           1,
		Timestamp:       now,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/enforce/payment", payReq)
	if code != http.StatusOK {
		t.Fatalf("POST /enforce/payment = %d: %s", code, string(raw))
	}
	var payRec PaymentAuthReceipt
	if err := json.Unmarshal(raw, &payRec); err != nil {
		t.Fatalf("decode payment receipt: %v", err)
	}
	if payRec.RemainingAtomic != 300000 {
		t.Fatalf("expected remaining 300000, got %d", payRec.RemainingAtomic)
	}

	// Record verifiable dual-signed WorkReceipt binding payment + tool output.
	workReceipt := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-001",
		AgentID:         agentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   payRec.PaymentTxHash,
		AmountAtomic:    payRec.AmountAtomic,
		Asset:           payRec.Asset,
		ToolCallsDigest: Keccak256Hex([]byte(toolRec.RequestID), []byte(toolRec.OutputHash)),
		TaskOutputHash:  toolRec.OutputHash,
		CompletedAt:     now,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/receipts", workReceipt)
	if code != http.StatusOK {
		t.Fatalf("POST /receipts = %d: %s", code, string(raw))
	}

	// 4. Client submits independently verifiable signed reputation feedback bound to paid receipt.
	fb := signFeedback(t, domain, actors.client, ReputationFeedback{
		FeedbackID:          "fb-001",
		AgentID:             agentID,
		ClientAddress:       actors.client.Address(),
		Score:               96,
		Tags:                []string{"accurate", "fast", "bounded"},
		Comment:             "Completed PR audit strictly within sandbox and budget.",
		WorkReceiptHash:     workReceipt.WorkReceiptHash,
		PaymentAmountAtomic: workReceipt.AmountAtomic,
		SubmittedAt:         now,
		Nonce:               1,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/feedback", fb)
	if code != http.StatusOK {
		t.Fatalf("POST /feedback = %d: %s", code, string(raw))
	}
	if err := VerifyFeedbackOffline(fb, workReceipt, domain); err != nil {
		t.Fatalf("VerifyFeedbackOffline: %v", err)
	}
	summary := svc.Reputation.GetReputation(agentID)
	if summary.FeedbackCount != 1 || summary.AverageScore != 96 || summary.TotalPaidAtomic != 200000 {
		t.Fatalf("unexpected reputation summary: %+v", summary)
	}

	// 5. Compromised delegation is instantaneously revoked -> subsequent tool & payment calls fail closed!
	revSig, err := actors.owner.SignDigest(RevocationDigest(domain, agentID, grant.DelegationID, 1))
	if err != nil {
		t.Fatalf("sign revocation: %v", err)
	}
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/delegations/revoke", RevokeDelegationIn{
		AgentID:        agentID,
		DelegationID:   grant.DelegationID,
		Nonce:          1,
		OwnerSignature: revSig,
	})
	if code != http.StatusOK {
		t.Fatalf("POST /delegations/revoke = %d: %s", code, string(raw))
	}

	// Subsequent tool call by compromised delegate MUST fail closed immediately.
	postRevTool := signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "req-tool-after-revoke",
		DelegationID:    grant.DelegationID,
		AgentID:         agentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "code.analyze",
		Resource:        "sandbox://tenant-a/workspace/main.go",
		InputHash:       Keccak256Hex([]byte("post-revoke")),
		Nonce:           2,
		Timestamp:       now,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/enforce/tool", postRevTool)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for revoked tool call, got %d: %s", code, string(raw))
	}
	if executedTools != 1 {
		t.Fatalf("revoked tool call must not execute tool callback; executedTools=%d", executedTools)
	}

	// Subsequent payment call by compromised delegate MUST fail closed immediately.
	postRevPay := signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-after-revoke",
		DelegationID:    grant.DelegationID,
		AgentID:         agentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.attacker.Address(),
		AmountAtomic:    1000,
		Asset:           "USDC",
		TaskRef:         "drain",
		Nonce:           2,
		Timestamp:       now,
	})
	code, raw = doJSON(http.MethodPost, "/v1/erc8004/enforce/payment", postRevPay)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for revoked payment call, got %d: %s", code, string(raw))
	}
}

// TestAdversarialTrustProbes verifies 100% detection across all security probes and
// contrasts against the nft_ownership_only_negative control.
func TestAdversarialTrustProbes(t *testing.T) {
	now := int64(1760000000)
	clock := func() int64 { return now }
	domain := DefaultDomain()
	actors := newTestActors(t)

	svc := NewService(domain, clock, nil)
	ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://agent-probe-meta",
		Capabilities:     []string{"sandbox.read"},
	}))
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "read-only-audit",
		AllowedTools:     []string{"sandbox.read"},
		AllowedResources: []string{"sandbox://tenant-a/docs/*"},
		MaxSpendAtomic:   100000,
		MaxPerCallAtomic: 60000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 600,
		Nonce:            1,
		RevocationEpoch:  0,
	})
	if _, err := svc.Identity.RegisterDelegation(grant); err != nil {
		t.Fatalf("RegisterDelegation: %v", err)
	}

	// Negative control: NFT-only check allows out-of-scope escalation & overspend!
	nftOnly := NFTOnlyNegativeEnforcer{Registry: svc.Identity}
	if err := nftOnly.CheckToolNFTOnly(ag.AgentID); err != nil {
		t.Fatalf("expected NFT-only negative control to pass blindly, got %v", err)
	}
	if err := nftOnly.CheckPaymentNFTOnly(ag.AgentID); err != nil {
		t.Fatalf("expected NFT-only negative control payment to pass blindly, got %v", err)
	}

	// Probe 1: Out-of-scope tool escalation ("shell.exec" not in allowlist).
	_, err = svc.Gate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "probe-tool-esc",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "shell.exec",
		Resource:        "sandbox://tenant-a/docs/readme.md",
		InputHash:       "0x01",
		Nonce:           1,
		Timestamp:       now,
	}), nil)
	if !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("probe tool escalation: got %v, want ErrToolNotAllowed", err)
	}

	// Probe 2: Sandbox path traversal escape ("..").
	_, err = svc.Gate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "probe-traversal",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/docs/../../tenant-b/secrets.env",
		InputHash:       "0x02",
		Nonce:           2,
		Timestamp:       now,
	}), nil)
	if !errors.Is(err, ErrResourceNotAllowed) {
		t.Fatalf("probe sandbox traversal: got %v, want ErrResourceNotAllowed", err)
	}

	// Probe 3: Per-call payment cap exceeded (60001 > 60000).
	_, err = svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "probe-per-call",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    60001,
		Asset:           "USDC",
		TaskRef:         "t1",
		Nonce:           1,
		Timestamp:       now,
	}))
	if !errors.Is(err, ErrPerCallLimitExceeded) {
		t.Fatalf("probe per-call cap: got %v, want ErrPerCallLimitExceeded", err)
	}

	// Probe 4: Cumulative spend cap exceeded (60000 + 50000 > 100000).
	if _, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-ok-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    60000,
		Asset:           "USDC",
		TaskRef:         "t1",
		Nonce:           2,
		Timestamp:       now,
	})); err != nil {
		t.Fatalf("valid payment 1 failed: %v", err)
	}
	_, err = svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "probe-cum-cap",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    50000,
		Asset:           "USDC",
		TaskRef:         "t2",
		Nonce:           3,
		Timestamp:       now,
	}))
	if !errors.Is(err, ErrSpendLimitExceeded) {
		t.Fatalf("probe cumulative cap: got %v, want ErrSpendLimitExceeded", err)
	}

	// Probe 5: Replay attack on previous valid payment nonce/authID.
	_, err = svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, actors.delegate, PaymentAuthRequest{
		AuthID:          "pay-ok-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Payee:           actors.client.Address(),
		AmountAtomic:    1000,
		Asset:           "USDC",
		TaskRef:         "replay",
		Nonce:           2,
		Timestamp:       now,
	}))
	if !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("probe replay: got %v, want ErrReplayDetected", err)
	}

	// Probe 6: Cross-chain domain signature replay rejected.
	otherDomain := domain
	otherDomain.ChainID = 1
	_, err = svc.Gate.Tools.Execute(signToolReq(t, otherDomain, actors.delegate, ToolActionRequest{
		RequestID:       "probe-cross-chain",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/docs/a.md",
		InputHash:       "0x03",
		Nonce:           10,
		Timestamp:       now,
	}), nil)
	if !errors.Is(err, ErrUnauthorizedSigner) {
		t.Fatalf("probe cross-chain replay: got %v, want ErrUnauthorizedSigner", err)
	}

	// Probe 7: Forged feedback without verified paid work receipt.
	_, err = svc.Reputation.SubmitFeedback(signFeedback(t, domain, actors.attacker, ReputationFeedback{
		FeedbackID:          "fb-forged",
		AgentID:             ag.AgentID,
		ClientAddress:       actors.attacker.Address(),
		Score:               100,
		WorkReceiptHash:     Keccak256Hex([]byte("nonexistent-work")),
		PaymentAmountAtomic: 50000,
		SubmittedAt:         now,
		Nonce:               1,
	}))
	if !errors.Is(err, ErrWorkReceiptNotFound) {
		t.Fatalf("probe forged unpaid feedback: got %v, want ErrWorkReceiptNotFound", err)
	}

	// Probe 8: Direct self-feedback by agent owner rejected.
	selfPayTx := Keccak256Hex([]byte("self-tx"))
	if err := svc.Identity.RecordVerifiedPayment(PaymentAuthReceipt{
		AgentID:       ag.AgentID,
		DelegationID:  grant.DelegationID,
		PayerAddress:  actors.owner.Address(),
		PayeeAddress:  actors.delegate.Address(),
		AmountAtomic:  10000,
		Asset:         "USDC",
		PaymentTxHash: selfPayTx,
		AuthorizedAt:  now,
	}); err != nil {
		t.Fatalf("RecordVerifiedPayment (self test setup): %v", err)
	}
	selfReceipt := signWorkReceipt(t, domain, actors.delegate, actors.owner, WorkReceipt{
		ReceiptID:       "wr-self",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.owner.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   selfPayTx,
		AmountAtomic:    10000,
		Asset:           "USDC",
		ToolCallsDigest: Keccak256Hex([]byte("tc")),
		TaskOutputHash:  Keccak256Hex([]byte("out")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(selfReceipt); err != nil {
		t.Fatalf("RecordWorkReceipt (self test setup): %v", err)
	}
	_, err = svc.Reputation.SubmitFeedback(signFeedback(t, domain, actors.owner, ReputationFeedback{
		FeedbackID:          "fb-self",
		AgentID:             ag.AgentID,
		ClientAddress:       actors.owner.Address(),
		Score:               100,
		WorkReceiptHash:     selfReceipt.WorkReceiptHash,
		PaymentAmountAtomic: selfReceipt.AmountAtomic,
		SubmittedAt:         now,
		Nonce:               1,
	}))
	if !errors.Is(err, ErrSelfFeedbackDetected) {
		t.Fatalf("probe direct self-feedback: got %v, want ErrSelfFeedbackDetected", err)
	}

	// Probe 9: Bulk epoch revocation invalidates older grants immediately.
	bumpSig, err := actors.owner.SignDigest(EpochBumpDigest(domain, ag.AgentID, 1))
	if err != nil {
		t.Fatalf("sign epoch bump: %v", err)
	}
	if _, err := svc.Identity.BumpRevocationEpoch(ag.AgentID, 1, bumpSig); err != nil {
		t.Fatalf("BumpRevocationEpoch: %v", err)
	}
	_, err = svc.Gate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "probe-epoch-stale",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/docs/a.md",
		InputHash:       "0x04",
		Nonce:           11,
		Timestamp:       now,
	}), nil)
	if !errors.Is(err, ErrRevocationEpochStale) {
		t.Fatalf("probe stale revocation epoch: got %v, want ErrRevocationEpochStale", err)
	}

	// Probe 10: Expired delegation rejected even at new epoch.
	grant2 := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "short-lived",
		AllowedTools:     []string{"sandbox.read"},
		AllowedResources: []string{"sandbox://tenant-a/docs/*"},
		MaxSpendAtomic:   10000,
		MaxPerCallAtomic: 10000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 10,
		Nonce:            2,
		RevocationEpoch:  1,
	})
	if _, err := svc.Identity.RegisterDelegation(grant2); err != nil {
		t.Fatalf("RegisterDelegation grant2: %v", err)
	}
	now += 20 // advance past expiry
	_, err = svc.Gate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "probe-expired",
		DelegationID:    grant2.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/docs/a.md",
		InputHash:       "0x05",
		Nonce:           12,
		Timestamp:       now,
	}), nil)
	if !errors.Is(err, ErrDelegationExpired) {
		t.Fatalf("probe expired delegation: got %v, want ErrDelegationExpired", err)
	}
}

// TestBoundaryRegimes validates the stale_cache_revocation_window and
// sybil_self_feedback_wash_trading negative/boundary regimes.
func TestBoundaryRegimes(t *testing.T) {
	baseTime := time.Unix(1760000000, 0)
	curTime := baseTime
	clock := func() int64 { return curTime.Unix() }
	domain := DefaultDomain()
	actors := newTestActors(t)

	reg := NewIdentityRegistry(domain, clock)
	ag, _, err := reg.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://boundary-agent",
		Capabilities:     []string{"sandbox.read"},
	}))
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "cache-window-test",
		AllowedTools:     []string{"sandbox.read"},
		AllowedResources: []string{"sandbox://tenant-a/*"},
		MaxSpendAtomic:   100000,
		MaxPerCallAtomic: 10000,
		Asset:            "USDC",
		IssuedAt:         curTime.Unix(),
		ExpiresAt:        curTime.Unix() + 3600,
		Nonce:            1,
	})
	if _, err := reg.RegisterDelegation(grant); err != nil {
		t.Fatalf("RegisterDelegation: %v", err)
	}

	// Regime 2: stale_cache_revocation_window (TTL = 5s vs live O(1)).
	cachedChecker := NewCachedRevocationChecker(LiveRevocationChecker{Registry: reg}, 5*time.Second, func() time.Time { return curTime })
	cachedGate := NewEnforcementGate(reg, cachedChecker)
	liveGate := NewEnforcementGate(reg, LiveRevocationChecker{Registry: reg})

	// Warm the cache with a legitimate call at t=0.
	if _, err := cachedGate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "warm-1",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/file1",
		InputHash:       "0x11",
		Nonce:           1,
		Timestamp:       curTime.Unix(),
	}), nil); err != nil {
		t.Fatalf("warm call failed: %v", err)
	}

	// Revoke compromised delegation at t=1s.
	curTime = baseTime.Add(1 * time.Second)
	revSig, _ := actors.owner.SignDigest(RevocationDigest(domain, ag.AgentID, grant.DelegationID, 1))
	if _, err := reg.RevokeDelegation(ag.AgentID, grant.DelegationID, revSig, 1); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}

	// At t=2s (inside 5s cache TTL): live gate rejects immediately, cached gate suffers stale-window bypass!
	curTime = baseTime.Add(2 * time.Second)
	if _, err := liveGate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "live-post-revoke",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/file2",
		InputHash:       "0x12",
		Nonce:           2,
		Timestamp:       curTime.Unix(),
	}), nil); !errors.Is(err, ErrDelegationRevoked) {
		t.Fatalf("live gate must reject immediately at t=2s, got %v", err)
	}
	if _, err := cachedGate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "cached-stale-bypass",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/file2",
		InputHash:       "0x12",
		Nonce:           2,
		Timestamp:       curTime.Unix(),
	}), nil); err != nil {
		t.Fatalf("expected stale cache window bypass inside TTL=5s, got %v", err)
	}

	// At t=6s (past 5s cache TTL): cached gate refreshes and rejects.
	curTime = baseTime.Add(6 * time.Second)
	if _, err := cachedGate.Tools.Execute(signToolReq(t, domain, actors.delegate, ToolActionRequest{
		RequestID:       "cached-after-ttl",
		DelegationID:    grant.DelegationID,
		AgentID:         ag.AgentID,
		DelegateAddress: actors.delegate.Address(),
		Tool:            "sandbox.read",
		Resource:        "sandbox://tenant-a/file3",
		InputHash:       "0x13",
		Nonce:           3,
		Timestamp:       curTime.Unix(),
	}), nil); !errors.Is(err, ErrDelegationRevoked) {
		t.Fatalf("expected cached gate to reject after TTL expires, got %v", err)
	}

	// Regime 3: sybil_self_feedback_wash_trading.
	// A fresh sockpuppet wallet paying the agent passes cryptographic signature checks,
	// demonstrating honestly that signature-bound receipts alone do not prevent wash trading
	// without concentration/stake/graph weighting.
	rep := NewReputationRegistry(reg)
	sockpuppet, _ := GenerateKeyPair()
	for i := 1; i <= 4; i++ {
		washTx := Keccak256Hex([]byte(fmt.Sprintf("wash-tx-%d", i)))
		if err := reg.RecordVerifiedPayment(PaymentAuthReceipt{
			AgentID:       ag.AgentID,
			DelegationID:  grant.DelegationID,
			PayerAddress:  sockpuppet.Address(),
			PayeeAddress:  actors.delegate.Address(),
			AmountAtomic:  1000,
			Asset:         "USDC",
			PaymentTxHash: washTx,
			AuthorizedAt:  curTime.Unix(),
		}); err != nil {
			t.Fatalf("RecordVerifiedPayment wash %d: %v", i, err)
		}
		wr := signWorkReceipt(t, domain, actors.delegate, sockpuppet, WorkReceipt{
			ReceiptID:       fmt.Sprintf("wash-%d", i),
			AgentID:         ag.AgentID,
			DelegationID:    grant.DelegationID,
			ClientAddress:   sockpuppet.Address(),
			WorkerAddress:   actors.delegate.Address(),
			PaymentTxHash:   washTx,
			AmountAtomic:    1000,
			Asset:           "USDC",
			ToolCallsDigest: Keccak256Hex([]byte("noop")),
			TaskOutputHash:  Keccak256Hex([]byte("noop-out")),
			CompletedAt:     curTime.Unix(),
		})
		if _, err := rep.RecordWorkReceipt(wr); err != nil {
			t.Fatalf("RecordWorkReceipt wash %d: %v", i, err)
		}
		if _, err := rep.SubmitFeedback(signFeedback(t, domain, sockpuppet, ReputationFeedback{
			FeedbackID:          fmt.Sprintf("wash-fb-%d", i),
			AgentID:             ag.AgentID,
			ClientAddress:       sockpuppet.Address(),
			Score:               100,
			WorkReceiptHash:     wr.WorkReceiptHash,
			PaymentAmountAtomic: wr.AmountAtomic,
			SubmittedAt:         curTime.Unix(),
			Nonce:               uint64(i),
		})); err != nil {
			t.Fatalf("SubmitFeedback wash %d: %v", i, err)
		}
	}
	summary := rep.GetReputation(ag.AgentID)
	if summary.FeedbackCount != 4 || summary.UniqueClients != 1 || summary.ClientConcentrationHHI != 1.0 || !summary.SybilWashTradeWarning {
		t.Fatalf("expected HHI=1.0 and SybilWashTradeWarning=true for single-sockpuppet wash trading, got %+v", summary)
	}
}

// TestSovereignExportMigrationAndTamperDetection verifies full offline JSONL export,
// independent verification, migration to a new registry, and tamper rejection.
func TestSovereignExportMigrationAndTamperDetection(t *testing.T) {
	now := int64(1760000000)
	clock := func() int64 { return now }
	domain := DefaultDomain()
	actors := newTestActors(t)

	svc := NewService(domain, clock, nil)
	ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, actors.owner, AgentIdentity{
		OwnerAddress:     actors.owner.Address(),
		ControllerPubKey: actors.controller.CompressedPubKeyHex(),
		MetadataURI:      "ipfs://sovereign-agent",
		MCPEndpoints:     []EndpointDescriptor{{Protocol: "mcp", URL: "https://independent.example/mcp", Version: "2025-03-26", AuthScheme: "erc8004-delegation"}},
		Capabilities:     []string{"code.analyze"},
	}))
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	grant := signDelegation(t, domain, actors.owner, DelegationGrant{
		AgentID:          ag.AgentID,
		OwnerAddress:     actors.owner.Address(),
		DelegateAddress:  actors.delegate.Address(),
		DelegatePubKey:   actors.delegate.CompressedPubKeyHex(),
		TaskScope:        "migrate-task",
		AllowedTools:     []string{"code.analyze"},
		AllowedResources: []string{"sandbox://org/*"},
		MaxSpendAtomic:   50000,
		MaxPerCallAtomic: 25000,
		Asset:            "USDC",
		IssuedAt:         now,
		ExpiresAt:        now + 3600,
		Nonce:            1,
	})
	if _, err := svc.Identity.RegisterDelegation(grant); err != nil {
		t.Fatalf("RegisterDelegation: %v", err)
	}
	migPayTx := Keccak256Hex([]byte("mig-pay"))
	if err := svc.Identity.RecordVerifiedPayment(PaymentAuthReceipt{
		AgentID:       ag.AgentID,
		DelegationID:  grant.DelegationID,
		PayerAddress:  actors.client.Address(),
		PayeeAddress:  actors.delegate.Address(),
		AmountAtomic:  25000,
		Asset:         "USDC",
		PaymentTxHash: migPayTx,
		AuthorizedAt:  now,
	}); err != nil {
		t.Fatalf("RecordVerifiedPayment: %v", err)
	}
	wr := signWorkReceipt(t, domain, actors.delegate, actors.client, WorkReceipt{
		ReceiptID:       "wr-mig-1",
		AgentID:         ag.AgentID,
		DelegationID:    grant.DelegationID,
		ClientAddress:   actors.client.Address(),
		WorkerAddress:   actors.delegate.Address(),
		PaymentTxHash:   migPayTx,
		AmountAtomic:    25000,
		Asset:           "USDC",
		ToolCallsDigest: Keccak256Hex([]byte("mig-tools")),
		TaskOutputHash:  Keccak256Hex([]byte("mig-out")),
		CompletedAt:     now,
	})
	if _, err := svc.Reputation.RecordWorkReceipt(wr); err != nil {
		t.Fatalf("RecordWorkReceipt: %v", err)
	}
	fb := signFeedback(t, domain, actors.client, ReputationFeedback{
		FeedbackID:          "fb-mig-1",
		AgentID:             ag.AgentID,
		ClientAddress:       actors.client.Address(),
		Score:               94,
		WorkReceiptHash:     wr.WorkReceiptHash,
		PaymentAmountAtomic: wr.AmountAtomic,
		SubmittedAt:         now,
		Nonce:               1,
	})
	if _, err := svc.Reputation.SubmitFeedback(fb); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	revSig, _ := actors.owner.SignDigest(RevocationDigest(domain, ag.AgentID, grant.DelegationID, 1))
	if _, err := svc.Identity.RevokeDelegation(ag.AgentID, grant.DelegationID, revSig, 1); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}

	// Export -> JSONL -> Read back -> Verify offline -> Import into fresh independent node.
	snap := svc.Identity.ExportSnapshot(svc.Reputation)
	var buf bytes.Buffer
	if err := WriteSnapshotJSONL(&buf, snap); err != nil {
		t.Fatalf("WriteSnapshotJSONL: %v", err)
	}
	decoded, err := ReadSnapshotJSONL(&buf)
	if err != nil {
		t.Fatalf("ReadSnapshotJSONL: %v", err)
	}
	if err := VerifySnapshotOffline(decoded); err != nil {
		t.Fatalf("VerifySnapshotOffline: %v", err)
	}

	migratedID, migratedRep, err := ImportSnapshot(decoded, clock)
	if err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	if migratedID.StateRoot() != svc.Identity.StateRoot() {
		t.Fatalf("migrated state root mismatch: got %s, want %s", migratedID.StateRoot(), svc.Identity.StateRoot())
	}
	if revoked, _ := migratedID.IsRevoked(grant.DelegationID, ag.AgentID, grant.RevocationEpoch); !revoked {
		t.Fatal("revoked delegation must remain revoked after migration")
	}
	if repSum := migratedRep.GetReputation(ag.AgentID); repSum.FeedbackCount != 1 || repSum.AverageScore != 94 {
		t.Fatalf("migrated reputation mismatch: %+v", repSum)
	}

	// Tamper detection: mutating feedback score without valid client signature must fail!
	decoded.Feedback[0].Score = 100
	if err := VerifySnapshotOffline(decoded); !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("expected ErrSnapshotTampered on tampered feedback, got %v", err)
	}
}

// TestConcurrentEnforcementAndRevocationRace exercises 16 concurrent goroutines racing
// spend-cap authorization and tool execution against mid-flight delegation revocation
// under `go test -race`.
func TestConcurrentEnforcementAndRevocationRace(t *testing.T) {
	now := int64(1760000000)
	clock := func() int64 { return now }
	domain := DefaultDomain()

	svc := NewService(domain, clock, nil)

	const numTenants = 4
	const workersPerTenant = 4 // 16 concurrent goroutines total
	const maxSpend uint64 = 50000
	const perCall uint64 = 10000

	type tenantCtx struct {
		owner    *KeyPair
		delegate *KeyPair
		payee    *KeyPair
		agentID  string
		grant    DelegationGrant
	}
	tenants := make([]tenantCtx, numTenants)
	for i := 0; i < numTenants; i++ {
		owner, _ := GenerateKeyPair()
		ctrl, _ := GenerateKeyPair()
		del, _ := GenerateKeyPair()
		payee, _ := GenerateKeyPair()

		ag, _, err := svc.Identity.RegisterAgent(signAgent(t, domain, owner, AgentIdentity{
			OwnerAddress:     owner.Address(),
			ControllerPubKey: ctrl.CompressedPubKeyHex(),
			MetadataURI:      fmt.Sprintf("ipfs://tenant-%d", i),
			Capabilities:     []string{"compute.run"},
		}))
		if err != nil {
			t.Fatalf("register tenant %d: %v", i, err)
		}
		g := signDelegation(t, domain, owner, DelegationGrant{
			AgentID:          ag.AgentID,
			OwnerAddress:     owner.Address(),
			DelegateAddress:  del.Address(),
			DelegatePubKey:   del.CompressedPubKeyHex(),
			TaskScope:        fmt.Sprintf("tenant-%d-race", i),
			AllowedTools:     []string{"compute.run"},
			AllowedResources: []string{fmt.Sprintf("sandbox://tenant-%d/*", i)},
			MaxSpendAtomic:   maxSpend,
			MaxPerCallAtomic: perCall,
			Asset:            "USDC",
			IssuedAt:         now,
			ExpiresAt:        now + 3600,
			Nonce:            1,
		})
		if _, err := svc.Identity.RegisterDelegation(g); err != nil {
			t.Fatalf("register delegation %d: %v", i, err)
		}
		tenants[i] = tenantCtx{owner: owner, delegate: del, payee: payee, agentID: ag.AgentID, grant: g}
	}

	var wg sync.WaitGroup
	var nonceCounter atomic.Uint64
	var approvedPayments atomic.Int64
	var revokedRejects atomic.Int64
	var capRejects atomic.Int64

	// Launch 16 racing worker goroutines across the 4 tenants.
	for ti := 0; ti < numTenants; ti++ {
		tc := tenants[ti]
		for wi := 0; wi < workersPerTenant; wi++ {
			wg.Add(1)
			go func(tenantIdx, workerIdx int, tctx tenantCtx) {
				defer wg.Done()
				for iter := 0; iter < 12; iter++ {
					n := nonceCounter.Add(1)
					_, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, tctx.delegate, PaymentAuthRequest{
						AuthID:          fmt.Sprintf("race-pay-%d-%d-%d", tenantIdx, workerIdx, n),
						DelegationID:    tctx.grant.DelegationID,
						AgentID:         tctx.agentID,
						DelegateAddress: tctx.delegate.Address(),
						Payee:           tctx.payee.Address(),
						AmountAtomic:    perCall,
						Asset:           "USDC",
						TaskRef:         "race",
						Nonce:           n,
						Timestamp:       now,
					}))
					switch {
					case err == nil:
						approvedPayments.Add(1)
					case errors.Is(err, ErrDelegationRevoked):
						revokedRejects.Add(1)
					case errors.Is(err, ErrSpendLimitExceeded):
						capRejects.Add(1)
					default:
						t.Errorf("unexpected payment error: %v", err)
					}

					// Interleave tool calls on the same delegation.
					tn := nonceCounter.Add(1)
					_, terr := svc.Gate.Tools.Execute(signToolReq(t, domain, tctx.delegate, ToolActionRequest{
						RequestID:       fmt.Sprintf("race-tool-%d-%d-%d", tenantIdx, workerIdx, tn),
						DelegationID:    tctx.grant.DelegationID,
						AgentID:         tctx.agentID,
						DelegateAddress: tctx.delegate.Address(),
						Tool:            "compute.run",
						Resource:        fmt.Sprintf("sandbox://tenant-%d/job-%d", tenantIdx, tn),
						InputHash:       "0xabc",
						Nonce:           tn,
						Timestamp:       now,
					}), nil)
					if terr != nil && !errors.Is(terr, ErrDelegationRevoked) {
						t.Errorf("unexpected tool error: %v", terr)
					}
				}
			}(ti, wi, tc)
		}
	}

	// Concurrently revoke tenants 2 and 3 mid-flight!
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Millisecond)
		for _, ti := range []int{2, 3} {
			tc := tenants[ti]
			sig, err := tc.owner.SignDigest(RevocationDigest(domain, tc.agentID, tc.grant.DelegationID, 1))
			if err != nil {
				t.Errorf("sign mid-flight revoke: %v", err)
				return
			}
			if _, err := svc.Identity.RevokeDelegation(tc.agentID, tc.grant.DelegationID, sig, 1); err != nil {
				t.Errorf("mid-flight revoke tenant %d: %v", ti, err)
			}
		}
	}()

	wg.Wait()

	// Strict invariant: NO tenant ever exceeded MaxSpendAtomic, even under 16-way contention!
	for i, tc := range tenants {
		spent := svc.Gate.Payments.Spent(tc.grant.DelegationID)
		if spent > maxSpend {
			t.Fatalf("tenant %d overspent cap: spent=%d > max=%d", i, spent, maxSpend)
		}
	}

	// Post-revocation invariant: tenants 2 and 3 MUST reject every subsequent call immediately.
	for _, ti := range []int{2, 3} {
		tc := tenants[ti]
		n := nonceCounter.Add(1)
		_, err := svc.Gate.Payments.AuthorizePayment(signPayReq(t, domain, tc.delegate, PaymentAuthRequest{
			AuthID:          fmt.Sprintf("post-race-pay-%d", ti),
			DelegationID:    tc.grant.DelegationID,
			AgentID:         tc.agentID,
			DelegateAddress: tc.delegate.Address(),
			Payee:           tc.payee.Address(),
			AmountAtomic:    1,
			Asset:           "USDC",
			TaskRef:         "post-revoke",
			Nonce:           n,
			Timestamp:       now,
		}))
		if !errors.Is(err, ErrDelegationRevoked) {
			t.Fatalf("tenant %d post-revocation payment: got %v, want ErrDelegationRevoked", ti, err)
		}
	}

	if approvedPayments.Load() == 0 || capRejects.Load() == 0 {
		t.Fatalf("expected both approvals and cap rejections, got approved=%d capRejects=%d revokedRejects=%d",
			approvedPayments.Load(), capRejects.Load(), revokedRejects.Load())
	}
}
