// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
)

// RoutingStrategy controls how MultiFacilitator selects among healthy facilitators.
type RoutingStrategy string

const (
	// StrategyFailover routes to the primary healthy facilitator and fails over
	// automatically to independent backups on transport/5xx errors.
	StrategyFailover RoutingStrategy = "failover"
	// StrategyRoundRobin distributes traffic evenly across healthy facilitators
	// while failing over automatically if any facilitator becomes unreachable.
	StrategyRoundRobin RoutingStrategy = "round_robin"
)

// FacilitatorProvenance is attached in Extensions[ExtensionFacilitator] on
// VerifyResponse and SettlementResponse so callers and ledgers can attribute
// every settled transaction to the exact facilitator and operator.
type FacilitatorProvenance struct {
	ID       string `json:"id"`
	Operator string `json:"operator"`
	Endpoint string `json:"endpoint,omitempty"`
}

// FacilitatorStats exposes measured per-facilitator health and traffic counters.
type FacilitatorStats struct {
	ID              string `json:"id"`
	Operator        string `json:"operator"`
	Endpoint        string `json:"endpoint,omitempty"`
	Healthy         bool   `json:"healthy"`
	Verified        int64  `json:"verified"`
	Settled         int64  `json:"settled"`
	TransportErrors int64  `json:"transportErrors"`
	Failovers       int64  `json:"failovers"`
}

type facilitatorSlot struct {
	nf              NamedFacilitator
	unhealthyUntil  time.Time
	verified        int64
	settled         int64
	transportErrors int64
	failovers       int64
}

// MultiFacilitator implements protocol.Facilitator across two or more
// independent facilitators with automatic failover, settlement pinning to the
// verifying facilitator, and zero-downtime live facilitator migration.
//
// Neither Hanzo nor Circle is mandatory: every slot may point to an independent
// community or self-hosted x402 Section 7 HTTP facilitator.
type MultiFacilitator struct {
	mu              sync.RWMutex
	slots           []*facilitatorSlot
	retired         map[string]*facilitatorSlot // preserves in-flight pinned settlements across SwitchFacilitators
	pinned          map[string]string           // claimKey -> facilitator ID
	strategy        RoutingStrategy
	cooldown        time.Duration
	rr              uint64
	now             func() time.Time
	totalFailovers  atomic.Int64
	migrationsCount atomic.Int64
}

// NewMultiFacilitator constructs a MultiFacilitator over one or more named
// facilitators.
func NewMultiFacilitator(strategy RoutingStrategy, facilitators ...NamedFacilitator) (*MultiFacilitator, error) {
	if len(facilitators) == 0 {
		return nil, errors.New("x402: multi-facilitator requires at least one facilitator")
	}
	if strategy == "" {
		strategy = StrategyFailover
	}
	slots, err := buildSlots(facilitators)
	if err != nil {
		return nil, err
	}
	return &MultiFacilitator{
		slots:    slots,
		retired:  make(map[string]*facilitatorSlot),
		pinned:   make(map[string]string),
		strategy: strategy,
		cooldown: 15 * time.Second,
	}, nil
}

func buildSlots(facilitators []NamedFacilitator) ([]*facilitatorSlot, error) {
	seen := make(map[string]struct{}, len(facilitators))
	slots := make([]*facilitatorSlot, 0, len(facilitators))
	for _, nf := range facilitators {
		nf.ID = strings.TrimSpace(nf.ID)
		if nf.ID == "" {
			return nil, errors.New("x402: facilitator id is required")
		}
		if nf.Facilitator == nil {
			return nil, fmt.Errorf("x402: facilitator %q has nil implementation", nf.ID)
		}
		if _, dup := seen[nf.ID]; dup {
			return nil, fmt.Errorf("x402: duplicate facilitator id %q", nf.ID)
		}
		seen[nf.ID] = struct{}{}
		if strings.TrimSpace(nf.Operator) == "" {
			nf.Operator = "independent"
		}
		slots = append(slots, &facilitatorSlot{nf: nf})
	}
	return slots, nil
}

func (m *MultiFacilitator) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// SwitchFacilitators replaces the active facilitator set with zero downtime.
// Any previously active facilitator that still has pinned in-flight settlements
// remains reachable until those settlements complete.
func (m *MultiFacilitator) SwitchFacilitators(next []NamedFacilitator) error {
	if len(next) == 0 {
		return errors.New("x402: cannot switch to an empty facilitator set")
	}
	slots, err := buildSlots(next)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for _, old := range m.slots {
		m.retired[old.nf.ID] = old
	}
	for _, cur := range slots {
		delete(m.retired, cur.nf.ID)
	}
	m.slots = slots
	m.mu.Unlock()
	m.migrationsCount.Add(1)
	return nil
}

// Migrations returns the number of zero-downtime facilitator migrations executed.
func (m *MultiFacilitator) Migrations() int64 {
	return m.migrationsCount.Load()
}

// Verify attempts verification across healthy facilitators in strategy order,
// failing over on transport errors while immediately returning semantic payment
// refusals (IsValid == false). On success it pins the authorization claim to
// the verifying facilitator so Settle targets the same facilitator.
func (m *MultiFacilitator) Verify(ctx context.Context, pay protocol.PaymentPayload, offered protocol.PaymentRequirements) (protocol.VerifyResponse, error) {
	candidates := m.orderedCandidates()
	var lastErr error
	for idx, slot := range candidates {
		resp, err := slot.nf.Facilitator.Verify(ctx, pay, offered)
		if err != nil {
			lastErr = err
			m.markUnhealthy(slot, idx < len(candidates)-1)
			continue
		}
		m.mu.Lock()
		slot.unhealthyUntil = time.Time{}
		if resp.IsValid {
			slot.verified++
			m.pinned[claimKeyOf(pay, offered)] = slot.nf.ID
		}
		m.mu.Unlock()
		resp.Extensions = attachProvenance(resp.Extensions, slot.nf)
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("x402: no facilitators available")
	}
	return protocol.VerifyResponse{}, lastErr
}

// Settle commits the payment on the facilitator that verified it (falling back
// to another healthy facilitator only if the pinned facilitator was not set).
func (m *MultiFacilitator) Settle(ctx context.Context, pay protocol.PaymentPayload, offered protocol.PaymentRequirements) (protocol.SettlementResponse, error) {
	key := claimKeyOf(pay, offered)
	slot := m.takePinned(key)
	if slot != nil {
		resp, err := slot.nf.Facilitator.Settle(ctx, pay, offered)
		if err != nil {
			m.markUnhealthy(slot, false)
			return protocol.SettlementResponse{}, err
		}
		if resp.Success {
			m.mu.Lock()
			slot.settled++
			m.mu.Unlock()
		}
		resp.Extensions = attachProvenance(resp.Extensions, slot.nf)
		return resp, nil
	}

	candidates := m.orderedCandidates()
	var lastErr error
	for idx, cand := range candidates {
		resp, err := cand.nf.Facilitator.Settle(ctx, pay, offered)
		if err != nil {
			lastErr = err
			m.markUnhealthy(cand, idx < len(candidates)-1)
			continue
		}
		if resp.Success {
			m.mu.Lock()
			cand.settled++
			m.mu.Unlock()
		}
		resp.Extensions = attachProvenance(resp.Extensions, cand.nf)
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("x402: no facilitators available for settlement")
	}
	return protocol.SettlementResponse{}, lastErr
}

func (m *MultiFacilitator) orderedCandidates() []*facilitatorSlot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	n := len(m.slots)
	if n == 0 {
		return nil
	}
	now := m.clock()
	start := 0
	if m.strategy == StrategyRoundRobin {
		start = int((atomic.AddUint64(&m.rr, 1) - 1) % uint64(n))
	}

	healthy := make([]*facilitatorSlot, 0, n)
	cooling := make([]*facilitatorSlot, 0, n)
	for i := 0; i < n; i++ {
		s := m.slots[(start+i)%n]
		if s.unhealthyUntil.IsZero() || !now.Before(s.unhealthyUntil) {
			healthy = append(healthy, s)
		} else {
			cooling = append(cooling, s)
		}
	}
	return append(healthy, cooling...)
}

func (m *MultiFacilitator) markUnhealthy(slot *facilitatorSlot, failoverOccurred bool) {
	m.mu.Lock()
	slot.transportErrors++
	slot.unhealthyUntil = m.clock().Add(m.cooldown)
	if failoverOccurred {
		slot.failovers++
	}
	m.mu.Unlock()
	if failoverOccurred {
		m.totalFailovers.Add(1)
	}
}

func (m *MultiFacilitator) takePinned(key string) *facilitatorSlot {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.pinned[key]
	if !ok {
		return nil
	}
	delete(m.pinned, key)
	for _, s := range m.slots {
		if s.nf.ID == id {
			return s
		}
	}
	if old, ok := m.retired[id]; ok {
		return old
	}
	return nil
}

// Stats returns live per-facilitator health and settled counters.
func (m *MultiFacilitator) Stats() []FacilitatorStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.clock()
	out := make([]FacilitatorStats, 0, len(m.slots))
	for _, s := range m.slots {
		out = append(out, FacilitatorStats{
			ID:              s.nf.ID,
			Operator:        s.nf.Operator,
			Endpoint:        s.nf.Endpoint,
			Healthy:         s.unhealthyUntil.IsZero() || !now.Before(s.unhealthyUntil),
			Verified:        s.verified,
			Settled:         s.settled,
			TransportErrors: s.transportErrors,
			Failovers:       s.failovers,
		})
	}
	return out
}

// TotalFailovers returns the count of automatic failovers triggered by
// facilitator transport errors.
func (m *MultiFacilitator) TotalFailovers() int64 {
	return m.totalFailovers.Load()
}

func claimKeyOf(pay protocol.PaymentPayload, offered protocol.PaymentRequirements) string {
	return NonceClaim{
		Network: offered.Network,
		Asset:   offered.Asset,
		From:    pay.Payload.Authorization.From,
		Nonce:   pay.Payload.Authorization.Nonce,
	}.Key()
}

func attachProvenance(ext protocol.Extensions, nf NamedFacilitator) protocol.Extensions {
	raw, err := json.Marshal(FacilitatorProvenance{
		ID:       nf.ID,
		Operator: nf.Operator,
		Endpoint: nf.Endpoint,
	})
	if err != nil {
		return ext
	}
	out := make(protocol.Extensions, len(ext)+1)
	for k, v := range ext {
		out[k] = v
	}
	out[ExtensionFacilitator] = raw
	return out
}

// ExtractFacilitatorProvenance reads FacilitatorProvenance from an x402 v2
// Extensions map when populated by MultiFacilitator.
func ExtractFacilitatorProvenance(ext protocol.Extensions) (FacilitatorProvenance, bool) {
	if len(ext) == 0 {
		return FacilitatorProvenance{}, false
	}
	raw, ok := ext[ExtensionFacilitator]
	if !ok || len(raw) == 0 {
		return FacilitatorProvenance{}, false
	}
	var p FacilitatorProvenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return FacilitatorProvenance{}, false
	}
	return p, true
}

// NewCircleFacilitator builds an optional, non-mandatory Facilitator adapter
// targeting a Circle-compatible x402 HTTP endpoint with optional Bearer auth.
// Providers are never required to configure or route through Circle.
func NewCircleFacilitator(endpoint, apiKey string, client *http.Client) NamedFacilitator {
	remote := protocol.Remote{
		URL:    strings.TrimRight(endpoint, "/"),
		Client: client,
	}
	if strings.TrimSpace(apiKey) != "" {
		token := strings.TrimSpace(apiKey)
		remote.Authorize = func(r *http.Request, _ string) error {
			r.Header.Set("Authorization", "Bearer "+token)
			return nil
		}
	}
	return NamedFacilitator{
		ID:          "circle-optional",
		Operator:    "circle",
		Endpoint:    remote.URL,
		Facilitator: remote,
	}
}

// HTTPFacilitatorServer is a standalone, permissionless x402 Section 7 HTTP
// facilitator implementing POST /verify, POST /settle, POST /reverse, and
// GET /health. It performs real EIP-712 / ERC-3009 secp256k1 verification,
// tracks spent nonces atomically, and produces deterministic settlement
// transaction hashes for integration testing and reproducible benchmarks.
type HTTPFacilitatorServer struct {
	ID       string
	Operator string
	Now      func() time.Time

	// Optional hooks for fault/latency injection in tests and benchmarks.
	VerifyDelay time.Duration
	SettleDelay time.Duration

	mu          sync.Mutex
	spent       map[string]string // claimKey -> txHash
	txToClaim   map[string]string // txHash -> claimKey
	reversed    map[string]string // txHash -> reversalTx
	failNext    bool
	failReason  string
	verifyCount int64
	settleCount int64
}

// NewHTTPFacilitatorServer constructs a standalone x402 Section 7 HTTP
// facilitator server.
func NewHTTPFacilitatorServer(id, operator string) *HTTPFacilitatorServer {
	if strings.TrimSpace(operator) == "" {
		operator = "independent"
	}
	return &HTTPFacilitatorServer{
		ID:        id,
		Operator:  operator,
		spent:     make(map[string]string),
		txToClaim: make(map[string]string),
		reversed:  make(map[string]string),
	}
}

func (s *HTTPFacilitatorServer) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// FailNextSettle causes the next POST /settle request to return Success: false
// with the provided reason (simulating an on-chain revert or insufficient balance).
func (s *HTTPFacilitatorServer) FailNextSettle(reason string) {
	s.mu.Lock()
	s.failNext = true
	s.failReason = reason
	s.mu.Unlock()
}

// ReverseTransaction marks an already-settled transaction as reversed on-chain
// and returns a deterministic ReversalEvent for receipt reconciliation.
func (s *HTTPFacilitatorServer) ReverseTransaction(txHash, reason string, requireRefund bool) (ReversalEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.txToClaim[txHash]; !ok {
		return ReversalEvent{}, fmt.Errorf("x402 facilitator %s: unknown transaction %q", s.ID, txHash)
	}
	revHash := protocol.Hex(crypto.Keccak256([]byte("reverse|" + s.ID + "|" + txHash + "|" + reason)))
	s.reversed[txHash] = revHash
	return ReversalEvent{
		FacilitatorID: s.ID,
		Transaction:   txHash,
		ReversalTx:    revHash,
		Reason:        reason,
		RequireRefund: requireRefund,
		Timestamp:     s.clock().UTC().Unix(),
	}, nil
}

// Stats returns verified and settled counts observed directly by this HTTP server.
func (s *HTTPFacilitatorServer) Counts() (verified, settled int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifyCount, s.settleCount
}

type facilitatorReq struct {
	X402Version         int                          `json:"x402Version"`
	PaymentPayload      protocol.PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements protocol.PaymentRequirements `json:"paymentRequirements"`
}

// ServeHTTP serves GET /health, POST /verify, POST /settle, and POST /reverse.
func (s *HTTPFacilitatorServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"id":       s.ID,
			"operator": s.Operator,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/verify":
		s.handleVerify(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/settle":
		s.handleSettle(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/reverse":
		s.handleReverse(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}
}

func (s *HTTPFacilitatorServer) handleVerify(w http.ResponseWriter, r *http.Request) {
	if s.VerifyDelay > 0 {
		time.Sleep(s.VerifyDelay)
	}
	var req facilitatorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	if err := protocol.Verify(req.PaymentRequirements, req.PaymentPayload); err != nil {
		_ = json.NewEncoder(w).Encode(protocol.VerifyResponse{
			IsValid:        false,
			InvalidReason:  protocol.ReasonOf(err, protocol.ReasonVerifyError),
			InvalidMessage: err.Error(),
		})
		return
	}
	if err := protocol.InWindow(req.PaymentPayload.Payload.Authorization, s.clock()); err != nil {
		_ = json.NewEncoder(w).Encode(protocol.VerifyResponse{
			IsValid:        false,
			InvalidReason:  protocol.ReasonOf(err, protocol.ReasonVerifyError),
			InvalidMessage: err.Error(),
		})
		return
	}
	key := claimKeyOf(req.PaymentPayload, req.PaymentRequirements)
	s.mu.Lock()
	_, alreadySpent := s.spent[key]
	if !alreadySpent {
		s.verifyCount++
	}
	s.mu.Unlock()
	if alreadySpent {
		_ = json.NewEncoder(w).Encode(protocol.VerifyResponse{
			IsValid:        false,
			InvalidReason:  protocol.ReasonInvalidTransaction,
			InvalidMessage: "nonce already settled on facilitator " + s.ID,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(protocol.VerifyResponse{
		IsValid: true,
		Payer:   req.PaymentPayload.Payload.Authorization.From,
	})
}

func (s *HTTPFacilitatorServer) handleSettle(w http.ResponseWriter, r *http.Request) {
	if s.SettleDelay > 0 {
		time.Sleep(s.SettleDelay)
	}
	var req facilitatorReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	if err := protocol.Verify(req.PaymentRequirements, req.PaymentPayload); err != nil {
		_ = json.NewEncoder(w).Encode(protocol.SettlementResponse{
			Success:      false,
			ErrorReason:  protocol.ReasonOf(err, protocol.ReasonSettleError),
			ErrorMessage: err.Error(),
			Network:      req.PaymentRequirements.Network,
		})
		return
	}
	if err := protocol.InWindow(req.PaymentPayload.Payload.Authorization, s.clock()); err != nil {
		_ = json.NewEncoder(w).Encode(protocol.SettlementResponse{
			Success:      false,
			ErrorReason:  protocol.ReasonOf(err, protocol.ReasonSettleError),
			ErrorMessage: err.Error(),
			Network:      req.PaymentRequirements.Network,
		})
		return
	}

	key := claimKeyOf(req.PaymentPayload, req.PaymentRequirements)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failNext {
		reason := s.failReason
		if reason == "" {
			reason = protocol.ReasonSettleError
		}
		s.failNext = false
		s.failReason = ""
		_ = json.NewEncoder(w).Encode(protocol.SettlementResponse{
			Success:      false,
			ErrorReason:  reason,
			ErrorMessage: "simulated settlement revert on " + s.ID,
			Payer:        req.PaymentPayload.Payload.Authorization.From,
			Network:      req.PaymentRequirements.Network,
		})
		return
	}
	if _, alreadySpent := s.spent[key]; alreadySpent {
		_ = json.NewEncoder(w).Encode(protocol.SettlementResponse{
			Success:      false,
			ErrorReason:  protocol.ReasonInvalidTransaction,
			ErrorMessage: "nonce already settled on " + s.ID,
			Payer:        req.PaymentPayload.Payload.Authorization.From,
			Network:      req.PaymentRequirements.Network,
		})
		return
	}
	txHash := protocol.Hex(crypto.Keccak256([]byte("settle|" + s.ID + "|" + key + "|" + req.PaymentRequirements.Amount)))
	s.spent[key] = txHash
	s.txToClaim[txHash] = key
	s.settleCount++

	_ = json.NewEncoder(w).Encode(protocol.SettlementResponse{
		Success:     true,
		Payer:       req.PaymentPayload.Payload.Authorization.From,
		Transaction: txHash,
		Network:     req.PaymentRequirements.Network,
		Amount:      req.PaymentRequirements.Amount,
	})
}

func (s *HTTPFacilitatorServer) handleReverse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Transaction   string `json:"transaction"`
		Reason        string `json:"reason"`
		RequireRefund bool   `json:"requireRefund"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	ev, err := s.ReverseTransaction(in.Transaction, in.Reason, in.RequireRefund)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(ev)
}
