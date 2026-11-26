// SPDX-License-Identifier: Apache-2.0

package preble

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	maxPromptTextBytes = 2 << 20 // 2 MiB max raw prompt text over HTTP
	maxHTTPFieldLen    = 256
	maxWarmupMs        = 600_000 // 10 minutes max scale-out warmup
)

// Deps supplies the host dependencies required by the preble app.
// It imports nothing of its host so the package mounts unchanged in any zip host.
type Deps struct {
	// Log is the host logger scoped to the preble subsystem.
	Log luxlog.Logger

	// Org returns the validated org of the caller on ctx.
	Org func(ctx context.Context) (org string, ok bool)

	// Acting returns the validated org a call acts for, or a zip.ErrUnauthorized / zip.ErrForbidden.
	Acting func(ctx context.Context) (org string, err error)

	// Operator enforces that the caller is an authorized platform operator permitted to
	// mutate shared cluster GPU replicas (POST /replicas, DELETE /replicas/:id).
	// When nil, NewService defaults to a fail-closed gate requiring Acting(ctx) and
	// a validated platform SuperAdmin (zip.CallerOf(ctx).Admin).
	Operator func(ctx context.Context) error

	// Config optionally overrides default Preble E2 scheduler parameters.
	Config SchedulerConfig

	// InitialReplicas optionally seeds GPU replicas at mount time.
	InitialReplicas []ReplicaConfig
}

// Service owns the Preble E2 scheduler and host identity bindings.
type Service struct {
	log      luxlog.Logger
	org      func(context.Context) (string, bool)
	acting   func(context.Context) (string, error)
	operator func(context.Context) error
	sched    *Scheduler
}

// NewService constructs a standalone Preble Service from deps.
func NewService(deps Deps) (*Service, error) {
	if deps.Log == nil || deps.Org == nil || deps.Acting == nil {
		return nil, errors.New("preble: Deps requires Log, Org, and Acting")
	}
	operator := deps.Operator
	if operator == nil {
		operator = func(ctx context.Context) error {
			if _, err := deps.Acting(ctx); err != nil {
				return err
			}
			if !zip.CallerOf(ctx).Admin {
				return zip.ErrForbidden("platform operator authority required to mutate shared GPU replicas")
			}
			return nil
		}
	}
	sched, err := NewScheduler(deps.Config)
	if err != nil {
		return nil, err
	}
	for _, rc := range deps.InitialReplicas {
		if err := sched.AddReplica(rc); err != nil {
			return nil, fmt.Errorf("preble: initial replica %q: %w", rc.ID, err)
		}
	}
	return &Service{
		log:      deps.Log,
		org:      deps.Org,
		acting:   deps.Acting,
		operator: operator,
		sched:    sched,
	}, nil
}

// Scheduler returns the underlying Preble placement scheduler.
func (s *Service) Scheduler() *Scheduler { return s.sched }

// Mount registers the typed /v1/preble HTTP control and scheduling surface on app.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("preble: nil app")
	}
	svc, err := NewService(deps)
	if err != nil {
		return fmt.Errorf("preble.Mount: %w", err)
	}
	MountService(app, svc)
	svc.log.Info("preble scheduler mounted", "prefix", "/v1/preble", "policy", string(svc.sched.Policy()))
	return nil
}

// MountService mounts an existing Service on app at /v1/preble.
func MountService(app *zip.App, svc *Service) {
	o := ops{s: svc}
	g := app.Group("/v1/preble")
	g.Get("/health", o.health)
	g.Get("/replicas", o.listReplicas)
	g.Post("/replicas", o.addReplica, zip.WithStatus(http.StatusCreated))
	g.Delete("/replicas/:id", o.removeReplica)
	g.Post("/place", o.place)
	g.Post("/complete", o.complete)
	g.Get("/metrics", o.metrics)
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type ops struct{ s *Service }

// healthOut reports readiness and non-tenant-specific aggregate GPU KV-cache status.
type healthOut struct {
	// Status is "ok" when the Preble scheduler is ready.
	Status string `json:"status"`
	// Policy is the default scheduling policy ("preble_e2", "least_loaded", "round_robin", or "prefix_greedy").
	Policy Policy `json:"policy"`
	// Replicas is the count of currently registered GPU serving replicas.
	Replicas int `json:"replicas"`
	// TotalKVBudgetTokens is the aggregate KV-cache token capacity across all replicas.
	TotalKVBudgetTokens int64 `json:"totalKvBudgetTokens"`
	// TotalKVUsedTokens is the aggregate KV-cache tokens currently cached across all replicas.
	TotalKVUsedTokens int64 `json:"totalKvUsedTokens"`
	// TotalEvictedBlocks is the cumulative count of LRU-evicted KV blocks across the cluster.
	TotalEvictedBlocks uint64 `json:"totalEvictedBlocks"`
}

// replicaListOut wraps the aggregate status of all registered GPU replicas.
type replicaListOut struct {
	// Data lists aggregate non-tenant-specific status for each registered GPU replica.
	Data []ReplicaStatus `json:"data"`
}

// addReplicaIn registers a new GPU serving replica. Every field carries `url:"-"` so
// query parameters can never hijack or override the JSON request body.
type addReplicaIn struct {
	// ID uniquely identifies this GPU replica (1..128 characters).
	ID string `json:"id" url:"-"`
	// GPUModel describes the accelerator model (defaults to "H100-80GB").
	GPUModel string `json:"gpuModel" url:"-"`
	// KVBudgetTokens sets the exact KV-cache memory capacity in tokens (defaults to 131072).
	KVBudgetTokens int `json:"kvBudgetTokens" url:"-"`
	// PrefillTokPerSec sets sustained prefill throughput in tokens/sec (defaults to 40000).
	PrefillTokPerSec float64 `json:"prefillTokPerSec" url:"-"`
	// DecodeTokPerSec sets aggregate decode throughput in tokens/sec (defaults to 1800).
	DecodeTokPerSec float64 `json:"decodeTokPerSec" url:"-"`
	// MaxConcurrent bounds simultaneous active requests on this replica (defaults to 64).
	MaxConcurrent int `json:"maxConcurrent" url:"-"`
	// WarmupMs optionally specifies scale-out cold-start warmup duration in milliseconds.
	WarmupMs int64 `json:"warmupMs" url:"-"`
}

// replicaRef addresses one GPU replica by path parameter.
type replicaRef struct {
	// ID is the GPU replica identifier from the URL path.
	ID string `json:"id"`
}

// removeReplicaOut confirms replica removal.
type removeReplicaOut struct {
	// ID is the removed GPU replica identifier.
	ID string `json:"id"`
	// Removed is true when the replica and its prefix index memberships were removed.
	Removed bool `json:"removed"`
}

// placeIn schedules a prompt onto a GPU replica under the caller's server-stamped org.
// Tenant/org is NEVER accepted from the request body or query string; every field carries
// `url:"-"` to prevent query-string parameter hijacking.
type placeIn struct {
	// RequestID optionally pins a caller-supplied unique request identifier.
	RequestID string `json:"requestId" url:"-"`
	// ACLScope optionally isolates KV prefix reuse to a sub-org project, workspace, or role.
	ACLScope string `json:"aclScope" url:"-"`
	// TokenIDs provides pre-tokenized prompt token IDs (takes precedence over Prompt).
	TokenIDs []uint64 `json:"tokenIds" url:"-"`
	// Prompt provides raw UTF-8 prompt text tokenized deterministically if TokenIDs is empty.
	Prompt string `json:"prompt" url:"-"`
	// OutputTokens is the estimated or scheduled decode token budget.
	OutputTokens int `json:"outputTokens" url:"-"`
	// Policy optionally selects a policy override ("preble_e2", "least_loaded", "round_robin", "prefix_greedy").
	Policy string `json:"policy" url:"-"`
}

// completeIn marks an in-flight request completed to release GPU load and unpin KV blocks.
// Every field carries `url:"-"` to prevent query-string parameter hijacking.
type completeIn struct {
	// RequestID identifies the in-flight placement belonging exclusively to the caller's validated org.
	RequestID string `json:"requestId" url:"-"`
}

// completeOut confirms completion of an org-owned placement request.
type completeOut struct {
	// RequestID is the completed request identifier.
	RequestID string `json:"requestId"`
	// Completed is true once active load is released and KV prefix blocks are unpinned.
	Completed bool `json:"completed"`
}

// health returns readiness and cluster-wide aggregate KV-cache metrics with zero tenant leakage.
func (o ops) health(_ context.Context, _ *struct{}) (*healthOut, error) {
	reps := o.s.sched.Replicas()
	var totalBudget, totalUsed int64
	var totalEvicted uint64
	for _, r := range reps {
		totalBudget += int64(r.KVBudgetTokens)
		totalUsed += int64(r.KVUsedTokens)
		totalEvicted += r.EvictedBlocks
	}
	return &healthOut{
		Status:              "ok",
		Policy:              o.s.sched.Policy(),
		Replicas:            len(reps),
		TotalKVBudgetTokens: totalBudget,
		TotalKVUsedTokens:   totalUsed,
		TotalEvictedBlocks:  totalEvicted,
	}, nil
}

// listReplicas returns aggregate utilization and KV-cache occupancy for all registered GPU replicas.
func (o ops) listReplicas(ctx context.Context, _ *struct{}) (*replicaListOut, error) {
	if _, err := o.s.acting(ctx); err != nil {
		return nil, err
	}
	return &replicaListOut{Data: o.s.sched.Replicas()}, nil
}

// addReplica registers a GPU serving replica with validated KV-cache capacity and throughput bounds.
// Restricted to authorized platform operators (SuperAdmin).
func (o ops) addReplica(ctx context.Context, in *addReplicaIn) (*ReplicaStatus, error) {
	if err := o.s.operator(ctx); err != nil {
		return nil, err
	}
	if len(in.ID) > maxHTTPFieldLen || len(in.GPUModel) > maxHTTPFieldLen {
		return nil, zip.ErrBadRequest("field length exceeds limit")
	}
	if in.WarmupMs < 0 || in.WarmupMs > maxWarmupMs {
		return nil, zip.ErrBadRequest("warmupMs out of bounds [0, 600000]")
	}
	cfg := ReplicaConfig{
		ID:               in.ID,
		GPUModel:         in.GPUModel,
		KVBudgetTokens:   in.KVBudgetTokens,
		PrefillTokPerSec: in.PrefillTokPerSec,
		DecodeTokPerSec:  in.DecodeTokPerSec,
		MaxConcurrent:    in.MaxConcurrent,
		WarmupDuration:   time.Duration(in.WarmupMs) * time.Millisecond,
	}
	if err := o.s.sched.AddReplica(cfg); err != nil {
		if errors.Is(err, ErrReplicaExists) {
			return nil, zip.ErrConflict(err.Error())
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	id := strings.TrimSpace(in.ID)
	for _, st := range o.s.sched.Replicas() {
		if st.ID == id {
			res := st
			return &res, nil
		}
	}
	return nil, zip.Errorf(http.StatusInternalServerError, "preble: replica disappeared after registration")
}

// removeReplica removes a GPU replica and prunes all prefix cache entries referencing it.
// Restricted to authorized platform operators (SuperAdmin).
func (o ops) removeReplica(ctx context.Context, in *replicaRef) (*removeReplicaOut, error) {
	if err := o.s.operator(ctx); err != nil {
		return nil, err
	}
	if err := o.s.sched.RemoveReplica(in.ID); err != nil {
		if errors.Is(err, ErrReplicaNotFound) {
			return nil, zip.ErrNotFound(err.Error())
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &removeReplicaOut{ID: strings.TrimSpace(in.ID), Removed: true}, nil
}

// place computes a prompt-locality-aware GPU placement strictly within the caller's validated
// org and optional ACL scope, pinning reserved KV blocks until completion.
func (o ops) place(ctx context.Context, in *placeIn) (*PlacementDecision, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.RequestID) > maxHTTPFieldLen || len(in.ACLScope) > maxHTTPFieldLen {
		return nil, zip.ErrBadRequest("field length exceeds limit")
	}
	if len(in.Prompt) > maxPromptTextBytes {
		return nil, zip.ErrBadRequest("prompt exceeds 2 MiB limit")
	}
	if len(in.TokenIDs) > MaxPromptTokens {
		return nil, zip.ErrBadRequest("tokenIds exceeds maximum token limit")
	}
	var pol Policy
	if strings.TrimSpace(in.Policy) != "" {
		pol, err = ParsePolicy(in.Policy)
		if err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
	}
	dec, err := o.s.sched.Place(PlacementRequest{
		RequestID:    in.RequestID,
		Tenant:       org, // Strictly server-stamped from authenticated IAM principal.
		ACLScope:     in.ACLScope,
		TokenIDs:     in.TokenIDs,
		Prompt:       in.Prompt,
		OutputTokens: in.OutputTokens,
		Policy:       pol,
	})
	if err != nil {
		if errors.Is(err, ErrNoReplicas) {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
		}
		if errors.Is(err, ErrTenantLimitExceeded) {
			return nil, zip.Errorf(http.StatusTooManyRequests, "%s", err.Error())
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &dec, nil
}

// complete marks an org-owned placement request finished, releasing in-flight compute load
// and unpinning KV blocks while keeping them warm in LRU cache. Requests owned by another
// org answer 404.
func (o ops) complete(ctx context.Context, in *completeIn) (*completeOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > maxHTTPFieldLen {
		return nil, zip.ErrBadRequest("invalid requestId")
	}
	if err := o.s.sched.Complete(org, in.RequestID); err != nil {
		if errors.Is(err, ErrRequestNotFound) {
			return nil, zip.ErrNotFound("request not found")
		}
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &completeOut{RequestID: strings.TrimSpace(in.RequestID), Completed: true}, nil
}

// metrics returns prefix reuse, hot-prefix replication, and decision latency metrics
// strictly scoped to the caller's validated org plus aggregate cluster counters.
func (o ops) metrics(ctx context.Context, _ *struct{}) (*TenantMetrics, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	m := o.s.sched.TenantStats(org)
	return &m, nil
}
