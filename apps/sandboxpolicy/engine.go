// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"fmt"
	"math/bits"
	"net/netip"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultMaxTenants bounds distinct tenant namespaces stored in one Engine.
	DefaultMaxTenants = 4096
	// DefaultMaxPoliciesPerTenant bounds compiled policies stored per tenant.
	DefaultMaxPoliciesPerTenant = 256
	// DefaultMaxSandboxesPerTenant bounds bound sandboxes stored per tenant.
	DefaultMaxSandboxesPerTenant = 4096
)

// DangerousCapabilitiesMask flags Linux capabilities that must never be held
// or acquired inside an unprivileged Visor agent sandbox.
const (
	CapDACReadSearch uint64 = 1 << 2
	CapSetGID        uint64 = 1 << 6
	CapSetUID        uint64 = 1 << 7
	CapNetAdmin      uint64 = 1 << 12
	CapNetRaw        uint64 = 1 << 13
	CapSysModule     uint64 = 1 << 16
	CapSysRawIO      uint64 = 1 << 17
	CapSysChroot     uint64 = 1 << 18
	CapSysPtrace     uint64 = 1 << 19
	CapSysAdmin      uint64 = 1 << 21
	CapSysBoot       uint64 = 1 << 22
	CapBPF           uint64 = 1 << 39

	DangerousCapabilitiesMask uint64 = CapDACReadSearch | CapSetGID | CapSetUID |
		CapNetAdmin | CapNetRaw | CapSysModule | CapSysRawIO | CapSysChroot |
		CapSysPtrace | CapSysAdmin | CapSysBoot | CapBPF
)

// EngineConfig configures bounds and audit ring behavior for an Engine.
type EngineConfig struct {
	MaxTenants            int
	MaxPoliciesPerTenant  int
	MaxSandboxesPerTenant int
	AuditRingCapacity     int
	RingOverflow          RingOverflowPolicy
	Now                   func() time.Time
}

// SyscallEvent represents a runtime Linux syscall invocation to evaluate.
type SyscallEvent struct {
	// SyscallName is the canonical syscall name (e.g., "ptrace", "openat").
	SyscallName string `json:"syscallName,omitempty"`
	// SyscallNumber optionally specifies the numeric syscall ID.
	SyscallNumber *int `json:"syscallNumber,omitempty"`
	// Args holds up to 6 raw 64-bit syscall argument registers.
	Args [6]uint64 `json:"args,omitempty"`
	// ProcessLineage lists the ancestor binary/command chain.
	ProcessLineage []string `json:"processLineage,omitempty"`
}

// ProcessEvent represents a runtime process execution (execve) to evaluate.
type ProcessEvent struct {
	// BinaryPath is the invoked executable path (normalized before matching).
	BinaryPath string `json:"binaryPath"`
	// ResolvedPath optionally carries the kernel/VFS resolved symlink target.
	ResolvedPath string `json:"resolvedPath,omitempty"`
	// Args holds argv elements passed to the process.
	Args []string `json:"args,omitempty"`
	// ParentBinary is the immediate parent executable path or basename.
	ParentBinary string `json:"parentBinary,omitempty"`
	// ProcessLineage lists the ancestor binary/command chain.
	ProcessLineage []string `json:"processLineage,omitempty"`
	// UID is the calling process UID.
	UID uint32 `json:"uid,omitempty"`
	// GID is the calling process GID.
	GID uint32 `json:"gid,omitempty"`
	// NewUID optionally carries the target UID after exec (triggers PrivEsc if != UID).
	NewUID *uint32 `json:"newUid,omitempty"`
	// NewGID optionally carries the target GID after exec (triggers PrivEsc if != GID).
	NewGID *uint32 `json:"newGid,omitempty"`
	// Setuid indicates a setuid/setgid binary bit was present.
	Setuid bool `json:"setuid,omitempty"`
	// CapabilitiesAdded holds any Linux capability bits added across exec.
	CapabilitiesAdded uint64 `json:"capabilitiesAdded,omitempty"`
	// EffectiveCaps holds the resulting Linux effective capability bitmask.
	EffectiveCaps uint64 `json:"effectiveCaps,omitempty"`
	// NoNewPrivs optionally records PR_SET_NO_NEW_PRIVS state (false triggers PrivEsc).
	NoNewPrivs *bool `json:"noNewPrivs,omitempty"`
}

// NetworkEvent represents a runtime socket connect (egress) or bind event.
type NetworkEvent struct {
	// Direction is "egress" or "bind".
	Direction NetworkDirection `json:"direction"`
	// Protocol is "tcp" or "udp".
	Protocol string `json:"protocol"`
	// DestIP is the IPv4 or IPv6 destination/bind address (IPv4-mapped IPv6 is unmapped).
	DestIP string `json:"destIp"`
	// DestPort is the destination or bind port (1..65535).
	DestPort uint16 `json:"destPort"`
	// ProcessLineage lists the ancestor binary/command chain.
	ProcessLineage []string `json:"processLineage,omitempty"`
}

// EvaluationRequest bundles one runtime event scoped to a tenant and sandbox.
type EvaluationRequest struct {
	TenantID  string
	SandboxID string
	Domain    Domain
	Syscall   *SyscallEvent
	Process   *ProcessEvent
	Network   *NetworkEvent
	// Unindexed forces linear rule scanning (used strictly for Negative Regime 1 benchmarking).
	Unindexed bool
	// BypassAllowAudit overrides policy.AuditAllowEvents to avoid audit ring emission on ALLOW.
	BypassAllowAudit bool
}

// DecisionResult is the deterministic output returned for every evaluation.
type DecisionResult struct {
	// Decision is "ALLOW", "AUDIT_ALERT", "DENY", or "SIGKILL".
	Decision Decision `json:"decision"`
	// Blocked is true for DENY and SIGKILL.
	Blocked bool `json:"blocked"`
	// Errno is 0 (allow/audit), 1 (EPERM on DENY), or 9 (SIGKILL).
	Errno int `json:"errno"`
	// PolicyID is the evaluated policy identifier (empty if fail-closed unbound).
	PolicyID string `json:"policyId"`
	// PolicyVersion is the evaluated policy version (0 if fail-closed unbound).
	PolicyVersion uint64 `json:"policyVersion"`
	// Domain is the evaluated domain ("syscall", "process", or "network").
	Domain Domain `json:"domain"`
	// RuleID is the winning rule identifier or fail-closed sentinel.
	RuleID string `json:"ruleId"`
	// Subject is the normalized syscall, binary path, or IP:port evaluated.
	Subject string `json:"subject"`
	// Reason explains the enforcement verdict.
	Reason string `json:"reason"`
	// AuditRecorded is true if an audit event was appended to the tenant's hash-chained ring.
	AuditRecorded bool `json:"auditRecorded"`
	// AuditDropped is true if audit emission was dropped due to ring backpressure.
	AuditDropped bool `json:"auditDropped"`
	// AuditSeq is the sequence number of the appended audit event (0 if not recorded).
	AuditSeq uint64 `json:"auditSeq,omitempty"`
	// AuditHash is the SHA-256 chain hash of the appended audit event.
	AuditHash string `json:"auditHash,omitempty"`
}

// PolicySummary describes a compiled policy stored for a tenant.
type PolicySummary struct {
	// ID uniquely identifies the policy within the tenant.
	ID string `json:"id"`
	// Name is the human-readable policy name.
	Name string `json:"name"`
	// Version is the compiled policy version.
	Version uint64 `json:"version"`
	// Mode is the default enforcement mode.
	Mode EnforcementMode `json:"mode"`
	// Arch is the compiled BPF architecture ("amd64" or "arm64").
	Arch string `json:"arch"`
	// Digest is the canonical SHA-256 policy digest.
	Digest string `json:"digest"`
	// BPFDigest is the SHA-256 digest of the compiled seccomp-BPF instruction stream.
	BPFDigest string `json:"bpfDigest"`
	// BPFInsnCount is the number of classic BPF instructions in the compiled seccomp filter.
	BPFInsnCount int `json:"bpfInsnCount"`
	// SyscallRuleCount is the number of compiled syscall rules.
	SyscallRuleCount int `json:"syscallRuleCount"`
	// ProcessRuleCount is the number of compiled process rules.
	ProcessRuleCount int `json:"processRuleCount"`
	// NetworkRuleCount is the number of compiled network rules.
	NetworkRuleCount int `json:"networkRuleCount"`
}

// TenantMetrics reports per-tenant enforcement counters and audit ring health.
type TenantMetrics struct {
	// Policies is the count of compiled policies owned by this tenant.
	Policies int `json:"policies"`
	// Sandboxes is the count of sandboxes bound to policies for this tenant.
	Sandboxes int `json:"sandboxes"`
	// TotalEvaluations is total runtime events evaluated for this tenant.
	TotalEvaluations uint64 `json:"totalEvaluations"`
	// Allowed is total ALLOW decisions for this tenant.
	Allowed uint64 `json:"allowed"`
	// AuditAlerts is total AUDIT_ALERT decisions for this tenant.
	AuditAlerts uint64 `json:"auditAlerts"`
	// Denied is total DENY (EPERM) decisions for this tenant.
	Denied uint64 `json:"denied"`
	// Sigkills is total SIGKILL decisions for this tenant.
	Sigkills uint64 `json:"sigkills"`
	// AuditRing holds bounded per-tenant ring telemetry including drops and head hash.
	AuditRing AuditRingStats `json:"auditRing"`
}

// AggregateHealth holds non-tenant-specific cluster-wide readiness counters.
type AggregateHealth struct {
	Policies         int
	Sandboxes        int
	TotalEvaluations uint64
	TotalDenied      uint64
	TotalSigkills    uint64
}

type tenantState struct {
	policies  map[string]*CompiledPolicy
	sandboxes map[string]*CompiledPolicy
	audit     *TenantAuditRing

	evals       atomic.Uint64
	allowed     atomic.Uint64
	auditAlerts atomic.Uint64
	denied      atomic.Uint64
	sigkills    atomic.Uint64
}

// Engine manages tenant-isolated compiled TracingPolicies, sandbox bindings,
// deterministic runtime evaluation, and SHA-256 hash-chained audit logs.
type Engine struct {
	mu      sync.RWMutex
	cfg     EngineConfig
	tenants map[string]*tenantState

	totalEvals    atomic.Uint64
	totalDenied   atomic.Uint64
	totalSigkills atomic.Uint64
}

// NewEngine constructs a concurrency-safe Engine with bounded per-tenant state.
func NewEngine(cfg EngineConfig) *Engine {
	if cfg.MaxTenants <= 0 {
		cfg.MaxTenants = DefaultMaxTenants
	}
	if cfg.MaxPoliciesPerTenant <= 0 {
		cfg.MaxPoliciesPerTenant = DefaultMaxPoliciesPerTenant
	}
	if cfg.MaxSandboxesPerTenant <= 0 {
		cfg.MaxSandboxesPerTenant = DefaultMaxSandboxesPerTenant
	}
	if cfg.AuditRingCapacity <= 0 {
		cfg.AuditRingCapacity = DefaultAuditRingCapacity
	}
	if cfg.RingOverflow == "" {
		cfg.RingOverflow = RingOverwriteOldest
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Engine{
		cfg:     cfg,
		tenants: make(map[string]*tenantState),
	}
}

func (e *Engine) getOrCreateTenantLocked(tenantID string) (*tenantState, error) {
	if tenantID == "" || len(tenantID) > MaxFieldLen {
		return nil, fmt.Errorf("%w: invalid tenant id", ErrInvalidPolicy)
	}
	ts, ok := e.tenants[tenantID]
	if ok {
		return ts, nil
	}
	if len(e.tenants) >= e.cfg.MaxTenants {
		return nil, ErrTenantLimitExceeded
	}
	ts = &tenantState{
		policies:  make(map[string]*CompiledPolicy),
		sandboxes: make(map[string]*CompiledPolicy),
		audit:     NewTenantAuditRing(e.cfg.AuditRingCapacity, e.cfg.RingOverflow),
	}
	e.tenants[tenantID] = ts
	return ts, nil
}

// UpsertPolicy validates, compiles (with seccomp-BPF generation), and stores a
// TracingPolicy strictly within tenantID. If compilation fails or rules conflict,
// the error is returned and any previously active policy remains untouched.
func (e *Engine) UpsertPolicy(tenantID string, spec TracingPolicy) (*CompiledPolicy, error) {
	cp, err := CompilePolicy(spec)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	ts, err := e.getOrCreateTenantLocked(tenantID)
	if err != nil {
		return nil, err
	}
	if _, exists := ts.policies[cp.Spec.ID]; !exists && len(ts.policies) >= e.cfg.MaxPoliciesPerTenant {
		return nil, ErrTenantLimitExceeded
	}
	ts.policies[cp.Spec.ID] = cp
	return cp, nil
}

// GetPolicy returns a compiled policy owned by tenantID, or ErrPolicyNotFound.
func (e *Engine) GetPolicy(tenantID, policyID string) (*CompiledPolicy, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		return nil, ErrPolicyNotFound
	}
	cp, ok := ts.policies[strings.TrimSpace(policyID)]
	if !ok {
		return nil, ErrPolicyNotFound
	}
	return cp, nil
}

// ListPolicies returns sorted summaries of all compiled policies belonging strictly to tenantID.
func (e *Engine) ListPolicies(tenantID string) []PolicySummary {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		return []PolicySummary{}
	}
	out := make([]PolicySummary, 0, len(ts.policies))
	for _, cp := range ts.policies {
		out = append(out, SummarizePolicy(cp))
	}
	// Deterministic lexicographic ordering by policy ID.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// SummarizePolicy converts a CompiledPolicy into its JSON-serializable summary.
func SummarizePolicy(cp *CompiledPolicy) PolicySummary {
	return PolicySummary{
		ID:               cp.Spec.ID,
		Name:             cp.Spec.Name,
		Version:          cp.Spec.Version,
		Mode:             cp.Spec.Mode,
		Arch:             cp.Spec.Arch,
		Digest:           cp.Digest,
		BPFDigest:        cp.BPFDigest,
		BPFInsnCount:     len(cp.BPF.Insns),
		SyscallRuleCount: len(cp.allSyscalls),
		ProcessRuleCount: len(cp.allProcs),
		NetworkRuleCount: len(cp.allNets),
	}
}

// BindSandbox associates sandboxID with an existing compiled policyID strictly
// within tenantID.
func (e *Engine) BindSandbox(tenantID, sandboxID, policyID string) (*CompiledPolicy, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	policyID = strings.TrimSpace(policyID)
	if sandboxID == "" || len(sandboxID) > MaxFieldLen {
		return nil, fmt.Errorf("%w: invalid sandboxId", ErrInvalidPolicy)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		return nil, ErrPolicyNotFound
	}
	cp, ok := ts.policies[policyID]
	if !ok {
		return nil, ErrPolicyNotFound
	}
	if _, exists := ts.sandboxes[sandboxID]; !exists && len(ts.sandboxes) >= e.cfg.MaxSandboxesPerTenant {
		return nil, ErrTenantLimitExceeded
	}
	ts.sandboxes[sandboxID] = cp
	return cp, nil
}

// AuditEvents returns the chronological retained audit events for tenantID,
// optionally filtered by sandboxID and capped to the latest limit entries.
func (e *Engine) AuditEvents(tenantID, sandboxID string, limit int) ([]AuditEvent, AuditRingStats) {
	e.mu.RLock()
	ts, ok := e.tenants[tenantID]
	e.mu.RUnlock()
	if !ok {
		return []AuditEvent{}, AuditRingStats{Capacity: e.cfg.AuditRingCapacity, HeadHash: GenesisAuditHash}
	}
	evs := ts.audit.Snapshot(strings.TrimSpace(sandboxID), limit)
	if evs == nil {
		evs = []AuditEvent{}
	}
	return evs, ts.audit.Stats()
}

// DrainAuditRing drains up to n oldest events from tenantID's audit ring.
func (e *Engine) DrainAuditRing(tenantID string, n int) int {
	e.mu.RLock()
	ts, ok := e.tenants[tenantID]
	e.mu.RUnlock()
	if !ok {
		return 0
	}
	return ts.audit.Drain(n)
}

// TenantStats returns enforcement and audit ring telemetry strictly scoped to tenantID.
func (e *Engine) TenantStats(tenantID string) TenantMetrics {
	e.mu.RLock()
	ts, ok := e.tenants[tenantID]
	if !ok {
		e.mu.RUnlock()
		return TenantMetrics{
			AuditRing: AuditRingStats{Capacity: e.cfg.AuditRingCapacity, HeadHash: GenesisAuditHash},
		}
	}
	polCount := len(ts.policies)
	sbCount := len(ts.sandboxes)
	e.mu.RUnlock()

	return TenantMetrics{
		Policies:         polCount,
		Sandboxes:        sbCount,
		TotalEvaluations: ts.evals.Load(),
		Allowed:          ts.allowed.Load(),
		AuditAlerts:      ts.auditAlerts.Load(),
		Denied:           ts.denied.Load(),
		Sigkills:         ts.sigkills.Load(),
		AuditRing:        ts.audit.Stats(),
	}
}

// Health returns non-tenant-specific aggregate counters for readiness probes.
func (e *Engine) Health() AggregateHealth {
	e.mu.RLock()
	var policies, sandboxes int
	for _, ts := range e.tenants {
		policies += len(ts.policies)
		sandboxes += len(ts.sandboxes)
	}
	e.mu.RUnlock()
	return AggregateHealth{
		Policies:         policies,
		Sandboxes:        sandboxes,
		TotalEvaluations: e.totalEvals.Load(),
		TotalDenied:      e.totalDenied.Load(),
		TotalSigkills:    e.totalSigkills.Load(),
	}
}

// Evaluate evaluates one runtime event against the compiled policy bound to
// (req.TenantID, req.SandboxID), failing closed whenever no valid policy is
// bound or inputs fail validation, and appending a hash-chained audit record.
func (e *Engine) Evaluate(req EvaluationRequest) DecisionResult {
	e.totalEvals.Add(1)

	e.mu.RLock()
	ts, hasTenant := e.tenants[req.TenantID]
	var cp *CompiledPolicy
	if hasTenant {
		cp = ts.sandboxes[strings.TrimSpace(req.SandboxID)]
	}
	e.mu.RUnlock()

	if !hasTenant {
		// Create tenant audit bucket on demand if valid tenantID so fail-closed
		// denials are still auditable under the tenant's own ring.
		e.mu.Lock()
		var err error
		ts, err = e.getOrCreateTenantLocked(req.TenantID)
		e.mu.Unlock()
		if err != nil {
			e.totalDenied.Add(1)
			return DecisionResult{
				Decision: DecisionDeny,
				Blocked:  true,
				Errno:    ErrnoEPERM,
				Domain:   req.Domain,
				RuleID:   "fail_closed_invalid_tenant",
				Reason:   "missing or invalid tenant identity",
			}
		}
	}

	ts.evals.Add(1)

	var lineage []string
	switch req.Domain {
	case DomainSyscall:
		if req.Syscall != nil {
			lineage = req.Syscall.ProcessLineage
		}
	case DomainProcess:
		if req.Process != nil {
			lineage = req.Process.ProcessLineage
		}
	case DomainNetwork:
		if req.Network != nil {
			lineage = req.Network.ProcessLineage
		}
	}

	// Fail closed when sandbox has no compiled policy bound.
	if cp == nil {
		res := DecisionResult{
			Decision: DecisionDeny,
			Blocked:  true,
			Errno:    ErrnoEPERM,
			Domain:   req.Domain,
			RuleID:   "fail_closed_no_policy",
			Subject:  "unbound_sandbox",
			Reason:   "sandbox has no compiled tracing policy bound (fail-closed)",
		}
		return e.finalizeDecision(ts, req, lineage, res, true)
	}

	var res DecisionResult
	var emitAudit bool

	switch req.Domain {
	case DomainSyscall:
		res, emitAudit = evaluateSyscall(cp, req.Syscall, req.Unindexed)
	case DomainProcess:
		res, emitAudit = evaluateProcess(cp, req.Process, req.Unindexed)
	case DomainNetwork:
		res, emitAudit = evaluateNetwork(cp, req.Network, req.Unindexed)
	default:
		res = DecisionResult{
			Decision:      DecisionDeny,
			Blocked:       true,
			Errno:         ErrnoEPERM,
			PolicyID:      cp.Spec.ID,
			PolicyVersion: cp.Spec.Version,
			Domain:        req.Domain,
			RuleID:        "fail_closed_unknown_domain",
			Reason:        fmt.Sprintf("unsupported enforcement domain %q", req.Domain),
		}
		emitAudit = true
	}

	if res.Decision == DecisionAllow {
		if req.BypassAllowAudit {
			emitAudit = false
		} else if cp.Spec.AuditAllowEvents {
			emitAudit = true
		}
	} else {
		// Every AUDIT_ALERT, DENY, and SIGKILL is always audited.
		emitAudit = true
	}

	return e.finalizeDecision(ts, req, lineage, res, emitAudit)
}

func (e *Engine) finalizeDecision(ts *tenantState, req EvaluationRequest, lineage []string, res DecisionResult, emitAudit bool) DecisionResult {
	switch res.Decision {
	case DecisionAllow:
		ts.allowed.Add(1)
	case DecisionAuditAlert:
		ts.auditAlerts.Add(1)
	case DecisionDeny:
		ts.denied.Add(1)
		e.totalDenied.Add(1)
	case DecisionSigkill:
		ts.sigkills.Add(1)
		e.totalSigkills.Add(1)
	}

	if emitAudit {
		ev, ok := ts.audit.Append(AuditEvent{
			TenantID:       req.TenantID,
			SandboxID:      strings.TrimSpace(req.SandboxID),
			PolicyID:       res.PolicyID,
			PolicyVersion:  res.PolicyVersion,
			Timestamp:      e.cfg.Now().UTC(),
			Domain:         res.Domain,
			ProcessLineage: lineage,
			RuleID:         res.RuleID,
			Action:         res.Decision,
			Errno:          res.Errno,
			Subject:        res.Subject,
			Reason:         res.Reason,
		})
		if ok {
			res.AuditRecorded = true
			res.AuditSeq = ev.Seq
			res.AuditHash = ev.Hash
		} else {
			res.AuditDropped = true
		}
	}
	return res
}

func decisionForAction(effect RuleEffect, action EnforcementMode) (Decision, int) {
	return decisionForActionWithErrno(effect, action, ErrnoEPERM)
}

func decisionForActionWithErrno(effect RuleEffect, action EnforcementMode, denyErrno int) (Decision, int) {
	if effect == EffectAllow {
		return DecisionAllow, 0
	}
	if denyErrno <= 0 {
		denyErrno = ErrnoEPERM
	}
	switch action {
	case ModeAuditOnly:
		return DecisionAuditAlert, 0
	case ModeEnforceDeny:
		return DecisionDeny, denyErrno
	case ModeEnforceSigkill:
		return DecisionSigkill, SignalSIGKILL
	default:
		return DecisionDeny, denyErrno
	}
}

func evaluateSyscall(cp *CompiledPolicy, ev *SyscallEvent, unindexed bool) (DecisionResult, bool) {
	base := DecisionResult{
		PolicyID:      cp.Spec.ID,
		PolicyVersion: cp.Spec.Version,
		Domain:        DomainSyscall,
	}
	if ev == nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_nil_syscall"
		base.Reason = "missing syscall event payload"
		return base, true
	}

	num, name, err := resolveSyscallIdentityForArch(cp.Spec.Arch, ev.SyscallName, ev.SyscallNumber)
	if err != nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_unknown_syscall"
		base.Subject = ev.SyscallName
		base.Reason = err.Error()
		return base, true
	}
	base.Subject = fmt.Sprintf("%s(%d)", name, num)

	var candidates []compiledSyscallRule
	if unindexed {
		candidates = cp.allSyscalls
	} else {
		candidates = cp.syscallsByNum[num]
	}

	for _, cr := range candidates {
		if cr.num != num {
			continue
		}
		if !matchSyscallArgs(cr.rule.ArgFilters, ev.Args) {
			continue
		}
		dec, errno := decisionForActionWithErrno(cr.rule.Effect, cr.action, cr.errno)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = cr.rule.ID
		base.Reason = cr.rule.Reason
		return base, cr.rule.AuditOnAllow || dec != DecisionAllow
	}

	if cp.Spec.DefaultSyscallAllow {
		base.Decision = DecisionAllow
		base.RuleID = "default_allow"
		base.Reason = "default syscall allow"
		return base, false
	}
	dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
	base.Decision = dec
	base.Blocked = dec.Blocked()
	base.Errno = errno
	base.RuleID = "default_deny_syscall"
	base.Reason = "syscall not in sandbox allowlist (fail-closed)"
	return base, true
}

func matchSyscallArgs(filters []SyscallArgFilter, args [6]uint64) bool {
	for _, f := range filters {
		if !f.Matches(args) {
			return false
		}
	}
	return true
}

func procRuleWins(cand, best compiledProcessRule, hasBest bool) bool {
	if !hasBest {
		return true
	}
	if cand.rule.Priority != best.rule.Priority {
		return cand.rule.Priority > best.rule.Priority
	}
	si, sj := modeSeverity(cand.action), modeSeverity(best.action)
	if si != sj {
		return si > sj
	}
	return cand.rule.ID < best.rule.ID
}

func matchProcessRule(cr compiledProcessRule, canonPath, joinedArgs string, ev *ProcessEvent) bool {
	if cr.exact != "" && canonPath != cr.exact {
		return false
	}
	if cr.prefix != "" && !PathMatchesPrefix(canonPath, cr.prefix) {
		return false
	}
	if cr.rule.ArgContains != "" {
		found := false
		for _, a := range ev.Args {
			if strings.Contains(a, cr.rule.ArgContains) {
				found = true
				break
			}
		}
		if !found && !strings.Contains(joinedArgs, cr.rule.ArgContains) {
			return false
		}
	}
	if cr.argRe != nil && !cr.argRe.MatchString(joinedArgs) {
		return false
	}
	if cr.rule.RequiredParent != "" {
		parent := ev.ParentBinary
		if parent == "" && len(ev.ProcessLineage) > 0 {
			parent = ev.ProcessLineage[len(ev.ProcessLineage)-1]
		}
		if parent != cr.rule.RequiredParent && path.Base(parent) != cr.rule.RequiredParent {
			return false
		}
	}
	if cr.rule.ForbiddenAncestor != "" {
		matched := false
		for _, anc := range ev.ProcessLineage {
			if anc == cr.rule.ForbiddenAncestor || path.Base(anc) == cr.rule.ForbiddenAncestor {
				matched = true
				break
			}
		}
		if !matched && ev.ParentBinary != "" {
			if ev.ParentBinary == cr.rule.ForbiddenAncestor || path.Base(ev.ParentBinary) == cr.rule.ForbiddenAncestor {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func evaluateProcess(cp *CompiledPolicy, ev *ProcessEvent, unindexed bool) (DecisionResult, bool) {
	base := DecisionResult{
		PolicyID:      cp.Spec.ID,
		PolicyVersion: cp.Spec.Version,
		Domain:        DomainProcess,
	}
	if ev == nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_nil_process"
		base.Reason = "missing process event payload"
		return base, true
	}

	canonBin, err := NormalizeCanonicalPath(ev.BinaryPath)
	if err != nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_invalid_binary_path"
		base.Subject = ev.BinaryPath
		base.Reason = fmt.Sprintf("invalid binary path: %v", err)
		return base, true
	}

	effectivePath := canonBin
	var symlinkCheckPaths []string
	symlinkCheckPaths = append(symlinkCheckPaths, canonBin)
	if strings.TrimSpace(ev.ResolvedPath) != "" {
		canonResolved, err := NormalizeCanonicalPath(ev.ResolvedPath)
		if err != nil {
			dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
			base.Decision = dec
			base.Blocked = dec.Blocked()
			base.Errno = errno
			base.RuleID = "fail_closed_invalid_resolved_path"
			base.Subject = ev.ResolvedPath
			base.Reason = fmt.Sprintf("invalid resolved symlink target: %v", err)
			return base, true
		}
		effectivePath = canonResolved
		if canonResolved != canonBin {
			symlinkCheckPaths = append(symlinkCheckPaths, canonResolved)
		}
	}
	base.Subject = effectivePath

	// Privilege escalation & capability-change refusal check (runs before allowlists).
	if cp.Spec.DenyPrivEsc {
		if reason := detectPrivEsc(ev); reason != "" {
			act, _ := resolveEffectiveAction(cp.Spec.Mode, ModeEnforceSigkill, EffectDeny)
			dec, errno := decisionForAction(EffectDeny, act)
			base.Decision = dec
			base.Blocked = dec.Blocked()
			base.Errno = errno
			base.RuleID = "priv_esc_refused"
			base.Reason = reason
			return base, true
		}
	}

	joinedArgs := strings.Join(ev.Args, " ")

	var best compiledProcessRule
	hasBest := false

	scan := func(rules []compiledProcessRule, targetPath string, denyOnly bool) {
		for _, cr := range rules {
			if denyOnly && cr.rule.Effect != EffectDeny {
				continue
			}
			// Early exit when rules are priority-sorted and cannot beat current best.
			if hasBest && cr.rule.Priority < best.rule.Priority {
				break
			}
			if matchProcessRule(cr, targetPath, joinedArgs, ev) {
				if procRuleWins(cr, best, hasBest) {
					best = cr
					hasBest = true
				}
			}
		}
	}

	if unindexed {
		// Unindexed linear scan over all process rules (Negative Regime 1 baseline).
		for _, p := range symlinkCheckPaths {
			denyOnly := p != effectivePath
			for _, cr := range cp.allProcs {
				if denyOnly && cr.rule.Effect != EffectDeny {
					continue
				}
				if matchProcessRule(cr, p, joinedArgs, ev) {
					if procRuleWins(cr, best, hasBest) {
						best = cr
						hasBest = true
					}
				}
			}
		}
	} else {
		// Indexed lookup: O(1) exact path map + root-bucketed prefix map + general rules.
		for _, p := range symlinkCheckPaths {
			denyOnly := p != effectivePath
			scan(cp.procExact[p], p, denyOnly)
			root := pathRootSegment(p)
			scan(cp.procPrefixByRoot[root], p, denyOnly)
			if root != "/" {
				scan(cp.procPrefixByRoot["/"], p, denyOnly)
			}
			scan(cp.procGeneral, p, denyOnly)
		}
	}

	if hasBest {
		dec, errno := decisionForAction(best.rule.Effect, best.action)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = best.rule.ID
		base.Reason = best.rule.Reason
		return base, dec != DecisionAllow
	}

	if cp.Spec.DefaultProcessAllow {
		base.Decision = DecisionAllow
		base.RuleID = "default_allow"
		base.Reason = "default process allow"
		return base, false
	}
	dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
	base.Decision = dec
	base.Blocked = dec.Blocked()
	base.Errno = errno
	base.RuleID = "default_deny_process"
	base.Reason = "executable path not in sandbox allowlist (fail-closed)"
	return base, true
}

func detectPrivEsc(ev *ProcessEvent) string {
	if ev.Setuid {
		return "setuid/setgid binary execution refused"
	}
	if ev.NewUID != nil && *ev.NewUID != ev.UID {
		return fmt.Sprintf("UID privilege transition %d -> %d refused", ev.UID, *ev.NewUID)
	}
	if ev.NewGID != nil && *ev.NewGID != ev.GID {
		return fmt.Sprintf("GID privilege transition %d -> %d refused", ev.GID, *ev.NewGID)
	}
	if ev.CapabilitiesAdded != 0 {
		return fmt.Sprintf("Linux capability acquisition (0x%x) refused", ev.CapabilitiesAdded)
	}
	if (ev.EffectiveCaps & DangerousCapabilitiesMask) != 0 {
		return fmt.Sprintf("dangerous Linux effective capabilities (0x%x) refused", ev.EffectiveCaps&DangerousCapabilitiesMask)
	}
	if ev.NoNewPrivs != nil && !*ev.NoNewPrivs {
		return "PR_SET_NO_NEW_PRIVS disabled (privilege escalation guard)"
	}
	return ""
}

func netRuleWins(cand, best compiledNetworkRule, hasBest bool) bool {
	if !hasBest {
		return true
	}
	if cand.rule.Priority != best.rule.Priority {
		return cand.rule.Priority > best.rule.Priority
	}
	si, sj := modeSeverity(cand.action), modeSeverity(best.action)
	if si != sj {
		return si > sj
	}
	return cand.rule.ID < best.rule.ID
}

func matchNetworkRuleMeta(cr compiledNetworkRule, dir NetworkDirection, proto string, port uint16) bool {
	if cr.direction != NetDirAny && cr.direction != dir {
		return false
	}
	if cr.protocol != "" && cr.protocol != proto {
		return false
	}
	if cr.matchPortZero {
		return port == 0
	}
	if cr.portStart > 0 && (port < cr.portStart || port > cr.portEnd) {
		return false
	}
	return true
}

func evaluateNetwork(cp *CompiledPolicy, ev *NetworkEvent, unindexed bool) (DecisionResult, bool) {
	base := DecisionResult{
		PolicyID:      cp.Spec.ID,
		PolicyVersion: cp.Spec.Version,
		Domain:        DomainNetwork,
	}
	if ev == nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_nil_network"
		base.Reason = "missing network event payload"
		return base, true
	}

	dir := ev.Direction
	if dir != NetDirEgress && dir != NetDirBind {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_invalid_direction"
		base.Reason = fmt.Sprintf("invalid network direction %q", dir)
		return base, true
	}

	addr, err := NormalizeIP(ev.DestIP)
	if err != nil {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_invalid_ip"
		base.Subject = ev.DestIP
		base.Reason = err.Error()
		return base, true
	}

	proto := strings.ToLower(strings.TrimSpace(ev.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = "fail_closed_invalid_protocol"
		base.Subject = fmt.Sprintf("%s:%d", addr.String(), ev.DestPort)
		base.Reason = fmt.Sprintf("unsupported protocol %q", proto)
		return base, true
	}

	if addr.Is6() {
		base.Subject = fmt.Sprintf("%s %s [%s]:%d", dir, proto, addr.String(), ev.DestPort)
	} else {
		base.Subject = fmt.Sprintf("%s %s %s:%d", dir, proto, addr.String(), ev.DestPort)
	}

	var best compiledNetworkRule
	hasBest := false

	checkList := func(rules []compiledNetworkRule) {
		for _, cr := range rules {
			if hasBest && cr.rule.Priority < best.rule.Priority {
				break
			}
			if matchNetworkRuleMeta(cr, dir, proto, ev.DestPort) {
				if netRuleWins(cr, best, hasBest) {
					best = cr
					hasBest = true
				}
			}
		}
	}

	if unindexed {
		for _, cr := range cp.allNets {
			if !cr.prefix.Contains(addr) {
				continue
			}
			if matchNetworkRuleMeta(cr, dir, proto, ev.DestPort) {
				if netRuleWins(cr, best, hasBest) {
					best = cr
					hasBest = true
				}
			}
		}
	} else if addr.Is4() {
		// Bitmask-indexed CIDR lookup: inspects only populated prefix lengths (O(k), typically 3-5 map lookups).
		mask := cp.netV4Mask
		for mask != 0 {
			b := bits.TrailingZeros64(mask)
			mask &= mask - 1
			pfx, _ := addr.Prefix(b)
			if rules, ok := cp.netV4ByBit[b][pfx]; ok {
				checkList(rules)
			}
		}
	} else {
		maskL := cp.netV6MaskL
		for maskL != 0 {
			b := bits.TrailingZeros64(maskL)
			maskL &= maskL - 1
			pfx, _ := addr.Prefix(b)
			if rules, ok := cp.netV6ByBit[b][pfx]; ok {
				checkList(rules)
			}
		}
		maskH := cp.netV6MaskH
		for maskH != 0 {
			b := 64 + bits.TrailingZeros64(maskH)
			maskH &= maskH - 1
			pfx, _ := addr.Prefix(b)
			if rules, ok := cp.netV6ByBit[b][pfx]; ok {
				checkList(rules)
			}
		}
		if cp.netV6Has128 {
			pfx := netip.PrefixFrom(addr, 128)
			if rules, ok := cp.netV6ByBit[128][pfx]; ok {
				checkList(rules)
			}
		}
	}

	if hasBest {
		dec, errno := decisionForAction(best.rule.Effect, best.action)
		base.Decision = dec
		base.Blocked = dec.Blocked()
		base.Errno = errno
		base.RuleID = best.rule.ID
		base.Reason = best.rule.Reason
		return base, dec != DecisionAllow
	}

	if cp.Spec.DefaultNetworkAllow {
		base.Decision = DecisionAllow
		base.RuleID = "default_allow"
		base.Reason = "default network allow"
		return base, false
	}
	dec, errno := decisionForAction(EffectDeny, cp.Spec.Mode)
	base.Decision = dec
	base.Blocked = dec.Blocked()
	base.Errno = errno
	base.RuleID = "default_deny_network"
	base.Reason = "network target not in sandbox allowlist (fail-closed)"
	return base, true
}
