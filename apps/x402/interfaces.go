// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"context"
	"io"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
)

// InterfaceVersion identifies the versioned extension contract layered on top
// of github.com/hanzoai/commerce/payment/x402 (x402 v2 wire protocol).
const InterfaceVersion = "x402/v1"

// Extension keys carried inside the x402 v2 Extensions map so provider-signed
// quotes and facilitator provenance travel without inventing a second protocol.
const (
	ExtensionSignedQuote = "hanzo.signedQuote"
	ExtensionFacilitator = "hanzo.facilitator"
)

// HTTP headers used for idempotent request deduplication and receipt tracking.
const (
	HeaderIdempotencyKey   = "Idempotency-Key"
	HeaderIdempotentReplay = "X-Idempotent-Replay"
	HeaderReceiptID        = "X-Payment-Receipt-Id"
	HeaderRequestModel     = "X-Inference-Model"
)

// Facilitator is the canonical x402 Section 7 verify/settle interface from
// github.com/hanzoai/commerce/payment/x402.
type Facilitator = protocol.Facilitator

// NamedFacilitator pairs a Facilitator implementation with operator metadata
// so routing, failover, settlement pinning, and decentralization telemetry
// distinguish independent facilitators from any hosted convenience path.
type NamedFacilitator struct {
	ID          string      `json:"id"`
	Operator    string      `json:"operator"` // e.g. "independent", "community", "self-hosted", "circle", "hanzo"
	Endpoint    string      `json:"endpoint,omitempty"`
	Facilitator Facilitator `json:"-"`
}

// QuoteSigner signs exact per-request price quotes bound to a canonical request
// body hash and verifies returned quotes before inference execution.
type QuoteSigner interface {
	SignQuote(ctx context.Context, req QuoteRequest) (SignedPriceQuote, error)
	VerifyQuote(quote SignedPriceQuote, reqBodyHash string, now time.Time) error
	ProviderAddress() string
}

// NonceReplayStore tracks in-flight and settled ERC-3009 authorizations and
// correlates Idempotency-Key retries with their cached verified responses.
type NonceReplayStore interface {
	Reserve(ctx context.Context, claim NonceClaim, idempotencyKey, reqBodyHash string) (ReserveResult, error)
	Commit(ctx context.Context, claim NonceClaim, idempotencyKey string, cached *CachedResponse) error
	Release(ctx context.Context, claim NonceClaim, idempotencyKey string) error
	IsSpent(ctx context.Context, claim NonceClaim) bool
	LookupIdempotency(ctx context.Context, idempotencyKey string) (*IdempotencyRecord, bool)
}

// ReceiptLedger records cryptographically verifiable settlement receipts,
// handles facilitator reversal reconciliation, and exports sovereign JSONL logs.
type ReceiptLedger interface {
	Record(ctx context.Context, rec Receipt) (Receipt, error)
	Get(ctx context.Context, receiptID string) (Receipt, bool)
	FindByTransaction(ctx context.Context, facilitatorID, txHash string) (Receipt, bool)
	List(ctx context.Context, filter ReceiptFilter) []Receipt
	ReconcileReversal(ctx context.Context, ev ReversalEvent) (Receipt, error)
	ExportJSONL(w io.Writer) error
}

// InferenceRequest represents a metered model inference call protected by x402.
type InferenceRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// InferenceResponse represents a completed model inference answer with token
// usage for cost-per-million-token accounting.
type InferenceResponse struct {
	Model            string `json:"model"`
	Output           string `json:"output"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

// InferenceHandler executes model inference after payment admission succeeds.
type InferenceHandler interface {
	Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error)
}

// InferenceHandlerFunc adapts a plain function to InferenceHandler.
type InferenceHandlerFunc func(ctx context.Context, req InferenceRequest) (InferenceResponse, error)

// Infer calls f(ctx, req).
func (f InferenceHandlerFunc) Infer(ctx context.Context, req InferenceRequest) (InferenceResponse, error) {
	return f(ctx, req)
}
