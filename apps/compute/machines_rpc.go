// Copyright © 2026 Hanzo AI. MIT License.

package compute

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// mountedCompute holds the live compute service for internal plane RPCs.
var mountedCompute *cloud.Service[state]

// exposeMachinesRPC publishes ComputeListMachines on the internal plane so
// /v1/machines (apps/source) can merge linked personal machines and cloud
// compute / BYO machines into one canonical inventory.
func exposeMachinesRPC(s *cloud.Service[state]) {
	mountedCompute = s
	cloud.Plane().Post("/compute/machines/list",
		listMachinesRPC,
		zip.WithOperationID(client.ComputeListMachines),
		zip.WithSummary("List the caller org's cloud compute and BYO fleet machines"))
}

func listMachinesRPC(ctx context.Context, _ *client.Unit) (*client.ComputeMachinesOut, error) {
	who := cloud.Who(ctx)
	org := strings.TrimSpace(who.Org)
	if org == "" {
		return nil, zip.ErrUnauthorized("compute: no org on the call")
	}
	if mountedCompute == nil {
		return &client.ComputeMachinesOut{Machines: []client.ComputeMachine{}}, nil
	}
	return collectComputeMachines(ctx, mountedCompute, org, strings.TrimSpace(who.User)), nil
}

func collectComputeMachines(ctx context.Context, s *cloud.Service[state], org, user string) *client.ComputeMachinesOut {
	var registry, nodes []visorMachine
	if err := s.State.cl.callCtx(ctx, org, user, http.MethodGet, "/v1/machines", q("owner", org), nil, &registry); err != nil {
		s.Log.Warn("visor machines plane rpc failed; visor machines omitted", "org", org, "err", err)
		registry = nil
	}
	var res visorNodes
	if err := s.State.cl.opCtx(ctx, org, user, http.MethodGet, "/v1/k8s/nodes", q("owner", org), nil, &res); err != nil {
		s.Log.Warn("visor k8s nodes plane rpc failed; DOKS nodes omitted", "org", org, "err", err)
	} else if res.Nodes != nil {
		nodes = res.Nodes
	}

	seen := make(map[string]struct{}, 2*(len(registry)+len(nodes)))
	out := make([]client.ComputeMachine, 0, len(registry)+len(nodes))
	addView := func(v machineView) {
		if v.ID != "" {
			if _, ok := seen["id:"+v.ID]; ok {
				return
			}
			seen["id:"+v.ID] = struct{}{}
		}
		if v.Name != "" {
			if _, ok := seen["name:"+v.Name]; ok {
				return
			}
			seen["name:"+v.Name] = struct{}{}
		}
		out = append(out, client.ComputeMachine{
			ID:          v.ID,
			Name:        v.Name,
			Provider:    v.Provider,
			Region:      v.Region,
			Type:        v.Type,
			Status:      v.Status,
			PublicIP:    v.PublicIp,
			PrivateIP:   v.PrivateIp,
			OS:          v.Os,
			GPU:         v.GPU,
			Mem:         v.Mem,
			Vcpu:        v.Vcpu,
			Agent:       v.Agent,
			CreatedTime: v.CreatedTime,
		})
	}
	for _, m := range registry {
		addView(toMachineView(m))
	}
	for _, m := range nodes {
		addView(toMachineView(m))
	}
	for _, w := range byoWorkers(ctx, org) {
		addView(byoMachineView(w))
	}
	return &client.ComputeMachinesOut{Machines: out}
}
