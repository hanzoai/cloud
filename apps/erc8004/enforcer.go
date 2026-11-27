// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
)

// RevocationChecker abstracts live vs cached revocation lookups so the runtime
// enforcer can run fail-closed against live state or measure the stale-cache window.
type RevocationChecker interface {
	IsRevoked(delegationID, agentID string, grantEpoch uint64) (bool, string)
}

// LiveRevocationChecker queries IdentityRegistry synchronously with zero grace window.
type LiveRevocationChecker struct {
	Registry *IdentityRegistry
}

// IsRevoked performs an immediate O(1) revocation lookup.
func (c LiveRevocationChecker) IsRevoked(delegationID, agentID string, grantEpoch uint64) (bool, string) {
	if c.Registry == nil {
		return true, "nil_registry_fail_closed"
	}
	return c.Registry.IsRevoked(delegationID, agentID, grantEpoch)
}

type cacheEntry struct {
	revoked   bool
	reason    string
	expiresAt time.Time
}

// CachedRevocationChecker caches negative/positive revocation lookups for TTL.
// Used explicitly to quantify the stale_cache_revocation_window boundary regime:
// any TTL > 0 leaves a measurable window where a compromised revoked delegation
// continues to execute until the cache entry expires.
type CachedRevocationChecker struct {
	mu       sync.RWMutex
	upstream RevocationChecker
	ttl      time.Duration
	nowFn    func() time.Time
	entries  map[string]cacheEntry
}

// NewCachedRevocationChecker builds a TTL-caching revocation checker.
func NewCachedRevocationChecker(upstream RevocationChecker, ttl time.Duration, nowFn func() time.Time) *CachedRevocationChecker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &CachedRevocationChecker{
		upstream: upstream,
		ttl:      ttl,
		nowFn:    nowFn,
		entries:  make(map[string]cacheEntry),
	}
}

// IsRevoked returns the cached decision while within TTL, otherwise queries upstream.
func (c *CachedRevocationChecker) IsRevoked(delegationID, agentID string, grantEpoch uint64) (bool, string) {
	now := c.nowFn()
	c.mu.RLock()
	if ent, ok := c.entries[delegationID]; ok && now.Before(ent.expiresAt) {
		c.mu.RUnlock()
		return ent.revoked, ent.reason
	}
	c.mu.RUnlock()

	revoked, reason := c.upstream.IsRevoked(delegationID, agentID, grantEpoch)
	if c.ttl > 0 {
		c.mu.Lock()
		c.entries[delegationID] = cacheEntry{
			revoked:   revoked,
			reason:    reason,
			expiresAt: now.Add(c.ttl),
		}
		c.mu.Unlock()
	}
	return revoked, reason
}

// ToolExecutor executes the actual underlying tool/sandbox command ONLY after
// ToolSandboxEnforcer verifies cryptographic delegation, scope, replay, and revocation.
type ToolExecutor func(req ToolActionRequest) (string, error)

// ToolSandboxEnforcer enforces ERC-8004 delegation boundaries at the actual tool
// and sandbox layer. Holding an agent NFT or wallet address alone is never sufficient.
type ToolSandboxEnforcer struct {
	mu         sync.Mutex
	registry   *IdentityRegistry
	revocation RevocationChecker
}

// NewToolSandboxEnforcer constructs a runtime tool/sandbox enforcer backed by registry
// and revocation checker (defaults to live O(1) fail-closed checking).
func NewToolSandboxEnforcer(registry *IdentityRegistry, checker RevocationChecker) *ToolSandboxEnforcer {
	if checker == nil {
		checker = LiveRevocationChecker{Registry: registry}
	}
	return &ToolSandboxEnforcer{
		registry:   registry,
		revocation: checker,
	}
}

// Execute verifies the delegate-signed ToolActionRequest against the registered
// agent identity, signed delegation grant, tool allowlist, sandbox resource scope,
// replay guard, and revocation state before invoking execFn.
func (e *ToolSandboxEnforcer) Execute(req ToolActionRequest, execFn ToolExecutor) (*ToolExecutionRecord, error) {
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.Tool) == "" || strings.TrimSpace(req.Resource) == "" {
		return nil, fmt.Errorf("%w: tool request missing requestId, tool, or resource", ErrInvalidRequest)
	}
	ag, err := e.registry.GetAgent(req.AgentID)
	if err != nil {
		return nil, err
	}
	if ag.Status != StatusActive {
		return nil, ErrAgentInactive
	}
	grant, err := e.registry.GetDelegation(req.DelegationID)
	if err != nil {
		return nil, err
	}
	if grant.AgentID != req.AgentID {
		return nil, fmt.Errorf("%w: delegation agent mismatch", ErrUnauthorizedSigner)
	}
	now := e.registry.Now()
	domain := e.registry.Domain()

	// Verify owner's EIP-712 signature on the delegation grant itself.
	if err := ValidateDelegationOffline(*grant, ag.OwnerAddress, domain, now); err != nil {
		return nil, err
	}
	// Verify delegate's EIP-712 signature on the tool request.
	if !SameAddress(req.DelegateAddress, grant.DelegateAddress) {
		return nil, ErrUnauthorizedSigner
	}
	if err := VerifySigner(req.Digest(domain), req.DelegateSignature, grant.DelegateAddress); err != nil {
		return nil, err
	}
	// Enforce exact tool allowlist (deny-by-default).
	if !toolAllowed(grant.AllowedTools, req.Tool) {
		return nil, fmt.Errorf("%w: tool %q not in allowlist", ErrToolNotAllowed, req.Tool)
	}
	// Enforce normalized sandbox resource scope (prevents path traversal).
	if !resourceAllowed(grant.AllowedResources, req.Resource) {
		return nil, fmt.Errorf("%w: resource %q outside delegation scope", ErrResourceNotAllowed, req.Resource)
	}

	// Atomically check live revocation and registry-persisted separated replay guards.
	e.mu.Lock()
	if revoked, reason := e.revocation.IsRevoked(grant.DelegationID, grant.AgentID, grant.RevocationEpoch); revoked {
		e.mu.Unlock()
		if reason == "revocation_epoch_stale" {
			return nil, ErrRevocationEpochStale
		}
		return nil, ErrDelegationRevoked
	}
	if err := e.registry.reserveToolExecution(grant.DelegationID, req.RequestID, req.Nonce, grant.DelegateAddress); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	e.mu.Unlock()

	output := "ok"
	if execFn != nil {
		out, err := execFn(req)
		if err != nil {
			return nil, err
		}
		output = out
	}
	outHash := Keccak256Hex([]byte(output))
	return &ToolExecutionRecord{
		RequestID:    req.RequestID,
		DelegationID: grant.DelegationID,
		AgentID:      grant.AgentID,
		Tool:         req.Tool,
		Resource:     req.Resource,
		Output:       output,
		OutputHash:   outHash,
		ExecutedAt:   now,
	}, nil
}

// PaymentLimitEnforcer enforces per-call and cumulative delegation spend limits,
// asset binding, replay protection, and live revocation atomically under concurrency.
type PaymentLimitEnforcer struct {
	mu         sync.Mutex
	registry   *IdentityRegistry
	revocation RevocationChecker
}

// NewPaymentLimitEnforcer constructs a concurrency-safe runtime payment enforcer.
func NewPaymentLimitEnforcer(registry *IdentityRegistry, checker RevocationChecker) *PaymentLimitEnforcer {
	if checker == nil {
		checker = LiveRevocationChecker{Registry: registry}
	}
	return &PaymentLimitEnforcer{
		registry:   registry,
		revocation: checker,
	}
}

// AuthorizePayment verifies the delegate's payment request and atomically reserves
// AmountAtomic against the delegation's MaxPerCallAtomic and MaxSpendAtomic caps,
// persisting spend/replay state and verified payment evidence on the registry.
func (p *PaymentLimitEnforcer) AuthorizePayment(req PaymentAuthRequest) (*PaymentAuthReceipt, error) {
	if strings.TrimSpace(req.AuthID) == "" || req.AmountAtomic == 0 {
		return nil, fmt.Errorf("%w: payment auth requires non-empty authId and positive amountAtomic", ErrInvalidRequest)
	}
	payeeNorm, err := NormalizeAddress(req.Payee)
	if err != nil {
		return nil, fmt.Errorf("%w: payee: %v", ErrInvalidRequest, err)
	}
	ag, err := p.registry.GetAgent(req.AgentID)
	if err != nil {
		return nil, err
	}
	if ag.Status != StatusActive {
		return nil, ErrAgentInactive
	}
	grant, err := p.registry.GetDelegation(req.DelegationID)
	if err != nil {
		return nil, err
	}
	if grant.AgentID != req.AgentID {
		return nil, fmt.Errorf("%w: delegation agent mismatch", ErrUnauthorizedSigner)
	}
	now := p.registry.Now()
	domain := p.registry.Domain()

	if err := ValidateDelegationOffline(*grant, ag.OwnerAddress, domain, now); err != nil {
		return nil, err
	}
	if !SameAddress(req.DelegateAddress, grant.DelegateAddress) {
		return nil, ErrUnauthorizedSigner
	}
	if err := VerifySigner(req.Digest(domain), req.DelegateSignature, grant.DelegateAddress); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.Asset), strings.TrimSpace(grant.Asset)) {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrAssetMismatch, req.Asset, grant.Asset)
	}
	if grant.MaxPerCallAtomic > 0 && req.AmountAtomic > grant.MaxPerCallAtomic {
		return nil, fmt.Errorf("%w: %d > per-call limit %d", ErrPerCallLimitExceeded, req.AmountAtomic, grant.MaxPerCallAtomic)
	}

	// Atomically verify live revocation, separated replay guards, and persistent spend cap.
	p.mu.Lock()
	defer p.mu.Unlock()

	if revoked, reason := p.revocation.IsRevoked(grant.DelegationID, grant.AgentID, grant.RevocationEpoch); revoked {
		if reason == "revocation_epoch_stale" {
			return nil, ErrRevocationEpochStale
		}
		return nil, ErrDelegationRevoked
	}

	payerAddr := ag.PaymentWallet
	if payerAddr == "" {
		payerAddr = ag.OwnerAddress
	}
	return p.registry.reservePaymentExecution(*grant, req, payerAddr, payeeNorm, now)
}

// Spent returns the current cumulative atomic units authorized under delegationID.
func (p *PaymentLimitEnforcer) Spent(delegationID string) uint64 {
	if p.registry == nil {
		return 0
	}
	return p.registry.DelegationSpent(delegationID)
}

// EnforcementGate combines ToolSandboxEnforcer and PaymentLimitEnforcer behind a single
// runtime policy enforcement boundary.
type EnforcementGate struct {
	Tools    *ToolSandboxEnforcer
	Payments *PaymentLimitEnforcer
}

// NewEnforcementGate constructs a fail-closed EnforcementGate with live revocation checking.
func NewEnforcementGate(registry *IdentityRegistry, checker RevocationChecker) *EnforcementGate {
	if checker == nil {
		checker = LiveRevocationChecker{Registry: registry}
	}
	return &EnforcementGate{
		Tools:    NewToolSandboxEnforcer(registry, checker),
		Payments: NewPaymentLimitEnforcer(registry, checker),
	}
}

// NFTOnlyNegativeEnforcer is an intentionally insecure NEGATIVE CONTROL
// (`nft_ownership_only_negative`) that verifies ONLY whether an ERC-8004 identity NFT
// exists for the caller's wallet/agentID without checking tool allowlists, sandbox paths,
// spending limits, or delegation revocation.
//
// It exists solely to demonstrate quantitatively why "an NFT or wallet address is not security."
type NFTOnlyNegativeEnforcer struct {
	Registry *IdentityRegistry
}

// CheckToolNFTOnly returns nil whenever agentID exists on the registry, ignoring tool
// scope, sandbox path, expiration, and revocation.
func (n NFTOnlyNegativeEnforcer) CheckToolNFTOnly(agentID string) error {
	if n.Registry == nil {
		return ErrAgentNotFound
	}
	_, err := n.Registry.GetAgent(agentID)
	return err
}

// CheckPaymentNFTOnly returns nil whenever agentID exists on the registry, ignoring
// per-call caps, cumulative spending limits, and revocation.
func (n NFTOnlyNegativeEnforcer) CheckPaymentNFTOnly(agentID string) error {
	if n.Registry == nil {
		return ErrAgentNotFound
	}
	_, err := n.Registry.GetAgent(agentID)
	return err
}

func toolAllowed(allowlist []string, tool string) bool {
	want := strings.TrimSpace(tool)
	if want == "" {
		return false
	}
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == want {
			return true
		}
	}
	return false
}

// resourceAllowed enforces strict sandbox resource scoping with path normalization and
// rejection of ".." traversal segments so a delegate cannot escape its sandbox prefix.
func resourceAllowed(allowlist []string, resource string) bool {
	resScheme, resPath, ok := splitResourceURI(resource)
	if !ok {
		return false
	}
	for _, rawPattern := range allowlist {
		patScheme, patPath, ok := splitResourceURI(rawPattern)
		if !ok || patScheme != resScheme {
			continue
		}
		if strings.HasSuffix(patPath, "/*") {
			prefix := strings.TrimSuffix(patPath, "*") // retains trailing "/"
			if strings.HasPrefix(resPath, prefix) {
				return true
			}
			continue
		}
		if resPath == patPath {
			return true
		}
	}
	return false
}

func splitResourceURI(raw string) (string, string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.Contains(s, "\\") {
		return "", "", false
	}
	scheme, rest, found := strings.Cut(s, "://")
	if !found || scheme == "" || rest == "" {
		return "", "", false
	}
	// Reject raw ".." segments before cleaning so traversal attempts are always denied.
	for _, seg := range strings.Split(rest, "/") {
		if seg == ".." {
			return "", "", false
		}
	}
	cleaned := path.Clean("/" + rest)
	return strings.ToLower(scheme), cleaned, true
}
