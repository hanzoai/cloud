// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxPolicyRules bounds total rules across all three domains per policy.
	MaxPolicyRules = 4096
	// MaxArgFiltersPerRule bounds argument predicates attached to a single syscall rule.
	MaxArgFiltersPerRule = 6
	// MaxSyscallNumber bounds valid Linux syscall numbers for validation and BPF emission.
	MaxSyscallNumber = 1024
	// MaxLineageDepth bounds process lineage slices to prevent unbounded memory growth.
	MaxLineageDepth = 64
	// MaxFieldLen bounds identifier and pattern lengths.
	MaxFieldLen = 512

	// ErrnoEPERM is POSIX EPERM (Operation not permitted) returned under EnforceDeny.
	ErrnoEPERM = 1
	// ErrnoENOSYS is POSIX ENOSYS (Function not implemented), used for safe runtime
	// fallback (e.g., clone3 -> legacy clone) when pointer arguments cannot be inspected.
	ErrnoENOSYS = 38
	// SignalSIGKILL is POSIX SIGKILL (9) emitted under EnforceSigkill.
	SignalSIGKILL = 9
)

// Sentinel errors returned by policy validation, compilation, and engine operations.
var (
	ErrInvalidPolicy       = errors.New("sandboxpolicy: invalid policy")
	ErrPolicyConflict      = errors.New("sandboxpolicy: conflicting rules at identical priority and match key")
	ErrPolicyNotFound      = errors.New("sandboxpolicy: policy not found")
	ErrSandboxNotFound     = errors.New("sandboxpolicy: sandbox not found")
	ErrTenantLimitExceeded = errors.New("sandboxpolicy: tenant capacity limit exceeded")
	ErrRuleLimitExceeded   = errors.New("sandboxpolicy: policy rule limit exceeded")
)

// EnforcementMode determines how policy violations are handled.
type EnforcementMode string

const (
	// ModeAuditOnly logs violations as AUDIT_ALERT without blocking execution.
	ModeAuditOnly EnforcementMode = "audit_only"
	// ModeEnforceDeny synchronously blocks violating operations with EPERM (Override).
	ModeEnforceDeny EnforcementMode = "enforce_deny"
	// ModeEnforceSigkill synchronously terminates the violating process (Tetragon Sigkill).
	ModeEnforceSigkill EnforcementMode = "enforce_sigkill"
)

// ParseEnforcementMode validates and normalizes an EnforcementMode string.
func ParseEnforcementMode(raw string) (EnforcementMode, error) {
	switch EnforcementMode(strings.ToLower(strings.TrimSpace(raw))) {
	case ModeAuditOnly:
		return ModeAuditOnly, nil
	case ModeEnforceDeny:
		return ModeEnforceDeny, nil
	case ModeEnforceSigkill:
		return ModeEnforceSigkill, nil
	default:
		return "", fmt.Errorf("%w: unsupported enforcement mode %q", ErrInvalidPolicy, raw)
	}
}

func modeSeverity(m EnforcementMode) int {
	switch m {
	case ModeEnforceSigkill:
		return 3
	case ModeEnforceDeny:
		return 2
	case ModeAuditOnly:
		return 1
	default:
		return 0
	}
}

// Decision represents the concrete runtime verdict emitted for an evaluated event.
type Decision string

const (
	DecisionAllow      Decision = "ALLOW"
	DecisionAuditAlert Decision = "AUDIT_ALERT"
	DecisionDeny       Decision = "DENY"
	DecisionSigkill    Decision = "SIGKILL"
)

// Blocked reports whether the decision prevents the operation from executing.
func (d Decision) Blocked() bool {
	return d == DecisionDeny || d == DecisionSigkill
}

// Domain identifies the runtime enforcement domain.
type Domain string

const (
	DomainSyscall Domain = "syscall"
	DomainProcess Domain = "process"
	DomainNetwork Domain = "network"
)

// RuleEffect determines whether a matching rule permits or forbids an operation.
type RuleEffect string

const (
	EffectAllow RuleEffect = "allow"
	EffectDeny  RuleEffect = "deny"
)

// ArgOp specifies a comparison operator for a syscall argument filter.
type ArgOp string

const (
	ArgOpEqual      ArgOp = "eq"
	ArgOpNotEqual   ArgOp = "ne"
	ArgOpEqual32    ArgOp = "eq32" // compares low 32-bit C int width
	ArgOpNotEqual32 ArgOp = "ne32" // compares low 32-bit C int width
	ArgOpMaskAny    ArgOp = "mask_any"
	ArgOpMaskAll    ArgOp = "mask_all"
	ArgOpGreaterEq  ArgOp = "ge"
	ArgOpLessEq     ArgOp = "le"
)

// SyscallArgFilter matches one of the 6 syscall argument registers (index 0..5).
type SyscallArgFilter struct {
	// Index selects the syscall argument register (0..5).
	Index uint8 `json:"index"`
	// Op selects the comparison operator ("eq", "ne", "eq32", "ne32", "mask_any", "mask_all", "ge", "le").
	Op ArgOp `json:"op"`
	// Value is the immediate operand compared against the argument register.
	Value uint64 `json:"value"`
}

// Matches evaluates the argument filter against a 6-register syscall argument vector.
func (f SyscallArgFilter) Matches(args [6]uint64) bool {
	if f.Index > 5 {
		return false
	}
	v := args[f.Index]
	switch f.Op {
	case ArgOpEqual:
		return v == f.Value
	case ArgOpNotEqual:
		return v != f.Value
	case ArgOpEqual32:
		return uint32(v) == uint32(f.Value)
	case ArgOpNotEqual32:
		return uint32(v) != uint32(f.Value)
	case ArgOpMaskAny:
		return (v & f.Value) != 0
	case ArgOpMaskAll:
		return (v & f.Value) == f.Value
	case ArgOpGreaterEq:
		return v >= f.Value
	case ArgOpLessEq:
		return v <= f.Value
	default:
		return false
	}
}

// SyscallRule defines an allow or deny rule over Linux syscalls and optional arguments.
type SyscallRule struct {
	// ID uniquely identifies the rule within the policy.
	ID string `json:"id"`
	// Priority determines deterministic evaluation order (higher runs first).
	Priority int `json:"priority"`
	// Effect is "allow" or "deny".
	Effect RuleEffect `json:"effect"`
	// Action optionally overrides the policy mode when Effect is "deny".
	Action EnforcementMode `json:"action,omitempty"`
	// Errno optionally overrides the errno returned when resolved Action is "enforce_deny"
	// (defaults to EPERM=1; e.g., ENOSYS=38 for safe clone3 fallback).
	Errno int `json:"errno,omitempty"`
	// SyscallName is the canonical Linux syscall name for the policy's Arch (e.g., "ptrace", "openat").
	SyscallName string `json:"syscallName,omitempty"`
	// SyscallNumber optionally specifies the numeric syscall ID (-1 or omitted when using SyscallName).
	SyscallNumber *int `json:"syscallNumber,omitempty"`
	// ArgFilters optionally restricts matches by syscall argument register predicates.
	ArgFilters []SyscallArgFilter `json:"argFilters,omitempty"`
	// AuditOnAllow controls whether allow hits emit userspace audit events (false = in-kernel fast-path).
	AuditOnAllow bool `json:"auditOnAllow,omitempty"`
	// Reason explains the security rationale recorded in audit events.
	Reason string `json:"reason,omitempty"`
}

// ProcessRule defines an allow or deny rule over process executions.
type ProcessRule struct {
	// ID uniquely identifies the rule within the policy.
	ID string `json:"id"`
	// Priority determines deterministic evaluation order (higher runs first).
	Priority int `json:"priority"`
	// Effect is "allow" or "deny".
	Effect RuleEffect `json:"effect"`
	// Action optionally overrides the policy mode when Effect is "deny".
	Action EnforcementMode `json:"action,omitempty"`
	// BinaryExact matches an exact canonical normalized binary path (e.g., "/usr/bin/sudo").
	BinaryExact string `json:"binaryExact,omitempty"`
	// BinaryPrefix matches a canonical path directory prefix (e.g., "/usr/bin" matches "/usr/bin/go", not "/usr/bin2/x").
	BinaryPrefix string `json:"binaryPrefix,omitempty"`
	// ArgContains optionally requires at least one argv element to contain this substring.
	ArgContains string `json:"argContains,omitempty"`
	// ArgRegex optionally matches the joined argv string using RE2 regular expression syntax.
	ArgRegex string `json:"argRegex,omitempty"`
	// RequiredParent optionally requires the immediate parent binary basename or path to match.
	RequiredParent string `json:"requiredParent,omitempty"`
	// ForbiddenAncestor optionally triggers when any ancestor in process lineage matches.
	ForbiddenAncestor string `json:"forbiddenAncestor,omitempty"`
	// Reason explains the security rationale recorded in audit events.
	Reason string `json:"reason,omitempty"`
}

// NetworkDirection identifies egress or bind socket operations.
type NetworkDirection string

const (
	NetDirAny    NetworkDirection = "any"
	NetDirEgress NetworkDirection = "egress"
	NetDirBind   NetworkDirection = "bind"
)

// NetworkRule defines an allow or deny rule over network egress or bind events.
type NetworkRule struct {
	// ID uniquely identifies the rule within the policy.
	ID string `json:"id"`
	// Priority determines deterministic evaluation order (higher runs first).
	Priority int `json:"priority"`
	// Effect is "allow" or "deny".
	Effect RuleEffect `json:"effect"`
	// Action optionally overrides the policy mode when Effect is "deny".
	Action EnforcementMode `json:"action,omitempty"`
	// Direction restricts matching to "egress", "bind", or "any" (default "any").
	Direction NetworkDirection `json:"direction,omitempty"`
	// Protocol restricts matching to "tcp", "udp", or "" / "any".
	Protocol string `json:"protocol,omitempty"`
	// CIDR specifies an IPv4 or IPv6 prefix (e.g., "169.254.169.254/32", "10.0.0.0/8", "0.0.0.0/0").
	CIDR string `json:"cidr"`
	// PortStart optionally restricts the destination/bind port lower bound (inclusive, 1..65535).
	PortStart uint16 `json:"portStart,omitempty"`
	// PortEnd optionally restricts the destination/bind port upper bound (inclusive, defaults to PortStart if PortStart > 0).
	PortEnd uint16 `json:"portEnd,omitempty"`
	// MatchPortZero restricts matching strictly to port 0 (e.g., pre-bind ephemeral
	// loopback requests such as 127.0.0.1:0 or [::1]:0). Cannot be combined with PortStart/PortEnd.
	MatchPortZero bool `json:"matchPortZero,omitempty"`
	// Reason explains the security rationale recorded in audit events.
	Reason string `json:"reason,omitempty"`
}

// TracingPolicy is a declarative, Tetragon-inspired runtime enforcement policy
// governing syscall, process execution, and network domains inside a Visor sandbox.
type TracingPolicy struct {
	// ID uniquely identifies the policy within a tenant.
	ID string `json:"id"`
	// Name is a human-readable policy name.
	Name string `json:"name"`
	// Version is a strictly positive monotonic policy version number.
	Version uint64 `json:"version"`
	// Mode sets the default enforcement mode ("audit_only", "enforce_deny", "enforce_sigkill").
	Mode EnforcementMode `json:"mode"`
	// Arch selects the target BPF architecture ("amd64" or "arm64"; defaults to "amd64").
	Arch string `json:"arch,omitempty"`
	// DefaultSyscallAllow determines the default verdict when no syscall rule matches (false = fail-closed).
	DefaultSyscallAllow bool `json:"defaultSyscallAllow"`
	// DefaultProcessAllow determines the default verdict when no process rule matches (false = fail-closed).
	DefaultProcessAllow bool `json:"defaultProcessAllow"`
	// DefaultNetworkAllow determines the default verdict when no network rule matches (false = fail-closed).
	DefaultNetworkAllow bool `json:"defaultNetworkAllow"`
	// DenyPrivEsc refuses UID/GID transitions, setuid executions, and unauthorized Linux capabilities.
	DenyPrivEsc bool `json:"denyPrivEsc"`
	// AuditAllowEvents controls whether benign ALLOW decisions are emitted to the audit ring.
	AuditAllowEvents bool `json:"auditAllowEvents,omitempty"`
	// SyscallRules lists declarative syscall rules.
	SyscallRules []SyscallRule `json:"syscallRules,omitempty"`
	// ProcessRules lists declarative process execution rules.
	ProcessRules []ProcessRule `json:"processRules,omitempty"`
	// NetworkRules lists declarative network egress/bind rules.
	NetworkRules []NetworkRule `json:"networkRules,omitempty"`
}

// CompiledPolicy is an immutable, pre-indexed runtime policy with generated
// seccomp-BPF filter bytecode ready for kernel attachment or VM evaluation.
type CompiledPolicy struct {
	Spec      TracingPolicy
	Digest    string
	BPF       BPFProgram
	BPFDigest string

	// O(1) syscall index keyed by Linux syscall number.
	syscallsByNum map[uint32][]compiledSyscallRule

	// Process indexes: O(1) exact canonical path map + root-segment bucketed prefix table + general rules.
	procExact        map[string][]compiledProcessRule
	procPrefixByRoot map[string][]compiledProcessRule
	procPrefix       []compiledProcessRule
	procGeneral      []compiledProcessRule

	// Network indexes: exact IP + bitmask-indexed CIDR tables for IPv4 (0..32) and IPv6 (0..128).
	netV4Mask   uint64
	netV4ByBit  [33]map[netip.Prefix][]compiledNetworkRule
	netV6MaskL  uint64 // bit lengths 0..63
	netV6MaskH  uint64 // bit lengths 64..127
	netV6Has128 bool
	netV6ByBit  [129]map[netip.Prefix][]compiledNetworkRule

	// Flat slices preserved for deterministic unindexed baseline comparisons.
	allSyscalls []compiledSyscallRule
	allProcs    []compiledProcessRule
	allNets     []compiledNetworkRule
}

type compiledSyscallRule struct {
	rule   SyscallRule
	num    uint32
	name   string
	action EnforcementMode
	errno  int
}

type compiledProcessRule struct {
	rule   ProcessRule
	exact  string
	prefix string
	argRe  *regexp.Regexp
	action EnforcementMode
}

type compiledNetworkRule struct {
	rule          NetworkRule
	prefix        netip.Prefix
	direction     NetworkDirection
	protocol      string
	portStart     uint16
	portEnd       uint16
	matchPortZero bool
	action        EnforcementMode
}

// NormalizeCanonicalPath validates and cleans an absolute POSIX path prior to
// prefix or exact matching. It rejects empty paths, NUL bytes, and relative paths.
func NormalizeCanonicalPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("path contains NUL byte")
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("path %q must be absolute", raw)
	}
	cleaned := path.Clean(raw)
	if !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("cleaned path %q is not absolute", cleaned)
	}
	return cleaned, nil
}

// PathMatchesPrefix reports whether canonicalPath equals prefix or resides
// strictly beneath prefix as a directory boundary (so "/usr/bin" matches
// "/usr/bin/go" and "/usr/bin", but NEVER "/usr/bin2/go").
func PathMatchesPrefix(canonicalPath, canonicalPrefix string) bool {
	if canonicalPrefix == "/" {
		return strings.HasPrefix(canonicalPath, "/")
	}
	if canonicalPath == canonicalPrefix {
		return true
	}
	return strings.HasPrefix(canonicalPath, canonicalPrefix+"/")
}

func pathRootSegment(canonicalPath string) string {
	if canonicalPath == "" || canonicalPath == "/" {
		return "/"
	}
	rest := strings.TrimPrefix(canonicalPath, "/")
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return "/" + rest[:idx]
	}
	return "/" + rest
}

// NormalizeIP parses an IP string and unmaps IPv4-mapped IPv6 addresses
// (e.g., "::ffff:169.254.169.254" -> "169.254.169.254") so IPv4 CIDR denylists
// cannot be bypassed via IPv6 dual-stack notation.
func NormalizeIP(raw string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IP %q: %w", raw, err)
	}
	return addr.Unmap(), nil
}

// NormalizeCIDR parses and canonicalizes a CIDR prefix, unmapping IPv4-mapped
// IPv6 prefixes (e.g., "::ffff:169.254.169.254/128" -> "169.254.169.254/32").
func NormalizeCIDR(raw string) (netip.Prefix, error) {
	pfx, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR %q: %w", raw, err)
	}
	addr := pfx.Addr()
	if addr.Is4In6() && pfx.Bits() >= 96 {
		unmapped := addr.Unmap()
		return netip.PrefixFrom(unmapped, pfx.Bits()-96).Masked(), nil
	}
	return pfx.Masked(), nil
}

func resolveEffectiveAction(policyMode, ruleAction EnforcementMode, effect RuleEffect) (EnforcementMode, error) {
	if effect == EffectAllow {
		return "", nil
	}
	// Under policy-wide AuditOnly, enforcement never escalates to blocking unless
	// explicitly running in an enforcement mode — matching Tetragon audit mode semantics.
	if policyMode == ModeAuditOnly {
		return ModeAuditOnly, nil
	}
	if ruleAction == "" {
		return policyMode, nil
	}
	parsed, err := ParseEnforcementMode(string(ruleAction))
	if err != nil {
		return "", err
	}
	// Under policy-wide EnforceDeny, cap rule action at EnforceDeny so an operator
	// choosing non-lethal EPERM mode is never unexpectedly killed by a Sigkill rule.
	if policyMode == ModeEnforceDeny && parsed == ModeEnforceSigkill {
		return ModeEnforceDeny, nil
	}
	return parsed, nil
}

// CompilePolicy validates, detects conflicts, indexes all three domains, and
// compiles kernel-ready seccomp-BPF filter bytecode. Any validation error or
// same-priority rule conflict fails closed with an error.
func CompilePolicy(spec TracingPolicy) (*CompiledPolicy, error) {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.ID == "" || len(spec.ID) > MaxFieldLen {
		return nil, fmt.Errorf("%w: policy id must be 1..%d chars", ErrInvalidPolicy, MaxFieldLen)
	}
	if spec.Name == "" {
		spec.Name = spec.ID
	}
	if len(spec.Name) > MaxFieldLen {
		return nil, fmt.Errorf("%w: policy name exceeds %d chars", ErrInvalidPolicy, MaxFieldLen)
	}
	if spec.Version == 0 {
		spec.Version = 1
	}
	mode, err := ParseEnforcementMode(string(spec.Mode))
	if err != nil {
		return nil, err
	}
	spec.Mode = mode
	if spec.Arch == "" {
		spec.Arch = "amd64"
	}
	if spec.Arch != "amd64" && spec.Arch != "arm64" {
		return nil, fmt.Errorf("%w: unsupported arch %q (want amd64 or arm64)", ErrInvalidPolicy, spec.Arch)
	}

	totalRules := len(spec.SyscallRules) + len(spec.ProcessRules) + len(spec.NetworkRules)
	if totalRules > MaxPolicyRules {
		return nil, fmt.Errorf("%w: %d rules exceeds max %d", ErrRuleLimitExceeded, totalRules, MaxPolicyRules)
	}

	cp := &CompiledPolicy{
		Spec:             spec,
		syscallsByNum:    make(map[uint32][]compiledSyscallRule, len(spec.SyscallRules)),
		procExact:        make(map[string][]compiledProcessRule),
		procPrefixByRoot: make(map[string][]compiledProcessRule),
	}

	seenRuleIDs := make(map[string]struct{}, totalRules)
	checkID := func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > MaxFieldLen {
			return fmt.Errorf("%w: rule id must be 1..%d chars", ErrInvalidPolicy, MaxFieldLen)
		}
		if _, dup := seenRuleIDs[id]; dup {
			return fmt.Errorf("%w: duplicate rule id %q", ErrInvalidPolicy, id)
		}
		seenRuleIDs[id] = struct{}{}
		return nil
	}

	// 1. Compile and conflict-check Syscall rules.
	type sysConflictKey struct {
		num      uint32
		priority int
		argsKey  string
	}
	sysConflicts := make(map[sysConflictKey]RuleEffect, len(spec.SyscallRules))

	for i, r := range spec.SyscallRules {
		r.ID = strings.TrimSpace(r.ID)
		if err := checkID(r.ID); err != nil {
			return nil, fmt.Errorf("syscall rule[%d]: %w", i, err)
		}
		if r.Effect != EffectAllow && r.Effect != EffectDeny {
			return nil, fmt.Errorf("%w: syscall rule %q invalid effect %q", ErrInvalidPolicy, r.ID, r.Effect)
		}
		if len(r.ArgFilters) > MaxArgFiltersPerRule {
			return nil, fmt.Errorf("%w: syscall rule %q has %d arg filters (max %d)", ErrInvalidPolicy, r.ID, len(r.ArgFilters), MaxArgFiltersPerRule)
		}
		num, name, err := resolveSyscallIdentityForArch(spec.Arch, r.SyscallName, r.SyscallNumber)
		if err != nil {
			return nil, fmt.Errorf("syscall rule %q: %w", r.ID, err)
		}
		if r.Errno < 0 || r.Errno > 4095 {
			return nil, fmt.Errorf("%w: syscall rule %q errno %d out of range [0..4095]", ErrInvalidPolicy, r.ID, r.Errno)
		}
		if r.Errno > 0 && r.Effect != EffectDeny {
			return nil, fmt.Errorf("%w: syscall rule %q cannot set errno on allow rule", ErrInvalidPolicy, r.ID)
		}
		var argsKeyB strings.Builder
		for _, af := range r.ArgFilters {
			if af.Index > 5 {
				return nil, fmt.Errorf("%w: syscall rule %q arg index %d out of range [0..5]", ErrInvalidPolicy, r.ID, af.Index)
			}
			switch af.Op {
			case ArgOpEqual, ArgOpNotEqual, ArgOpEqual32, ArgOpNotEqual32, ArgOpMaskAny, ArgOpMaskAll, ArgOpGreaterEq, ArgOpLessEq:
			default:
				return nil, fmt.Errorf("%w: syscall rule %q unsupported arg op %q", ErrInvalidPolicy, r.ID, af.Op)
			}
			fmt.Fprintf(&argsKeyB, "%d:%s:%d;", af.Index, af.Op, af.Value)
		}
		ck := sysConflictKey{num: num, priority: r.Priority, argsKey: argsKeyB.String()}
		if prevEff, exists := sysConflicts[ck]; exists && prevEff != r.Effect {
			return nil, fmt.Errorf("%w: syscall %s (%d) priority %d", ErrPolicyConflict, name, num, r.Priority)
		}
		sysConflicts[ck] = r.Effect

		act, err := resolveEffectiveAction(spec.Mode, r.Action, r.Effect)
		if err != nil {
			return nil, fmt.Errorf("syscall rule %q: %w", r.ID, err)
		}
		effErrno := 0
		if r.Effect == EffectDeny && act == ModeEnforceDeny {
			if r.Errno > 0 {
				effErrno = r.Errno
			} else {
				effErrno = ErrnoEPERM
			}
		}
		cr := compiledSyscallRule{rule: r, num: num, name: name, action: act, errno: effErrno}
		cp.syscallsByNum[num] = append(cp.syscallsByNum[num], cr)
		cp.allSyscalls = append(cp.allSyscalls, cr)
	}
	for num := range cp.syscallsByNum {
		sortSyscallRules(cp.syscallsByNum[num])
	}
	sortSyscallRules(cp.allSyscalls)

	// 2. Compile and conflict-check Process rules.
	type procConflictKey struct {
		exact    string
		prefix   string
		argSub   string
		argRe    string
		parent   string
		ancestor string
		priority int
	}
	procConflicts := make(map[procConflictKey]RuleEffect, len(spec.ProcessRules))

	for i, r := range spec.ProcessRules {
		r.ID = strings.TrimSpace(r.ID)
		if err := checkID(r.ID); err != nil {
			return nil, fmt.Errorf("process rule[%d]: %w", i, err)
		}
		if r.Effect != EffectAllow && r.Effect != EffectDeny {
			return nil, fmt.Errorf("%w: process rule %q invalid effect %q", ErrInvalidPolicy, r.ID, r.Effect)
		}
		var exact, pfx string
		if r.BinaryExact != "" {
			exact, err = NormalizeCanonicalPath(r.BinaryExact)
			if err != nil {
				return nil, fmt.Errorf("%w: process rule %q binaryExact: %v", ErrInvalidPolicy, r.ID, err)
			}
		}
		if r.BinaryPrefix != "" {
			pfx, err = NormalizeCanonicalPath(r.BinaryPrefix)
			if err != nil {
				return nil, fmt.Errorf("%w: process rule %q binaryPrefix: %v", ErrInvalidPolicy, r.ID, err)
			}
		}
		if exact == "" && pfx == "" && r.ArgContains == "" && r.ArgRegex == "" && r.RequiredParent == "" && r.ForbiddenAncestor == "" {
			return nil, fmt.Errorf("%w: process rule %q has no match criteria", ErrInvalidPolicy, r.ID)
		}
		var re *regexp.Regexp
		if r.ArgRegex != "" {
			if len(r.ArgRegex) > MaxFieldLen {
				return nil, fmt.Errorf("%w: process rule %q argRegex too long", ErrInvalidPolicy, r.ID)
			}
			re, err = regexp.Compile(r.ArgRegex)
			if err != nil {
				return nil, fmt.Errorf("%w: process rule %q invalid argRegex: %v", ErrInvalidPolicy, r.ID, err)
			}
		}
		ck := procConflictKey{
			exact: exact, prefix: pfx, argSub: r.ArgContains, argRe: r.ArgRegex,
			parent: r.RequiredParent, ancestor: r.ForbiddenAncestor, priority: r.Priority,
		}
		if prevEff, exists := procConflicts[ck]; exists && prevEff != r.Effect {
			return nil, fmt.Errorf("%w: process rule %q at priority %d", ErrPolicyConflict, r.ID, r.Priority)
		}
		procConflicts[ck] = r.Effect

		act, err := resolveEffectiveAction(spec.Mode, r.Action, r.Effect)
		if err != nil {
			return nil, fmt.Errorf("process rule %q: %w", r.ID, err)
		}
		cr := compiledProcessRule{
			rule:   r,
			exact:  exact,
			prefix: pfx,
			argRe:  re,
			action: act,
		}
		cp.allProcs = append(cp.allProcs, cr)
		if exact != "" {
			cp.procExact[exact] = append(cp.procExact[exact], cr)
		} else if pfx != "" {
			cp.procPrefix = append(cp.procPrefix, cr)
			root := pathRootSegment(pfx)
			cp.procPrefixByRoot[root] = append(cp.procPrefixByRoot[root], cr)
		} else {
			cp.procGeneral = append(cp.procGeneral, cr)
		}
	}
	for k := range cp.procExact {
		sortProcessRules(cp.procExact[k])
	}
	for k := range cp.procPrefixByRoot {
		sortProcessRules(cp.procPrefixByRoot[k])
	}
	sortProcessRules(cp.procPrefix)
	sortProcessRules(cp.procGeneral)
	sortProcessRules(cp.allProcs)

	// 3. Compile and conflict-check Network rules.
	type netConflictKey struct {
		cidr          string
		direction     NetworkDirection
		protocol      string
		portStart     uint16
		portEnd       uint16
		matchPortZero bool
		priority      int
	}
	netConflicts := make(map[netConflictKey]RuleEffect, len(spec.NetworkRules))

	for i, r := range spec.NetworkRules {
		r.ID = strings.TrimSpace(r.ID)
		if err := checkID(r.ID); err != nil {
			return nil, fmt.Errorf("network rule[%d]: %w", i, err)
		}
		if r.Effect != EffectAllow && r.Effect != EffectDeny {
			return nil, fmt.Errorf("%w: network rule %q invalid effect %q", ErrInvalidPolicy, r.ID, r.Effect)
		}
		pfx, err := NormalizeCIDR(r.CIDR)
		if err != nil {
			return nil, fmt.Errorf("%w: network rule %q: %v", ErrInvalidPolicy, r.ID, err)
		}
		dir := r.Direction
		if dir == "" {
			dir = NetDirAny
		}
		if dir != NetDirAny && dir != NetDirEgress && dir != NetDirBind {
			return nil, fmt.Errorf("%w: network rule %q invalid direction %q", ErrInvalidPolicy, r.ID, dir)
		}
		proto := strings.ToLower(strings.TrimSpace(r.Protocol))
		if proto == "any" {
			proto = ""
		}
		if proto != "" && proto != "tcp" && proto != "udp" {
			return nil, fmt.Errorf("%w: network rule %q invalid protocol %q", ErrInvalidPolicy, r.ID, proto)
		}
		if r.MatchPortZero && (r.PortStart != 0 || r.PortEnd != 0) {
			return nil, fmt.Errorf("%w: network rule %q cannot combine matchPortZero with portStart/portEnd", ErrInvalidPolicy, r.ID)
		}
		pStart, pEnd := r.PortStart, r.PortEnd
		if pStart > 0 && pEnd == 0 {
			pEnd = pStart
		}
		if pEnd > 0 && pStart == 0 {
			pStart = pEnd
		}
		if pEnd < pStart {
			return nil, fmt.Errorf("%w: network rule %q portEnd %d < portStart %d", ErrInvalidPolicy, r.ID, pEnd, pStart)
		}
		ck := netConflictKey{
			cidr: pfx.String(), direction: dir, protocol: proto,
			portStart: pStart, portEnd: pEnd, matchPortZero: r.MatchPortZero, priority: r.Priority,
		}
		if prevEff, exists := netConflicts[ck]; exists && prevEff != r.Effect {
			return nil, fmt.Errorf("%w: network CIDR %s priority %d", ErrPolicyConflict, pfx.String(), r.Priority)
		}
		netConflicts[ck] = r.Effect

		act, err := resolveEffectiveAction(spec.Mode, r.Action, r.Effect)
		if err != nil {
			return nil, fmt.Errorf("network rule %q: %w", r.ID, err)
		}
		cr := compiledNetworkRule{
			rule:          r,
			prefix:        pfx,
			direction:     dir,
			protocol:      proto,
			portStart:     pStart,
			portEnd:       pEnd,
			matchPortZero: r.MatchPortZero,
			action:        act,
		}
		cp.allNets = append(cp.allNets, cr)
		bits := pfx.Bits()
		if pfx.Addr().Is4() {
			if cp.netV4ByBit[bits] == nil {
				cp.netV4ByBit[bits] = make(map[netip.Prefix][]compiledNetworkRule)
			}
			cp.netV4ByBit[bits][pfx] = append(cp.netV4ByBit[bits][pfx], cr)
			cp.netV4Mask |= uint64(1) << uint(bits)
		} else {
			if cp.netV6ByBit[bits] == nil {
				cp.netV6ByBit[bits] = make(map[netip.Prefix][]compiledNetworkRule)
			}
			cp.netV6ByBit[bits][pfx] = append(cp.netV6ByBit[bits][pfx], cr)
			switch {
			case bits < 64:
				cp.netV6MaskL |= uint64(1) << uint(bits)
			case bits < 128:
				cp.netV6MaskH |= uint64(1) << uint(bits-64)
			default:
				cp.netV6Has128 = true
			}
		}
	}
	for b := range cp.netV4ByBit {
		for k := range cp.netV4ByBit[b] {
			sortNetworkRules(cp.netV4ByBit[b][k])
		}
	}
	for b := range cp.netV6ByBit {
		for k := range cp.netV6ByBit[b] {
			sortNetworkRules(cp.netV6ByBit[b][k])
		}
	}
	sortNetworkRules(cp.allNets)

	// 4. Compile real kernel-ready classic seccomp-BPF filter program.
	bpfProg, err := CompileSeccompBPF(cp)
	if err != nil {
		return nil, fmt.Errorf("compile seccomp-bpf: %w", err)
	}
	cp.BPF = bpfProg
	cp.BPFDigest = bpfProg.Digest()
	cp.Digest = computePolicyDigest(cp)
	return cp, nil
}

func sortSyscallRules(rules []compiledSyscallRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].rule.Priority != rules[j].rule.Priority {
			return rules[i].rule.Priority > rules[j].rule.Priority
		}
		si, sj := modeSeverity(rules[i].action), modeSeverity(rules[j].action)
		if si != sj {
			return si > sj
		}
		return rules[i].rule.ID < rules[j].rule.ID
	})
}

func sortProcessRules(rules []compiledProcessRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].rule.Priority != rules[j].rule.Priority {
			return rules[i].rule.Priority > rules[j].rule.Priority
		}
		si, sj := modeSeverity(rules[i].action), modeSeverity(rules[j].action)
		if si != sj {
			return si > sj
		}
		return rules[i].rule.ID < rules[j].rule.ID
	})
}

func sortNetworkRules(rules []compiledNetworkRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].rule.Priority != rules[j].rule.Priority {
			return rules[i].rule.Priority > rules[j].rule.Priority
		}
		si, sj := modeSeverity(rules[i].action), modeSeverity(rules[j].action)
		if si != sj {
			return si > sj
		}
		return rules[i].rule.ID < rules[j].rule.ID
	})
}

func computePolicyDigest(cp *CompiledPolicy) string {
	h := sha256.New()
	h.Write([]byte("hanzo-sandboxpolicy-v2\x00"))
	writeStr := func(s string) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(s)))
		h.Write(b[:])
		h.Write([]byte(s))
	}
	writeU64 := func(v uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	writeBool := func(v bool) {
		if v {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}

	// Policy header and all global enforcement defaults.
	writeStr(cp.Spec.ID)
	writeStr(cp.Spec.Name)
	writeU64(cp.Spec.Version)
	writeStr(string(cp.Spec.Mode))
	writeStr(cp.Spec.Arch)
	writeBool(cp.Spec.DefaultSyscallAllow)
	writeBool(cp.Spec.DefaultProcessAllow)
	writeBool(cp.Spec.DefaultNetworkAllow)
	writeBool(cp.Spec.DenyPrivEsc)
	writeBool(cp.Spec.AuditAllowEvents)
	writeStr(cp.BPFDigest)

	// Complete syscall rule enforcement fields.
	writeU64(uint64(len(cp.allSyscalls)))
	for _, r := range cp.allSyscalls {
		writeStr(r.rule.ID)
		writeU64(uint64(r.rule.Priority))
		writeU64(uint64(r.num))
		writeStr(string(r.rule.Effect))
		writeStr(string(r.action))
		writeU64(uint64(r.errno))
		writeBool(r.rule.AuditOnAllow)
		writeU64(uint64(len(r.rule.ArgFilters)))
		for _, af := range r.rule.ArgFilters {
			writeU64(uint64(af.Index))
			writeStr(string(af.Op))
			writeU64(af.Value)
		}
	}

	// Complete process rule enforcement fields.
	writeU64(uint64(len(cp.allProcs)))
	for _, r := range cp.allProcs {
		writeStr(r.rule.ID)
		writeU64(uint64(r.rule.Priority))
		writeStr(r.exact)
		writeStr(r.prefix)
		writeStr(r.rule.ArgContains)
		writeStr(r.rule.ArgRegex)
		writeStr(r.rule.RequiredParent)
		writeStr(r.rule.ForbiddenAncestor)
		writeStr(string(r.rule.Effect))
		writeStr(string(r.action))
	}

	// Complete network rule enforcement fields.
	writeU64(uint64(len(cp.allNets)))
	for _, r := range cp.allNets {
		writeStr(r.rule.ID)
		writeU64(uint64(r.rule.Priority))
		writeStr(r.prefix.String())
		writeStr(string(r.direction))
		writeStr(r.protocol)
		writeU64(uint64(r.portStart))
		writeU64(uint64(r.portEnd))
		writeBool(r.matchPortZero)
		writeStr(string(r.rule.Effect))
		writeStr(string(r.action))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Canonical Linux x86_64 (amd64) syscall table verified against asm/unistd_64.h.
var amd64SyscallNameToNum = map[string]uint32{
	"read":               0,
	"write":              1,
	"open":               2,
	"close":              3,
	"stat":               4,
	"fstat":              5,
	"lstat":              6,
	"poll":               7,
	"lseek":              8,
	"mmap":               9,
	"mprotect":           10,
	"munmap":             11,
	"brk":                12,
	"rt_sigaction":       13,
	"rt_sigprocmask":     14,
	"rt_sigreturn":       15,
	"ioctl":              16,
	"pread64":            17,
	"pwrite64":           18,
	"readv":              19,
	"writev":             20,
	"access":             21,
	"pipe":               22,
	"select":             23,
	"sched_yield":        24,
	"mremap":             25,
	"msync":              26,
	"mincore":            27,
	"madvise":            28,
	"dup":                32,
	"dup2":               33,
	"pause":              34,
	"nanosleep":          35,
	"getitimer":          36,
	"alarm":              37,
	"setitimer":          38,
	"getpid":             39,
	"sendfile":           40,
	"socket":             41,
	"connect":            42,
	"accept":             43,
	"sendto":             44,
	"recvfrom":           45,
	"sendmsg":            46,
	"recvmsg":            47,
	"shutdown":           48,
	"bind":               49,
	"listen":             50,
	"getsockname":        51,
	"getpeername":        52,
	"socketpair":         53,
	"setsockopt":         54,
	"getsockopt":         55,
	"clone":              56,
	"fork":               57,
	"vfork":              58,
	"execve":             59,
	"exit":               60,
	"wait4":              61,
	"kill":               62,
	"uname":              63,
	"fcntl":              72,
	"flock":              73,
	"fsync":              74,
	"fdatasync":          75,
	"truncate":           76,
	"ftruncate":          77,
	"getcwd":             79,
	"chdir":              80,
	"fchdir":             81,
	"rename":             82,
	"mkdir":              83,
	"rmdir":              84,
	"link":               86,
	"unlink":             87,
	"symlink":            88,
	"readlink":           89,
	"chmod":              90,
	"fchmod":             91,
	"chown":              92,
	"fchown":             93,
	"lchown":             94,
	"umask":              95,
	"gettimeofday":       96,
	"getrlimit":          97,
	"getrusage":          98,
	"sysinfo":            99,
	"times":              100,
	"ptrace":             101,
	"getuid":             102,
	"getgid":             104,
	"setuid":             105,
	"setgid":             106,
	"geteuid":            107,
	"getegid":            108,
	"setpgid":            109,
	"getppid":            110,
	"getpgrp":            111,
	"setsid":             112,
	"setreuid":           113,
	"setregid":           114,
	"getgroups":          115,
	"setresuid":          117,
	"getresuid":          118,
	"setresgid":          119,
	"getresgid":          120,
	"getpgid":            121,
	"getsid":             124,
	"capget":             125,
	"capset":             126,
	"rt_sigpending":      127,
	"rt_sigtimedwait":    128,
	"rt_sigsuspend":      130,
	"sigaltstack":        131,
	"statfs":             137,
	"fstatfs":            138,
	"sched_getparam":     143,
	"sched_getscheduler": 145,
	"mlock":              149,
	"munlock":            150,
	"pivot_root":         155,
	"prctl":              157,
	"arch_prctl":         158,
	"chroot":             161,
	"mount":              165,
	"umount2":            166,
	"init_module":        175,
	"delete_module":      176,
	"gettid":             186,
	"getxattr":           191,
	"lgetxattr":          192,
	"fgetxattr":          193,
	"listxattr":          194,
	"llistxattr":         195,
	"flistxattr":         196,
	"tkill":              200,
	"futex":              202,
	"sched_getaffinity":  204,
	"getdents64":         217,
	"set_tid_address":    218,
	"restart_syscall":    219,
	"fadvise64":          221,
	"clock_gettime":      228,
	"clock_getres":       229,
	"clock_nanosleep":    230,
	"exit_group":         231,
	"epoll_ctl":          233,
	"tgkill":             234,
	"kexec_load":         246,
	"waitid":             247,
	"add_key":            248,
	"request_key":        249,
	"keyctl":             250,
	"inotify_add_watch":  254,
	"inotify_rm_watch":   255,
	"openat":             257,
	"mkdirat":            258,
	"fchownat":           260,
	"newfstatat":         262,
	"unlinkat":           263,
	"renameat":           264,
	"linkat":             265,
	"symlinkat":          266,
	"readlinkat":         267,
	"fchmodat":           268,
	"faccessat":          269,
	"pselect6":           270,
	"ppoll":              271,
	"unshare":            272,
	"set_robust_list":    273,
	"splice":             275,
	"tee":                276,
	"sync_file_range":    277,
	"utimensat":          280,
	"epoll_pwait":        281,
	"timerfd_create":     283,
	"fallocate":          285,
	"timerfd_settime":    286,
	"timerfd_gettime":    287,
	"accept4":            288,
	"signalfd4":          289,
	"eventfd2":           290,
	"epoll_create1":      291,
	"dup3":               292,
	"pipe2":              293,
	"inotify_init1":      294,
	"perf_event_open":    298,
	"prlimit64":          302,
	"open_by_handle_at":  304,
	"setns":              308,
	"getcpu":             309,
	"process_vm_readv":   310,
	"process_vm_writev":  311,
	"finit_module":       313,
	"renameat2":          316,
	"seccomp":            317,
	"getrandom":          318,
	"memfd_create":       319,
	"kexec_file_load":    320,
	"bpf":                321,
	"execveat":           322,
	"userfaultfd":        323,
	"membarrier":         324,
	"copy_file_range":    326,
	"statx":              332,
	"rseq":               334,
	"io_uring_setup":     425,
	"io_uring_enter":     426,
	"io_uring_register":  427,
	"clone3":             435,
	"faccessat2":         439,
	"epoll_pwait2":       441,
}

// Canonical Linux aarch64 (arm64) syscall table verified against asm-generic/unistd.h.
var arm64SyscallNameToNum = map[string]uint32{
	"getxattr":           8,
	"lgetxattr":          9,
	"fgetxattr":          10,
	"listxattr":          11,
	"llistxattr":         12,
	"flistxattr":         13,
	"getcwd":             17,
	"eventfd2":           19,
	"epoll_create1":      20,
	"epoll_ctl":          21,
	"epoll_pwait":        22,
	"dup":                23,
	"dup3":               24,
	"fcntl":              25,
	"inotify_init1":      26,
	"inotify_add_watch":  27,
	"inotify_rm_watch":   28,
	"ioctl":              29,
	"flock":              32,
	"mkdirat":            34,
	"unlinkat":           35,
	"symlinkat":          36,
	"linkat":             37,
	"renameat":           38,
	"umount2":            39,
	"mount":              40,
	"pivot_root":         41,
	"statfs":             43,
	"fstatfs":            44,
	"truncate":           45,
	"ftruncate":          46,
	"fallocate":          47,
	"faccessat":          48,
	"chdir":              49,
	"fchdir":             50,
	"chroot":             51,
	"fchmod":             52,
	"fchmodat":           53,
	"fchownat":           54,
	"fchown":             55,
	"openat":             56,
	"close":              57,
	"pipe2":              59,
	"getdents64":         61,
	"lseek":              62,
	"read":               63,
	"write":              64,
	"readv":              65,
	"writev":             66,
	"pread64":            67,
	"pwrite64":           68,
	"sendfile":           71,
	"pselect6":           72,
	"ppoll":              73,
	"signalfd4":          74,
	"splice":             76,
	"tee":                77,
	"readlinkat":         78,
	"newfstatat":         79,
	"fstat":              80,
	"fsync":              82,
	"fdatasync":          83,
	"sync_file_range":    84,
	"timerfd_create":     85,
	"timerfd_settime":    86,
	"timerfd_gettime":    87,
	"utimensat":          88,
	"capget":             90,
	"capset":             91,
	"exit":               93,
	"exit_group":         94,
	"waitid":             95,
	"set_tid_address":    96,
	"unshare":            97,
	"futex":              98,
	"set_robust_list":    99,
	"nanosleep":          101,
	"getitimer":          102,
	"setitimer":          103,
	"kexec_load":         104,
	"init_module":        105,
	"delete_module":      106,
	"clock_gettime":      113,
	"clock_getres":       114,
	"clock_nanosleep":    115,
	"ptrace":             117,
	"sched_getscheduler": 120,
	"sched_getparam":     121,
	"sched_getaffinity":  123,
	"sched_yield":        124,
	"restart_syscall":    128,
	"kill":               129,
	"tkill":              130,
	"tgkill":             131,
	"sigaltstack":        132,
	"rt_sigsuspend":      133,
	"rt_sigaction":       134,
	"rt_sigprocmask":     135,
	"rt_sigpending":      136,
	"rt_sigtimedwait":    137,
	"rt_sigreturn":       139,
	"setregid":           143,
	"setgid":             144,
	"setreuid":           145,
	"setuid":             146,
	"setresuid":          147,
	"getresuid":          148,
	"setresgid":          149,
	"getresgid":          150,
	"times":              153,
	"setpgid":            154,
	"getpgid":            155,
	"getsid":             156,
	"setsid":             157,
	"getgroups":          158,
	"uname":              160,
	"getrlimit":          163,
	"getrusage":          165,
	"umask":              166,
	"prctl":              167,
	"getcpu":             168,
	"gettimeofday":       169,
	"getpid":             172,
	"getppid":            173,
	"getuid":             174,
	"geteuid":            175,
	"getgid":             176,
	"getegid":            177,
	"gettid":             178,
	"sysinfo":            179,
	"socket":             198,
	"socketpair":         199,
	"bind":               200,
	"listen":             201,
	"accept":             202,
	"connect":            203,
	"getsockname":        204,
	"getpeername":        205,
	"sendto":             206,
	"recvfrom":           207,
	"setsockopt":         208,
	"getsockopt":         209,
	"shutdown":           210,
	"sendmsg":            211,
	"recvmsg":            212,
	"brk":                214,
	"munmap":             215,
	"mremap":             216,
	"add_key":            217,
	"request_key":        218,
	"keyctl":             219,
	"clone":              220,
	"execve":             221,
	"mmap":               222,
	"fadvise64":          223,
	"mprotect":           226,
	"msync":              227,
	"mlock":              228,
	"munlock":            229,
	"mincore":            232,
	"madvise":            233,
	"perf_event_open":    241,
	"accept4":            242,
	"wait4":              260,
	"prlimit64":          261,
	"open_by_handle_at":  265,
	"setns":              268,
	"process_vm_readv":   270,
	"process_vm_writev":  271,
	"finit_module":       273,
	"renameat2":          276,
	"seccomp":            277,
	"getrandom":          278,
	"memfd_create":       279,
	"bpf":                280,
	"execveat":           281,
	"userfaultfd":        282,
	"membarrier":         283,
	"copy_file_range":    285,
	"statx":              291,
	"rseq":               293,
	"kexec_file_load":    294,
	"io_uring_setup":     425,
	"io_uring_enter":     426,
	"io_uring_register":  427,
	"clone3":             435,
	"faccessat2":         439,
	"epoll_pwait2":       441,
}

func invertSyscallTable(nameToNum map[string]uint32) map[uint32]string {
	m := make(map[uint32]string, len(nameToNum))
	for name, num := range nameToNum {
		m[num] = name
	}
	return m
}

var (
	amd64SyscallNumToName = invertSyscallTable(amd64SyscallNameToNum)
	arm64SyscallNumToName = invertSyscallTable(arm64SyscallNameToNum)
)

func syscallTablesForArch(arch string) (map[string]uint32, map[uint32]string, error) {
	switch arch {
	case "", "amd64":
		return amd64SyscallNameToNum, amd64SyscallNumToName, nil
	case "arm64":
		return arm64SyscallNameToNum, arm64SyscallNumToName, nil
	default:
		return nil, nil, fmt.Errorf("%w: unsupported arch %q", ErrInvalidPolicy, arch)
	}
}

// LookupSyscallNumber resolves a canonical Linux x86_64 (amd64) syscall name to its number.
func LookupSyscallNumber(name string) (uint32, bool) {
	return LookupSyscallNumberForArch("amd64", name)
}

// LookupSyscallName resolves a Linux x86_64 (amd64) syscall number to its canonical name.
func LookupSyscallName(num uint32) string {
	return LookupSyscallNameForArch("amd64", num)
}

// LookupSyscallNumberForArch resolves a canonical Linux syscall name for arch ("amd64" or "arm64").
func LookupSyscallNumberForArch(arch, name string) (uint32, bool) {
	nameToNum, _, err := syscallTablesForArch(arch)
	if err != nil {
		return 0, false
	}
	num, ok := nameToNum[strings.ToLower(strings.TrimSpace(name))]
	return num, ok
}

// LookupSyscallNameForArch resolves a Linux syscall number to its canonical name for arch ("amd64" or "arm64").
func LookupSyscallNameForArch(arch string, num uint32) string {
	_, numToName, err := syscallTablesForArch(arch)
	if err == nil {
		if name, ok := numToName[num]; ok {
			return name
		}
	}
	return fmt.Sprintf("syscall_%d", num)
}

func resolveSyscallIdentity(name string, numPtr *int) (uint32, string, error) {
	return resolveSyscallIdentityForArch("amd64", name, numPtr)
}

func resolveSyscallIdentityForArch(arch, name string, numPtr *int) (uint32, string, error) {
	nameToNum, numToName, err := syscallTablesForArch(arch)
	if err != nil {
		return 0, "", err
	}
	cleanName := strings.ToLower(strings.TrimSpace(name))
	if numPtr != nil {
		if *numPtr < 0 || *numPtr > MaxSyscallNumber {
			return 0, "", fmt.Errorf("%w: syscall number %d out of range [0..%d]", ErrInvalidPolicy, *numPtr, MaxSyscallNumber)
		}
		num := uint32(*numPtr)
		if cleanName != "" {
			expected, ok := nameToNum[cleanName]
			if !ok {
				return 0, "", fmt.Errorf("%w: unknown syscall name %q for arch %q", ErrInvalidPolicy, cleanName, arch)
			}
			if expected != num {
				return 0, "", fmt.Errorf("%w: syscall %q on %s maps to %d, not %d", ErrInvalidPolicy, cleanName, arch, expected, num)
			}
			return num, cleanName, nil
		}
		if resolved, ok := numToName[num]; ok {
			return num, resolved, nil
		}
		return num, fmt.Sprintf("syscall_%d", num), nil
	}
	if cleanName == "" {
		return 0, "", fmt.Errorf("%w: syscall rule requires syscallName or syscallNumber", ErrInvalidPolicy)
	}
	num, ok := nameToNum[cleanName]
	if !ok {
		return 0, "", fmt.Errorf("%w: unknown syscall name %q for arch %q", ErrInvalidPolicy, cleanName, arch)
	}
	return num, cleanName, nil
}

// Linux namespace/socket constants used by DefaultVisorSandboxPolicy.
const (
	CloneNewNS   uint64 = 0x00020000
	CloneNewUser uint64 = 0x10000000
	CloneNewPID  uint64 = 0x20000000
	CloneNewNet  uint64 = 0x40000000

	AFNetlink uint64 = 16
	AFPacket  uint64 = 17
)

// DefaultVisorSandboxPolicy returns a hardened, fail-closed Tetragon-inspired
// TracingPolicy targeting linux/amd64 tailored for Visor coding-agent sandboxes
// (Go, Git, Node/npm, shell utilities) while blocking container escapes, kernel
// attacks, privilege escalations, metadata-endpoint SSRF, and lateral private-network access.
func DefaultVisorSandboxPolicy(mode EnforcementMode) TracingPolicy {
	return DefaultVisorSandboxPolicyForArch(mode, "amd64")
}

// DefaultVisorSandboxPolicyForArch returns the hardened Visor sandbox TracingPolicy
// resolved for the requested target architecture ("amd64" or "arm64").
func DefaultVisorSandboxPolicyForArch(mode EnforcementMode, arch string) TracingPolicy {
	if mode == "" {
		mode = ModeEnforceSigkill
	}
	if arch == "" {
		arch = "amd64"
	}

	benignSyscalls := []string{
		"read", "write", "open", "openat", "close", "stat", "fstat", "lstat",
		"newfstatat", "statx", "poll", "ppoll", "lseek", "mmap", "mprotect", "munmap",
		"brk", "rt_sigaction", "rt_sigprocmask", "rt_sigreturn", "rt_sigpending",
		"rt_sigsuspend", "rt_sigtimedwait", "ioctl",
		"pread64", "pwrite64", "readv", "writev", "access", "faccessat", "faccessat2",
		"pipe", "pipe2", "select", "pselect6", "sched_yield", "sched_getaffinity",
		"sched_getparam", "sched_getscheduler", "mremap", "madvise",
		"dup", "dup2", "dup3", "nanosleep", "clock_gettime", "clock_getres", "clock_nanosleep",
		"getpid", "gettid", "getppid", "getuid", "geteuid", "getgid", "getegid",
		"getresuid", "getresgid", "getgroups", "getpgrp", "getpgid", "setpgid", "setsid", "getsid",
		"socket", "connect", "accept", "accept4", "sendto", "recvfrom",
		"sendmsg", "recvmsg", "shutdown", "bind", "listen", "getsockname",
		"getpeername", "socketpair", "setsockopt", "getsockopt",
		"clone", "fork", "vfork", "execve", "exit", "exit_group",
		"wait4", "waitid", "kill", "tkill", "tgkill", "uname", "fcntl", "flock",
		"fsync", "fdatasync", "truncate", "ftruncate", "fallocate", "fadvise64", "getdents64",
		"getcwd", "chdir", "fchdir", "rename", "renameat", "renameat2",
		"mkdir", "mkdirat", "rmdir", "link", "linkat", "unlink", "unlinkat",
		"symlink", "symlinkat", "readlink", "readlinkat", "chmod", "fchmod",
		"fchmodat", "umask", "utimensat", "gettimeofday", "getrlimit", "prlimit64",
		"getrusage", "sysinfo", "times", "sigaltstack", "statfs", "fstatfs",
		"prctl", "arch_prctl", "futex", "set_tid_address", "set_robust_list",
		"epoll_create1", "epoll_ctl", "epoll_pwait", "epoll_pwait2", "eventfd2",
		"timerfd_create", "timerfd_settime", "timerfd_gettime", "signalfd4",
		"sendfile", "copy_file_range", "getrandom", "rseq", "capget",
		"membarrier", "restart_syscall", "getcpu",
	}

	sysRules := []SyscallRule{
		// High-priority container-escape & kernel-surface denylist.
		{ID: "sys-deny-ptrace", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "ptrace", Reason: "prevent process memory inspection and sandbox breakout"},
		{ID: "sys-deny-mount", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "mount", Reason: "prevent filesystem mount escapes"},
		{ID: "sys-deny-umount2", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "umount2", Reason: "prevent filesystem unmount manipulation"},
		{ID: "sys-deny-pivot-root", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "pivot_root", Reason: "prevent rootfs pivot escape"},
		{ID: "sys-deny-chroot", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "chroot", Reason: "prevent chroot escape primitives"},
		{ID: "sys-deny-init-module", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "init_module", Reason: "prevent kernel module loading"},
		{ID: "sys-deny-finit-module", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "finit_module", Reason: "prevent fd kernel module loading"},
		{ID: "sys-deny-delete-module", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "delete_module", Reason: "prevent kernel module unloading"},
		{ID: "sys-deny-bpf", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "bpf", Reason: "prevent unprivileged eBPF program loading from sandbox"},
		{ID: "sys-deny-setns", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "setns", Reason: "prevent joining host namespaces"},
		{ID: "sys-deny-unshare", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "unshare", Reason: "prevent namespace creation for privilege escalation"},
		{ID: "sys-deny-open-by-handle-at", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "open_by_handle_at", Reason: "prevent Shocker file-handle container escape"},
		{ID: "sys-deny-kexec-load", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "kexec_load", Reason: "prevent kernel replacement"},
		{ID: "sys-deny-kexec-file-load", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "kexec_file_load", Reason: "prevent kernel replacement"},
		{ID: "sys-deny-process-vm-readv", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "process_vm_readv", Reason: "prevent cross-process memory reads"},
		{ID: "sys-deny-process-vm-writev", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "process_vm_writev", Reason: "prevent cross-process memory writes"},
		{ID: "sys-deny-userfaultfd", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, SyscallName: "userfaultfd", Reason: "reduce kernel exploit primitive surface"},
		{ID: "sys-deny-perf-event-open", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, SyscallName: "perf_event_open", Reason: "prevent side-channel and kernel perf attacks"},
		{ID: "sys-deny-io-uring-setup", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, SyscallName: "io_uring_setup", Reason: "prevent io_uring kernel bypass surface"},
		{ID: "sys-deny-memfd-create", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, SyscallName: "memfd_create", Reason: "prevent fileless in-memory ELF execution"},
		{ID: "sys-deny-keyctl", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, SyscallName: "keyctl", Reason: "prevent kernel keyring tampering"},
		{ID: "sys-deny-setuid", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "setuid", Reason: "prevent UID privilege escalation"},
		{ID: "sys-deny-setgid", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "setgid", Reason: "prevent GID privilege escalation"},
		// clone3 passes namespace flags inside a pointer-referenced struct clone_args that
		// classic seccomp-BPF cannot inspect; return ENOSYS so glibc/Go safely fall back
		// to legacy clone(2) where sys-deny-clone-ns enforces register-level flag checks.
		{
			ID: "sys-deny-clone3-enosys", Priority: 950, Effect: EffectDeny, Action: ModeEnforceDeny, Errno: ErrnoENOSYS,
			SyscallName: "clone3",
			Reason:      "return ENOSYS for clone3 so runtimes fall back to register-inspectable legacy clone",
		},
		// Argument-filtered syscall rules (e.g., clone with namespace flags, raw/netlink sockets).
		{
			ID: "sys-deny-clone-ns", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill,
			SyscallName: "clone",
			ArgFilters:  []SyscallArgFilter{{Index: 0, Op: ArgOpMaskAny, Value: CloneNewUser | CloneNewNS | CloneNewPID | CloneNewNet}},
			Reason:      "prevent user/mount/pid/net namespace creation via clone",
		},
		// Linux socket(int domain, ...) consumes domain as a 32-bit C int; compare with eq32
		// so upper-bit register spoofing such as (1<<32) | AF_NETLINK cannot bypass the deny.
		{
			ID: "sys-deny-socket-packet", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny,
			SyscallName: "socket",
			ArgFilters:  []SyscallArgFilter{{Index: 0, Op: ArgOpEqual32, Value: AFPacket}},
			Reason:      "prevent AF_PACKET raw packet capture",
		},
		{
			ID: "sys-deny-socket-netlink", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny,
			SyscallName: "socket",
			ArgFilters:  []SyscallArgFilter{{Index: 0, Op: ArgOpEqual32, Value: AFNetlink}},
			Reason:      "prevent AF_NETLINK kernel netlink socket manipulation",
		},
	}

	nameTable, _, tableErr := syscallTablesForArch(arch)
	for _, name := range benignSyscalls {
		if tableErr == nil {
			if _, exists := nameTable[name]; !exists {
				// Skip legacy x86_64-only syscalls (e.g., open, stat, fork) when emitting an arm64 policy.
				continue
			}
		}
		sysRules = append(sysRules, SyscallRule{
			ID:          "sys-allow-" + name,
			Priority:    100,
			Effect:      EffectAllow,
			SyscallName: name,
			Reason:      "benign sandbox syscall allowlisted in-kernel",
		})
	}

	procRules := []ProcessRule{
		// Explicit high-priority blocked binaries and escape tools.
		{ID: "proc-deny-sudo", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/sudo", Reason: "privilege escalation binary forbidden"},
		{ID: "proc-deny-su", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/su", Reason: "privilege escalation binary forbidden"},
		{ID: "proc-deny-bin-su", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/bin/su", Reason: "privilege escalation binary forbidden"},
		{ID: "proc-deny-nsenter", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/nsenter", Reason: "namespace breakout binary forbidden"},
		{ID: "proc-deny-unshare", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/unshare", Reason: "namespace creation binary forbidden"},
		{ID: "proc-deny-mount", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/mount", Reason: "mount binary forbidden"},
		{ID: "proc-deny-bin-mount", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/bin/mount", Reason: "mount binary forbidden"},
		{ID: "proc-deny-chroot", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/sbin/chroot", Reason: "chroot binary forbidden"},
		{ID: "proc-deny-docker", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/docker", Reason: "container runtime control binary forbidden"},
		{ID: "proc-deny-kubectl", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/kubectl", Reason: "cluster control binary forbidden"},
		{ID: "proc-deny-gdb", Priority: 950, Effect: EffectDeny, Action: ModeEnforceDeny, BinaryExact: "/usr/bin/gdb", Reason: "debugger binary forbidden in sandbox"},
		{ID: "proc-deny-strace", Priority: 950, Effect: EffectDeny, Action: ModeEnforceDeny, BinaryExact: "/usr/bin/strace", Reason: "tracer binary forbidden in sandbox"},
		{ID: "proc-deny-sbin", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryPrefix: "/sbin", Reason: "system admin binaries forbidden"},
		{ID: "proc-deny-usr-sbin", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryPrefix: "/usr/sbin", Reason: "system admin binaries forbidden"},

		// Argument pattern rules blocking reverse shells, host proc escapes, and IMDS fetches.
		{ID: "proc-deny-arg-proc-root", Priority: 900, Effect: EffectDeny, Action: ModeEnforceSigkill, ArgContains: "/proc/1/root", Reason: "block host filesystem traversal via /proc/1/root"},
		{ID: "proc-deny-arg-docker-sock", Priority: 900, Effect: EffectDeny, Action: ModeEnforceSigkill, ArgContains: "docker.sock", Reason: "block Docker socket escape"},
		{ID: "proc-deny-arg-dev-tcp", Priority: 900, Effect: EffectDeny, Action: ModeEnforceSigkill, ArgContains: "/dev/tcp/", Reason: "block bash /dev/tcp reverse shell"},
		{ID: "proc-deny-arg-imds", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, ArgContains: "169.254.169.254", Reason: "block cloud metadata SSRF via CLI"},

		// Lineage restriction: forbid netcat binary.
		{ID: "proc-deny-lineage-nc", Priority: 900, Effect: EffectDeny, Action: ModeEnforceSigkill, BinaryExact: "/usr/bin/nc", Reason: "netcat binary forbidden"},

		// Allowlisted Visor agent toolchain prefixes.
		{ID: "proc-allow-go", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/usr/local/go/bin", Reason: "Go toolchain allowlisted"},
		{ID: "proc-allow-usr-bin", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/usr/bin", Reason: "standard userland utilities allowlisted"},
		{ID: "proc-allow-bin", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/bin", Reason: "core POSIX utilities allowlisted"},
		{ID: "proc-allow-usr-local-bin", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/usr/local/bin", Reason: "local developer tools allowlisted"},
		{ID: "proc-allow-workspace-bin", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/workspace/.bin", Reason: "workspace build outputs allowlisted"},
		{ID: "proc-allow-tmp-go-build", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/tmp/go-build", Reason: "go test compiled test binaries allowlisted"},
	}

	netRules := []NetworkRule{
		// Highest priority: Cloud IMDS & Link-Local metadata endpoints.
		{ID: "net-deny-imds-v4", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, Direction: NetDirAny, CIDR: "169.254.169.254/32", Reason: "block AWS/GCP/Azure IMDSv1/v2 metadata endpoint"},
		{ID: "net-deny-link-local-v4", Priority: 990, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirAny, CIDR: "169.254.0.0/16", Reason: "block IPv4 link-local range"},
		{ID: "net-deny-imds-v6", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, Direction: NetDirAny, CIDR: "fd00:ec2::254/128", Reason: "block AWS IPv6 metadata endpoint"},
		{ID: "net-deny-link-local-v6", Priority: 990, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirAny, CIDR: "fe80::/10", Reason: "block IPv6 link-local range"},

		// High priority: Private / cluster / control-plane networks (prevent lateral movement).
		{ID: "net-deny-rfc1918-10", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirEgress, CIDR: "10.0.0.0/8", Reason: "block RFC1918 10/8 internal cluster egress"},
		{ID: "net-deny-rfc1918-172", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirEgress, CIDR: "172.16.0.0/12", Reason: "block RFC1918 172.16/12 internal cluster egress"},
		{ID: "net-deny-rfc1918-192", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirEgress, CIDR: "192.168.0.0/16", Reason: "block RFC1918 192.168/16 internal network egress"},
		{ID: "net-deny-cgnat", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirEgress, CIDR: "100.64.0.0/10", Reason: "block CGNAT/mesh internal egress"},
		{ID: "net-deny-ula-v6", Priority: 900, Effect: EffectDeny, Action: ModeEnforceDeny, Direction: NetDirEgress, CIDR: "fc00::/7", Reason: "block IPv6 ULA private egress"},

		// Loopback daemon ports denied (Docker 2375/2376, etcd 2379, kubelet 10250, K8s 6443).
		{ID: "net-deny-loopback-docker", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, Direction: NetDirEgress, CIDR: "127.0.0.0/8", PortStart: 2375, PortEnd: 2379, Reason: "block loopback Docker/etcd daemon access"},
		{ID: "net-deny-loopback-kube", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, Direction: NetDirEgress, CIDR: "127.0.0.0/8", PortStart: 6443, PortEnd: 6443, Reason: "block loopback Kubernetes API access"},
		{ID: "net-deny-loopback-v6-docker", Priority: 950, Effect: EffectDeny, Action: ModeEnforceSigkill, Direction: NetDirEgress, CIDR: "::1/128", PortStart: 2375, PortEnd: 2379, Reason: "block IPv6 loopback Docker/etcd daemon access"},
		// Allow pre-bind port-0 loopback listeners (httptest.NewServer / net.Listen requesting
		// kernel-assigned ephemeral ports) strictly on bind without relaxing egress restrictions.
		{ID: "net-allow-loopback-bind-zero-v4", Priority: 200, Effect: EffectAllow, Direction: NetDirBind, Protocol: "tcp", CIDR: "127.0.0.0/8", MatchPortZero: true, Reason: "allow pre-bind port-0 IPv4 loopback ephemeral listener requests"},
		{ID: "net-allow-loopback-bind-zero-v6", Priority: 200, Effect: EffectAllow, Direction: NetDirBind, Protocol: "tcp", CIDR: "::1/128", MatchPortZero: true, Reason: "allow pre-bind port-0 IPv6 loopback ephemeral listener requests"},
		// Allow ephemeral local dev/test loopback binds & connections (e.g., httptest in go test).
		{ID: "net-allow-loopback-ephemeral-v4", Priority: 200, Effect: EffectAllow, Direction: NetDirAny, Protocol: "tcp", CIDR: "127.0.0.0/8", PortStart: 32768, PortEnd: 65535, Reason: "allow ephemeral loopback ports for local unit tests"},
		{ID: "net-allow-loopback-ephemeral-v6", Priority: 200, Effect: EffectAllow, Direction: NetDirAny, Protocol: "tcp", CIDR: "::1/128", PortStart: 32768, PortEnd: 65535, Reason: "allow ephemeral IPv6 loopback ports for local unit tests"},

		// Allowlisted public API HTTPS (443) and DNS (53) egress.
		{ID: "net-allow-https-v4", Priority: 100, Effect: EffectAllow, Direction: NetDirEgress, Protocol: "tcp", CIDR: "0.0.0.0/0", PortStart: 443, PortEnd: 443, Reason: "allow HTTPS egress to public APIs and package registries"},
		{ID: "net-allow-https-v6", Priority: 100, Effect: EffectAllow, Direction: NetDirEgress, Protocol: "tcp", CIDR: "::/0", PortStart: 443, PortEnd: 443, Reason: "allow IPv6 HTTPS egress"},
		{ID: "net-allow-dns-udp-v4", Priority: 100, Effect: EffectAllow, Direction: NetDirEgress, Protocol: "udp", CIDR: "0.0.0.0/0", PortStart: 53, PortEnd: 53, Reason: "allow UDP DNS resolution"},
		{ID: "net-allow-dns-tcp-v4", Priority: 100, Effect: EffectAllow, Direction: NetDirEgress, Protocol: "tcp", CIDR: "0.0.0.0/0", PortStart: 53, PortEnd: 53, Reason: "allow TCP DNS resolution"},
	}

	return TracingPolicy{
		ID:                  "visor-sandbox-default",
		Name:                "Visor Agent Sandbox Default Policy (HIP-1349)",
		Version:             1,
		Mode:                mode,
		Arch:                arch,
		DefaultSyscallAllow: false,
		DefaultProcessAllow: false,
		DefaultNetworkAllow: false,
		DenyPrivEsc:         true,
		AuditAllowEvents:    false,
		SyscallRules:        sysRules,
		ProcessRules:        procRules,
		NetworkRules:        netRules,
	}
}
