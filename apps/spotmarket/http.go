// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luxfi/crypto/secp256k1"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const maxPrincipalLen = 128

// Server wraps a Market with an optional export signer, clock source, and
// host-supplied principal resolvers for dual-surface HTTP mounting (both
// github.com/zap-proto/zip typed operations and standard net/http).
type Server struct {
	Market *Market
	Signer *secp256k1.PrivateKey
	Now    func() int64

	// Acting resolves the host-validated buyer/org principal for mutating
	// buyer operations (order submission, clearing, and lease settlement).
	// When nil, Server reads zip.CallerOf(ctx) and fails closed with 401/403
	// unless both a validated User and a non-empty Org (<= 128 bytes) exist.
	Acting func(ctx context.Context) (string, error)

	// AuthorizeExport gates GET /v1/spotmarket/export, which dumps the entire
	// market ledger and auction state. When nil, Server reads zip.CallerOf(ctx)
	// and requires a validated platform-admin caller (401 if unauthenticated,
	// 403 if non-admin) so a multi-org tenant cannot inspect another org's state.
	AuthorizeExport func(ctx context.Context) error

	// AuthorizeSettlement optionally validates settlement authority and
	// evidence for POST /v1/spotmarket/leases/:id/settle. When nil, an
	// independently authorized settlement reporter (validated admin caller)
	// may submit any verified settlement report, while a lease's owning buyer
	// may only confirm full honest delivery (no slashing or escrow clawback).
	AuthorizeSettlement func(ctx context.Context, lease Lease, rep DeliveryReport) error
}

// NewServer constructs a dual-surface HTTP Server backed by m. If signer is
// nil, a fresh ephemeral secp256k1 signing key is generated for export bundles.
func NewServer(m *Market, signer *secp256k1.PrivateKey) (*Server, error) {
	if m == nil {
		return nil, errors.New("spotmarket: nil market")
	}
	if signer == nil {
		kp, err := GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		signer = kp.PrivateKey
	}
	return &Server{
		Market: m,
		Signer: signer,
		Now:    func() int64 { return time.Now().Unix() },
	}, nil
}

func (s *Server) nowUnix() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().Unix()
}

func defaultActing(ctx context.Context) (string, error) {
	c := zip.CallerOf(ctx)
	if strings.TrimSpace(c.User) == "" {
		return "", zip.ErrUnauthorized("a validated principal is required")
	}
	org := strings.TrimSpace(c.Org)
	if org == "" || len(org) > maxPrincipalLen {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return strings.Clone(org), nil
}

func (s *Server) acting(ctx context.Context) (string, error) {
	if s.Acting != nil {
		owner, err := s.Acting(ctx)
		if err != nil {
			return "", err
		}
		owner = strings.TrimSpace(owner)
		if owner == "" || len(owner) > maxPrincipalLen {
			return "", zip.ErrForbidden("validated principal org required")
		}
		return strings.Clone(owner), nil
	}
	return defaultActing(ctx)
}

func (s *Server) authorizeExport(ctx context.Context) error {
	if s.AuthorizeExport != nil {
		return s.AuthorizeExport(ctx)
	}
	c := zip.CallerOf(ctx)
	if strings.TrimSpace(c.User) == "" {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	if !c.Admin {
		return zip.ErrForbidden("admin principal required for full market export")
	}
	return nil
}

func (s *Server) submitOrderAuthorized(ctx context.Context, order Order) (Order, error) {
	owner, err := s.acting(ctx)
	if err != nil {
		return Order{}, err
	}
	if reqBuyer := strings.TrimSpace(order.BuyerID); reqBuyer != "" && reqBuyer != owner {
		return Order{}, zip.ErrForbidden("buyerId must match authenticated principal")
	}
	order.BuyerID = owner
	if err := s.Market.SubmitOrder(order, s.nowUnix()); err != nil {
		return Order{}, httpErrorFor(err)
	}
	if stored, ok := s.Market.Order(order.OrderID); ok {
		return stored, nil
	}
	return order, nil
}

func (s *Server) clearOrderAuthorized(ctx context.Context, orderID string) ([]Lease, []Rejection, error) {
	owner, err := s.acting(ctx)
	if err != nil {
		return nil, nil, err
	}
	ord, ok := s.Market.Order(orderID)
	if !ok {
		return nil, nil, zip.ErrNotFound("order not found")
	}
	if ord.BuyerID != owner {
		return nil, nil, zip.ErrForbidden("order belongs to another principal")
	}
	leases, rejections, err := s.Market.ClearOrder(orderID, s.nowUnix())
	if err != nil {
		he := zip.Errorf(statusForError(err), "%s", err.Error())
		if len(rejections) > 0 {
			he = he.With(map[string]any{"rejections": rejections})
		}
		return nil, rejections, he
	}
	return leases, rejections, nil
}

// settlementRequiresIndependentReporter reports whether rep would penalize the
// provider via bond slashing or escrow clawback (bait-and-switch, non-delivery,
// partial delivery, or SLA breach) rather than confirming full honest delivery.
func settlementRequiresIndependentReporter(lease Lease, rep DeliveryReport) bool {
	if rep.BaitAndSwitchPriceAttempted {
		return true
	}
	if rep.ReportedMicroUSDPer1MTokens > 0 && rep.ReportedMicroUSDPer1MTokens > lease.AgreedMicroUSDPer1MTokens {
		return true
	}
	if !rep.Completed || rep.DeliveredTokens < lease.AllocatedTokens {
		return true
	}
	if lease.TargetTTFTMs > 0 && !(rep.ObservedTTFTMs > 0 && rep.ObservedTTFTMs <= float64(lease.TargetTTFTMs)) {
		return true
	}
	if lease.MinThroughputTokPerSec > 0 && !(rep.ObservedTokPerSec >= lease.MinThroughputTokPerSec) {
		return true
	}
	return false
}

func (s *Server) settleLeaseAuthorized(ctx context.Context, leaseID string, rep DeliveryReport) (SettlementReceipt, error) {
	rep.LeaseID = leaseID
	if s.AuthorizeSettlement != nil {
		lease, ok := s.Market.Lease(leaseID)
		if !ok {
			return SettlementReceipt{}, zip.ErrNotFound("lease not found")
		}
		if err := s.AuthorizeSettlement(ctx, lease, rep); err != nil {
			return SettlementReceipt{}, err
		}
		rcpt, err := s.Market.SettleLease(leaseID, rep, s.nowUnix())
		if err != nil {
			return SettlementReceipt{}, httpErrorFor(err)
		}
		return rcpt, nil
	}

	c := zip.CallerOf(ctx)
	isIndependentReporter := strings.TrimSpace(c.User) != "" && c.Admin
	var owner string
	if !isIndependentReporter {
		var err error
		owner, err = s.acting(ctx)
		if err != nil {
			return SettlementReceipt{}, err
		}
	}

	lease, ok := s.Market.Lease(leaseID)
	if !ok {
		return SettlementReceipt{}, zip.ErrNotFound("lease not found")
	}
	if !isIndependentReporter {
		if lease.BuyerID != owner {
			return SettlementReceipt{}, zip.ErrForbidden("lease belongs to another principal")
		}
		if settlementRequiresIndependentReporter(lease, rep) {
			return SettlementReceipt{}, zip.ErrForbidden("slashing or clawback settlement requires verified evidence or an independently authorized settlement reporter")
		}
	}

	rcpt, err := s.Market.SettleLease(leaseID, rep, s.nowUnix())
	if err != nil {
		return SettlementReceipt{}, httpErrorFor(err)
	}
	return rcpt, nil
}

type noIn struct{}

type healthResponse struct {
	// Service is the subsystem identifier ("spotmarket").
	Service string `json:"service"`
	// Status is the liveness status ("ok").
	Status string `json:"status"`
	// ProtocolVersion is the active HIP-1350 wire domain version.
	ProtocolVersion string `json:"protocolVersion"`
	// ActiveOffers is the count of non-expired offers currently indexed.
	ActiveOffers int `json:"activeOffers"`
	// HanzoCalls is the cumulative number of upstream Hanzo control-plane calls (always 0).
	HanzoCalls int64 `json:"hanzoCalls"`
	// LedgerConserved reports whether total ledger balances conserve deposited microUSD with zero drift.
	LedgerConserved bool `json:"ledgerConserved"`
}

type registerOfferIn struct {
	// Offer is the canonical unsigned GPU capacity advertisement.
	Offer Offer `json:"offer" url:"-"`
	// CommitmentHex is the hex-encoded SHA-256 commitment digest of Offer.
	CommitmentHex string `json:"commitmentHex" url:"-"`
	// SignatureHex is the 65-byte EIP-2 low-S secp256k1 signature over CommitmentHex.
	SignatureHex string `json:"signatureHex" url:"-"`
	// VerificationClass declares the verification boundary for this offer envelope.
	VerificationClass VerificationClass `json:"verificationClass" url:"-"`
}

type listOffersIn struct {
	// Model optionally filters active offers supporting the named model.
	Model string `json:"model,omitempty"`
	// Region optionally filters active offers located in the named region.
	Region string `json:"region,omitempty"`
	// Jurisdiction optionally filters active offers in the named jurisdiction.
	Jurisdiction string `json:"jurisdiction,omitempty"`
	// MaxPrice optionally filters active offers priced at or below this microUSD per 1M tokens ceiling.
	MaxPrice int64 `json:"max_price,omitempty"`
}

type listOffersOut struct {
	// Data is the deterministically sorted list of matching active SignedOffers.
	Data []SignedOffer `json:"data"`
}

type submitOrderIn struct {
	// OrderID uniquely identifies the buyer's auction order.
	OrderID string `json:"orderId" url:"-"`
	// BuyerID is optional; when provided it must match the host-validated caller principal.
	BuyerID string `json:"buyerId,omitempty" url:"-"`
	// Model is the target inference model identifier.
	Model string `json:"model" url:"-"`
	// RequestedTokens is the total number of tokens requested by the order.
	RequestedTokens int64 `json:"requestedTokens" url:"-"`
	// MinVRAMGB is the minimum GPU VRAM in gigabytes required to serve the order.
	MinVRAMGB int `json:"minVramGb" url:"-"`
	// MaxMicroUSDPer1MTokens is the buyer's reserve price ceiling in microUSD per 1M tokens.
	MaxMicroUSDPer1MTokens int64 `json:"maxMicroUsdPer1MTokens" url:"-"`
	// AllowedRegions restricts matching offers to the listed regions when non-empty.
	AllowedRegions []string `json:"allowedRegions,omitempty" url:"-"`
	// AllowedJurisdictions restricts matching offers to the listed jurisdictions when non-empty.
	AllowedJurisdictions []string `json:"allowedJurisdictions,omitempty" url:"-"`
	// BlockedJurisdictions excludes matching offers from the listed jurisdictions.
	BlockedJurisdictions []string `json:"blockedJurisdictions,omitempty" url:"-"`
	// MaxTTFTMs is the optional maximum acceptable time-to-first-token in milliseconds.
	MaxTTFTMs int64 `json:"maxTtftMs,omitempty" url:"-"`
	// MinReliabilityBPS is the optional minimum provider reliability in basis points (0..10000).
	MinReliabilityBPS int `json:"minReliabilityBps,omitempty" url:"-"`
	// AllowPartialFill allows Enso to split the order across multiple providers.
	AllowPartialFill bool `json:"allowPartialFill" url:"-"`
	// EscrowMicroUSD is the microUSD escrow to lock; 0 defaults to the reserve ceiling cost.
	EscrowMicroUSD int64 `json:"escrowMicroUsd" url:"-"`
	// CreatedAtUnix is the Unix timestamp when the order was created.
	CreatedAtUnix int64 `json:"createdAtUnix" url:"-"`
	// ExpiresAtUnix is the Unix timestamp when the order expires if uncleared.
	ExpiresAtUnix int64 `json:"expiresAtUnix" url:"-"`
	// RankingWeights configures buyer-controlled price, latency, and reliability weights.
	RankingWeights RankingWeights `json:"rankingWeights" url:"-"`
	// RequireHardwareAttested requires offers to carry verified hardware TEE attestation evidence.
	RequireHardwareAttested bool `json:"requireHardwareAttested,omitempty" url:"-"`
}

type submitBidIn struct {
	// Bid is the provider's reverse-auction bid against an open order.
	Bid Bid `json:"bid" url:"-"`
	// CommitmentHex is the hex-encoded SHA-256 commitment digest of Bid.
	CommitmentHex string `json:"commitmentHex" url:"-"`
	// SignatureHex is the 65-byte EIP-2 low-S secp256k1 signature over CommitmentHex.
	SignatureHex string `json:"signatureHex" url:"-"`
	// VerificationClass declares the verification boundary for this bid envelope.
	VerificationClass VerificationClass `json:"verificationClass" url:"-"`
}

type clearOrderIn struct {
	// ID is the order identifier from the URL path.
	ID string `json:"id"`
}

type clearOrderOut struct {
	// Leases contains all leases awarded by Enso for the cleared order.
	Leases []Lease `json:"leases"`
	// Rejections lists candidate bids excluded during validation or ranking.
	Rejections []Rejection `json:"rejections"`
}

type settleLeaseIn struct {
	// ID is the lease identifier from the URL path.
	ID string `json:"id"`
	// DeliveredTokens is the total number of useful tokens delivered during execution.
	DeliveredTokens int64 `json:"deliveredTokens" url:"-"`
	// ObservedTTFTMs is the measured time-to-first-token in milliseconds.
	ObservedTTFTMs float64 `json:"observedTtftMs" url:"-"`
	// ObservedLatencyMs is the measured end-to-end latency in milliseconds.
	ObservedLatencyMs float64 `json:"observedLatencyMs" url:"-"`
	// ObservedTokPerSec is the measured generation throughput in tokens per second.
	ObservedTokPerSec float64 `json:"observedTokPerSec" url:"-"`
	// Completed indicates whether the provider finished execution without crashing.
	Completed bool `json:"completed" url:"-"`
	// FailureReason optionally describes why execution failed or aborted.
	FailureReason string `json:"failureReason,omitempty" url:"-"`
	// ReportedMicroUSDPer1MTokens is the provider-reported billing rate checked against agreed terms.
	ReportedMicroUSDPer1MTokens int64 `json:"reportedMicroUsdPer1MTokens,omitempty" url:"-"`
	// BaitAndSwitchPriceAttempted flags detected post-award price inflation.
	BaitAndSwitchPriceAttempted bool `json:"baitAndSwitchPriceAttempted,omitempty" url:"-"`
	// VerificationOverheadMicroUSD optionally supplies metered verification overhead in microUSD.
	VerificationOverheadMicroUSD int64 `json:"verificationOverheadMicroUsd,omitempty" url:"-"`
}

func (s *Server) buildHealth() healthResponse {
	now := s.nowUnix()
	offers := s.Market.ListOffers(now)
	return healthResponse{
		Service:         "spotmarket",
		Status:          "ok",
		ProtocolVersion: ProtocolVersion,
		ActiveOffers:    len(offers),
		HanzoCalls:      s.Market.HanzoCalls(),
		LedgerConserved: s.Market.CheckConservation() == nil,
	}
}

func (s *Server) filterOffers(model, region, jurisdiction string, maxPrice int64) []SignedOffer {
	offers := s.Market.ListOffers(s.nowUnix())
	out := make([]SignedOffer, 0, len(offers))
	for _, so := range offers {
		o := so.Offer
		if model != "" && !containsString(o.SupportedModels, model) {
			continue
		}
		if region != "" && o.Location.Region != region {
			continue
		}
		if jurisdiction != "" && o.Location.Jurisdiction != jurisdiction {
			continue
		}
		if maxPrice > 0 && o.Pricing.MicroUSDPer1MTokens > maxPrice {
			continue
		}
		out = append(out, so)
	}
	return out
}

// spotOps binds a Server to typed ZIP operations so every route projects
// simultaneously into REST, /v1/openapi.json, /mcp tools, CLI, and SDKs.
type spotOps struct{ srv *Server }

// health reports spot market liveness, protocol version, active offer count,
// Hanzo control-plane call count, and ledger conservation health.
func (o spotOps) health(_ context.Context, _ *noIn) (*healthResponse, error) {
	h := o.srv.buildHealth()
	return &h, nil
}

// registerOffer verifies and indexes a provider's signed GPU capacity offer.
func (o spotOps) registerOffer(_ context.Context, in *registerOfferIn) (*SignedOffer, error) {
	so := SignedOffer{
		Offer:             in.Offer,
		CommitmentHex:     in.CommitmentHex,
		SignatureHex:      in.SignatureHex,
		VerificationClass: in.VerificationClass,
	}
	if err := o.srv.Market.RegisterOffer(so, o.srv.nowUnix()); err != nil {
		return nil, httpErrorFor(err)
	}
	return &so, nil
}

// listOffers returns active, non-expired GPU spot offers matching optional
// model, region, jurisdiction, and max_price filters.
func (o spotOps) listOffers(_ context.Context, in *listOffersIn) (*listOffersOut, error) {
	data := o.srv.filterOffers(
		strings.TrimSpace(in.Model),
		strings.TrimSpace(in.Region),
		strings.TrimSpace(in.Jurisdiction),
		in.MaxPrice,
	)
	return &listOffersOut{Data: data}, nil
}

// submitOrder creates a buyer reverse-auction order bound strictly to the
// host-validated caller principal and locks its microUSD escrow.
func (o spotOps) submitOrder(ctx context.Context, in *submitOrderIn) (*Order, error) {
	order := Order{
		OrderID:                 in.OrderID,
		BuyerID:                 in.BuyerID,
		Model:                   in.Model,
		RequestedTokens:         in.RequestedTokens,
		MinVRAMGB:               in.MinVRAMGB,
		MaxMicroUSDPer1MTokens:  in.MaxMicroUSDPer1MTokens,
		AllowedRegions:          in.AllowedRegions,
		AllowedJurisdictions:    in.AllowedJurisdictions,
		BlockedJurisdictions:    in.BlockedJurisdictions,
		MaxTTFTMs:               in.MaxTTFTMs,
		MinReliabilityBPS:       in.MinReliabilityBPS,
		AllowPartialFill:        in.AllowPartialFill,
		EscrowMicroUSD:          in.EscrowMicroUSD,
		CreatedAtUnix:           in.CreatedAtUnix,
		ExpiresAtUnix:           in.ExpiresAtUnix,
		RankingWeights:          in.RankingWeights,
		RequireHardwareAttested: in.RequireHardwareAttested,
	}
	stored, err := o.srv.submitOrderAuthorized(ctx, order)
	if err != nil {
		return nil, err
	}
	return &stored, nil
}

// submitBid verifies and accepts a provider's signed reverse-auction bid and
// locks its collateral bond against the provider's bound signing identity.
func (o spotOps) submitBid(_ context.Context, in *submitBidIn) (*SignedBid, error) {
	sb := SignedBid{
		Bid:               in.Bid,
		CommitmentHex:     in.CommitmentHex,
		SignatureHex:      in.SignatureHex,
		VerificationClass: in.VerificationClass,
	}
	if err := o.srv.Market.SubmitBid(sb, o.srv.nowUnix()); err != nil {
		return nil, httpErrorFor(err)
	}
	return &sb, nil
}

// clearOrder runs neutral Enso multi-dimensional auction clearing for an order
// owned by the host-validated caller principal.
func (o spotOps) clearOrder(ctx context.Context, in *clearOrderIn) (*clearOrderOut, error) {
	leases, rejections, err := o.srv.clearOrderAuthorized(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	return &clearOrderOut{Leases: leases, Rejections: rejections}, nil
}

// settleLease settles an active lease owned by the host-validated caller
// principal, enforcing agreed pricing, SLA commitments, and bond slashing.
func (o spotOps) settleLease(ctx context.Context, in *settleLeaseIn) (*SettlementReceipt, error) {
	rep := DeliveryReport{
		LeaseID:                      strings.TrimSpace(in.ID),
		DeliveredTokens:              in.DeliveredTokens,
		ObservedTTFTMs:               in.ObservedTTFTMs,
		ObservedLatencyMs:            in.ObservedLatencyMs,
		ObservedTokPerSec:            in.ObservedTokPerSec,
		Completed:                    in.Completed,
		FailureReason:                in.FailureReason,
		ReportedMicroUSDPer1MTokens:  in.ReportedMicroUSDPer1MTokens,
		BaitAndSwitchPriceAttempted:  in.BaitAndSwitchPriceAttempted,
		VerificationOverheadMicroUSD: in.VerificationOverheadMicroUSD,
	}
	rcpt, err := o.srv.settleLeaseAuthorized(ctx, strings.TrimSpace(in.ID), rep)
	if err != nil {
		return nil, err
	}
	return &rcpt, nil
}

// peers returns a deterministic permissionless gossip snapshot of active
// peers, offers, measured telemetry, and the SHA-256 state root.
func (o spotOps) peers(_ context.Context, _ *noIn) (*GossipSnapshot, error) {
	snap := o.srv.Market.GossipSnapshot(o.srv.nowUnix())
	return &snap, nil
}

// exportBundle returns a cryptographically signed, self-contained market state
// bundle for offline verification, migration, or cold-start forking.
func (o spotOps) exportBundle(ctx context.Context, _ *noIn) (*MarketExportBundle, error) {
	if err := o.srv.authorizeExport(ctx); err != nil {
		return nil, err
	}
	bundle, err := o.srv.Market.ExportBundle(o.srv.Signer, o.srv.nowUnix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	return &bundle, nil
}

// Mount attaches the HIP-1350 GPU spot market REST surface onto a *zip.App
// under /v1/spotmarket using strictly typed ZIP operations so every route
// projects automatically into OpenAPI, MCP tools, CLI commands, and SDKs.
func Mount(app *zip.App, log luxlog.Logger, srv *Server) error {
	if app == nil {
		return errors.New("spotmarket: nil app")
	}
	if srv == nil || srv.Market == nil {
		return errors.New("spotmarket: nil server or market")
	}

	o := spotOps{srv: srv}
	g := app.Group("/v1/spotmarket")
	g.Get("/health", o.health)
	g.Post("/offers", o.registerOffer, zip.WithStatus(http.StatusCreated))
	g.Get("/offers", o.listOffers)
	g.Post("/orders", o.submitOrder, zip.WithStatus(http.StatusCreated))
	g.Post("/bids", o.submitBid, zip.WithStatus(http.StatusCreated))
	g.Post("/orders/:id/clear", o.clearOrder)
	g.Post("/leases/:id/settle", o.settleLease)
	g.Get("/peers", o.peers)
	g.Get("/export", o.exportBundle)

	if log != nil {
		log.Info("spotmarket mounted", "protocol", ProtocolVersion)
	}
	return nil
}

// Handler returns a pure standard-library net/http.Handler exposing the exact
// same /v1/spotmarket REST surface and principal authorization checks so
// independent operators can run the spot market without zip or any Hanzo host.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/spotmarket/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.buildHealth())
	})
	mux.HandleFunc("POST /v1/spotmarket/offers", func(w http.ResponseWriter, r *http.Request) {
		var so SignedOffer
		if err := decodeJSONBody(r, &so); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.Market.RegisterOffer(so, s.nowUnix()); err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, so)
	})
	mux.HandleFunc("GET /v1/spotmarket/offers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		maxPrice, _ := strconv.ParseInt(strings.TrimSpace(q.Get("max_price")), 10, 64)
		data := s.filterOffers(
			strings.TrimSpace(q.Get("model")),
			strings.TrimSpace(q.Get("region")),
			strings.TrimSpace(q.Get("jurisdiction")),
			maxPrice,
		)
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	})
	mux.HandleFunc("POST /v1/spotmarket/orders", func(w http.ResponseWriter, r *http.Request) {
		var order Order
		if err := decodeJSONBody(r, &order); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		stored, err := s.submitOrderAuthorized(r.Context(), order)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, stored)
	})
	mux.HandleFunc("POST /v1/spotmarket/bids", func(w http.ResponseWriter, r *http.Request) {
		var sb SignedBid
		if err := decodeJSONBody(r, &sb); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.Market.SubmitBid(sb, s.nowUnix()); err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, sb)
	})
	mux.HandleFunc("POST /v1/spotmarket/orders/{id}/clear", func(w http.ResponseWriter, r *http.Request) {
		orderID := strings.TrimSpace(r.PathValue("id"))
		leases, rejections, err := s.clearOrderAuthorized(r.Context(), orderID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, clearOrderOut{Leases: leases, Rejections: rejections})
	})
	mux.HandleFunc("POST /v1/spotmarket/leases/{id}/settle", func(w http.ResponseWriter, r *http.Request) {
		leaseID := strings.TrimSpace(r.PathValue("id"))
		var rep DeliveryReport
		if err := decodeJSONBody(r, &rep); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		rcpt, err := s.settleLeaseAuthorized(r.Context(), leaseID, rep)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rcpt)
	})
	mux.HandleFunc("GET /v1/spotmarket/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Market.GossipSnapshot(s.nowUnix()))
	})
	mux.HandleFunc("GET /v1/spotmarket/export", func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorizeExport(r.Context()); err != nil {
			writeHTTPError(w, err)
			return
		}
		bundle, err := s.Market.ExportBundle(s.Signer, s.nowUnix())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})
	return mux
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTTPError(w http.ResponseWriter, err error) {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		payload := map[string]any{"error": he.Msg}
		for k, v := range he.Detail {
			payload[k] = v
		}
		writeJSON(w, he.Status, payload)
		return
	}
	writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
}

func httpErrorFor(err error) error {
	if err == nil {
		return nil
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	return zip.Errorf(statusForError(err), "%s", err.Error())
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrHighSMalleableSignature), errors.Is(err, ErrCommitmentMismatch), errors.Is(err, ErrUnauthorizedProviderKey):
		return http.StatusUnauthorized
	case errors.Is(err, ErrInsufficientBalance), errors.Is(err, ErrInsufficientBond):
		return http.StatusPaymentRequired
	case errors.Is(err, ErrSybilBidLimit):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrNoEligibleBids), errors.Is(err, ErrCapacityOverclaim), errors.Is(err, ErrLeaseAlreadySettled), errors.Is(err, ErrLeaseNotEligibleForFailover), errors.Is(err, ErrReplayNonce):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}
