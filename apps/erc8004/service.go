// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"context"
	"errors"
	"net/http"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Service composes the ERC-8004 IdentityRegistry, EnforcementGate, and ReputationRegistry
// into a self-contained, host-independent subsystem mountable on any zip.App.
type Service struct {
	Identity   *IdentityRegistry
	Gate       *EnforcementGate
	Reputation *ReputationRegistry
	Executor   ToolExecutor
}

// NewService constructs a standalone ERC-8004 Service using live O(1) revocation checking.
func NewService(domain EIP712Domain, clock Clock, executor ToolExecutor) *Service {
	id := NewIdentityRegistry(domain, clock)
	gate := NewEnforcementGate(id, LiveRevocationChecker{Registry: id})
	rep := NewReputationRegistry(id)
	return &Service{
		Identity:   id,
		Gate:       gate,
		Reputation: rep,
		Executor:   executor,
	}
}

// Mount exposes the ERC-8004 typed HTTP operations on app under /v1/erc8004.
// It imports only zip, luxlog, the Go standard library, and this package — never its host.
func Mount(app *zip.App, log luxlog.Logger, svc *Service) error {
	if app == nil {
		return errors.New("erc8004: nil app")
	}
	if svc == nil {
		svc = NewService(DefaultDomain(), nil, nil)
	}
	o := httpOps{svc: svc}
	g := app.Group("/v1/erc8004")
	g.Get("/health", o.health)
	g.Post("/agents", o.registerAgent)
	g.Get("/agents", o.discoverAgents)
	g.Get("/agents/:id", o.getAgent)
	g.Post("/credentials/verify", o.verifyCredential)
	g.Post("/delegations", o.registerDelegation)
	g.Post("/delegations/revoke", o.revokeDelegation)
	g.Post("/enforce/tool", o.enforceTool)
	g.Post("/enforce/payment", o.enforcePayment)
	g.Post("/receipts", o.recordWorkReceipt)
	g.Post("/feedback", o.submitFeedback)
	g.Get("/export", o.exportSnapshot)

	if log != nil {
		dom := svc.Identity.Domain()
		log.Info("erc8004 mounted", "chainId", dom.ChainID, "registry", dom.VerifyingContract)
	}
	return nil
}

type httpOps struct {
	svc *Service
}

type emptyIn struct{}

// HealthOut reports ERC-8004 service status and current test-chain state root.
type HealthOut struct {
	Service         string `json:"service"`
	Status          string `json:"status"`
	ChainID         uint64 `json:"chainId"`
	RegistryAddress string `json:"registryAddress"`
	BlockNumber     uint64 `json:"blockNumber"`
	StateRoot       string `json:"stateRoot"`
}

func (o httpOps) health(_ context.Context, _ *emptyIn) (*HealthOut, error) {
	dom := o.svc.Identity.Domain()
	return &HealthOut{
		Service:         "erc8004",
		Status:          "ok",
		ChainID:         dom.ChainID,
		RegistryAddress: dom.VerifyingContract,
		BlockNumber:     o.svc.Identity.BlockNumber(),
		StateRoot:       o.svc.Identity.StateRoot(),
	}, nil
}

// RegisterAgentOut returns the registered AgentIdentity and its deterministic test-chain tx receipt.
type RegisterAgentOut struct {
	Agent *AgentIdentity `json:"agent"`
	Tx    ChainTxReceipt `json:"tx"`
}

func (o httpOps) registerAgent(_ context.Context, in *AgentIdentity) (*RegisterAgentOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	ag, tx, err := o.svc.Identity.RegisterAgent(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return &RegisterAgentOut{Agent: ag, Tx: tx}, nil
}

// DiscoverIn filters active agents by optional capability and protocol ("mcp" or "a2a").
type DiscoverIn struct {
	Capability string `query:"capability"`
	Protocol   string `query:"protocol"`
}

// DiscoverOut lists matching ERC-8004 agent identities.
type DiscoverOut struct {
	Agents []*AgentIdentity `json:"agents"`
}

func (o httpOps) discoverAgents(_ context.Context, in *DiscoverIn) (*DiscoverOut, error) {
	var capFilter, protoFilter string
	if in != nil {
		capFilter = in.Capability
		protoFilter = in.Protocol
	}
	if capFilter == "" && protoFilter == "" {
		return &DiscoverOut{Agents: o.svc.Identity.ListAgents()}, nil
	}
	return &DiscoverOut{Agents: o.svc.Identity.DiscoverByCapability(capFilter, protoFilter)}, nil
}

// AgentIDIn binds the :id path parameter.
type AgentIDIn struct {
	ID string `path:"id"`
}

// AgentDetailOut combines an agent's ERC-8004 identity and verifiable reputation summary.
type AgentDetailOut struct {
	Agent      *AgentIdentity    `json:"agent"`
	Reputation ReputationSummary `json:"reputation"`
}

func (o httpOps) getAgent(_ context.Context, in *AgentIDIn) (*AgentDetailOut, error) {
	if in == nil || in.ID == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	ag, err := o.svc.Identity.GetAgent(in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return &AgentDetailOut{
		Agent:      ag,
		Reputation: o.svc.Reputation.GetReputation(ag.AgentID),
	}, nil
}

// CredentialVerifyOut reports stateless offline verification result.
type CredentialVerifyOut struct {
	Valid         bool   `json:"valid"`
	CredentialID  string `json:"credentialId"`
	IssuerAddress string `json:"issuerAddress"`
}

func (o httpOps) verifyCredential(_ context.Context, in *SignedCredential) (*CredentialVerifyOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	if err := VerifyCredentialOffline(*in, o.svc.Identity.Domain(), o.svc.Identity.Now()); err != nil {
		return nil, mapError(err)
	}
	return &CredentialVerifyOut{
		Valid:         true,
		CredentialID:  in.CredentialID,
		IssuerAddress: in.IssuerAddress,
	}, nil
}

// TxOut wraps a deterministic test-chain transaction receipt.
type TxOut struct {
	Tx ChainTxReceipt `json:"tx"`
}

func (o httpOps) registerDelegation(_ context.Context, in *DelegationGrant) (*TxOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	tx, err := o.svc.Identity.RegisterDelegation(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return &TxOut{Tx: tx}, nil
}

// RevokeDelegationIn carries an owner-signed revocation command.
type RevokeDelegationIn struct {
	AgentID        string `json:"agentId"`
	DelegationID   string `json:"delegationId"`
	Nonce          uint64 `json:"nonce"`
	OwnerSignature string `json:"ownerSignature"`
}

func (o httpOps) revokeDelegation(_ context.Context, in *RevokeDelegationIn) (*TxOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	tx, err := o.svc.Identity.RevokeDelegation(in.AgentID, in.DelegationID, in.OwnerSignature, in.Nonce)
	if err != nil {
		return nil, mapError(err)
	}
	return &TxOut{Tx: tx}, nil
}

func (o httpOps) enforceTool(_ context.Context, in *ToolActionRequest) (*ToolExecutionRecord, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.svc.Gate.Tools.Execute(*in, o.svc.Executor)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

func (o httpOps) enforcePayment(_ context.Context, in *PaymentAuthRequest) (*PaymentAuthReceipt, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.svc.Gate.Payments.AuthorizePayment(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

func (o httpOps) recordWorkReceipt(_ context.Context, in *WorkReceipt) (*TxOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	tx, err := o.svc.Reputation.RecordWorkReceipt(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return &TxOut{Tx: tx}, nil
}

func (o httpOps) submitFeedback(_ context.Context, in *ReputationFeedback) (*TxOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	tx, err := o.svc.Reputation.SubmitFeedback(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return &TxOut{Tx: tx}, nil
}

func (o httpOps) exportSnapshot(_ context.Context, _ *emptyIn) (*StateSnapshot, error) {
	return o.svc.Identity.ExportSnapshot(o.svc.Reputation), nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, ErrAgentNotFound), errors.Is(err, ErrDelegationNotFound), errors.Is(err, ErrWorkReceiptNotFound):
		return zip.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrUnauthorizedSigner):
		return zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	case errors.Is(err, ErrDelegationRevoked),
		errors.Is(err, ErrRevocationEpochStale),
		errors.Is(err, ErrDelegationExpired),
		errors.Is(err, ErrAgentInactive),
		errors.Is(err, ErrToolNotAllowed),
		errors.Is(err, ErrResourceNotAllowed),
		errors.Is(err, ErrPerCallLimitExceeded),
		errors.Is(err, ErrSpendLimitExceeded),
		errors.Is(err, ErrAssetMismatch),
		errors.Is(err, ErrSelfFeedbackDetected),
		errors.Is(err, ErrUnpaidWorkReceipt):
		return zip.Errorf(http.StatusForbidden, "%s", err.Error())
	case errors.Is(err, ErrReplayDetected), errors.Is(err, ErrDuplicateFeedback), errors.Is(err, ErrAgentAlreadyExists):
		return zip.Errorf(http.StatusConflict, "%s", err.Error())
	default:
		return zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
}
