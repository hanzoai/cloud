// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	maxAuditQueryLimit = 4096
)

// Deps supplies the host dependencies required by the sandboxpolicy app.
// It imports nothing of its host so the package mounts unchanged in any zip host.
type Deps struct {
	// Log is the host logger scoped to the sandboxpolicy subsystem.
	Log luxlog.Logger

	// Org returns the validated org of the caller on ctx.
	Org func(ctx context.Context) (org string, ok bool)

	// Acting returns the validated org a call acts for, or zip.ErrUnauthorized / zip.ErrForbidden.
	Acting func(ctx context.Context) (org string, err error)

	// Config optionally overrides default Engine capacity and ring parameters.
	Config EngineConfig
}

// Service owns the sandboxpolicy Engine and host identity bindings.
type Service struct {
	log    luxlog.Logger
	org    func(context.Context) (string, bool)
	acting func(context.Context) (string, error)
	engine *Engine
}

// NewService constructs a standalone sandboxpolicy Service from deps.
func NewService(deps Deps) (*Service, error) {
	if deps.Log == nil || deps.Org == nil || deps.Acting == nil {
		return nil, errors.New("sandboxpolicy: Deps requires Log, Org, and Acting")
	}
	return &Service{
		log:    deps.Log,
		org:    deps.Org,
		acting: deps.Acting,
		engine: NewEngine(deps.Config),
	}, nil
}

// Engine returns the underlying policy enforcement engine.
func (s *Service) Engine() *Engine { return s.engine }

// Mount registers the typed /v1/sandboxpolicy HTTP control and audit surface on app.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("sandboxpolicy: nil app")
	}
	svc, err := NewService(deps)
	if err != nil {
		return fmt.Errorf("sandboxpolicy.Mount: %w", err)
	}
	MountService(app, svc)
	svc.log.Info("sandboxpolicy enforcement engine mounted", "prefix", "/v1/sandboxpolicy")
	return nil
}

// MountService mounts an existing Service on app at /v1/sandboxpolicy.
func MountService(app *zip.App, svc *Service) {
	o := ops{s: svc}
	g := app.Group("/v1/sandboxpolicy")
	g.Get("/health", o.health)
	g.Post("/policies", o.upsertPolicy, zip.WithStatus(http.StatusCreated))
	g.Get("/policies", o.listPolicies)
	g.Get("/policies/:id", o.getPolicy)
	g.Post("/sandboxes/:id/bind", o.bindSandbox)
	g.Post("/evaluate", o.evaluate)
	g.Get("/audit", o.listAudit)
	g.Get("/metrics", o.metrics)
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type ops struct{ s *Service }

// healthOut reports readiness and non-tenant-specific aggregate enforcement counters.
type healthOut struct {
	// Status is "ok" when the sandbox policy engine is ready.
	Status string `json:"status"`
	// Policies is the aggregate count of compiled tracing policies across all orgs.
	Policies int `json:"policies"`
	// Sandboxes is the aggregate count of bound agent sandboxes across all orgs.
	Sandboxes int `json:"sandboxes"`
	// TotalEvaluations is the cumulative count of runtime events evaluated.
	TotalEvaluations uint64 `json:"totalEvaluations"`
	// TotalDenied is the cumulative count of EPERM denials enforced.
	TotalDenied uint64 `json:"totalDenied"`
	// TotalSigkills is the cumulative count of SIGKILL terminations enforced.
	TotalSigkills uint64 `json:"totalSigkills"`
}

// upsertPolicyIn submits a declarative TracingPolicy for validation, conflict checking,
// seccomp-BPF compilation, and storage under the caller's server-stamped org.
// Every field carries `url:"-"` to prevent query-string parameter hijacking.
type upsertPolicyIn struct {
	// ID uniquely identifies this policy within the caller's org.
	ID string `json:"id" url:"-"`
	// Name is a human-readable policy display name.
	Name string `json:"name" url:"-"`
	// Version is the monotonic policy version number (defaults to 1).
	Version uint64 `json:"version" url:"-"`
	// Mode sets the default enforcement mode ("audit_only", "enforce_deny", or "enforce_sigkill").
	Mode string `json:"mode" url:"-"`
	// Arch selects the target seccomp-BPF architecture ("amd64" or "arm64").
	Arch string `json:"arch" url:"-"`
	// DefaultSyscallAllow controls default syscall behavior when no rule matches (false = fail-closed).
	DefaultSyscallAllow bool `json:"defaultSyscallAllow" url:"-"`
	// DefaultProcessAllow controls default process exec behavior when no rule matches (false = fail-closed).
	DefaultProcessAllow bool `json:"defaultProcessAllow" url:"-"`
	// DefaultNetworkAllow controls default network behavior when no rule matches (false = fail-closed).
	DefaultNetworkAllow bool `json:"defaultNetworkAllow" url:"-"`
	// DenyPrivEsc refuses UID/GID transitions, setuid executions, and unauthorized Linux capabilities.
	DenyPrivEsc bool `json:"denyPrivEsc" url:"-"`
	// AuditAllowEvents controls whether benign ALLOW decisions are emitted to the audit ring.
	AuditAllowEvents bool `json:"auditAllowEvents" url:"-"`
	// SyscallRules lists declarative syscall rules compiled to seccomp-BPF.
	SyscallRules []SyscallRule `json:"syscallRules" url:"-"`
	// ProcessRules lists declarative process execution rules.
	ProcessRules []ProcessRule `json:"processRules" url:"-"`
	// NetworkRules lists declarative network egress and bind rules.
	NetworkRules []NetworkRule `json:"networkRules" url:"-"`
}

// policyListOut wraps all compiled policies belonging to the caller's org.
type policyListOut struct {
	// Data lists compiled policy summaries strictly owned by the caller's org.
	Data []PolicySummary `json:"data"`
}

// policyRef addresses one compiled policy by path parameter.
type policyRef struct {
	// ID is the policy identifier from the URL path.
	ID string `json:"id"`
}

// policyDetailOut returns compiled policy metadata and seccomp-BPF program artifact.
type policyDetailOut struct {
	// Summary holds the compiled policy metadata and digests.
	Summary PolicySummary `json:"summary"`
	// BPF holds the kernel-ready classic BPF seccomp instruction stream.
	BPF BPFProgram `json:"bpf"`
}

// bindSandboxIn binds a sandbox (identified by URL path :id) to a compiled policy
// belonging strictly to the caller's validated org. Body fields carry `url:"-"`.
type bindSandboxIn struct {
	// ID is the sandbox identifier from the URL path.
	ID string `json:"id"`
	// PolicyID is the compiled policy identifier to attach to the sandbox.
	PolicyID string `json:"policyId" url:"-"`
}

// bindSandboxOut confirms a sandbox-to-policy binding.
type bindSandboxOut struct {
	// SandboxID is the bound sandbox identifier.
	SandboxID string `json:"sandboxId"`
	// PolicyID is the bound compiled policy identifier.
	PolicyID string `json:"policyId"`
	// PolicyVersion is the bound compiled policy version.
	PolicyVersion uint64 `json:"policyVersion"`
	// BPFDigest is the SHA-256 digest of the compiled seccomp-BPF program attached.
	BPFDigest string `json:"bpfDigest"`
}

// evaluateIn submits a single runtime event for deterministic policy enforcement
// strictly under the caller's server-stamped org. Every field carries `url:"-"`.
type evaluateIn struct {
	// SandboxID identifies the sandbox emitting the runtime event.
	SandboxID string `json:"sandboxId" url:"-"`
	// Domain selects the enforcement domain ("syscall", "process", or "network").
	Domain string `json:"domain" url:"-"`
	// Syscall carries the syscall event payload when Domain is "syscall".
	Syscall *SyscallEvent `json:"syscall" url:"-"`
	// Process carries the process execution payload when Domain is "process".
	Process *ProcessEvent `json:"process" url:"-"`
	// Network carries the network egress or bind payload when Domain is "network".
	Network *NetworkEvent `json:"network" url:"-"`
}

// auditQueryIn filters the caller's org hash-chained audit ring.
type auditQueryIn struct {
	// SandboxID optionally filters returned audit events to a single org-owned sandbox.
	SandboxID string `json:"sandboxId" query:"sandboxId"`
	// Limit optionally caps the number of latest audit events returned (1..4096).
	Limit int `json:"limit" query:"limit"`
}

// auditListOut returns hash-chained audit events and chain verification status
// strictly scoped to the caller's validated org.
type auditListOut struct {
	// Events lists retained hash-chained audit events in chronological order.
	Events []AuditEvent `json:"events"`
	// ChainValid is true when VerifyAuditChain succeeds over Events.
	ChainValid bool `json:"chainValid"`
	// Ring reports bounded ring capacity, retention, drop, and head-hash telemetry.
	Ring AuditRingStats `json:"ring"`
}

// health returns readiness and aggregate non-tenant enforcement counters.
func (o ops) health(_ context.Context, _ *struct{}) (*healthOut, error) {
	h := o.s.engine.Health()
	return &healthOut{
		Status:           "ok",
		Policies:         h.Policies,
		Sandboxes:        h.Sandboxes,
		TotalEvaluations: h.TotalEvaluations,
		TotalDenied:      h.TotalDenied,
		TotalSigkills:    h.TotalSigkills,
	}, nil
}

// upsertPolicy validates, compiles (with seccomp-BPF bytecode), and stores a
// TracingPolicy strictly within the caller's authenticated org.
func (o ops) upsertPolicy(ctx context.Context, in *upsertPolicyIn) (*PolicySummary, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	spec := TracingPolicy{
		ID:                  in.ID,
		Name:                in.Name,
		Version:             in.Version,
		Mode:                EnforcementMode(in.Mode),
		Arch:                in.Arch,
		DefaultSyscallAllow: in.DefaultSyscallAllow,
		DefaultProcessAllow: in.DefaultProcessAllow,
		DefaultNetworkAllow: in.DefaultNetworkAllow,
		DenyPrivEsc:         in.DenyPrivEsc,
		AuditAllowEvents:    in.AuditAllowEvents,
		SyscallRules:        in.SyscallRules,
		ProcessRules:        in.ProcessRules,
		NetworkRules:        in.NetworkRules,
	}
	cp, err := o.s.engine.UpsertPolicy(org, spec)
	if err != nil {
		if errors.Is(err, ErrPolicyConflict) {
			return nil, zip.ErrConflict(err.Error())
		}
		if errors.Is(err, ErrTenantLimitExceeded) {
			return nil, zip.Errorf(http.StatusTooManyRequests, "%s", err.Error())
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	sum := SummarizePolicy(cp)
	return &sum, nil
}

// listPolicies lists all compiled TracingPolicies owned strictly by the caller's org.
func (o ops) listPolicies(ctx context.Context, _ *struct{}) (*policyListOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	return &policyListOut{Data: o.s.engine.ListPolicies(org)}, nil
}

// getPolicy fetches one compiled policy and its kernel-ready seccomp-BPF filter
// program belonging strictly to the caller's org. Policies owned by another org return 404.
func (o ops) getPolicy(ctx context.Context, in *policyRef) (*policyDetailOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	cp, err := o.s.engine.GetPolicy(org, in.ID)
	if err != nil {
		if errors.Is(err, ErrPolicyNotFound) {
			return nil, zip.ErrNotFound("policy not found")
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &policyDetailOut{
		Summary: SummarizePolicy(cp),
		BPF:     cp.BPF,
	}, nil
}

// bindSandbox attaches a compiled org-owned policy to a sandbox within the caller's org.
func (o ops) bindSandbox(ctx context.Context, in *bindSandboxIn) (*bindSandboxOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.PolicyID) == "" {
		return nil, zip.ErrBadRequest("sandbox id and policyId are required")
	}
	cp, err := o.s.engine.BindSandbox(org, in.ID, in.PolicyID)
	if err != nil {
		if errors.Is(err, ErrPolicyNotFound) {
			return nil, zip.ErrNotFound("policy not found")
		}
		if errors.Is(err, ErrTenantLimitExceeded) {
			return nil, zip.Errorf(http.StatusTooManyRequests, "%s", err.Error())
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &bindSandboxOut{
		SandboxID:     strings.TrimSpace(in.ID),
		PolicyID:      cp.Spec.ID,
		PolicyVersion: cp.Spec.Version,
		BPFDigest:     cp.BPFDigest,
	}, nil
}

// evaluate deterministically evaluates a syscall, process, or network event
// against the sandbox's bound policy strictly within the caller's validated org.
func (o ops) evaluate(ctx context.Context, in *evaluateIn) (*DecisionResult, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	sb := strings.TrimSpace(in.SandboxID)
	if sb == "" || len(sb) > MaxFieldLen {
		return nil, zip.ErrBadRequest("invalid sandboxId")
	}
	dom := Domain(strings.ToLower(strings.TrimSpace(in.Domain)))
	if dom != DomainSyscall && dom != DomainProcess && dom != DomainNetwork {
		return nil, zip.ErrBadRequest("domain must be syscall, process, or network")
	}
	res := o.s.engine.Evaluate(EvaluationRequest{
		TenantID:  org, // Strictly server-stamped from authenticated IAM principal.
		SandboxID: sb,
		Domain:    dom,
		Syscall:   in.Syscall,
		Process:   in.Process,
		Network:   in.Network,
	})
	return &res, nil
}

// listAudit returns the caller's org-isolated hash-chained audit events and chain
// verification status.
func (o ops) listAudit(ctx context.Context, in *auditQueryIn) (*auditListOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit < 0 || limit > maxAuditQueryLimit {
		return nil, zip.ErrBadRequest(fmt.Sprintf("limit must be in [0, %d]", maxAuditQueryLimit))
	}
	if len(in.SandboxID) > MaxFieldLen {
		return nil, zip.ErrBadRequest("sandboxId exceeds maximum length")
	}
	evs, stats := o.s.engine.AuditEvents(org, in.SandboxID, limit)
	// When unfiltered, verify contiguous hash-chain integrity across the retained window.
	// When filtered by sandboxID, verify each event's own SHA-256 integrity.
	chainOK := true
	if strings.TrimSpace(in.SandboxID) == "" {
		chainOK = VerifyAuditChain(evs) == nil
	} else {
		for _, ev := range evs {
			if ComputeAuditEventHash(ev) != ev.Hash {
				chainOK = false
				break
			}
		}
	}
	return &auditListOut{
		Events:     evs,
		ChainValid: chainOK,
		Ring:       stats,
	}, nil
}

// metrics returns enforcement decision counters and audit ring health strictly
// scoped to the caller's authenticated org.
func (o ops) metrics(ctx context.Context, _ *struct{}) (*TenantMetrics, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	m := o.s.engine.TenantStats(org)
	return &m, nil
}
