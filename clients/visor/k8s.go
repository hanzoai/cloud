// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// k8s.go is the UNIFIED /v1/k8s surface — the ONE Kubernetes noun on api.hanzo.ai:
// list clusters, one cluster's detail (node pools + worker nodes), DEPLOY (create)
// and delete DOKS clusters, and the fleet-wide worker NODES. Every route is a thin,
// tenant-scoped proxy to Visor (which OWNS the DigitalOcean lifecycle); this client
// fabricates nothing — a cluster row is a real DOKS cluster, its nodes are real
// droplets, and an honestly-absent field is omitted, never invented.
//
// Surface (org taken verbatim from the validated IAM owner claim, never a client
// field, so a caller only ever sees or mutates its OWN tenant's clusters):
//
//	GET    /v1/k8s/clusters        list the org's DOKS clusters (+ BYO fold-in) -> {clusters:[clusterView]}
//	GET    /v1/k8s/clusters/:id    one cluster's detail: pools + worker nodes   -> clusterDetailView (404 if absent)
//	POST   /v1/k8s/clusters        provision a DOKS cluster    (ADMIN-GATED)     -> clusterView (201)
//	DELETE /v1/k8s/clusters/:id    destroy a DOKS cluster      (ADMIN-GATED)     -> 204
//	GET    /v1/k8s/nodes           every DOKS worker node as a machine          -> {nodes:[machineView]}
//
// READS are org-scoped (any validated member of the org). MUTATIONS (create/delete)
// are admin-gated — a SuperAdmin (platform sudo) OR an OrgAdmin of the owning org —
// because provisioning spends real infrastructure on Hanzo's house account. The gate
// is the SAME principal predicate the rest of the cloud mutating surface uses.
package visor

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// ---- Visor wire structs (service.KubernetesCluster / KubernetesClusterDetail) ----

// visorKubernetesCluster mirrors visor/service.KubernetesCluster.
type visorKubernetesCluster struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	RegionSlug string   `json:"regionSlug"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
}

// visorK8sNodePool mirrors visor/service.NodePool (the cluster-detail subset). Its
// pool id arrives as `id` (not the `poolId` the verb node-pool surface emits), so it
// is a distinct wire type mapped explicitly to nodePoolView.
type visorK8sNodePool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	MinNodes  int    `json:"minNodes"`
	MaxNodes  int    `json:"maxNodes"`
	AutoScale bool   `json:"autoScale"`
}

// visorKubernetesClusterDetail mirrors visor/service.KubernetesClusterDetail: the
// cluster flattened, plus its node pools and worker nodes (as machines).
type visorKubernetesClusterDetail struct {
	visorKubernetesCluster
	NodePools []visorK8sNodePool `json:"nodePools"`
	Nodes     []visorMachine     `json:"nodes"`
}

// clusterDetailView is the GET .../clusters/:id shape: the cluster (with its pools)
// plus the worker nodes as the SAME machineView the machines surface emits.
type clusterDetailView struct {
	clusterView
	Nodes []machineView `json:"nodes"`
}

// k8sClusterView maps a Visor cluster to the console clusterView. Kind is "managed"
// (DOKS-provisioned); pool detail is carried by the DETAIL endpoint, so the list row
// leaves NodePools empty (honest — the list is lightweight, not a fabricated 0-pool).
func k8sClusterView(kc visorKubernetesCluster) clusterView {
	return clusterView{
		DoksClusterID: kc.ID,
		DoClusterID:   kc.ID,
		Name:          kc.Name,
		Region:        kc.RegionSlug,
		Status:        firstNonEmpty(kc.Status, "unknown"),
		Kind:          "managed",
		NodePools:     []nodePoolView{},
	}
}

// requireClusterAdmin is the mutation gate: platform SuperAdmin OR an OrgAdmin of the
// caller's own org. Returns a 403 error when neither holds (the caller returns it).
func requireClusterAdmin(c *zip.Ctx) error {
	if principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c) {
		return nil
	}
	return zip.ErrForbidden("admin required: DOKS cluster provisioning is admin-gated")
}

// ---- handlers ----

// listK8sClusters lists the org's DOKS clusters (Visor, house account) folded with
// the org's BYO clusters — ONE fleet cluster view under the unified k8s noun. A Visor
// outage is logged and skipped so a down optional provider never hides the BYO list.
func listK8sClusters(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var clusters []visorKubernetesCluster
	if err := s.State.cl.call(c, http.MethodGet, "/v1/k8s/clusters", q("owner", org), nil, &clusters); err != nil {
		s.Log.Warn("visor k8s clusters failed; returning BYO-only cluster list", "org", org, "err", err)
		clusters = nil
	}
	out := make([]clusterView, 0, len(clusters))
	for _, kc := range clusters {
		out = append(out, k8sClusterView(kc))
	}
	out = append(out, byoClusters(s, org, project(c))...)
	return c.JSON(http.StatusOK, map[string]any{"clusters": out})
}

// getK8sCluster returns one cluster's detail: node pools + worker nodes. Visor scopes
// the lookup to the org (a foreign or missing id resolves to not-found), so a tenant
// can never read another tenant's cluster by guessing an id.
func getK8sCluster(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("cluster id required")
	}
	var d visorKubernetesClusterDetail
	if err := s.State.cl.call(c, http.MethodGet, "/v1/k8s/clusters/"+id, q("owner", org), nil, &d); err != nil {
		return err
	}
	if d.ID == "" && d.Name == "" {
		return zip.ErrNotFound("cluster not found")
	}
	view := clusterDetailView{clusterView: k8sClusterView(d.visorKubernetesCluster)}
	view.NodePools = make([]nodePoolView, 0, len(d.NodePools))
	for _, p := range d.NodePools {
		view.NodePools = append(view.NodePools, nodePoolView{
			PoolID: firstNonEmpty(p.ID, p.Name), Name: p.Name, Size: p.Size,
			Count: p.Count, MinNodes: p.MinNodes, MaxNodes: p.MaxNodes, AutoScale: p.AutoScale,
		})
		view.NodeCount += p.Count
		if view.NodeSize == "" {
			view.NodeSize = p.Size
		}
	}
	view.Nodes = make([]machineView, 0, len(d.Nodes))
	for _, m := range d.Nodes {
		view.Nodes = append(view.Nodes, toMachineView(m))
	}
	return c.JSON(http.StatusOK, view)
}

// createClusterReq is the provision body: identity, placement, version and ONE seed
// node pool. It IS Visor's CreateClusterSpec shape, so it forwards without re-mapping.
type createClusterReq struct {
	Name     string `json:"name"`
	Region   string `json:"region"`
	Version  string `json:"version,omitempty"`
	NodePool struct {
		Name  string `json:"name,omitempty"`
		Size  string `json:"size"`
		Count int    `json:"count"`
	} `json:"nodePool"`
}

// createK8sCluster provisions a DOKS cluster for the caller's org. ADMIN-GATED: real
// infrastructure spend on the house account. Validated at this boundary, then Visor
// owns provisioning + the hanzo-org ownership tag.
func createK8sCluster(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if err := requireClusterAdmin(c); err != nil {
		return err
	}
	var body createClusterReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Region = strings.TrimSpace(body.Region)
	body.NodePool.Size = strings.TrimSpace(body.NodePool.Size)
	if body.Name == "" {
		return zip.ErrBadRequest("'name' is required")
	}
	if body.Region == "" {
		return zip.ErrBadRequest("'region' is required")
	}
	if body.NodePool.Size == "" {
		return zip.ErrBadRequest("'nodePool.size' is required")
	}
	if body.NodePool.Count < 1 {
		return zip.ErrBadRequest("'nodePool.count' must be at least 1")
	}
	var kc visorKubernetesCluster
	if err := s.State.cl.call(c, http.MethodPost, "/v1/k8s/clusters", q("owner", org), body, &kc); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, k8sClusterView(kc))
}

// deleteK8sCluster destroys a DOKS cluster by id. ADMIN-GATED, like create. Visor
// scopes the delete to the org (refuses a foreign id), so this can only ever remove
// the caller org's own cluster.
func deleteK8sCluster(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if err := requireClusterAdmin(c); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("cluster id required")
	}
	if err := s.State.cl.call(c, http.MethodDelete, "/v1/k8s/clusters/"+id, q("owner", org), nil, nil); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// listK8sNodes returns every DOKS worker node in the org's clusters as a machine —
// the SAME set the fleet folds in (managedMachines), exposed directly under the k8s
// noun. House account (hanzo-org cluster tag) + BYOC, deduped by Visor.
func listK8sNodes(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var nodes []visorMachine
	if err := s.State.cl.call(c, http.MethodGet, "/v1/k8s/nodes", q("owner", org), nil, &nodes); err != nil {
		return err
	}
	out := make([]machineView, 0, len(nodes))
	for _, m := range nodes {
		out = append(out, toMachineView(m))
	}
	return c.JSON(http.StatusOK, map[string]any{"nodes": out})
}
