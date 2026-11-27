package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// engineReplica is the native router's worker state. Unknown measurements remain
// absent; roles and capacity describe placement, not the model's GPU scheduler.
type engineReplica struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	Healthy       bool     `json:"healthy"`
	Inflight      int      `json:"inflight"`
	Capacity      int      `json:"capacity"`
	Weight        int      `json:"weight"`
	Roles         []string `json:"roles"`
	UpstreamModel *string  `json:"upstream_model"`
	TTFT          *float64 `json:"ttft_ewma_ms"`
}

type engineReplicaPool map[string][]engineReplica

// engineReplicaUpdate retunes a worker already enrolled by deployment config.
// Neither addresses nor model aliases are writable through this operation.
type engineReplicaUpdate struct {
	Model    string   `json:"model"`
	ID       string   `json:"id"`
	Capacity int      `json:"capacity"`
	Weight   int      `json:"weight"`
	Roles    []string `json:"roles"`
}

func replicaOperator(ctx context.Context) error {
	if err := caller(ctx); err != nil {
		return err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return principal.RefusedFrom(ctx)
	}
	if !principal.Super(c) {
		return zip.ErrForbidden("shared inference workers require SuperAdmin")
	}
	return nil
}

// Replicas reads the private native router's configured model pools, worker
// health, concurrency, role preferences and observed time to first byte.
// This shared infrastructure inventory is restricted to platform SuperAdmins.
func (o ops) replicas(ctx context.Context, _ *engineNoInput) (*engineReplicaPool, error) {
	if err := replicaOperator(ctx); err != nil {
		return nil, err
	}
	return replicaCall(ctx, http.MethodGet, nil)
}

// UpdateReplica saves capacity, weight and preferred agent roles in the native
// router's pool file and applies them without dropping running streams or pins.
// Only platform SuperAdmins can retune shared workers. The Rust runtime owns
// validation, durable storage and activation; cloud only carries the operation.
func (o ops) updateReplica(ctx context.Context, in *engineReplicaUpdate) (*engineReplicaPool, error) {
	if err := replicaOperator(ctx); err != nil {
		return nil, err
	}
	if in.Model == "" || in.ID == "" || in.Capacity < 1 || in.Capacity > 4096 || in.Weight < 1 || in.Weight > 1000000 || len(in.Roles) > 16 {
		return nil, zip.ErrBadRequest("provide a model, worker ID, capacity (1–4096), weight (1–1000000), and up to 16 roles")
	}
	if in.Roles == nil {
		in.Roles = []string{}
	}
	return replicaCall(ctx, http.MethodPut, in)
}

func replicaCall(ctx context.Context, method string, input any) (*engineReplicaPool, error) {
	base := strings.TrimRight(env.Or("ENGINE_ROUTER_UPSTREAM", ""), "/")
	if base == "" {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "native engine router is not configured")
	}
	st, data, err := sendTo(ctx, base, env.Or("ENGINE_ROUTER_API_KEY", ""), method, "/v1/replicas", input)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "native engine router unavailable")
	}
	if st < 200 || st >= 300 {
		return nil, engineErr(st, data)
	}
	var result engineReplicaPool
	if json.Unmarshal(data, &result) != nil || result == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "invalid engine router pool")
	}
	// Older routers cannot describe or save placement. Refuse instead of making
	// zero capacity or empty preferences look like the running configuration.
	for _, rows := range result {
		for _, row := range rows {
			if row.ID == "" || row.URL == "" || row.Capacity < 1 || row.Weight < 1 || row.Roles == nil {
				return nil, zip.Errorf(http.StatusBadGateway, "upgrade the native engine router to manage worker placement")
			}
		}
	}
	return &result, nil
}
