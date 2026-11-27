// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ReputationSummary aggregates independently verifiable feedback for an ERC-8004 agent,
// including transparent Sybil wash-trading exposure metrics.
type ReputationSummary struct {
	AgentID                string         `json:"agentId"`
	FeedbackCount          int            `json:"feedbackCount"`
	UniqueClients          int            `json:"uniqueClients"`
	TotalPaidAtomic        uint64         `json:"totalPaidAtomic"`
	AverageScore           float64        `json:"averageScore"`
	PaymentWeightedScore   float64        `json:"paymentWeightedScore"`
	TagCounts              map[string]int `json:"tagCounts"`
	ClientConcentrationHHI float64        `json:"clientConcentrationHhi"` // 1.0 = single client dominates; 1/N = uniform
	SybilWashTradeWarning  bool           `json:"sybilWashTradeWarning"`
}

// SybilAnalysis quantifies the boundary between cryptographic receipt verification and
// economic Sybil wash-trading (an attacker funding fresh sockpuppet wallets to pay
// themselves and submit high-score feedback).
type SybilAnalysis struct {
	AgentID                 string  `json:"agentId"`
	TotalFeedback           int     `json:"totalFeedback"`
	UniqueClients           int     `json:"uniqueClients"`
	TotalPaidAtomic         uint64  `json:"totalPaidAtomic"`
	ClientConcentrationHHI  float64 `json:"clientConcentrationHhi"`
	DirectSelfAttempts      int     `json:"directSelfAttemptsBlocked"`
	UnpaidAttemptsBlocked   int     `json:"unpaidAttemptsBlocked"`
	SockpuppetWashSucceeded int     `json:"sockpuppetWashSucceeded"`
	FeeBurnedPerPointAtomic float64 `json:"feeBurnedPerPointAtomic"`
	MitigationNote          string  `json:"mitigationNote"`
}

// ReputationRegistry tracks verified paid WorkReceipts and client-signed ERC-8004
// ReputationFeedback bound to actual completed paid work.
type ReputationRegistry struct {
	mu              sync.RWMutex
	identity        *IdentityRegistry
	receipts        map[string]WorkReceipt        // lower(workReceiptHash) -> WorkReceipt
	usedPaymentTx   map[string]bool               // lower(paymentTxHash) -> true
	feedback        map[string]ReputationFeedback // feedbackID -> ReputationFeedback
	byAgent         map[string][]string           // agentID -> []feedbackID
	usedReceipts    map[string]bool               // lower(workReceiptHash)+":"+lower(clientAddr) -> true
	paymentVerifier func(receipt WorkReceipt, ag AgentIdentity) error
	directSelf      int
	unpaidDenied    int
}

// NewReputationRegistry creates a reputation registry linked to identity.
func NewReputationRegistry(identity *IdentityRegistry) *ReputationRegistry {
	return &ReputationRegistry{
		identity:      identity,
		receipts:      make(map[string]WorkReceipt),
		usedPaymentTx: make(map[string]bool),
		feedback:      make(map[string]ReputationFeedback),
		byAgent:       make(map[string][]string),
		usedReceipts:  make(map[string]bool),
	}
}

// SetPaymentVerifier installs an optional external settlement verifier (e.g., on-chain
// ERC-3009 / x402 facilitator proof check) used by RecordWorkReceipt instead of the
// default IdentityRegistry verified-payment ledger lookup.
func (r *ReputationRegistry) SetPaymentVerifier(fn func(receipt WorkReceipt, ag AgentIdentity) error) {
	r.mu.Lock()
	r.paymentVerifier = fn
	r.mu.Unlock()
}

// VerifyWorkReceiptOffline independently verifies a WorkReceipt's hash commitment,
// 32-byte payment transaction hash, non-zero payment, and dual EIP-712 signatures
// (worker + client) without any remote call.
func VerifyWorkReceiptOffline(receipt WorkReceipt, domain EIP712Domain) error {
	if strings.TrimSpace(receipt.ReceiptID) == "" || strings.TrimSpace(receipt.AgentID) == "" {
		return fmt.Errorf("%w: work receipt missing receiptId or agentId", ErrInvalidRequest)
	}
	rawTx, err := decodeHex(receipt.PaymentTxHash)
	if err != nil || len(rawTx) != 32 {
		return fmt.Errorf("%w: work receipt paymentTxHash must be a 32-byte hex hash", ErrInvalidRequest)
	}
	if strings.TrimSpace(receipt.TaskOutputHash) == "" || strings.TrimSpace(receipt.Asset) == "" {
		return fmt.Errorf("%w: work receipt missing taskOutputHash or asset", ErrInvalidRequest)
	}
	if receipt.AmountAtomic == 0 {
		return ErrUnpaidWorkReceipt
	}
	if _, err := NormalizeAddress(receipt.ClientAddress); err != nil {
		return fmt.Errorf("%w: clientAddress: %v", ErrInvalidRequest, err)
	}
	if _, err := NormalizeAddress(receipt.WorkerAddress); err != nil {
		return fmt.Errorf("%w: workerAddress: %v", ErrInvalidRequest, err)
	}
	expectedHash := receipt.ComputeHash()
	if !strings.EqualFold(receipt.WorkReceiptHash, expectedHash) {
		return fmt.Errorf("%w: got %s, want %s", ErrWorkReceiptMismatch, receipt.WorkReceiptHash, expectedHash)
	}
	digest := receipt.Digest(domain)
	if err := VerifySigner(digest, receipt.WorkerSignature, receipt.WorkerAddress); err != nil {
		return fmt.Errorf("%w: worker signature: %v", ErrInvalidSignature, err)
	}
	if err := VerifySigner(digest, receipt.ClientAckSignature, receipt.ClientAddress); err != nil {
		return fmt.Errorf("%w: client acknowledgment signature: %v", ErrInvalidSignature, err)
	}
	return nil
}

func verifyWorkReceiptAgainstPayment(receipt WorkReceipt, ag AgentIdentity, vp PaymentAuthReceipt) error {
	if vp.AmountAtomic == 0 || vp.AmountAtomic != receipt.AmountAtomic {
		return fmt.Errorf("%w: payment amount %d != receipt amount %d", ErrWorkReceiptMismatch, vp.AmountAtomic, receipt.AmountAtomic)
	}
	if !strings.EqualFold(strings.TrimSpace(vp.Asset), strings.TrimSpace(receipt.Asset)) {
		return fmt.Errorf("%w: payment asset %q != receipt asset %q", ErrAssetMismatch, vp.Asset, receipt.Asset)
	}
	if vp.AgentID != "" && vp.AgentID != receipt.AgentID {
		return fmt.Errorf("%w: payment agentId %s != receipt agentId %s", ErrWorkReceiptMismatch, vp.AgentID, receipt.AgentID)
	}
	if receipt.DelegationID != "" && vp.DelegationID != "" && vp.DelegationID != receipt.DelegationID {
		return fmt.Errorf("%w: payment delegationId %s != receipt delegationId %s", ErrWorkReceiptMismatch, vp.DelegationID, receipt.DelegationID)
	}
	isAgentParty := func(addr string) bool {
		return SameAddress(addr, ag.OwnerAddress) ||
			SameAddress(addr, ag.ControllerAddress) ||
			(ag.PaymentWallet != "" && SameAddress(addr, ag.PaymentWallet)) ||
			SameAddress(addr, receipt.WorkerAddress)
	}
	clientPaidAgent := SameAddress(vp.PayerAddress, receipt.ClientAddress) && isAgentParty(vp.PayeeAddress)
	agentPaidClient := SameAddress(vp.PayeeAddress, receipt.ClientAddress) && isAgentParty(vp.PayerAddress)
	if !clientPaidAgent && !agentPaidClient {
		return fmt.Errorf("%w: payment participants (%s -> %s) do not match receipt client %s and agent %s",
			ErrWorkReceiptMismatch, vp.PayerAddress, vp.PayeeAddress, receipt.ClientAddress, receipt.AgentID)
	}
	return nil
}

// RecordWorkReceipt verifies dual signatures, worker authorization, and settled payment
// evidence matching amount, asset, and participants before registering a WorkReceipt.
func (r *ReputationRegistry) RecordWorkReceipt(receipt WorkReceipt) (ChainTxReceipt, error) {
	ag, err := r.identity.GetAgent(receipt.AgentID)
	if err != nil {
		return ChainTxReceipt{}, err
	}
	domain := r.identity.Domain()
	if err := VerifyWorkReceiptOffline(receipt, domain); err != nil {
		return ChainTxReceipt{}, err
	}

	// Verify the worker address is authorized for this agent (owner, controller, or registered delegate).
	workerAuthorized := SameAddress(receipt.WorkerAddress, ag.OwnerAddress) ||
		SameAddress(receipt.WorkerAddress, ag.ControllerAddress)
	if !workerAuthorized && receipt.DelegationID != "" {
		if del, err := r.identity.GetDelegation(receipt.DelegationID); err == nil &&
			del.AgentID == receipt.AgentID && SameAddress(del.DelegateAddress, receipt.WorkerAddress) {
			workerAuthorized = true
		}
	}
	if !workerAuthorized {
		return ChainTxReceipt{}, fmt.Errorf("%w: worker %s is not authorized for agent %s", ErrUnauthorizedSigner, receipt.WorkerAddress, receipt.AgentID)
	}

	// Require verified payment settlement evidence matching amount, asset, and participants.
	r.mu.RLock()
	verifier := r.paymentVerifier
	r.mu.RUnlock()
	if verifier != nil {
		if err := verifier(receipt, *ag); err != nil {
			r.mu.Lock()
			r.unpaidDenied++
			r.mu.Unlock()
			return ChainTxReceipt{}, err
		}
	} else {
		vp, ok := r.identity.GetVerifiedPayment(receipt.PaymentTxHash)
		if !ok {
			r.mu.Lock()
			r.unpaidDenied++
			r.mu.Unlock()
			return ChainTxReceipt{}, fmt.Errorf("%w: no verified payment settlement for tx %s", ErrUnpaidWorkReceipt, receipt.PaymentTxHash)
		}
		if err := verifyWorkReceiptAgainstPayment(receipt, *ag, vp); err != nil {
			r.mu.Lock()
			r.unpaidDenied++
			r.mu.Unlock()
			return ChainTxReceipt{}, err
		}
	}

	receipt.ClientAddress, _ = NormalizeAddress(receipt.ClientAddress)
	receipt.WorkerAddress, _ = NormalizeAddress(receipt.WorkerAddress)
	key := strings.ToLower(receipt.WorkReceiptHash)
	payKey := strings.ToLower(strings.TrimSpace(receipt.PaymentTxHash))

	r.mu.Lock()
	if _, exists := r.receipts[key]; exists {
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrReplayDetected
	}
	if r.usedPaymentTx[payKey] {
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrReplayDetected
	}
	r.receipts[key] = receipt
	r.usedPaymentTx[payKey] = true
	r.mu.Unlock()

	digest := receipt.Digest(domain)
	tx := r.identity.RecordExternalTx("record_work_receipt", receipt.ClientAddress, receipt.WorkReceiptHash, 52000, digest[:])
	return tx, nil
}

// VerifyFeedbackOffline cryptographically verifies a ReputationFeedback attestation
// against its corresponding paid WorkReceipt with zero registry or Hanzo dependency.
func VerifyFeedbackOffline(fb ReputationFeedback, receipt WorkReceipt, domain EIP712Domain) error {
	if fb.Score > 100 {
		return ErrInvalidScore
	}
	if strings.TrimSpace(fb.FeedbackID) == "" || strings.TrimSpace(fb.WorkReceiptHash) == "" {
		return fmt.Errorf("%w: feedback missing feedbackId or workReceiptHash", ErrInvalidRequest)
	}
	if err := VerifyWorkReceiptOffline(receipt, domain); err != nil {
		return err
	}
	if !strings.EqualFold(fb.WorkReceiptHash, receipt.WorkReceiptHash) {
		return ErrWorkReceiptMismatch
	}
	if fb.AgentID != receipt.AgentID {
		return fmt.Errorf("%w: feedback agentId %s != receipt agentId %s", ErrWorkReceiptMismatch, fb.AgentID, receipt.AgentID)
	}
	if !SameAddress(fb.ClientAddress, receipt.ClientAddress) {
		return fmt.Errorf("%w: feedback client %s != receipt client %s", ErrUnauthorizedSigner, fb.ClientAddress, receipt.ClientAddress)
	}
	if fb.PaymentAmountAtomic == 0 || fb.PaymentAmountAtomic != receipt.AmountAtomic {
		return fmt.Errorf("%w: feedback paymentAmountAtomic %d != receipt amountAtomic %d", ErrWorkReceiptMismatch, fb.PaymentAmountAtomic, receipt.AmountAtomic)
	}
	return VerifySigner(fb.Digest(domain), fb.ClientSignature, fb.ClientAddress)
}

// SubmitFeedback verifies a client-signed ERC-8004 reputation attestation against a
// verified completed paid WorkReceipt, rejecting forged/unpaid feedback, duplicates,
// and direct self-feedback.
func (r *ReputationRegistry) SubmitFeedback(fb ReputationFeedback) (ChainTxReceipt, error) {
	if fb.Score > 100 {
		return ChainTxReceipt{}, ErrInvalidScore
	}
	ag, err := r.identity.GetAgent(fb.AgentID)
	if err != nil {
		return ChainTxReceipt{}, err
	}

	key := strings.ToLower(strings.TrimSpace(fb.WorkReceiptHash))
	r.mu.Lock()
	receipt, ok := r.receipts[key]
	if !ok {
		r.unpaidDenied++
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrWorkReceiptNotFound
	}
	if receipt.AmountAtomic == 0 || fb.PaymentAmountAtomic == 0 {
		r.unpaidDenied++
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrUnpaidWorkReceipt
	}

	// Reject direct self-feedback where client == agent owner, controller, payment wallet, or worker.
	if SameAddress(fb.ClientAddress, ag.OwnerAddress) ||
		SameAddress(fb.ClientAddress, ag.ControllerAddress) ||
		(ag.PaymentWallet != "" && SameAddress(fb.ClientAddress, ag.PaymentWallet)) ||
		SameAddress(fb.ClientAddress, receipt.WorkerAddress) {
		r.directSelf++
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrSelfFeedbackDetected
	}
	r.mu.Unlock()

	domain := r.identity.Domain()
	if err := VerifyFeedbackOffline(fb, receipt, domain); err != nil {
		return ChainTxReceipt{}, err
	}

	clientNorm, _ := NormalizeAddress(fb.ClientAddress)
	fb.ClientAddress = clientNorm
	fb.Tags = sortedCopy(fb.Tags)
	dedupKey := key + ":" + strings.ToLower(clientNorm)

	r.mu.Lock()
	if r.usedReceipts[dedupKey] {
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrDuplicateFeedback
	}
	if _, exists := r.feedback[fb.FeedbackID]; exists {
		r.mu.Unlock()
		return ChainTxReceipt{}, ErrDuplicateFeedback
	}
	r.usedReceipts[dedupKey] = true
	r.feedback[fb.FeedbackID] = cloneFeedback(fb)
	r.byAgent[fb.AgentID] = append(r.byAgent[fb.AgentID], fb.FeedbackID)
	r.mu.Unlock()

	digest := fb.Digest(domain)
	tx := r.identity.RecordExternalTx("submit_feedback", clientNorm, fb.FeedbackID, GasSubmitFeedback, digest[:])
	return tx, nil
}

// GetReputation returns the aggregated verifiable reputation summary for agentID.
func (r *ReputationRegistry) GetReputation(agentID string) ReputationSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.byAgent[agentID]
	summary := ReputationSummary{
		AgentID:   agentID,
		TagCounts: make(map[string]int),
	}
	if len(ids) == 0 {
		return summary
	}

	var scoreSum float64
	var weightedNum float64
	clientSpend := make(map[string]uint64)

	for _, id := range ids {
		fb := r.feedback[id]
		summary.FeedbackCount++
		scoreSum += float64(fb.Score)
		weightedNum += float64(fb.Score) * float64(fb.PaymentAmountAtomic)
		summary.TotalPaidAtomic += fb.PaymentAmountAtomic
		clientSpend[fb.ClientAddress] += fb.PaymentAmountAtomic
		for _, tag := range fb.Tags {
			summary.TagCounts[tag]++
		}
	}

	summary.UniqueClients = len(clientSpend)
	summary.AverageScore = scoreSum / float64(summary.FeedbackCount)
	if summary.TotalPaidAtomic > 0 {
		summary.PaymentWeightedScore = weightedNum / float64(summary.TotalPaidAtomic)
		var hhi float64
		for _, amt := range clientSpend {
			share := float64(amt) / float64(summary.TotalPaidAtomic)
			hhi += share * share
		}
		summary.ClientConcentrationHHI = hhi
	}
	// Flag high concentration when multiple reviews are dominated by a narrow set of payers.
	if summary.FeedbackCount >= 3 && summary.ClientConcentrationHHI > 0.50 {
		summary.SybilWashTradeWarning = true
	}
	return summary
}

// ListFeedback returns all verified feedback records for agentID ordered deterministically.
func (r *ReputationRegistry) ListFeedback(agentID string) []ReputationFeedback {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.byAgent[agentID]
	out := make([]ReputationFeedback, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneFeedback(r.feedback[id]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FeedbackID < out[j].FeedbackID })
	return out
}

// GetWorkReceipt looks up a recorded WorkReceipt by its WorkReceiptHash.
func (r *ReputationRegistry) GetWorkReceipt(workReceiptHash string) (WorkReceipt, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wr, ok := r.receipts[strings.ToLower(strings.TrimSpace(workReceiptHash))]
	return wr, ok
}

// Stats returns counts of direct self-feedback and unpaid-feedback attempts rejected.
func (r *ReputationRegistry) Stats() (directSelfBlocked int, unpaidBlocked int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.directSelf, r.unpaidDenied
}

func (r *ReputationRegistry) exportAll() ([]WorkReceipt, []ReputationFeedback) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	receipts := make([]WorkReceipt, 0, len(r.receipts))
	for _, wr := range r.receipts {
		receipts = append(receipts, wr)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].ReceiptID < receipts[j].ReceiptID })

	fbs := make([]ReputationFeedback, 0, len(r.feedback))
	for _, fb := range r.feedback {
		fbs = append(fbs, cloneFeedback(fb))
	}
	sort.Slice(fbs, func(i, j int) bool { return fbs[i].FeedbackID < fbs[j].FeedbackID })
	return receipts, fbs
}

func (r *ReputationRegistry) importWorkReceipt(wr WorkReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receipts[strings.ToLower(wr.WorkReceiptHash)] = wr
	r.usedPaymentTx[strings.ToLower(wr.PaymentTxHash)] = true
}

func (r *ReputationRegistry) importFeedback(fb ReputationFeedback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feedback[fb.FeedbackID] = cloneFeedback(fb)
	r.byAgent[fb.AgentID] = append(r.byAgent[fb.AgentID], fb.FeedbackID)
	dedupKey := strings.ToLower(fb.WorkReceiptHash) + ":" + strings.ToLower(fb.ClientAddress)
	r.usedReceipts[dedupKey] = true
}

func cloneFeedback(f ReputationFeedback) ReputationFeedback {
	cp := f
	cp.Tags = append([]string(nil), f.Tags...)
	return cp
}
