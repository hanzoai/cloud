// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visor

// BYO clusters on the ONE fleet surface. Attaching an existing cluster (kubeconfig)
// and Visor-provisioned clusters both live under /v1/clusters — a customer brings
// their own compute (BYO GPU / bare metal / any k8s), sees it in their fleet, and
// schedules work on it, exactly like a managed cluster. Tenant-scoped: the org is
// the ZAP-propagated, gateway-validated owner (never a client field), so BYO clusters
// are per-org isolated. Billed a nominal management fee (customer brings the compute).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/fleet"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// byoClusterKind is the billing meter/fee key. The nominal fee rides the shared
// compute-fee config (CLOUD_COMPUTE_FEE_CENTS, whose real source is the pricing
// service) — no bespoke env var. Customer brings compute; Hanzo meters the mgmt plane.
const byoClusterKind = "byo-cluster"

// attachCluster (POST /v1/clusters) attaches a BYO cluster to the caller's org: a
// kubeconfig body = BYO attach (validated, KMS-sealed, added to the fleet). Managed
// provisioning (a provider+spec body → Visor) is the future sibling variant.
func attachCluster(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var req struct {
		Name       string `json:"name"`
		Kubeconfig string `json:"kubeconfig"`
		Provider   string `json:"provider"`
		Default    bool   `json:"default"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		return zip.ErrBadRequest("'name' is required")
	}
	if strings.TrimSpace(req.Kubeconfig) == "" {
		return zip.ErrBadRequest("'kubeconfig' is required (BYO cluster attach)")
	}
	if !s.State.fleet.Enabled() {
		return zip.Errorf(http.StatusServiceUnavailable, "BYO cluster attach not configured on this deployment (KMS required)")
	}
	// Nominal management-fee gate (fail-closed, per-org — billing keys on the paying
	// org, not the project sub-scope).
	fee := cloud.ResourceFeeCents("CLOUD_COMPUTE_FEE_CENTS", byoClusterKind)
	if err := s.State.bill.Gate(c.Context(), org, principal.Project(c), byoClusterKind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}
	rec, err := s.State.fleet.Register(c.Context(), org, project(c), name, req.Kubeconfig, req.Provider, req.Default)
	if err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	s.State.bill.Meter(org, principal.Project(c), byoClusterKind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, byoToClusterView(rec))
}

// detachCluster (DELETE /v1/clusters/:id) removes a BYO cluster from the org's fleet.
// Only touches BYO clusters; managed node-pool deletes use the deeper pool routes.
func detachCluster(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := strings.ToLower(strings.TrimSpace(c.Param("id")))
	if name == "" {
		return zip.ErrBadRequest("cluster id required")
	}
	found, err := s.State.fleet.Deregister(org, project(c), name)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "detach: %v", err)
	}
	if !found {
		return zip.ErrNotFound("BYO cluster not found in your fleet")
	}
	return c.JSON(http.StatusOK, map[string]any{"detached": name})
}

// byoClusters returns the org+project's BYO clusters as clusterViews for the fleet
// merge. The default project resolves the legacy org-only shard (unchanged view).
func byoClusters(s *cloud.Service[state], org, project string) []clusterView {
	list, err := s.State.fleet.List(org, project)
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
