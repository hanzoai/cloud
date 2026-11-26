// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// SettlementGuard enforces the full permissionless x402 payment lifecycle on
// top of github.com/hanzoai/commerce/payment/x402:
//
//  1. Canonical request-body hashing & provider-signed exact price quote issuance;
//  2. Authenticated idempotent duplicate retry replay (bound to caller identity,
//     method, URL, request body hash, and payment authorization) and conflict
//     rejection (HTTP 409);
//  3. Pre-inference cryptographic verification of SignedPriceQuote, EIP-712
//     ERC-3009 authorization, validity window, atomic nonce reservation, and
//     independent facilitator Verify;
//  4. Atomic post-inference settlement: errors or 4xx/5xx void the reservation
//     without settling (customer pays $0); successes settle once, persist the
//     hash-chained provider-signed receipt, and commit the nonce/idempotency record.
type SettlementGuard struct {
	Table       *protocol.Table
	Facilitator Facilitator
	QuoteSigner QuoteSigner
	NonceStore  NonceReplayStore
	Ledger      ReceiptLedger
	Now         func() time.Time

	fallbackOnce  sync.Once
	fallbackStore *MemoryNonceStore
}

func (g *SettlementGuard) clock() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// nonceStore returns the configured NonceReplayStore or an always-on in-memory
// process-local guard so concurrent duplicate authorizations can never bypass
// admission even when no external replay store was explicitly wired.
func (g *SettlementGuard) nonceStore() NonceReplayStore {
	if g.NonceStore != nil {
		return g.NonceStore
	}
	g.fallbackOnce.Do(func() {
		g.fallbackStore = NewMemoryNonceStore()
	})
	return g.fallbackStore
}

// ZipPaywall returns a zip middleware enforcing SettlementGuard on priced routes.
func (g *SettlementGuard) ZipPaywall(name func(method, path string) string) zip.Handler {
	return func(c *zip.Ctx) error {
		method := lookupHTTPMethod(c.Method())
		key := name(method, c.Path())
		if c.Method() == http.MethodOptions || !g.Table.Priced(key) {
			return c.Next()
		}

		fc := c.Fiber()
		reqBody := append([]byte(nil), fc.Body()...)
		reqBodyHash := HashRequestBody(reqBody)
		idemKey := strings.TrimSpace(c.Header(HeaderIdempotencyKey))
		sigHeader := c.Header(protocol.HeaderPaymentSignature)
		reqTarget := fc.OriginalURL()
		reqURL := fc.Scheme() + "://" + c.Host() + reqTarget
		callerOrg := strings.TrimSpace(c.Org())
		callerUser := strings.TrimSpace(c.User())
		model, maxTokens := extractInferenceHints(reqBody, c.Header(HeaderRequestModel))

		// No payment signature -> issue 402 challenge (never serve cached responses
		// without authenticating the payer and request identity).
		if sigHeader == "" {
			ref := g.challenge(c.Context(), key, reqURL, model, maxTokens, reqBodyHash, protocol.HeaderPaymentSignature+" header is required")
			return writeZipRefusal(c, ref)
		}

		admitted, ref := g.admit(c.Context(), admitInput{
			resource:    key,
			method:      method,
			reqTarget:   reqTarget,
			reqURL:      reqURL,
			model:       model,
			maxTokens:   maxTokens,
			reqBodyHash: reqBodyHash,
			idemKey:     idemKey,
			sigHeader:   sigHeader,
			callerOrg:   callerOrg,
			callerUser:  callerUser,
		})
		if ref != nil {
			return writeZipRefusal(c, ref)
		}
		if admitted.cached != nil {
			return writeCachedZipResponse(c, admitted.cached)
		}

		settled := false
		defer func() {
			if !settled {
				_ = g.releaseNonce(c.Context(), admitted.claim, admitted.scopedIdemKey)
			}
		}()

		err := c.Next()
		statusCode := fc.Response().StatusCode()
		if err != nil || statusCode >= 400 {
			// Failed inference: NEVER settle; release nonce and record VOIDED ($0).
			_ = g.releaseNonce(c.Context(), admitted.claim, admitted.scopedIdemKey)
			settled = true
			g.recordReceipt(c.Context(), Receipt{
				QuoteID:        admitted.quoteID,
				IdempotencyKey: idemKey,
				Resource:       key,
				Model:          model,
				FacilitatorID:  admitted.facilitatorID,
				Payer:          admitted.pay.Payload.Authorization.From,
				PayTo:          admitted.offered.PayTo,
				Network:        admitted.offered.Network,
				Asset:          admitted.offered.Asset,
				AmountAtomic:   admitted.offered.Amount,
				ChargedAtomic:  "0",
				Nonce:          admitted.pay.Payload.Authorization.Nonce,
				Status:         StatusVoided,
				ErrorReason:    failureReason(err, statusCode),
			})
			return err
		}

		sResp, ref, facID, facOp := g.settle(c.Context(), admitted)
		if ref != nil {
			_ = g.releaseNonce(c.Context(), admitted.claim, admitted.scopedIdemKey)
			settled = true
			g.recordReceipt(c.Context(), Receipt{
				QuoteID:             admitted.quoteID,
				IdempotencyKey:      idemKey,
				Resource:            key,
				Model:               model,
				FacilitatorID:       facID,
				FacilitatorOperator: facOp,
				Payer:               admitted.pay.Payload.Authorization.From,
				PayTo:               admitted.offered.PayTo,
				Network:             admitted.offered.Network,
				Asset:               admitted.offered.Asset,
				AmountAtomic:        admitted.offered.Amount,
				ChargedAtomic:       "0",
				Nonce:               admitted.pay.Payload.Authorization.Nonce,
				Status:              StatusFailed,
				ErrorReason:         ref.Reason,
			})
			return writeZipRefusal(c, ref)
		}

		rcpt := g.recordReceipt(c.Context(), Receipt{
			QuoteID:             admitted.quoteID,
			IdempotencyKey:      idemKey,
			Resource:            key,
			Model:               model,
			FacilitatorID:       facID,
			FacilitatorOperator: facOp,
			Payer:               admitted.pay.Payload.Authorization.From,
			PayTo:               admitted.offered.PayTo,
			Network:             admitted.offered.Network,
			Asset:               admitted.offered.Asset,
			AmountAtomic:        admitted.offered.Amount,
			ChargedAtomic:       admitted.offered.Amount,
			Nonce:               admitted.pay.Payload.Authorization.Nonce,
			Transaction:         sResp.Transaction,
			Status:              StatusSettled,
		})

		payRespHeader, _ := protocol.EncodeHeader(sResp)
		if payRespHeader != "" {
			c.SetHeader(protocol.HeaderPaymentResponse, payRespHeader)
		}
		if rcpt.ReceiptID != "" {
			c.SetHeader(HeaderReceiptID, rcpt.ReceiptID)
		}

		respBody := append([]byte(nil), fc.Response().Body()...)
		ct := string(fc.Response().Header.ContentType())
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		// Settlement succeeded on the facilitator: never release the nonce even if
		// durable replay-store commit fails.
		settled = true
		if err := g.commitSettlement(c.Context(), admitted.claim, admitted.scopedIdemKey, &CachedResponse{
			StatusCode:            statusCode,
			ContentType:           ct,
			Body:                  respBody,
			PaymentResponseHeader: payRespHeader,
			ReceiptID:             rcpt.ReceiptID,
		}); err != nil {
			c.SetHeader("Cache-Control", "no-store")
			return c.JSON(http.StatusInternalServerError, map[string]any{
				"x402Version": protocol.Version,
				"error":       "settlement_commit_failed",
				"receiptId":   rcpt.ReceiptID,
				"transaction": sResp.Transaction,
			})
		}
		return nil
	}
}

// HTTPMiddleware wraps a standard net/http Handler with SettlementGuard so
// standalone providers can operate independently over net/http as well as zip.
func (g *SettlementGuard) HTTPMiddleware(name func(method, path string) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method := lookupHTTPMethod(r.Method)
			key := name(method, r.URL.Path)
			if r.Method == http.MethodOptions || !g.Table.Priced(key) {
				next.ServeHTTP(w, r)
				return
			}

			var reqBody []byte
			if r.Body != nil {
				var err error
				reqBody, err = io.ReadAll(r.Body)
				_ = r.Body.Close()
				if err != nil {
					writeHTTPJSON(w, http.StatusBadRequest, map[string]any{"x402Version": protocol.Version, "error": "read_body_failed"})
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}

			reqBodyHash := HashRequestBody(reqBody)
			idemKey := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
			sigHeader := r.Header.Get(protocol.HeaderPaymentSignature)
			reqTarget := r.URL.RequestURI()
			reqURL := buildRequestURL(r)
			callerOrg := strings.TrimSpace(r.Header.Get("X-Org-Id"))
			callerUser := strings.TrimSpace(r.Header.Get("X-User-Id"))
			model, maxTokens := extractInferenceHints(reqBody, r.Header.Get(HeaderRequestModel))

			if sigHeader == "" {
				ref := g.challenge(r.Context(), key, reqURL, model, maxTokens, reqBodyHash, protocol.HeaderPaymentSignature+" header is required")
				writeHTTPRefusal(w, ref)
				return
			}

			admitted, ref := g.admit(r.Context(), admitInput{
				resource:    key,
				method:      method,
				reqTarget:   reqTarget,
				reqURL:      reqURL,
				model:       model,
				maxTokens:   maxTokens,
				reqBodyHash: reqBodyHash,
				idemKey:     idemKey,
				sigHeader:   sigHeader,
				callerOrg:   callerOrg,
				callerUser:  callerUser,
			})
			if ref != nil {
				writeHTTPRefusal(w, ref)
				return
			}
			if admitted.cached != nil {
				writeCachedHTTPResponse(w, admitted.cached)
				return
			}

			settled := false
			defer func() {
				if !settled {
					_ = g.releaseNonce(r.Context(), admitted.claim, admitted.scopedIdemKey)
				}
			}()

			rec := &bufferedResponseWriter{header: make(http.Header)}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			if status >= 400 {
				_ = g.releaseNonce(r.Context(), admitted.claim, admitted.scopedIdemKey)
				settled = true
				g.recordReceipt(r.Context(), Receipt{
					QuoteID:        admitted.quoteID,
					IdempotencyKey: idemKey,
					Resource:       key,
					Model:          model,
					FacilitatorID:  admitted.facilitatorID,
					Payer:          admitted.pay.Payload.Authorization.From,
					PayTo:          admitted.offered.PayTo,
					Network:        admitted.offered.Network,
					Asset:          admitted.offered.Asset,
					AmountAtomic:   admitted.offered.Amount,
					ChargedAtomic:  "0",
					Nonce:          admitted.pay.Payload.Authorization.Nonce,
					Status:         StatusVoided,
					ErrorReason:    fmt.Sprintf("http_status_%d", status),
				})
				copyHeaders(w.Header(), rec.header)
				w.WriteHeader(status)
				_, _ = w.Write(rec.buf.Bytes())
				return
			}

			sResp, ref, facID, facOp := g.settle(r.Context(), admitted)
			if ref != nil {
				_ = g.releaseNonce(r.Context(), admitted.claim, admitted.scopedIdemKey)
				settled = true
				g.recordReceipt(r.Context(), Receipt{
					QuoteID:             admitted.quoteID,
					IdempotencyKey:      idemKey,
					Resource:            key,
					Model:               model,
					FacilitatorID:       facID,
					FacilitatorOperator: facOp,
					Payer:               admitted.pay.Payload.Authorization.From,
					PayTo:               admitted.offered.PayTo,
					Network:             admitted.offered.Network,
					Asset:               admitted.offered.Asset,
					AmountAtomic:        admitted.offered.Amount,
					ChargedAtomic:       "0",
					Nonce:               admitted.pay.Payload.Authorization.Nonce,
					Status:              StatusFailed,
					ErrorReason:         ref.Reason,
				})
				writeHTTPRefusal(w, ref)
				return
			}

			rcpt := g.recordReceipt(r.Context(), Receipt{
				QuoteID:             admitted.quoteID,
				IdempotencyKey:      idemKey,
				Resource:            key,
				Model:               model,
				FacilitatorID:       facID,
				FacilitatorOperator: facOp,
				Payer:               admitted.pay.Payload.Authorization.From,
				PayTo:               admitted.offered.PayTo,
				Network:             admitted.offered.Network,
				Asset:               admitted.offered.Asset,
				AmountAtomic:        admitted.offered.Amount,
				ChargedAtomic:       admitted.offered.Amount,
				Nonce:               admitted.pay.Payload.Authorization.Nonce,
				Transaction:         sResp.Transaction,
				Status:              StatusSettled,
			})

			payRespHeader, _ := protocol.EncodeHeader(sResp)
			copyHeaders(w.Header(), rec.header)
			if payRespHeader != "" {
				w.Header().Set(protocol.HeaderPaymentResponse, payRespHeader)
			}
			if rcpt.ReceiptID != "" {
				w.Header().Set(HeaderReceiptID, rcpt.ReceiptID)
			}
			// Settlement succeeded on the facilitator: never release the nonce even if
			// durable replay-store commit fails.
			settled = true
			if err := g.commitSettlement(r.Context(), admitted.claim, admitted.scopedIdemKey, &CachedResponse{
				StatusCode:            status,
				ContentType:           rec.header.Get("Content-Type"),
				Body:                  append([]byte(nil), rec.buf.Bytes()...),
				PaymentResponseHeader: payRespHeader,
				ReceiptID:             rcpt.ReceiptID,
			}); err != nil {
				w.Header().Set("Cache-Control", "no-store")
				writeHTTPJSON(w, http.StatusInternalServerError, map[string]any{
					"x402Version": protocol.Version,
					"error":       "settlement_commit_failed",
					"receiptId":   rcpt.ReceiptID,
					"transaction": sResp.Transaction,
				})
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write(rec.buf.Bytes())
		})
	}
}

type admitInput struct {
	resource    string
	method      string
	reqTarget   string
	reqURL      string
	model       string
	maxTokens   int
	reqBodyHash string
	idemKey     string
	sigHeader   string
	callerOrg   string
	callerUser  string
}

type admittedPayment struct {
	pay           protocol.PaymentPayload
	offered       protocol.PaymentRequirements
	resource      protocol.ResourceInfo
	claim         NonceClaim
	scopedIdemKey string
	quoteID       string
	facilitatorID string
	operator      string
	cached        *CachedResponse
}

func (g *SettlementGuard) challenge(ctx context.Context, resource, reqURL, model string, maxTokens int, reqBodyHash, reason string) *protocol.Refusal {
	accepts, info, ok := g.Table.Quote(resource)
	if !ok {
		return &protocol.Refusal{Status: http.StatusInternalServerError, Reason: "resource is not for sale: " + resource}
	}
	info.URL = reqURL
	pr := protocol.PaymentRequired{
		X402Version: protocol.Version,
		Error:       reason,
		Resource:    info,
		Accepts:     accepts,
	}
	if g.QuoteSigner != nil && len(accepts) > 0 {
		first := accepts[0]
		q, err := g.QuoteSigner.SignQuote(ctx, QuoteRequest{
			Resource:        resource,
			Model:           model,
			MaxTokens:       maxTokens,
			RequestBodyHash: reqBodyHash,
			Network:         first.Network,
			Asset:           first.Asset,
			AmountAtomic:    first.Amount,
			PayTo:           first.PayTo,
			TTLSeconds:      first.MaxTimeoutSeconds,
		})
		if err == nil {
			if ext, err := AttachQuoteExtension(pr.Extensions, q); err == nil {
				pr.Extensions = ext
			}
		}
	}
	return &protocol.Refusal{
		Status:   http.StatusPaymentRequired,
		Reason:   reason,
		Required: &pr,
	}
}

func (g *SettlementGuard) admit(ctx context.Context, in admitInput) (*admittedPayment, *protocol.Refusal) {
	accepts, info, ok := g.Table.Quote(in.resource)
	if !ok {
		return nil, &protocol.Refusal{Status: http.StatusInternalServerError, Reason: "resource is not for sale: " + in.resource}
	}
	info.URL = in.reqURL
	deny := func(status int, reason string) *protocol.Refusal {
		ref := g.challenge(ctx, in.resource, in.reqURL, in.model, in.maxTokens, in.reqBodyHash, reason)
		ref.Status = status
		return ref
	}

	pay, err := protocol.ParsePayment(in.sigHeader)
	if err != nil {
		return nil, &protocol.Refusal{Status: http.StatusBadRequest, Reason: protocol.ReasonOf(err, protocol.ReasonInvalidPayload)}
	}

	var offered *protocol.PaymentRequirements
	for i := range accepts {
		if protocol.Same(accepts[i], pay.Accepted) == nil {
			offered = &accepts[i]
			break
		}
	}
	if offered == nil {
		return nil, deny(http.StatusPaymentRequired, protocol.ReasonOf(protocol.Same(accepts[0], pay.Accepted), protocol.ReasonInvalidRequirements))
	}

	// Authenticate the payer's ERC-3009 EIP-712 signature BEFORE checking cached
	// idempotency records so unauthenticated callers or other tenants cannot
	// retrieve or probe cached responses.
	if err := protocol.Verify(*offered, pay); err != nil {
		return nil, deny(http.StatusPaymentRequired, protocol.ReasonOf(err, protocol.ReasonVerifyError))
	}

	claim := NonceClaim{
		Network: offered.Network,
		Asset:   offered.Asset,
		From:    pay.Payload.Authorization.From,
		Nonce:   pay.Payload.Authorization.Nonce,
	}
	scopedIdemKey := scopeIdempotencyKey(in.idemKey, in.callerOrg, in.callerUser, claim.From)
	reqFingerprint := computeRequestFingerprint(in.method, in.reqTarget, in.reqBodyHash, in.callerOrg, in.callerUser)
	store := g.nonceStore()

	// Authenticated idempotency fast path: replay only when caller identity,
	// full request identity (method, path, query, body hash, org, user), and
	// payment authorization claim all match a durably committed settlement.
	if scopedIdemKey != "" {
		if rec, ok := store.LookupIdempotency(ctx, scopedIdemKey); ok {
			if rec.RequestBodyHash != reqFingerprint {
				return nil, &protocol.Refusal{Status: http.StatusConflict, Reason: "idempotency_key_payload_mismatch"}
			}
			if rec.ClaimKey != "" && rec.ClaimKey != claim.Key() {
				return nil, &protocol.Refusal{Status: http.StatusConflict, Reason: "idempotency_key_payment_mismatch"}
			}
			if rec.PendingPersist {
				return nil, &protocol.Refusal{Status: http.StatusInternalServerError, Reason: "settlement_commit_pending"}
			}
			if rec.Committed && rec.Response != nil {
				return &admittedPayment{cached: rec.Response}, nil
			}
			if rec.InFlight {
				return nil, &protocol.Refusal{Status: http.StatusConflict, Reason: "identical idempotent request is already in flight"}
			}
		}
	}

	// Verify provider-signed exact price quote bound to the request body.
	var quoteID string
	if g.QuoteSigner != nil {
		q, present, err := ExtractQuoteExtension(pay.Extensions)
		if err != nil {
			return nil, deny(http.StatusPaymentRequired, protocol.ReasonInvalidRequirements)
		}
		if present {
			if err := g.QuoteSigner.VerifyQuote(q, in.reqBodyHash, g.clock()); err != nil {
				return nil, deny(http.StatusPaymentRequired, protocol.ReasonOf(err, protocol.ReasonSignature))
			}
			if q.Resource != in.resource || !quoteMatchesRequirements(q, *offered) {
				return nil, deny(http.StatusPaymentRequired, protocol.ReasonInvalidRequirements)
			}
			quoteID = q.QuoteID
		} else {
			return nil, deny(http.StatusPaymentRequired, "signed_quote_required")
		}
	}

	// Validity window check for fresh (non-replayed) payments.
	if err := protocol.InWindow(pay.Payload.Authorization, g.clock()); err != nil {
		return nil, deny(http.StatusPaymentRequired, protocol.ReasonOf(err, protocol.ReasonVerifyError))
	}

	// Atomic idempotency & in-flight/spent nonce guard before touching facilitator or inference.
	res, err := store.Reserve(ctx, claim, scopedIdemKey, reqFingerprint)
	if err != nil {
		return nil, &protocol.Refusal{Status: http.StatusInternalServerError, Reason: protocol.ReasonVerifyError}
	}
	switch res.Outcome {
	case ReserveIdempotentHit:
		return &admittedPayment{cached: res.Cached}, nil
	case ReserveIdempotencyMismatch, ReserveIdempotencyInFlight:
		return nil, &protocol.Refusal{Status: http.StatusConflict, Reason: res.Reason}
	case ReserveNonceReplay, ReserveNonceInFlight:
		return nil, deny(http.StatusPaymentRequired, protocol.ReasonInvalidTransaction)
	}

	vctx, cancel := context.WithTimeout(ctx, time.Duration(offered.MaxTimeoutSeconds)*time.Second)
	defer cancel()
	vResp, err := g.Facilitator.Verify(vctx, pay, *offered)
	if err != nil {
		_ = g.releaseNonce(ctx, claim, scopedIdemKey)
		return nil, &protocol.Refusal{Status: http.StatusInternalServerError, Reason: protocol.ReasonVerifyError}
	}
	if !vResp.IsValid {
		_ = g.releaseNonce(ctx, claim, scopedIdemKey)
		reason := vResp.InvalidReason
		if reason == "" {
			reason = protocol.ReasonVerifyError
		}
		return nil, deny(http.StatusPaymentRequired, reason)
	}

	facID := "default"
	facOp := "independent"
	if prov, ok := ExtractFacilitatorProvenance(vResp.Extensions); ok {
		facID = prov.ID
		facOp = prov.Operator
	}

	return &admittedPayment{
		pay:           pay,
		offered:       *offered,
		resource:      info,
		claim:         claim,
		scopedIdemKey: scopedIdemKey,
		quoteID:       quoteID,
		facilitatorID: facID,
		operator:      facOp,
	}, nil
}

func (g *SettlementGuard) settle(ctx context.Context, adm *admittedPayment) (protocol.SettlementResponse, *protocol.Refusal, string, string) {
	sctx, cancel := context.WithTimeout(ctx, time.Duration(adm.offered.MaxTimeoutSeconds)*time.Second)
	defer cancel()
	sResp, err := g.Facilitator.Settle(sctx, adm.pay, adm.offered)
	facID := adm.facilitatorID
	facOp := adm.operator
	if prov, ok := ExtractFacilitatorProvenance(sResp.Extensions); ok {
		facID = prov.ID
		facOp = prov.Operator
	}
	status := http.StatusPaymentRequired
	if err != nil {
		sResp = protocol.SettlementResponse{
			ErrorReason: protocol.ReasonSettleError,
			Payer:       adm.pay.Payload.Authorization.From,
			Network:     adm.offered.Network,
		}
		status = http.StatusInternalServerError
	}
	if sResp.Success {
		return sResp, nil, facID, facOp
	}
	if sResp.ErrorReason == "" {
		sResp.ErrorReason = protocol.ReasonSettleError
	}
	req := protocol.PaymentRequired{
		X402Version: protocol.Version,
		Error:       sResp.ErrorReason,
		Resource:    adm.resource,
		Accepts:     []protocol.PaymentRequirements{adm.offered},
	}
	return sResp, &protocol.Refusal{
		Status:     status,
		Reason:     sResp.ErrorReason,
		Required:   &req,
		Settlement: &sResp,
	}, facID, facOp
}

func (g *SettlementGuard) releaseNonce(ctx context.Context, claim NonceClaim, scopedIdemKey string) error {
	return g.nonceStore().Release(ctx, claim, scopedIdemKey)
}

func (g *SettlementGuard) commitSettlement(ctx context.Context, claim NonceClaim, scopedIdemKey string, cached *CachedResponse) error {
	store := g.nonceStore()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := store.Commit(ctx, claim, scopedIdemKey, cached); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return lastErr
}

func scopeIdempotencyKey(rawKey, org, user, payer string) string {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return ""
	}
	parts := []string{
		"org=" + strings.ToLower(strings.TrimSpace(org)),
		"user=" + strings.ToLower(strings.TrimSpace(user)),
		"payer=" + strings.ToLower(strings.TrimSpace(payer)),
		"key=" + rawKey,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "idem:" + hex.EncodeToString(sum[:])
}

func computeRequestFingerprint(method, reqTarget, reqBodyHash, org, user string) string {
	parts := []string{
		strings.ToUpper(strings.TrimSpace(method)),
		strings.TrimSpace(reqTarget),
		strings.TrimSpace(reqBodyHash),
		strings.ToLower(strings.TrimSpace(org)),
		strings.ToLower(strings.TrimSpace(user)),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "req:" + hex.EncodeToString(sum[:])
}

func quoteMatchesRequirements(q SignedPriceQuote, r protocol.PaymentRequirements) bool {
	return strings.EqualFold(strings.TrimSpace(q.Network), strings.TrimSpace(r.Network)) &&
		strings.EqualFold(strings.TrimSpace(q.Asset), strings.TrimSpace(r.Asset)) &&
		strings.EqualFold(strings.TrimSpace(q.PayTo), strings.TrimSpace(r.PayTo)) &&
		strings.TrimSpace(q.AmountAtomic) == strings.TrimSpace(r.Amount)
}

func (g *SettlementGuard) recordReceipt(ctx context.Context, rec Receipt) Receipt {
	if g.Ledger == nil {
		return rec
	}
	saved, err := g.Ledger.Record(ctx, rec)
	if err != nil {
		return rec
	}
	return saved
}

func lookupHTTPMethod(m string) string {
	if m == http.MethodHead {
		return http.MethodGet
	}
	return m
}

func extractInferenceHints(body []byte, headerModel string) (model string, maxTokens int) {
	model = strings.TrimSpace(headerModel)
	if len(body) == 0 {
		return model, 0
	}
	var req InferenceRequest
	if json.Unmarshal(body, &req) == nil {
		if req.Model != "" {
			model = req.Model
		}
		maxTokens = req.MaxTokens
	}
	return model, maxTokens
}

func failureReason(err error, status int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("http_status_%d", status)
}

func writeZipRefusal(c *zip.Ctx, ref *protocol.Refusal) error {
	headers, err := ref.Headers()
	if err != nil {
		ref = &protocol.Refusal{Status: http.StatusInternalServerError, Reason: protocol.ReasonSettleError}
		headers = nil
	}
	for k, v := range headers {
		c.SetHeader(k, v)
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(ref.Status, map[string]any{"x402Version": protocol.Version, "error": ref.Reason})
}

func writeCachedZipResponse(c *zip.Ctx, cached *CachedResponse) error {
	if cached.PaymentResponseHeader != "" {
		c.SetHeader(protocol.HeaderPaymentResponse, cached.PaymentResponseHeader)
	}
	if cached.ReceiptID != "" {
		c.SetHeader(HeaderReceiptID, cached.ReceiptID)
	}
	c.SetHeader(HeaderIdempotentReplay, "true")
	ct := cached.ContentType
	if ct == "" {
		ct = "application/json"
	}
	fc := c.Fiber()
	fc.Set("Content-Type", ct)
	return fc.Status(cached.StatusCode).Send(cached.Body)
}

func writeHTTPRefusal(w http.ResponseWriter, ref *protocol.Refusal) {
	headers, err := ref.Headers()
	if err != nil {
		ref = &protocol.Refusal{Status: http.StatusInternalServerError, Reason: protocol.ReasonSettleError}
		headers = nil
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeHTTPJSON(w, ref.Status, map[string]any{"x402Version": protocol.Version, "error": ref.Reason})
}

func writeCachedHTTPResponse(w http.ResponseWriter, cached *CachedResponse) {
	if cached.PaymentResponseHeader != "" {
		w.Header().Set(protocol.HeaderPaymentResponse, cached.PaymentResponseHeader)
	}
	if cached.ReceiptID != "" {
		w.Header().Set(HeaderReceiptID, cached.ReceiptID)
	}
	w.Header().Set(HeaderIdempotentReplay, "true")
	ct := cached.ContentType
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(cached.StatusCode)
	_, _ = w.Write(cached.Body)
}

func writeHTTPJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func buildRequestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.RequestURI()
}

type bufferedResponseWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferedResponseWriter) Header() http.Header { return b.header }

func (b *bufferedResponseWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedResponseWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.buf.Write(p)
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// AgentClient is a wallet-native HTTP client that handles HTTP 402 challenges,
// independently verifies provider-signed exact price quotes against the exact
// request body before signing an ERC-3009 TransferWithAuthorization, and sends
// the paid retry automatically without any Hanzo account or wallet custody.
type AgentClient struct {
	HTTPClient       *http.Client
	Key              *ecdsa.PrivateKey
	ExpectedProvider string
	Allow            func(resource protocol.ResourceInfo, r protocol.PaymentRequirements) bool
	Now              func() time.Time
}

// DoPaid executes req; on HTTP 402 it verifies any SignedPriceQuote bound to
// reqBody, signs an ERC-3009 authorization with Key, and retries once.
func (a *AgentClient) DoPaid(ctx context.Context, method, targetURL string, reqBody []byte, headers map[string]string) (*http.Response, []byte, *protocol.PaymentPayload, error) {
	if a.Key == nil {
		return nil, nil, nil, errors.New("x402: agent client requires signing key")
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	makeReq := func(sigHeader string) (*http.Request, error) {
		var rd io.Reader
		if len(reqBody) > 0 {
			rd = bytes.NewReader(reqBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, targetURL, rd)
		if err != nil {
			return nil, err
		}
		if len(reqBody) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if sigHeader != "" {
			req.Header.Set(protocol.HeaderPaymentSignature, sigHeader)
		}
		return req, nil
	}

	req1, err := makeReq("")
	if err != nil {
		return nil, nil, nil, err
	}
	resp1, err := client.Do(req1)
	if err != nil {
		return nil, nil, nil, err
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusPaymentRequired {
		return resp1, body1, nil, nil
	}

	hdr := resp1.Header.Get(protocol.HeaderPaymentRequired)
	var pr protocol.PaymentRequired
	if err := protocol.DecodeHeader(hdr, &pr); err != nil {
		return resp1, body1, nil, fmt.Errorf("x402: decode 402 challenge: %w", err)
	}

	// Independently verify provider-signed quote before signing any funds transfer.
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	var verifiedQuote *SignedPriceQuote
	if q, ok, err := ExtractQuoteExtension(pr.Extensions); err != nil {
		return resp1, body1, nil, err
	} else if ok {
		if err := VerifySignedQuoteStandalone(q, a.ExpectedProvider, HashRequestBody(reqBody), now()); err != nil {
			return resp1, body1, nil, fmt.Errorf("x402: untrusted or tampered quote: %w", err)
		}
		qCopy := q
		verifiedQuote = &qCopy
		matchedAccepts := make([]protocol.PaymentRequirements, 0, len(pr.Accepts))
		for _, r := range pr.Accepts {
			if quoteMatchesRequirements(q, r) {
				matchedAccepts = append(matchedAccepts, r)
			}
		}
		if len(matchedAccepts) == 0 {
			return resp1, body1, nil, errors.New("x402: payment requirements do not match verified signed quote")
		}
		pr.Accepts = matchedAccepts
	} else if a.ExpectedProvider != "" {
		return resp1, body1, nil, errors.New("x402: missing required provider signed quote")
	}

	userAllow := a.Allow
	allow := func(info protocol.ResourceInfo, r protocol.PaymentRequirements) bool {
		if verifiedQuote != nil && !quoteMatchesRequirements(*verifiedQuote, r) {
			return false
		}
		if userAllow != nil {
			return userAllow(info, r)
		}
		return true
	}
	signer := protocol.Signer{Key: a.Key, Allow: allow, Now: now}
	pay, err := signer.Pay(ctx, pr)
	if err != nil {
		return resp1, body1, nil, err
	}
	encoded, err := protocol.EncodeHeader(pay)
	if err != nil {
		return resp1, body1, nil, err
	}

	req2, err := makeReq(encoded)
	if err != nil {
		return nil, nil, &pay, err
	}
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, nil, &pay, err
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	return resp2, body2, &pay, nil
}
