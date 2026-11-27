// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package erc8004 implements portable ERC-8004 on-chain agent identity, discoverable
// MCP and A2A endpoint descriptors, cryptographically signed credentials, bounded
// scoped delegations with real runtime tool/sandbox/payment enforcement, instant
// revocation, and paid-work-bound verifiable reputation.
//
// Security invariant: an ERC-8004 identity NFT or wallet address alone is never
// sufficient for authorization. Every tool execution, sandbox resource access, and
// payment authorization is enforced fail-closed against signed delegation scopes,
// cumulative and per-call spend caps, replay protection, and live revocation state.
package erc8004

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Sentinel errors returned by the ERC-8004 registry, runtime enforcers, and reputation system.
var (
	ErrInvalidRequest       = errors.New("erc8004: invalid request")
	ErrInvalidSignature     = errors.New("erc8004: invalid signature")
	ErrUnauthorizedSigner   = errors.New("erc8004: unauthorized signer")
	ErrAgentNotFound        = errors.New("erc8004: agent not found")
	ErrAgentInactive        = errors.New("erc8004: agent is not active")
	ErrAgentAlreadyExists   = errors.New("erc8004: agent already registered")
	ErrCredentialExpired    = errors.New("erc8004: credential expired")
	ErrDelegationNotFound   = errors.New("erc8004: delegation not found")
	ErrDelegationRevoked    = errors.New("erc8004: delegation revoked")
	ErrRevocationEpochStale = errors.New("erc8004: delegation revocation epoch is stale")
	ErrDelegationExpired    = errors.New("erc8004: delegation expired")
	ErrToolNotAllowed       = errors.New("erc8004: tool not permitted by delegation")
	ErrResourceNotAllowed   = errors.New("erc8004: sandbox resource not permitted by delegation")
	ErrPerCallLimitExceeded = errors.New("erc8004: payment exceeds per-call delegation cap")
	ErrSpendLimitExceeded   = errors.New("erc8004: payment exceeds cumulative delegation spend cap")
	ErrAssetMismatch        = errors.New("erc8004: payment asset does not match delegation")
	ErrReplayDetected       = errors.New("erc8004: replay detected")
	ErrWorkReceiptNotFound  = errors.New("erc8004: verified paid work receipt not found")
	ErrWorkReceiptMismatch  = errors.New("erc8004: work receipt hash or fields mismatch")
	ErrUnpaidWorkReceipt    = errors.New("erc8004: feedback requires verified non-zero paid work")
	ErrDuplicateFeedback    = errors.New("erc8004: duplicate feedback for work receipt")
	ErrSelfFeedbackDetected = errors.New("erc8004: self-feedback from agent owner/controller/worker rejected")
	ErrInvalidScore         = errors.New("erc8004: reputation score must be in [0, 100]")
	ErrSnapshotTampered     = errors.New("erc8004: exported snapshot state root or signature verification failed")
)

// AgentStatus represents the lifecycle status of a registered ERC-8004 agent identity.
type AgentStatus string

const (
	StatusActive    AgentStatus = "active"
	StatusSuspended AgentStatus = "suspended"
	StatusRevoked   AgentStatus = "revoked"
)

// EndpointDescriptor advertises an agent's MCP, A2A, HTTP, or ZAP endpoint.
type EndpointDescriptor struct {
	Protocol     string   `json:"protocol"`               // "mcp", "a2a", "http", "zap"
	URL          string   `json:"url"`                    // endpoint URI
	Version      string   `json:"version"`                // protocol version, e.g. "2025-03-26" or "0.2"
	AuthScheme   string   `json:"authScheme"`             // e.g. "erc8004-delegation", "x402", "none"
	Capabilities []string `json:"capabilities,omitempty"` // endpoint-specific capability tags
}

// AgentIdentity is the portable ERC-8004 on-chain identity record for an agent.
type AgentIdentity struct {
	TokenID            uint64               `json:"tokenId"`
	AgentID            string               `json:"agentId"` // canonical CAIP-like eip155:<chainID>:<registry>:<tokenID>
	ChainID            uint64               `json:"chainId"`
	RegistryAddress    string               `json:"registryAddress"`
	OwnerAddress       string               `json:"ownerAddress"`
	ControllerPubKey   string               `json:"controllerPubKey"`
	ControllerAddress  string               `json:"controllerAddress"`
	PaymentWallet      string               `json:"paymentWallet,omitempty"`
	MetadataURI        string               `json:"metadataUri"`
	MCPEndpoints       []EndpointDescriptor `json:"mcpEndpoints,omitempty"`
	A2AEndpoints       []EndpointDescriptor `json:"a2aEndpoints,omitempty"`
	Capabilities       []string             `json:"capabilities"`
	Version            uint64               `json:"version"`
	Status             AgentStatus          `json:"status"`
	RevocationEpoch    uint64               `json:"revocationEpoch"`
	RegisteredAt       int64                `json:"registeredAt"`
	UpdatedAt          int64                `json:"updatedAt"`
	RegistrationTxHash string               `json:"registrationTxHash,omitempty"`
	OwnerSignature     string               `json:"ownerSignature"`
}

// CanonicalAgentID formats the portable ERC-8004 agent URI: eip155:<chainID>:<registry>:<tokenID>.
func CanonicalAgentID(chainID uint64, registryAddr string, tokenID uint64) string {
	norm, err := NormalizeAddress(registryAddr)
	if err != nil {
		norm = strings.ToLower(strings.TrimSpace(registryAddr))
	}
	return fmt.Sprintf("eip155:%d:%s:%d", chainID, norm, tokenID)
}

// RegistrationDigest computes the canonical EIP-712 signing digest committing to the agent's
// owner, controller, payment wallet, metadata, endpoints, capabilities, version, and status.
func (a AgentIdentity) RegistrationDigest(domain EIP712Domain) [32]byte {
	capsHash := eip712HashStringSlice(sortedCopy(a.Capabilities))
	mcpHash := hashEndpoints(a.MCPEndpoints)
	a2aHash := hashEndpoints(a.A2AEndpoints)

	typeHash := Keccak256Bytes([]byte(
		"AgentRegistration(uint64 chainId,address registry,address owner,string controllerPubKey,address paymentWallet,string metadataUri,bytes32 mcpHash,bytes32 a2aHash,bytes32 capabilitiesHash,uint64 version,string status)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712WordUint64(a.ChainID),
		eip712WordAddress(a.RegistryAddress),
		eip712WordAddress(a.OwnerAddress),
		eip712HashString(strings.ToLower(strings.TrimSpace(a.ControllerPubKey))),
		eip712WordAddress(a.PaymentWallet),
		eip712HashString(a.MetadataURI),
		mcpHash,
		a2aHash,
		capsHash,
		eip712WordUint64(a.Version),
		eip712HashString(string(a.Status)),
	)
	return domain.TypedDataDigest(structHash)
}

// UpdateDigest computes the canonical EIP-712 signing digest for an agent identity update,
// explicitly binding target TokenID and AgentID so a signed update for agent A cannot be
// replayed against agent B under the same owner.
func (a AgentIdentity) UpdateDigest(domain EIP712Domain) [32]byte {
	capsHash := eip712HashStringSlice(sortedCopy(a.Capabilities))
	mcpHash := hashEndpoints(a.MCPEndpoints)
	a2aHash := hashEndpoints(a.A2AEndpoints)

	typeHash := Keccak256Bytes([]byte(
		"AgentUpdate(uint64 chainId,address registry,uint64 tokenId,string agentId,address owner,string controllerPubKey,address paymentWallet,string metadataUri,bytes32 mcpHash,bytes32 a2aHash,bytes32 capabilitiesHash,uint64 version,string status)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712WordUint64(a.ChainID),
		eip712WordAddress(a.RegistryAddress),
		eip712WordUint64(a.TokenID),
		eip712HashString(a.AgentID),
		eip712WordAddress(a.OwnerAddress),
		eip712HashString(strings.ToLower(strings.TrimSpace(a.ControllerPubKey))),
		eip712WordAddress(a.PaymentWallet),
		eip712HashString(a.MetadataURI),
		mcpHash,
		a2aHash,
		capsHash,
		eip712WordUint64(a.Version),
		eip712HashString(string(a.Status)),
	)
	return domain.TypedDataDigest(structHash)
}

// SignedCredential is an offline-verifiable attestation of agent capabilities and MCP/A2A endpoints.
type SignedCredential struct {
	CredentialID   string               `json:"credentialId"`
	AgentID        string               `json:"agentId"`
	IssuerAddress  string               `json:"issuerAddress"`
	SubjectAddress string               `json:"subjectAddress"`
	ClaimType      string               `json:"claimType"`
	Capabilities   []string             `json:"capabilities,omitempty"`
	MCPEndpoints   []EndpointDescriptor `json:"mcpEndpoints,omitempty"`
	A2AEndpoints   []EndpointDescriptor `json:"a2aEndpoints,omitempty"`
	IssuedAt       int64                `json:"issuedAt"`
	ExpiresAt      int64                `json:"expiresAt"`
	Nonce          uint64               `json:"nonce"`
	Signature      string               `json:"signature"`
}

// Digest computes the canonical EIP-712 signing digest for a SignedCredential.
func (c SignedCredential) Digest(domain EIP712Domain) [32]byte {
	capsHash := eip712HashStringSlice(sortedCopy(c.Capabilities))
	mcpHash := hashEndpoints(c.MCPEndpoints)
	a2aHash := hashEndpoints(c.A2AEndpoints)
	typeHash := Keccak256Bytes([]byte(
		"SignedCredential(string credentialId,string agentId,address issuer,address subject,string claimType,bytes32 capabilitiesHash,bytes32 mcpHash,bytes32 a2aHash,int64 issuedAt,int64 expiresAt,uint64 nonce)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(c.CredentialID),
		eip712HashString(c.AgentID),
		eip712WordAddress(c.IssuerAddress),
		eip712WordAddress(c.SubjectAddress),
		eip712HashString(c.ClaimType),
		capsHash,
		mcpHash,
		a2aHash,
		eip712WordInt64(c.IssuedAt),
		eip712WordInt64(c.ExpiresAt),
		eip712WordUint64(c.Nonce),
	)
	return domain.TypedDataDigest(structHash)
}

// DelegationGrant bounds what tools, sandbox resources, and payment spend a delegate key
// may exercise on behalf of an ERC-8004 agent until ExpiresAt or revocation.
type DelegationGrant struct {
	DelegationID     string   `json:"delegationId"`
	AgentID          string   `json:"agentId"`
	OwnerAddress     string   `json:"ownerAddress"`
	DelegateAddress  string   `json:"delegateAddress"`
	DelegatePubKey   string   `json:"delegatePubKey"`
	TaskScope        string   `json:"taskScope"`
	AllowedTools     []string `json:"allowedTools"`
	AllowedResources []string `json:"allowedResources"`
	MaxSpendAtomic   uint64   `json:"maxSpendAtomic"`
	MaxPerCallAtomic uint64   `json:"maxPerCallAtomic"`
	Asset            string   `json:"asset"`
	IssuedAt         int64    `json:"issuedAt"`
	ExpiresAt        int64    `json:"expiresAt"`
	Nonce            uint64   `json:"nonce"`
	RevocationEpoch  uint64   `json:"revocationEpoch"`
	OwnerSignature   string   `json:"ownerSignature"`
}

// ComputeID returns the deterministic Keccak-256 delegation identifier derived from the
// grant's immutable security parameters.
func (g DelegationGrant) ComputeID() string {
	toolsHash := hashStrings(sortedCopy(g.AllowedTools)...)
	resHash := hashStrings(sortedCopy(g.AllowedResources)...)
	d := hashStrings(
		"erc8004:delegation:v1",
		g.AgentID,
		strings.ToLower(strings.TrimSpace(g.OwnerAddress)),
		strings.ToLower(strings.TrimSpace(g.DelegateAddress)),
		g.TaskScope,
		hex.EncodeToString(toolsHash[:]),
		hex.EncodeToString(resHash[:]),
		strconv.FormatUint(g.MaxSpendAtomic, 10),
		strconv.FormatUint(g.MaxPerCallAtomic, 10),
		g.Asset,
		strconv.FormatInt(g.IssuedAt, 10),
		strconv.FormatInt(g.ExpiresAt, 10),
		strconv.FormatUint(g.Nonce, 10),
		strconv.FormatUint(g.RevocationEpoch, 10),
	)
	return "0x" + hex.EncodeToString(d[:])
}

// Digest computes the canonical EIP-712 signing digest signed by OwnerAddress to authorize this grant.
func (g DelegationGrant) Digest(domain EIP712Domain) [32]byte {
	toolsHash := eip712HashStringSlice(sortedCopy(g.AllowedTools))
	resHash := eip712HashStringSlice(sortedCopy(g.AllowedResources))
	typeHash := Keccak256Bytes([]byte(
		"DelegationGrant(string delegationId,string agentId,address owner,address delegate,string delegatePubKey,string taskScope,bytes32 allowedToolsHash,bytes32 allowedResourcesHash,uint64 maxSpendAtomic,uint64 maxPerCallAtomic,string asset,int64 issuedAt,int64 expiresAt,uint64 nonce,uint64 revocationEpoch)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(g.DelegationID),
		eip712HashString(g.AgentID),
		eip712WordAddress(g.OwnerAddress),
		eip712WordAddress(g.DelegateAddress),
		eip712HashString(strings.ToLower(strings.TrimSpace(g.DelegatePubKey))),
		eip712HashString(g.TaskScope),
		toolsHash,
		resHash,
		eip712WordUint64(g.MaxSpendAtomic),
		eip712WordUint64(g.MaxPerCallAtomic),
		eip712HashString(g.Asset),
		eip712WordInt64(g.IssuedAt),
		eip712WordInt64(g.ExpiresAt),
		eip712WordUint64(g.Nonce),
		eip712WordUint64(g.RevocationEpoch),
	)
	return domain.TypedDataDigest(structHash)
}

// RevocationDigest computes the canonical EIP-712 digest signed by an agent owner to revoke a delegation.
func RevocationDigest(domain EIP712Domain, agentID, delegationID string, nonce uint64) [32]byte {
	typeHash := Keccak256Bytes([]byte("RevokeDelegation(string agentId,string delegationId,uint64 nonce)"))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(agentID),
		eip712HashString(delegationID),
		eip712WordUint64(nonce),
	)
	return domain.TypedDataDigest(structHash)
}

// EpochBumpDigest computes the canonical EIP-712 digest signed by an agent owner to bump the revocation epoch.
func EpochBumpDigest(domain EIP712Domain, agentID string, newEpoch uint64) [32]byte {
	typeHash := Keccak256Bytes([]byte("BumpRevocationEpoch(string agentId,uint64 newEpoch)"))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(agentID),
		eip712WordUint64(newEpoch),
	)
	return domain.TypedDataDigest(structHash)
}

// ToolActionRequest is a delegate-signed request to execute a specific tool against a sandbox resource.
type ToolActionRequest struct {
	RequestID         string `json:"requestId"`
	DelegationID      string `json:"delegationId"`
	AgentID           string `json:"agentId"`
	DelegateAddress   string `json:"delegateAddress"`
	Tool              string `json:"tool"`
	Resource          string `json:"resource"`
	InputHash         string `json:"inputHash"`
	Nonce             uint64 `json:"nonce"`
	Timestamp         int64  `json:"timestamp"`
	DelegateSignature string `json:"delegateSignature"`
}

// Digest computes the canonical EIP-712 signing digest signed by DelegateAddress for a tool call.
func (r ToolActionRequest) Digest(domain EIP712Domain) [32]byte {
	typeHash := Keccak256Bytes([]byte(
		"ToolActionRequest(string requestId,string delegationId,string agentId,address delegate,string tool,string resource,string inputHash,uint64 nonce,int64 timestamp)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(r.RequestID),
		eip712HashString(r.DelegationID),
		eip712HashString(r.AgentID),
		eip712WordAddress(r.DelegateAddress),
		eip712HashString(r.Tool),
		eip712HashString(r.Resource),
		eip712HashString(r.InputHash),
		eip712WordUint64(r.Nonce),
		eip712WordInt64(r.Timestamp),
	)
	return domain.TypedDataDigest(structHash)
}

// ToolExecutionRecord records an authorized tool execution and its output commitment.
type ToolExecutionRecord struct {
	RequestID    string `json:"requestId"`
	DelegationID string `json:"delegationId"`
	AgentID      string `json:"agentId"`
	Tool         string `json:"tool"`
	Resource     string `json:"resource"`
	Output       string `json:"output"`
	OutputHash   string `json:"outputHash"`
	ExecutedAt   int64  `json:"executedAt"`
}

// PaymentAuthRequest is a delegate-signed request to authorize a bounded payment.
type PaymentAuthRequest struct {
	AuthID            string `json:"authId"`
	DelegationID      string `json:"delegationId"`
	AgentID           string `json:"agentId"`
	DelegateAddress   string `json:"delegateAddress"`
	Payee             string `json:"payee"`
	AmountAtomic      uint64 `json:"amountAtomic"`
	Asset             string `json:"asset"`
	TaskRef           string `json:"taskRef"`
	Nonce             uint64 `json:"nonce"`
	Timestamp         int64  `json:"timestamp"`
	DelegateSignature string `json:"delegateSignature"`
}

// Digest computes the canonical EIP-712 signing digest signed by DelegateAddress for a payment authorization.
func (p PaymentAuthRequest) Digest(domain EIP712Domain) [32]byte {
	typeHash := Keccak256Bytes([]byte(
		"PaymentAuthRequest(string authId,string delegationId,string agentId,address delegate,address payee,uint64 amountAtomic,string asset,string taskRef,uint64 nonce,int64 timestamp)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(p.AuthID),
		eip712HashString(p.DelegationID),
		eip712HashString(p.AgentID),
		eip712WordAddress(p.DelegateAddress),
		eip712WordAddress(p.Payee),
		eip712WordUint64(p.AmountAtomic),
		eip712HashString(p.Asset),
		eip712HashString(p.TaskRef),
		eip712WordUint64(p.Nonce),
		eip712WordInt64(p.Timestamp),
	)
	return domain.TypedDataDigest(structHash)
}

// PaymentAuthReceipt proves that a payment authorization passed delegation enforcement
// and was atomically debited against the delegation's spend cap.
type PaymentAuthReceipt struct {
	AuthID          string `json:"authId"`
	DelegationID    string `json:"delegationId"`
	AgentID         string `json:"agentId"`
	PayerAddress    string `json:"payerAddress"`
	PayeeAddress    string `json:"payeeAddress"`
	AmountAtomic    uint64 `json:"amountAtomic"`
	SpentTotal      uint64 `json:"spentTotal"`
	RemainingAtomic uint64 `json:"remainingAtomic"`
	Asset           string `json:"asset"`
	PaymentTxHash   string `json:"paymentTxHash"`
	AuthorizedAt    int64  `json:"authorizedAt"`
}

// WorkReceipt cryptographically binds completed work (tool output hash) and settled
// payment (PaymentTxHash + AmountAtomic) so ERC-8004 reputation feedback can only be
// submitted against verifiable paid work.
type WorkReceipt struct {
	ReceiptID          string `json:"receiptId"`
	AgentID            string `json:"agentId"`
	DelegationID       string `json:"delegationId"`
	ClientAddress      string `json:"clientAddress"`
	WorkerAddress      string `json:"workerAddress"`
	PaymentTxHash      string `json:"paymentTxHash"`
	AmountAtomic       uint64 `json:"amountAtomic"`
	Asset              string `json:"asset"`
	ToolCallsDigest    string `json:"toolCallsDigest"`
	TaskOutputHash     string `json:"taskOutputHash"`
	CompletedAt        int64  `json:"completedAt"`
	WorkReceiptHash    string `json:"workReceiptHash"`
	WorkerSignature    string `json:"workerSignature"`
	ClientAckSignature string `json:"clientAckSignature"`
}

// ComputeHash returns the canonical Keccak-256 WorkReceiptHash linking payment and output.
func (w WorkReceipt) ComputeHash() string {
	d := hashStrings(
		"erc8004:work-receipt:v1",
		w.ReceiptID,
		w.AgentID,
		w.DelegationID,
		strings.ToLower(strings.TrimSpace(w.ClientAddress)),
		strings.ToLower(strings.TrimSpace(w.WorkerAddress)),
		strings.ToLower(strings.TrimSpace(w.PaymentTxHash)),
		strconv.FormatUint(w.AmountAtomic, 10),
		w.Asset,
		strings.ToLower(strings.TrimSpace(w.ToolCallsDigest)),
		strings.ToLower(strings.TrimSpace(w.TaskOutputHash)),
		strconv.FormatInt(w.CompletedAt, 10),
	)
	return "0x" + hex.EncodeToString(d[:])
}

// Digest computes the canonical EIP-712 digest signed by both worker and client to attest to completed paid work.
func (w WorkReceipt) Digest(domain EIP712Domain) [32]byte {
	typeHash := Keccak256Bytes([]byte(
		"WorkReceipt(string receiptId,string agentId,string delegationId,address client,address worker,string paymentTxHash,uint64 amountAtomic,string asset,string toolCallsDigest,string taskOutputHash,int64 completedAt,string workReceiptHash)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(w.ReceiptID),
		eip712HashString(w.AgentID),
		eip712HashString(w.DelegationID),
		eip712WordAddress(w.ClientAddress),
		eip712WordAddress(w.WorkerAddress),
		eip712HashString(strings.ToLower(strings.TrimSpace(w.PaymentTxHash))),
		eip712WordUint64(w.AmountAtomic),
		eip712HashString(w.Asset),
		eip712HashString(strings.ToLower(strings.TrimSpace(w.ToolCallsDigest))),
		eip712HashString(strings.ToLower(strings.TrimSpace(w.TaskOutputHash))),
		eip712WordInt64(w.CompletedAt),
		eip712HashString(strings.ToLower(strings.TrimSpace(w.WorkReceiptHash))),
	)
	return domain.TypedDataDigest(structHash)
}

// ReputationFeedback is a client-signed ERC-8004 reputation attestation bound to a verified paid WorkReceipt.
type ReputationFeedback struct {
	FeedbackID          string   `json:"feedbackId"`
	AgentID             string   `json:"agentId"`
	ClientAddress       string   `json:"clientAddress"`
	Score               uint8    `json:"score"` // 0..100
	Tags                []string `json:"tags,omitempty"`
	Comment             string   `json:"comment,omitempty"`
	WorkReceiptHash     string   `json:"workReceiptHash"`
	PaymentAmountAtomic uint64   `json:"paymentAmountAtomic"`
	SubmittedAt         int64    `json:"submittedAt"`
	Nonce               uint64   `json:"nonce"`
	ClientSignature     string   `json:"clientSignature"`
}

// Digest computes the canonical EIP-712 signing digest signed by ClientAddress.
func (f ReputationFeedback) Digest(domain EIP712Domain) [32]byte {
	tagsHash := eip712HashStringSlice(sortedCopy(f.Tags))
	typeHash := Keccak256Bytes([]byte(
		"ReputationFeedback(string feedbackId,string agentId,address client,uint8 score,bytes32 tagsHash,string comment,string workReceiptHash,uint64 paymentAmountAtomic,int64 submittedAt,uint64 nonce)",
	))
	structHash := eip712EncodeStruct(
		typeHash,
		eip712HashString(f.FeedbackID),
		eip712HashString(f.AgentID),
		eip712WordAddress(f.ClientAddress),
		eip712WordUint8(f.Score),
		tagsHash,
		eip712HashString(f.Comment),
		eip712HashString(strings.ToLower(strings.TrimSpace(f.WorkReceiptHash))),
		eip712WordUint64(f.PaymentAmountAtomic),
		eip712WordInt64(f.SubmittedAt),
		eip712WordUint64(f.Nonce),
	)
	return domain.TypedDataDigest(structHash)
}

// ChainTxReceipt records a deterministic test-chain transaction, block height, gas used, and state root.
type ChainTxReceipt struct {
	TxHash      string `json:"txHash"`
	BlockNumber uint64 `json:"blockNumber"`
	Timestamp   int64  `json:"timestamp"`
	Action      string `json:"action"`
	Actor       string `json:"actor"`
	SubjectID   string `json:"subjectId"`
	GasUsed     uint64 `json:"gasUsed"`
	StateRoot   string `json:"stateRoot"`
}

// RevocationRecord records an instantaneous delegation revocation on-chain.
type RevocationRecord struct {
	DelegationID string `json:"delegationId"`
	AgentID      string `json:"agentId"`
	OwnerAddress string `json:"ownerAddress"`
	RevokedAt    int64  `json:"revokedAt"`
	Nonce        uint64 `json:"nonce"`
	OwnerSig     string `json:"ownerSignature"`
	TxHash       string `json:"txHash"`
}

// EpochBumpRecord records an owner-signed bulk revocation epoch bump on-chain.
type EpochBumpRecord struct {
	AgentID      string `json:"agentId"`
	NewEpoch     uint64 `json:"newEpoch"`
	OwnerAddress string `json:"ownerAddress"`
	BumpedAt     int64  `json:"bumpedAt"`
	OwnerSig     string `json:"ownerSignature"`
	TxHash       string `json:"txHash"`
}

// EnforcementState captures runtime spend counters, consumed tool/payment replay keys,
// and verified payment settlements so snapshot migration preserves all security invariants.
type EnforcementState struct {
	DelegationSpent  map[string]uint64    `json:"delegationSpent,omitempty"`
	ToolRequestIDs   []string             `json:"toolRequestIds,omitempty"`
	ToolNonces       []string             `json:"toolNonces,omitempty"`
	PaymentAuthIDs   []string             `json:"paymentAuthIds,omitempty"`
	PaymentNonces    []string             `json:"paymentNonces,omitempty"`
	VerifiedPayments []PaymentAuthReceipt `json:"verifiedPayments,omitempty"`
}

// StateSnapshot is a self-contained, independently verifiable export of all ERC-8004
// registry, delegation, revocation, enforcement, work-receipt, and reputation state for
// sovereign migration across chains or operators.
type StateSnapshot struct {
	FormatVersion string               `json:"formatVersion"`
	Domain        EIP712Domain         `json:"domain"`
	BlockNumber   uint64               `json:"blockNumber"`
	ExportedAt    int64                `json:"exportedAt"`
	StateRoot     string               `json:"stateRoot"`
	EntityRoot    string               `json:"entityRoot"`
	Agents        []AgentIdentity      `json:"agents"`
	Credentials   []SignedCredential   `json:"credentials"`
	Delegations   []DelegationGrant    `json:"delegations"`
	Revocations   []RevocationRecord   `json:"revocations"`
	EpochBumps    []EpochBumpRecord    `json:"epochBumps,omitempty"`
	Enforcement   EnforcementState     `json:"enforcement"`
	WorkReceipts  []WorkReceipt        `json:"workReceipts"`
	Feedback      []ReputationFeedback `json:"feedback"`
	TxLog         []ChainTxReceipt     `json:"txLog"`
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

func uint64Bytes(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}
