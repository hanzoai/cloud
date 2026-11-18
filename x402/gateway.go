// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/cek"
	"github.com/zap-proto/zip"
)

const ledgerSchema = `
CREATE TABLE IF NOT EXISTS x402_receipts (
  receipt_id        TEXT    PRIMARY KEY,
  quote_id          TEXT    NOT NULL,
  provider_id       TEXT    NOT NULL,
  facilitator_id    TEXT    NOT NULL,
  chain_id          TEXT    NOT NULL,
  asset             TEXT    NOT NULL,
  payer             TEXT    NOT NULL,
  pay_to            TEXT    NOT NULL,
  nonce             TEXT    NOT NULL,
  idempotency_key   TEXT    NOT NULL,
  request_body_hash TEXT    NOT NULL,
  amount_atomic     INTEGER NOT NULL,
  useful_tokens     INTEGER NOT NULL,
  status            TEXT    NOT NULL,
  tx_hash           TEXT    NOT NULL DEFAULT '',
  response_status   INTEGER NOT NULL DEFAULT 0,
  response_body     TEXT    NOT NULL DEFAULT '',
  dispute_reason    TEXT    NOT NULL DEFAULT '',
  provider_sig_hex  TEXT    NOT NULL DEFAULT '',
  created_unix      INTEGER NOT NULL,
  updated_unix      INTEGER NOT NULL,
  UNIQUE(chain_id, asset, payer, nonce)
);
CREATE INDEX IF NOT EXISTS idx_x402_idem ON x402_receipts(payer, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_x402_status ON x402_receipts(status);
`

// Receipt is the verifiable, provider-signed settlement record persisted in
// SQLite and returned via X-Payment-Response and sovereign JSON export.
type Receipt struct {
	ReceiptID       string       `json:"receipt_id"`
	QuoteID         string       `json:"quote_id"`
	ProviderID      string       `json:"provider_id"`
	FacilitatorID   string       `json:"facilitator_id"`
	ChainID         string       `json:"chain_id"`
	Asset           string       `json:"asset"`
	Payer           string       `json:"payer"`
	PayTo           string       `json:"pay_to"`
	Nonce           string       `json:"nonce"`
	IdempotencyKey  string       `json:"idempotency_key,omitempty"`
	RequestBodyHash string       `json:"request_body_hash"`
	AmountAtomic    uint64       `json:"amount_atomic"`
	UsefulTokens    uint64       `json:"useful_tokens"`
	Status          LedgerStatus `json:"status"`
	TxHash          string       `json:"tx_hash,omitempty"`
	DisputeReason   string       `json:"dispute_reason,omitempty"`
	ProviderPubHex  string       `json:"provider_pub_hex"`
	ProviderSigHex  string       `json:"provider_sig_hex"`
	CreatedUnix     int64        `json:"created_unix"`
	UpdatedUnix     int64        `json:"updated_unix"`
}

// CanonicalBytes produces the domain-separated digest signed by the provider
// over a Receipt so third parties can audit receipts offline.
func (r Receipt) CanonicalBytes() []byte {
	msg := fmt.Sprintf(
		"hanzo-x402-receipt/v1|id=%s|quote=%s|provider=%s|fac=%s|chain=%s|asset=%s|from=%s|to=%s|nonce=%s|amount=%d|tokens=%d|status=%s|tx=%s",
		r.ReceiptID, r.QuoteID, r.ProviderID, r.FacilitatorID,
		r.ChainID, r.Asset, r.Payer, r.PayTo, r.Nonce,
		r.AmountAtomic, r.UsefulTokens, r.Status, r.TxHash,
	)
	sum := sha256.Sum256([]byte(msg))
	return sum[:]
}

// VerifyReceiptSignature verifies a provider-signed Receipt offline without any
// Hanzo-operated service.
func VerifyReceiptSignature(r Receipt) error {
	pubBytes, err := hex.DecodeString(r.ProviderPubHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return errors.New("invalid receipt provider pubkey")
	}
	sigBytes, err := hex.DecodeString(r.ProviderSigHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return errors.New("invalid receipt provider signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), r.CanonicalBytes(), sigBytes) {
		return errors.New("receipt signature verification failed")
	}
	return nil
}

// PriceFunc computes an exact stablecoin quote (atomic units and max tokens)
// for a given request path and body.
type PriceFunc func(path string, body []byte) (exactAmountAtomic uint64, maxTokens uint64)

// GatewayConfig configures a non-custodial ProviderGateway.
type GatewayConfig struct {
	DataDir      string
	ProviderID   string
	PayToWallet  string
	Asset        string
	ChainID      string
	QuoteTTLSec  int64
	ProviderPriv ed25519.PrivateKey
	Facilitators []Facilitator
	PriceFn      PriceFunc
	NowUnix      func() int64
}

// ProviderGateway enforces HTTP 402 exact-price quoting, ERC-3009 replay
// prevention, pluggable multi-facilitator settlement, failed-inference rollback,
// and asynchronous reversal reconciliation.
type ProviderGateway struct {
	cfg          GatewayConfig
	providerPub  ed25519.PublicKey
	facilitators map[string]Facilitator
	facOrder     []string
	db           *sql.DB
	writeMu      sync.Mutex
	quoteSeq     atomic.Uint64
}

// OpenGateway initializes a SQLite-backed ProviderGateway using cek.Open.
func OpenGateway(cfg GatewayConfig) (*ProviderGateway, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("x402.OpenGateway: empty DataDir")
	}
	if len(cfg.ProviderPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("x402.OpenGateway: invalid Ed25519 provider private key")
	}
	if cfg.ProviderID == "" || cfg.PayToWallet == "" || cfg.Asset == "" || cfg.ChainID == "" {
		return nil, errors.New("x402.OpenGateway: provider_id, pay_to, asset, and chain_id are required")
	}
	if len(cfg.Facilitators) == 0 {
		return nil, errors.New("x402.OpenGateway: at least one independent Facilitator is required")
	}
	if cfg.QuoteTTLSec <= 0 {
		cfg.QuoteTTLSec = 300
	}
	if cfg.NowUnix == nil {
		cfg.NowUnix = func() int64 { return time.Now().Unix() }
	}
	if cfg.PriceFn == nil {
		cfg.PriceFn = func(_ string, _ []byte) (uint64, uint64) { return 2000, 1000 } // $0.002 USDC (6 decimals) for 1k tokens
	}

	db, err := cek.Open(filepath.Join(cfg.DataDir, "x402.db"))
	if err != nil {
		return nil, fmt.Errorf("open x402 store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure x402 sqlite wal: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), ledgerSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init x402 schema: %w", err)
	}

	facMap := make(map[string]Facilitator, len(cfg.Facilitators))
	facOrder := make([]string, 0, len(cfg.Facilitators))
	for _, f := range cfg.Facilitators {
		if f == nil || f.ID() == "" {
			continue
		}
		if _, dup := facMap[f.ID()]; !dup {
			facMap[f.ID()] = f
			facOrder = append(facOrder, f.ID())
		}
	}
	if len(facMap) == 0 {
		_ = db.Close()
		return nil, errors.New("x402.OpenGateway: no valid facilitators configured")
	}

	return &ProviderGateway{
		cfg:          cfg,
		providerPub:  cfg.ProviderPriv.Public().(ed25519.PublicKey),
		facilitators: facMap,
		facOrder:     facOrder,
		db:           db,
	}, nil
}

// Close closes the underlying SQLite ledger.
func (g *ProviderGateway) Close() error {
	if g == nil || g.db == nil {
		return nil
	}
	return g.db.Close()
}

// IssueQuote creates a provider-signed exact price Quote for a request.
func (g *ProviderGateway) IssueQuote(path string, reqBody []byte) Quote {
	amt, maxTok := g.cfg.PriceFn(path, reqBody)
	seq := g.quoteSeq.Add(1)
	now := g.cfg.NowUnix()
	bodyHash := HashRequestBody(reqBody)

	qidRaw := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d|%s", g.cfg.ProviderID, path, now, seq, bodyHash)))
	q := Quote{
		QuoteID:           "q_" + hex.EncodeToString(qidRaw[:10]),
		ProviderID:        g.cfg.ProviderID,
		PayTo:             g.cfg.PayToWallet,
		Asset:             g.cfg.Asset,
		ChainID:           g.cfg.ChainID,
		ExactAmountAtomic: amt,
		MaxTokens:         maxTok,
		ResourcePath:      path,
		RequestBodyHash:   bodyHash,
		ExpiresAtUnix:     now + g.cfg.QuoteTTLSec,
		Facilitators:      append([]string(nil), g.facOrder...),
	}
	SignQuote(&q, g.cfg.ProviderPriv)
	return q
}

func (g *ProviderGateway) signReceipt(r *Receipt) {
	r.ProviderPubHex = hex.EncodeToString(g.providerPub)
	sig := ed25519.Sign(g.cfg.ProviderPriv, r.CanonicalBytes())
	r.ProviderSigHex = hex.EncodeToString(sig)
}

// PaymentGate wraps a zip inference endpoint with permissionless x402
// exact-price challenge, replay protection, multi-facilitator settlement, and
// failed-inference hold release.
func (g *ProviderGateway) PaymentGate() zip.Handler {
	return func(c *zip.Ctx) error {
		reqBody := c.Body()
		reqHash := HashRequestBody(reqBody)
		paymentHdr := strings.TrimSpace(c.Fiber().Get(HeaderPayment))

		if paymentHdr == "" {
			quote := g.IssueQuote(c.Path(), reqBody)
			c.Fiber().Set("WWW-Authenticate", `x402 realm="hanzo-decentralized-inference"`)
			return c.JSON(402, PaymentRequiredResponse{
				X402Version: ProtocolVersion,
				Error:       "payment_required",
				Quote:       quote,
			})
		}

		auth, err := DecodePaymentHeader(paymentHdr)
		if err != nil {
			return c.JSON(400, map[string]any{"code": "invalid_payment_header", "error": err.Error()})
		}
		if hdrIdem := strings.TrimSpace(c.Fiber().Get(HeaderIdemKey)); hdrIdem != "" && auth.IdempotencyKey == "" {
			auth.IdempotencyKey = hdrIdem
		}

		now := g.cfg.NowUnix()
		if err := VerifyAuthorization(auth, g.providerPub, reqHash, now); err != nil {
			quote := g.IssueQuote(c.Path(), reqBody)
			c.Fiber().Set("WWW-Authenticate", `x402 realm="hanzo-decentralized-inference"`)
			return c.JSON(402, map[string]any{
				"x402_version": ProtocolVersion,
				"code":         "invalid_authorization",
				"error":        err.Error(),
				"quote":        quote,
			})
		}

		fac, ok := g.facilitators[auth.FacilitatorID]
		if !ok {
			return c.JSON(400, map[string]any{"code": "unsupported_facilitator", "error": "facilitator not registered"})
		}

		// Check idempotent duplicate vs nonce replay atomically under writeMu.
		g.writeMu.Lock()
		cachedBody, cachedStatus, cachedReceipt, replayErr := g.checkExistingOrReserveLocked(c.Context(), auth, reqHash, now)
		g.writeMu.Unlock()

		if replayErr != nil {
			return c.JSON(409, map[string]any{
				"code":  "replay_detected",
				"error": replayErr.Error(),
			})
		}
		if cachedReceipt != nil {
			enc, _ := json.Marshal(cachedReceipt)
			c.Fiber().Set(HeaderReceipt, base64.StdEncoding.EncodeToString(enc))
			c.Fiber().Set("Content-Type", "application/json")
			return c.Fiber().Status(cachedStatus).SendString(cachedBody)
		}

		holdID, err := fac.VerifyHold(c.Context(), auth)
		if err != nil {
			g.markFailed(c.Context(), auth, "facilitator_verify_failed: "+err.Error())
			return c.JSON(402, map[string]any{
				"code":  "facilitator_rejected",
				"error": err.Error(),
			})
		}

		// Execute downstream inference handler.
		handlerErr := c.Next()
		respStatus := c.Fiber().Response().StatusCode()
		if handlerErr != nil || respStatus >= 500 {
			_ = fac.ReleaseHold(c.Context(), holdID, "downstream_inference_failed")
			g.markFailed(c.Context(), auth, "downstream_inference_failed")
			if handlerErr != nil {
				return handlerErr
			}
			return nil
		}

		proof, err := fac.SettleHold(c.Context(), holdID)
		if err != nil {
			_ = fac.ReleaseHold(c.Context(), holdID, "settlement_failed")
			g.markFailed(c.Context(), auth, "settlement_failed: "+err.Error())
			return c.JSON(502, map[string]any{
				"code":  "settlement_failed",
				"error": err.Error(),
			})
		}

		respBytes := append([]byte(nil), c.Fiber().Response().Body()...)
		receipt, err := g.finalizeSettlement(c.Context(), auth, proof, respStatus, string(respBytes))
		if err != nil {
			return c.JSON(500, map[string]any{"code": "ledger_persist_error", "error": err.Error()})
		}

		encReceipt, _ := json.Marshal(receipt)
		c.Fiber().Set(HeaderReceipt, base64.StdEncoding.EncodeToString(encReceipt))
		return nil
	}
}

func receiptIDFor(auth PaymentAuthorization) string {
	sum := sha256.Sum256([]byte(auth.ChainID + "|" + auth.Asset + "|" + strings.ToLower(auth.From) + "|" + auth.Nonce))
	return "rcpt_" + hex.EncodeToString(sum[:12])
}

func (g *ProviderGateway) checkExistingOrReserveLocked(
	ctx context.Context,
	auth PaymentAuthorization,
	reqHash string,
	now int64,
) (cachedBody string, cachedStatus int, cachedReceipt *Receipt, replayErr error) {
	var (
		r           Receipt
		statusStr   string
		respStatus  int
		respBody    string
		existingKey string
	)
	err := g.db.QueryRowContext(ctx, `
		SELECT receipt_id, quote_id, provider_id, facilitator_id, chain_id, asset,
		       payer, pay_to, nonce, idempotency_key, request_body_hash,
		       amount_atomic, useful_tokens, status, tx_hash, response_status,
		       response_body, dispute_reason, provider_sig_hex, created_unix, updated_unix
		FROM x402_receipts
		WHERE chain_id = ? AND asset = ? AND payer = ? AND nonce = ?
	`, auth.ChainID, auth.Asset, strings.ToLower(auth.From), auth.Nonce).Scan(
		&r.ReceiptID, &r.QuoteID, &r.ProviderID, &r.FacilitatorID, &r.ChainID, &r.Asset,
		&r.Payer, &r.PayTo, &r.Nonce, &existingKey, &r.RequestBodyHash,
		&r.AmountAtomic, &r.UsefulTokens, &statusStr, &r.TxHash, &respStatus,
		&respBody, &r.DisputeReason, &r.ProviderSigHex, &r.CreatedUnix, &r.UpdatedUnix,
	)
	if err == nil {
		r.Status = LedgerStatus(statusStr)
		r.IdempotencyKey = existingKey
		r.ProviderPubHex = hex.EncodeToString(g.providerPub)
		// Exact idempotent retry of an already settled request returns the cached
		// response and receipt without double-charging.
		if r.Status == StatusSettled &&
			auth.IdempotencyKey != "" &&
			auth.IdempotencyKey == existingKey &&
			r.RequestBodyHash == reqHash &&
			r.QuoteID == auth.Quote.QuoteID {
			return respBody, respStatus, &r, nil
		}
		return "", 0, nil, fmt.Errorf("ERC-3009 authorization nonce %s already used by payer %s (status=%s)", auth.Nonce, auth.From, r.Status)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, err
	}

	// Also ensure an Idempotency-Key is never reused across two different nonces
	// to try to confuse reconciliation.
	if auth.IdempotencyKey != "" {
		var existingNonce string
		errKey := g.db.QueryRowContext(ctx, `
			SELECT nonce FROM x402_receipts WHERE payer = ? AND idempotency_key = ? AND status = 'settled'
		`, strings.ToLower(auth.From), auth.IdempotencyKey).Scan(&existingNonce)
		if errKey == nil && existingNonce != auth.Nonce {
			return "", 0, nil, fmt.Errorf("idempotency_key %q already settled under a different nonce", auth.IdempotencyKey)
		}
	}

	rid := receiptIDFor(auth)
	_, insErr := g.db.ExecContext(ctx, `
		INSERT INTO x402_receipts (
			receipt_id, quote_id, provider_id, facilitator_id, chain_id, asset,
			payer, pay_to, nonce, idempotency_key, request_body_hash,
			amount_atomic, useful_tokens, status, created_unix, updated_unix
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rid, auth.Quote.QuoteID, g.cfg.ProviderID, auth.FacilitatorID, auth.ChainID, auth.Asset,
		strings.ToLower(auth.From), auth.To, auth.Nonce, auth.IdempotencyKey, reqHash,
		auth.ValueAtomic, auth.Quote.MaxTokens, string(StatusAuthorized), now, now,
	)
	if insErr != nil {
		return "", 0, nil, insErr
	}
	return "", 0, nil, nil
}

func (g *ProviderGateway) markFailed(ctx context.Context, auth PaymentAuthorization, reason string) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	now := g.cfg.NowUnix()
	_, _ = g.db.ExecContext(ctx, `
		UPDATE x402_receipts
		SET status = ?, dispute_reason = ?, updated_unix = ?
		WHERE chain_id = ? AND asset = ? AND payer = ? AND nonce = ?
	`, string(StatusInferenceFailed), reason, now, auth.ChainID, auth.Asset, strings.ToLower(auth.From), auth.Nonce)
}

func (g *ProviderGateway) finalizeSettlement(
	ctx context.Context,
	auth PaymentAuthorization,
	proof *SettlementProof,
	respStatus int,
	respBody string,
) (*Receipt, error) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	now := g.cfg.NowUnix()
	rid := receiptIDFor(auth)
	r := &Receipt{
		ReceiptID:       rid,
		QuoteID:         auth.Quote.QuoteID,
		ProviderID:      g.cfg.ProviderID,
		FacilitatorID:   proof.FacilitatorID,
		ChainID:         auth.ChainID,
		Asset:           auth.Asset,
		Payer:           strings.ToLower(auth.From),
		PayTo:           auth.To,
		Nonce:           auth.Nonce,
		IdempotencyKey:  auth.IdempotencyKey,
		RequestBodyHash: auth.RequestBodyHash,
		AmountAtomic:    proof.AmountAtomic,
		UsefulTokens:    auth.Quote.MaxTokens,
		Status:          StatusSettled,
		TxHash:          proof.TxHash,
		CreatedUnix:     now,
		UpdatedUnix:     now,
	}
	g.signReceipt(r)

	_, err := g.db.ExecContext(ctx, `
		UPDATE x402_receipts
		SET status = ?, tx_hash = ?, response_status = ?, response_body = ?,
		    provider_sig_hex = ?, updated_unix = ?
		WHERE receipt_id = ?
	`, string(StatusSettled), r.TxHash, respStatus, respBody, r.ProviderSigHex, now, rid)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ReconcileIn is the typed input for POST /v1/x402/reconcile.
type ReconcileIn struct {
	FacilitatorID string `json:"facilitator_id,omitempty"`
}

// ReconcileOut summarizes verified vs reversed settlements during reconciliation.
type ReconcileOut struct {
	CheckedCount  int       `json:"checked_count"`
	VerifiedCount int       `json:"verified_count"`
	ReversedCount int       `json:"reversed_count"`
	Reversed      []Receipt `json:"reversed,omitempty"`
}

// Reconcile cross-checks settled receipts against each Facilitator's chain
// finality view and transitions any reverted/underpaid settlement to StatusReversed.
func (g *ProviderGateway) Reconcile(ctx context.Context, filterFacilitator string) (*ReconcileOut, error) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	rows, err := g.db.QueryContext(ctx, `
		SELECT receipt_id, quote_id, provider_id, facilitator_id, chain_id, asset,
		       payer, pay_to, nonce, idempotency_key, request_body_hash,
		       amount_atomic, useful_tokens, status, tx_hash, dispute_reason,
		       provider_sig_hex, created_unix, updated_unix
		FROM x402_receipts
		WHERE status = ?
		ORDER BY created_unix ASC
	`, string(StatusSettled))
	if err != nil {
		return nil, err
	}
	var settled []Receipt
	for rows.Next() {
		var r Receipt
		var st string
		if err := rows.Scan(
			&r.ReceiptID, &r.QuoteID, &r.ProviderID, &r.FacilitatorID, &r.ChainID, &r.Asset,
			&r.Payer, &r.PayTo, &r.Nonce, &r.IdempotencyKey, &r.RequestBodyHash,
			&r.AmountAtomic, &r.UsefulTokens, &st, &r.TxHash, &r.DisputeReason,
			&r.ProviderSigHex, &r.CreatedUnix, &r.UpdatedUnix,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Status = LedgerStatus(st)
		r.ProviderPubHex = hex.EncodeToString(g.providerPub)
		if filterFacilitator == "" || r.FacilitatorID == filterFacilitator {
			settled = append(settled, r)
		}
	}
	_ = rows.Close()

	out := &ReconcileOut{}
	now := g.cfg.NowUnix()

	for _, r := range settled {
		out.CheckedCount++
		fac, ok := g.facilitators[r.FacilitatorID]
		if !ok {
			continue
		}
		chainSt, qErr := fac.QuerySettlement(ctx, r.TxHash)
		var revReason string
		switch {
		case qErr != nil:
			revReason = "facilitator query error: " + qErr.Error()
		case !chainSt.Found:
			revReason = "settlement tx missing on chain"
		case chainSt.Reverted || !chainSt.Finalized:
			if chainSt.Reason != "" {
				revReason = chainSt.Reason
			} else {
				revReason = "settlement reverted on chain"
			}
		case chainSt.AmountAtomic < r.AmountAtomic || !strings.EqualFold(chainSt.To, r.PayTo):
			revReason = "on-chain amount or recipient mismatch"
		}

		if revReason == "" {
			out.VerifiedCount++
			continue
		}

		r.Status = StatusReversed
		r.DisputeReason = revReason
		r.UpdatedUnix = now
		g.signReceipt(&r)

		if _, uErr := g.db.ExecContext(ctx, `
			UPDATE x402_receipts
			SET status = ?, dispute_reason = ?, provider_sig_hex = ?, updated_unix = ?
			WHERE receipt_id = ?
		`, string(StatusReversed), revReason, r.ProviderSigHex, now, r.ReceiptID); uErr != nil {
			return nil, uErr
		}
		out.ReversedCount++
		out.Reversed = append(out.Reversed, r)
	}
	return out, nil
}

// ListReceiptsIn is the typed input for GET /v1/x402/receipts.
type ListReceiptsIn struct {
	Status string `query:"status" json:"status,omitempty"`
}

// ListReceiptsOut is the sovereign JSON export of provider settlement records.
type ListReceiptsOut struct {
	ProviderID string    `json:"provider_id"`
	Receipts   []Receipt `json:"receipts"`
}

// ListReceipts exports persisted receipts ordered by creation timestamp so
// providers and customers can audit, migrate, or fork settlement state.
func (g *ProviderGateway) ListReceipts(ctx context.Context, statusFilter string) (*ListReceiptsOut, error) {
	query := `
		SELECT receipt_id, quote_id, provider_id, facilitator_id, chain_id, asset,
		       payer, pay_to, nonce, idempotency_key, request_body_hash,
		       amount_atomic, useful_tokens, status, tx_hash, dispute_reason,
		       provider_sig_hex, created_unix, updated_unix
		FROM x402_receipts
	`
	var args []any
	if statusFilter != "" {
		query += ` WHERE status = ?`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY created_unix ASC, receipt_id ASC`

	rows, err := g.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	out := &ListReceiptsOut{
		ProviderID: g.cfg.ProviderID,
		Receipts:   []Receipt{},
	}
	for rows.Next() {
		var r Receipt
		var st string
		if err := rows.Scan(
			&r.ReceiptID, &r.QuoteID, &r.ProviderID, &r.FacilitatorID, &r.ChainID, &r.Asset,
			&r.Payer, &r.PayTo, &r.Nonce, &r.IdempotencyKey, &r.RequestBodyHash,
			&r.AmountAtomic, &r.UsefulTokens, &st, &r.TxHash, &r.DisputeReason,
			&r.ProviderSigHex, &r.CreatedUnix, &r.UpdatedUnix,
		); err != nil {
			return nil, err
		}
		r.Status = LedgerStatus(st)
		r.ProviderPubHex = hex.EncodeToString(g.providerPub)
		out.Receipts = append(out.Receipts, r)
	}
	sort.SliceStable(out.Receipts, func(i, j int) bool {
		return out.Receipts[i].CreatedUnix < out.Receipts[j].CreatedUnix
	})
	return out, nil
}

// RegisterOps mounts typed settlement reconciliation and sovereign receipt
// export operations onto app per LLM.md Architecture Invariant #1.
func (g *ProviderGateway) RegisterOps(app *zip.App) error {
	if app == nil {
		return errors.New("x402.RegisterOps: nil zip.App")
	}
	zip.Post[ReconcileIn, ReconcileOut](app, "/v1/x402/reconcile",
		func(ctx context.Context, in *ReconcileIn) (*ReconcileOut, error) {
			fac := ""
			if in != nil {
				fac = in.FacilitatorID
			}
			return g.Reconcile(ctx, fac)
		},
		zip.WithOperationID("x402_reconcile"),
		zip.WithSummary("Reconcile settled x402 receipts against independent facilitator chain state"),
		zip.WithTags("x402"),
	)
	zip.Get[ListReceiptsIn, ListReceiptsOut](app, "/v1/x402/receipts",
		func(ctx context.Context, in *ListReceiptsIn) (*ListReceiptsOut, error) {
			st := ""
			if in != nil {
				st = in.Status
			}
			return g.ListReceipts(ctx, st)
		},
		zip.WithOperationID("x402_list_receipts"),
		zip.WithSummary("Export verifiable x402 payment receipts for sovereign audit or migration"),
		zip.WithTags("x402"),
	)
	return nil
}
