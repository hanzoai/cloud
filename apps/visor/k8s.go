// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// k8s.go is the UNIFIED /v1/visor/k8s surface — the ONE Kubernetes noun on api.hanzo.ai:
// list clusters, one cluster's detail (node pools + worker nodes), DEPLOY (create)
// and delete DOKS clusters, and the fleet-wide worker NODES. Every route is a thin,
// tenant-scoped proxy to Visor (which OWNS the DigitalOcean lifecycle); this client
// fabricates nothing — a cluster row is a real DOKS cluster, its nodes are real
// droplets, and an honestly-absent field is omitted, never invented.
//
// Surface (org taken verbatim from the validated IAM owner claim, never a client
// field, so a caller only ever sees or mutates its OWN tenant's clusters):
//
//	GET    /v1/visor/k8s/clusters        list the org's DOKS clusters (+ BYO fold-in) -> {clusters:[clusterView]}
//	GET    /v1/visor/k8s/clusters/:id    one cluster's detail: pools + worker nodes   -> clusterDetailView (404 if absent)
//	POST   /v1/visor/k8s/clusters        provision a DOKS cluster    (ADMIN-GATED)     -> clusterView (201)
//	DELETE /v1/visor/k8s/clusters/:id    destroy a DOKS cluster      (ADMIN-GATED)     -> 204
//	GET    /v1/visor/k8s/nodes           every DOKS worker node as a machine          -> {nodes:[machineView]}
//
// READS are org-scoped (any validated member of the org). MUTATIONS (create/delete)
// are admin-gated — a SuperAdmin (platform sudo) OR an OrgAdmin of the owning org —
// because provisioning spends real infrastructure on Hanzo's house account. The gate
// is the SAME principal predicate the rest of the cloud mutating surface uses.

package visor

import (
	"cmp"
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
// pool id arrives as `id` (not the `poolId` a pool read emits), so it
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
	// Nodes is every worker node in the cluster, each in the same shape the
	// machines surface uses — a node IS a machine, addressable by its own id. This
	// is the individual hardware behind the pool counts above.
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
		Status:        cmp.Or(kc.Status, "unknown"),
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

// k8sClusterRef addresses ONE DOKS cluster.
type k8sClusterRef struct {
	// ID is the provider's DOKS cluster id. Visor scopes the lookup to the caller's
	// org, so another tenant's id resolves to not-found rather than their cluster.
	ID string `json:"id"`
}

// listK8sClusters lists the org's DOKS clusters (Visor, house account) folded with
// the org's BYO clusters — ONE fleet cluster view under the unified k8s noun. A Visor
// outage is logged and skipped so a down optional provider never hides the BYO list.
//
// Response: {"clusters":[{"doksClusterId":"cl-1","doClusterId":"cl-1","name":"prod","region":"nyc3","status":"running","nodePools":[],"nodeCount":0,"kind":"managed"}]}
func (o ops) listK8sClusters(ctx context.Context, _ *cloud.Unit) (*clusterList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var clusters []visorKubernetesCluster
	var down []sourceFailure
	if err := o.State.cl.call(c, http.MethodGet, "/v1/k8s/clusters", q("owner", org), nil, &clusters); err != nil {
		o.Log.Warn("visor k8s clusters failed; returning BYO-only cluster list", "org", org, "err", err)
		clusters, down = nil, visorDown(err)
	}
	out := make([]clusterView, 0, len(clusters))
	for _, kc := range clusters {
		out = append(out, k8sClusterView(kc))
	}
	out = append(out, byoClusters(ctx, o.Service, org, project(c))...)
	return &clusterList{Clusters: out, Degraded: down}, nil
}

// getK8sCluster returns one cluster's detail: node pools + worker nodes. Visor scopes
// the lookup to the org (a foreign or missing id resolves to not-found), so a tenant
// can never read another tenant's cluster by guessing an id.
//
// Response: {"doksClusterId":"cl-1","name":"prod","region":"nyc3","status":"running","nodePools":[{"poolId":"p-1","name":"gpu","size":"gpu-h100x8-640gb","count":1}],"nodeSize":"gpu-h100x8-640gb","nodeCount":1,"kind":"managed","nodes":[{"id":"node-1","name":"node-1","status":"active"}]}
func (o ops) getK8sCluster(ctx context.Context, in *k8sClusterRef) (*clusterDetailView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("cluster id required")
	}
	var d visorKubernetesClusterDetail
	if err := o.State.cl.call(c, http.MethodGet, "/v1/k8s/clusters/"+id, q("owner", org), nil, &d); err != nil {
		return nil, err
	}
	if d.ID == "" && d.Name == "" {
		return nil, zip.ErrNotFound("cluster not found")
	}
	view := clusterDetailView{clusterView: k8sClusterView(d.visorKubernetesCluster)}
	view.NodePools = make([]nodePoolView, 0, len(d.NodePools))
	for _, p := range d.NodePools {
		view.NodePools = append(view.NodePools, nodePoolView{
			PoolID: cmp.Or(strings.TrimSpace(p.ID), strings.TrimSpace(p.Name)), Name: p.Name, Size: p.Size,
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
	return &view, nil
}

// createClusterReq is the provision body: identity, placement, version and ONE seed
// node pool. It IS Visor's CreateClusterSpec shape, so it forwards without re-mapping.
type createClusterReq struct {
	// Name is the cluster's name. Required.
	Name string `json:"name"`
	// Region is the provider region slug (e.g. "nyc3"). Required.
	Region string `json:"region"`
	// Version is the Kubernetes version slug; empty takes the provider default.
	Version string `json:"version,omitempty"`
	// NodePool is the ONE pool the cluster is born with — a cluster with no nodes
	// runs nothing, so it is not optional. More pools are added afterwards through
	// POST /v1/visor/clusters/:clusterId/pools.
	NodePool struct {
		// Name is the seed pool's name; empty takes the provider default.
		Name string `json:"name,omitempty"`
		// Size is the provider size slug for each node. Required.
		Size string `json:"size"`
		// Count is how many nodes the seed pool starts with. At least 1.
		Count int `json:"count"`
	} `json:"nodePool"`
}

// createK8sCluster provisions a DOKS cluster for the caller's org and answers 201.
// ADMIN-GATED — a SuperAdmin, or an OrgAdmin of the caller's own org — because
// provisioning spends real infrastructure on the house account. The request is
// validated at this boundary, then Visor owns provisioning and the hanzo-org
// ownership tag.
//
// Example: {"name":"prod","region":"nyc3","nodePool":{"name":"gpu","size":"gpu-h100x8-640gb","count":2}}
// Response: {"doksClusterId":"cl-1","doClusterId":"cl-1","name":"prod","region":"nyc3","status":"provisioning","nodePools":[],"nodeCount":0,"kind":"managed"}
func (o ops) createK8sCluster(ctx context.Context, in *createClusterReq) (*clusterView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireClusterAdmin(c); err != nil {
		return nil, err
	}
	body := *in
	body.Name = strings.TrimSpace(body.Name)
	body.Region = strings.TrimSpace(body.Region)
	body.NodePool.Size = strings.TrimSpace(body.NodePool.Size)
	if body.Name == "" {
		return nil, zip.ErrBadRequest("'name' is required")
	}
	if body.Region == "" {
		return nil, zip.ErrBadRequest("'region' is required")
	}
	if body.NodePool.Size == "" {
		return nil, zip.ErrBadRequest("'nodePool.size' is required")
	}
	if body.NodePool.Count < 1 {
		return nil, zip.ErrBadRequest("'nodePool.count' must be at least 1")
	}
	var kc visorKubernetesCluster
	if err := o.State.cl.call(c, http.MethodPost, "/v1/k8s/clusters", q("owner", org), body, &kc); err != nil {
		return nil, err
	}
	cloud.Created(ctx)
	v := k8sClusterView(kc)
	return &v, nil
}

// deleteK8sCluster destroys a DOKS cluster by id and answers 204. ADMIN-GATED, like
// create. Visor scopes the delete to the org (refuses a foreign id), so this can
// only ever remove the caller org's own cluster.
func (o ops) deleteK8sCluster(ctx context.Context, in *k8sClusterRef) (*cloud.Unit, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireClusterAdmin(c); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("cluster id required")
	}
	if err := o.State.cl.call(c, http.MethodDelete, "/v1/k8s/clusters/"+id, q("owner", org), nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// nodeList is the fleet-wide worker inventory: every DOKS node as a machine.
type nodeList struct {
	// Nodes is one row per worker node, in the SAME machineView shape the machines
	// surface emits — a node IS a machine.
	Nodes []machineView `json:"nodes"`
}

// listK8sNodes returns every DOKS worker node in the org's clusters as a machine —
// the SAME set the fleet folds in (managedMachines), exposed directly under the k8s
// noun. House account (hanzo-org cluster tag) + BYOC, deduped by Visor.
//
// Response: {"nodes":[{"id":"node-1","name":"node-1","region":"nyc3","type":"s-4vcpu-8gb","status":"active","vcpu":4}]}
func (o ops) listK8sNodes(ctx context.Context, _ *cloud.Unit) (*nodeList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var res visorNodes
	if err := o.State.cl.op(c, http.MethodGet, "/v1/k8s/nodes", q("owner", org), nil, &res); err != nil {
		return nil, err
	}
	// The op ALWAYS writes the key, so a missing one is not an empty fleet — it is
	// a Visor that does not serve this op, and answering `{"nodes":[]}` to that
	// would report an operator's clusters as having no workers. Say so instead.
	if res.Nodes == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "visor: /v1/k8s/nodes answered without a nodes list")
	}
	out := make([]machineView, 0, len(res.Nodes))
	for _, m := range res.Nodes {
		out = append(out, toMachineView(m))
	}
	return &nodeList{Nodes: out}, nil
}
