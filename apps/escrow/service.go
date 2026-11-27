// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Mount exposes the compute escrow HTTP operations on app under /v1/escrow.
// It imports only zip, luxlog, the Go standard library, and this package — never its host.
func Mount(app *zip.App, log luxlog.Logger, engine *EscrowEngine) error {
	if app == nil {
		return errors.New("escrow: nil app")
	}
	if engine == nil {
		engine = NewEscrowEngine(DefaultDomain(), nil, nil)
	}
	o := httpOps{engine: engine}
	g := app.Group("/v1/escrow")
	g.Get("/health", o.health)
	g.Post("/accounts/mint", o.mintAccount)
	g.Get("/accounts/:address", o.getAccount)
	g.Post("/quotes", o.createQuote)
	g.Post("/fund", o.fundEscrow)
	g.Post("/claim", o.submitClaim)
	g.Post("/challenge", o.openChallenge)
	g.Post("/resolve", o.resolveChallenge)
	g.Post("/finalize", o.finalizeEscrow)
	g.Post("/refund", o.refundMissedDeadline)
	g.Get("/jobs/:id", o.getEscrow)
	g.Get("/jobs", o.listEscrows)
	g.Get("/conservation", o.conservation)
	g.Get("/ledger", o.exportLedger)

	if log != nil {
		dom := engine.Domain()
		log.Info("escrow mounted", "chainId", dom.ChainID, "contract", dom.VerifyingContract)
	}
	return nil
}

type httpOps struct {
	engine *EscrowEngine
}

type emptyIn struct{}

// HealthOut reports escrow engine status, chain domain, ledger head hash, and conservation health.
type HealthOut struct {
	Service         string `json:"service"`
	Status          string `json:"status"`
	ChainID         uint64 `json:"chainId"`
	ContractAddress string `json:"contractAddress"`
	EscrowCount     int    `json:"escrowCount"`
	HeadHash        string `json:"headHash"`
	Conserved       bool   `json:"conserved"`
	Drift           int64  `json:"drift"`
}

func (o httpOps) health(_ context.Context, _ *emptyIn) (*HealthOut, error) {
	dom := o.engine.Domain()
	cons := o.engine.VerifyConservation()
	return &HealthOut{
		Service:         "escrow",
		Status:          "ok",
		ChainID:         dom.ChainID,
		ContractAddress: dom.VerifyingContract,
		EscrowCount:     o.engine.EscrowCount(),
		HeadHash:        o.engine.HeadHash(),
		Conserved:       cons.Conserved,
		Drift:           cons.Drift,
	}, nil
}

// MintIn funds a test-chain account with atomic settlement units.
type MintIn struct {
	Address string `json:"address"`
	Amount  uint64 `json:"amount"`
}

// AccountOut reports an account's unlocked and locked atomic balances.
type AccountOut struct {
	Address string `json:"address"`
	Balance uint64 `json:"balance"`
	Locked  uint64 `json:"locked"`
}

func (o httpOps) mintAccount(_ context.Context, in *MintIn) (*AccountOut, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	if err := o.engine.Mint(in.Address, in.Amount); err != nil {
		return nil, mapError(err)
	}
	norm, _ := NormalizeAddress(in.Address)
	return &AccountOut{
		Address: norm,
		Balance: o.engine.BalanceOf(norm),
		Locked:  o.engine.LockedOf(norm),
	}, nil
}

// AddressIn binds the :address path parameter.
type AddressIn struct {
	Address string `path:"address"`
}

func (o httpOps) getAccount(_ context.Context, in *AddressIn) (*AccountOut, error) {
	if in == nil || in.Address == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	norm, err := NormalizeAddress(in.Address)
	if err != nil {
		return nil, mapError(err)
	}
	return &AccountOut{
		Address: norm,
		Balance: o.engine.BalanceOf(norm),
		Locked:  o.engine.LockedOf(norm),
	}, nil
}

func (o httpOps) createQuote(_ context.Context, in *JobQuote) (*EscrowRecord, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.engine.CreateQuote(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

func (o httpOps) fundEscrow(_ context.Context, in *QuoteAcceptance) (*EscrowRecord, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.engine.FundEscrow(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

func (o httpOps) submitClaim(_ context.Context, in *CompletionClaim) (*EscrowRecord, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.engine.SubmitClaim(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

func (o httpOps) openChallenge(_ context.Context, in *ChallengeSubmission) (*EscrowRecord, error) {
	if in == nil {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.engine.OpenChallenge(*in)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

// JobActionIn identifies an escrow job for Finalize, RefundMissedDeadline, or ResolveChallenge.
type JobActionIn struct {
	JobID string `json:"jobId"`
}

func (o httpOps) resolveChallenge(_ context.Context, in *JobActionIn) (*PayoutRecord, error) {
	if in == nil || in.JobID == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	payout, err := o.engine.ResolveChallenge(in.JobID)
	if err != nil {
		return nil, mapError(err)
	}
	return payout, nil
}

func (o httpOps) finalizeEscrow(_ context.Context, in *JobActionIn) (*PayoutRecord, error) {
	if in == nil || in.JobID == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	payout, err := o.engine.Finalize(in.JobID)
	if err != nil {
		return nil, mapError(err)
	}
	return payout, nil
}

func (o httpOps) refundMissedDeadline(_ context.Context, in *JobActionIn) (*PayoutRecord, error) {
	if in == nil || in.JobID == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	payout, err := o.engine.RefundMissedDeadline(in.JobID)
	if err != nil {
		return nil, mapError(err)
	}
	return payout, nil
}

// JobIDIn binds the :id path parameter.
type JobIDIn struct {
	ID string `path:"id"`
}

func (o httpOps) getEscrow(_ context.Context, in *JobIDIn) (*EscrowRecord, error) {
	if in == nil || in.ID == "" {
		return nil, mapError(ErrInvalidRequest)
	}
	rec, err := o.engine.GetEscrow(in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return rec, nil
}

// EscrowsListOut returns all registered escrow records.
type EscrowsListOut struct {
	Escrows []*EscrowRecord `json:"escrows"`
}

func (o httpOps) listEscrows(_ context.Context, _ *emptyIn) (*EscrowsListOut, error) {
	return &EscrowsListOut{Escrows: o.engine.ListEscrows()}, nil
}

func (o httpOps) conservation(_ context.Context, _ *emptyIn) (*ConservationReport, error) {
	rep := o.engine.VerifyConservation()
	return &rep, nil
}

// LedgerExportOut returns the hash-chained JSONL settlement ledger and its tip hash.
type LedgerExportOut struct {
	HeadHash string `json:"headHash"`
	JSONL    string `json:"jsonl"`
}

func (o httpOps) exportLedger(_ context.Context, _ *emptyIn) (*LedgerExportOut, error) {
	var buf bytes.Buffer
	if err := o.engine.ExportLedgerJSONL(&buf); err != nil {
		return nil, mapError(err)
	}
	return &LedgerExportOut{
		HeadHash: o.engine.HeadHash(),
		JSONL:    buf.String(),
	}, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, ErrEscrowNotFound):
		return zip.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrSignerMismatch):
		return zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	case errors.Is(err, ErrSubjectiveSlashingForbidden),
		errors.Is(err, ErrUnknownViolationType),
		errors.Is(err, ErrNonDeterministicVerificationNeeded):
		return zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	case errors.Is(err, ErrAlreadyFinalized),
		errors.Is(err, ErrQuoteAlreadyExists),
		errors.Is(err, ErrInvalidStateTransition),
		errors.Is(err, ErrChallengeWindowActive),
		errors.Is(err, ErrChallengeWindowClosed),
		errors.Is(err, ErrDeadlineNotReached),
		errors.Is(err, ErrDeadlineExceeded):
		return zip.Errorf(http.StatusConflict, "%s", err.Error())
	case errors.Is(err, ErrInsufficientBalance):
		return zip.Errorf(http.StatusPaymentRequired, "%s", err.Error())
	default:
		return zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
}
