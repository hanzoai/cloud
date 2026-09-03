// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visor

// BYO clusters on the ONE fleet surface. Attaching an existing cluster (kubeconfig)
// and Visor-provisioned clusters both live under /v1/visor/clusters — a customer brings
// their own compute (BYO GPU / bare metal / any k8s), sees it in their fleet, and
// schedules work on it, exactly like a managed cluster. Tenant-scoped: the org is
// the ZAP-propagated, gateway-validated owner (never a client field), so BYO clusters
// are per-org isolated. Billed a nominal management fee (customer brings the compute).

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// byoClusterKind is the billing meter/fee key. The nominal fee rides the shared
// compute-fee config (CLOUD_COMPUTE_FEE_CENTS, whose real source is the pricing
// service) — no bespoke env var. Customer brings compute; Hanzo meters the mgmt plane.
const byoClusterKind = "byo-cluster"

// clusterAttach is the BYO attach body: the cluster's name and the kubeconfig that
// reaches it. The kubeconfig is KMS-sealed at rest and never echoed back.
type clusterAttach struct {
	// Name is the fleet-local name for the cluster; lower-cased, and the key the
	// detach route addresses it by. Required.
	Name string `json:"name"`
	// Kubeconfig is the cluster's kubeconfig, verbatim. Required — a body without
	// one is not an attach.
	Kubeconfig string `json:"kubeconfig"`
	// Provider is a free-form label for where the cluster runs ("gke", "on-prem");
	// it is display only, not a routing key.
	Provider string `json:"provider"`
	// Default marks this the org's default cluster for scheduling.
	Default bool `json:"default"`
}

// clusterDetached names the cluster a detach removed.
type clusterDetached struct {
	// Detached is the lower-cased fleet name that was removed.
	Detached string `json:"detached"`
}

// attachCluster attaches a BYO cluster to the caller's org — the kubeconfig is
// validated, KMS-sealed and added to the fleet — and answers 201 with the cluster
// as it now appears on GET /v1/visor/clusters. Billed the nominal management fee: the
// customer brings the compute, Hanzo meters the management plane.
//
// Example: {"name":"lab","kubeconfig":"apiVersion: v1\nkind: Config\n...","provider":"on-prem","default":false}
// Response: {"name":"lab","region":"on-prem","status":"attached","nodePools":[],"nodeCount":3,"kind":"byo","nvidiaGpu":2}
func (o ops) attachCluster(ctx context.Context, in *clusterAttach) (*clusterView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		return nil, zip.ErrBadRequest("'name' is required")
	}
	if strings.TrimSpace(in.Kubeconfig) == "" {
		return nil, zip.ErrBadRequest("'kubeconfig' is required (BYO cluster attach)")
	}
	if !o.State.fleet.Enabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "BYO cluster attach not configured on this deployment (KMS required)")
	}
	// Nominal management-fee gate (fail-closed, per-org — billing keys on the paying
	// org, not the project sub-scope).
	fee := cloud.ResourceFeeCents("CLOUD_COMPUTE_FEE_CENTS", byoClusterKind)
	_, projectValidated := principal.ValidatedProject(c)
	if err := o.State.bill.Authorize(c.Context(), principal.Payer(c), principal.Project(c), projectValidated, byoClusterKind, fee); err != nil {
		return nil, cloud.DenyResource(c, err)
	}
	rec, err := o.State.fleet.Register(c.Context(), org, project(c), name, in.Kubeconfig, in.Provider, in.Default)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	o.State.bill.Record(principal.Payer(c), byoClusterKind, metering.Usage{
		Model:       byoClusterKind,
		AmountCents: fee,
		Project:     principal.Project(c),
		RequestID:   c.RequestID(),
		ClientIP:    cloud.ClientIP(c),
	})
	cloud.Created(ctx)
	v := byoToClusterView(rec)
	return &v, nil
}

// clusterRef addresses ONE BYO cluster in the org's fleet.
type clusterRef struct {
	// ID is the cluster's fleet name (the `name` it was attached under), matched
	// lower-cased.
	ID string `json:"id"`
}

// detachCluster removes a BYO cluster from the caller org's fleet. It only ever
// touches BYO clusters — a managed cluster's nodes are removed through the node-pool
// routes — and answers 404 when the name is not in this org's fleet.
//
// Response: {"detached":"lab"}
func (o ops) detachCluster(ctx context.Context, in *clusterRef) (*clusterDetached, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(in.ID))
	if name == "" {
		return nil, zip.ErrBadRequest("cluster id required")
	}
	found, err := o.State.fleet.Deregister(ctx, org, project(c), name)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "detach: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("BYO cluster not found in your fleet")
	}
	return &clusterDetached{Detached: name}, nil
}

// byoClusters returns the org+project's BYO clusters as clusterViews for the fleet
// merge. The default project resolves the legacy org-only shard (unchanged view).
func byoClusters(ctx context.Context, s *cloud.Service[state], org, project string) []clusterView {
	list, err := s.State.fleet.List(ctx, org, project)
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]clusterView, 0, len(list))
	for _, cl := range list {
		out = append(out, byoToClusterView(cl))
	}
	return out
}

// byoToClusterView maps a fleet.Cluster into the console's clusterView shape.
func byoToClusterView(cl fleet.Cluster) clusterView {
	return clusterView{
		Name:      cl.Name,
		Region:    cl.Provider,
		Status:    "attached",
		Kind:      "byo",
		NodeCount: cl.Nodes,
		NvidiaGPU: cl.NvidiaGPU,
		AmdGPU:    cl.AmdGPU,
		CreatedAt: cl.Registered,
		NodePools: []nodePoolView{},
	}
}
