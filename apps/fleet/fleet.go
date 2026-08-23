// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package fleet is your own compute, attached: bring a Kubernetes cluster, a GPU
// box or bare metal and run work on it.
//
// It is the ONE per-org registry of that attached compute (BYO k8s clusters /
// BYO GPU / bare metal), the single source of truth consumed by BOTH the fleet
// surface (apps/visor, which serves /v1/visor/clusters — managed clusters from Visor
// MERGED with these BYO ones) AND ML serving (apps/ml, whose dynForOrg federates a
// workload onto the org's registered cluster). One registry, two consumers — never a
// second cluster surface.
//
// Tenant isolation is the org boundary, narrowed by the org SUB-SCOPE (project):
// the kubeconfig is sealed in the org's KMS (MPC nodes see only ciphertext) under a
// per-org(+project) ref prefix, and every method takes the org + project as resolved
// from the ZAP-propagated, gateway-validated X-Org-Id / X-Project-Id — never a client
// field. So lux sees only lux's clusters, zoo only zoo's, a customer only their own —
// and, within an org, one project's fleet is a distinct shard. The DEFAULT project
// (principal.IsDefaultProject) keeps the legacy org-only key, so existing single-
// project fleets are untouched (scopeRef).
package fleet

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	kms "github.com/hanzoai/cloud/apps/mpc"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// allowPrivateHostsEnv, when set, disables the SSRF guard's non-routable-address
// rejection on a fold target's apiserver host. Off in production; set ONLY in
// tests whose apiserver runs on loopback. Mirrors clients/git's private-host env.
const allowPrivateHostsEnv = "FLEET_ALLOW_PRIVATE_HOSTS"

var nodeListGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}

// Cluster is an attached compute source (metadata only — the kubeconfig is sealed
// separately in KMS and never surfaced). Owned by exactly one org.
type Cluster struct {
	Name       string `json:"name"`
	Org        string `json:"org"`
	Kind       string `json:"kind"`     // byo | byo-gpu | metal (managed clusters come from Visor, not here)
	Provider   string `json:"provider"` // byo | k3s | ...
	Endpoint   string `json:"endpoint,omitempty"`
	Nodes      int    `json:"nodes"`
	NvidiaGPU  int    `json:"nvidiaGpu"`
	AmdGPU     int    `json:"amdGpu"`
	Registered string `json:"registered"`
	Default    bool   `json:"default"`
}

// Registry is the KMS-backed BYO-cluster store. A nil KMS (unconfigured) makes it
// disabled: Register fails closed (never plaintext) and DynForOrg/List no-op.
type Registry struct {
	kms *kms.Client
	log luxlog.Logger
	// dynCache holds federated per-cluster clients (key "org/name") so a registered
	// cluster becomes the org's compute plane without rebuilding on every request.
	cache map[string]dynamic.Interface
}

// New opens the registry against the deployment's KMS (CLOUD_KMS_NODES /
// CLOUD_KMS_PASSPHRASE — the ONE bootstrap env, shared with the rest of cloud). Nil
// KMS => Enabled() is false and the registry is a graceful no-op.
func New(brand string, log luxlog.Logger) *Registry {
	return &Registry{kms: openKMS(brand), log: log, cache: map[string]dynamic.Interface{}}
}

// Enabled reports whether BYO registration can persist (KMS reachable).
func (r *Registry) Enabled() bool { return r != nil && r.kms != nil }

// scopeRef is the KMS key prefix for an org's fleet within a project — the ONE
// backward-compat client. The default project (principal.IsDefaultProject) keeps the
// legacy org-only prefix so existing keys are byte-identical; a non-default project
// shards under "<org>/<project>". Every fleet key (index, kubeconfig, cache) derives
// from here, so there is exactly one place the project segment is added or omitted.
func scopeRef(org, project string) string {
	if principal.IsDefaultProject(project) {
		return org
	}
	return org + "/" + project
}

func indexRef(org, project string) string { return scopeRef(org, project) + "/fleet/clusters" }
func configRef(org, project, name string) string {
	return scopeRef(org, project) + "/fleet/clusters/" + name + "/kubeconfig"
}

// List returns the org+project's registered BYO clusters (metadata only). Absent == empty.
func (r *Registry) List(org, project string) ([]Cluster, error) {
	if !r.Enabled() {
		return nil, nil
	}
	raw, err := r.kms.Get(indexRef(org, project))
	if err != nil || len(raw) == 0 {
		return nil, nil
	}
	var list []Cluster
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("corrupt fleet index for %s: %w", scopeRef(org, project), err)
	}
	return list, nil
}

// Register attaches a BYO cluster to the org+project fleet: validate by REACHING it
// (node + GPU inventory), seal the kubeconfig in the org's KMS, and index the
// metadata. Idempotent on name within the (org, project) shard.
func (r *Registry) Register(ctx context.Context, org, project, name, kubeconfig, provider string, isDefault bool) (Cluster, error) {
	if !r.Enabled() {
		return Cluster{}, fmt.Errorf("fleet registration requires KMS (set CLOUD_KMS_NODES + CLOUD_KMS_PASSPHRASE)")
	}
	kubeBytes := []byte(kubeconfig)
	dyn, endpoint, err := dynFromKubeconfig(kubeBytes)
	if err != nil {
		return Cluster{}, fmt.Errorf("kubeconfig unusable: %w", err)
	}
	inv, err := inventoryOf(ctx, dyn)
	if err != nil {
		return Cluster{}, fmt.Errorf("cluster unreachable with this kubeconfig: %w", err)
	}
	if err := r.kms.Set(configRef(org, project, name), kubeBytes); err != nil {
		return Cluster{}, fmt.Errorf("seal kubeconfig: %w", err)
	}
	list, err := r.List(org, project)
	if err != nil {
		return Cluster{}, err
	}
	rec := Cluster{
		Name: name, Org: org, Kind: "byo", Provider: cmp.Or(strings.TrimSpace(provider), "byo"),
		Endpoint: endpoint, Nodes: inv.nodes, NvidiaGPU: inv.nvidia, AmdGPU: inv.amd,
		Registered: time.Now().UTC().Format(time.RFC3339), Default: isDefault,
	}
	list = upsert(list, rec)
	if err := r.writeIndex(org, project, list); err != nil {
		return Cluster{}, err
	}
	r.cache[scopeRef(org, project)+"/"+name] = dyn
	return rec, nil
}

// Deregister detaches a BYO cluster from the org+project fleet (index + sealed
// kubeconfig + cached client).
func (r *Registry) Deregister(org, project, name string) (bool, error) {
	if !r.Enabled() {
		return false, nil
	}
	list, err := r.List(org, project)
	if err != nil {
		return false, err
	}
	out := list[:0]
	found := false
	for _, cl := range list {
		if cl.Name == name {
			found = true
			continue
		}
		out = append(out, cl)
	}
	if !found {
		return false, nil
	}
	if err := r.writeIndex(org, project, out); err != nil {
		return false, err
	}
	_ = r.kms.Delete(configRef(org, project, name))
	delete(r.cache, scopeRef(org, project)+"/"+name)
	return true, nil
}

// DynForOrg returns the k8s client the org+project's workloads should target: its
// default registered cluster (KMS-loaded, cached) or nil when the shard has none
// (the caller then falls back to the home in-cluster client). This is the ONE
// federation client.
func (r *Registry) DynForOrg(org, project string) dynamic.Interface {
	if !r.Enabled() {
		return nil
	}
	list, err := r.List(org, project)
	if err != nil || len(list) == 0 {
		return nil
	}
	target := list[0]
	for _, cl := range list {
		if cl.Default {
			target = cl
			break
		}
	}
	key := scopeRef(org, project) + "/" + target.Name
	if dyn, ok := r.cache[key]; ok {
		return dyn
	}
	raw, err := r.kms.Get(configRef(org, project, target.Name))
	if err != nil || len(raw) == 0 {
		return nil
	}
	dyn, _, err := dynFromKubeconfig(raw)
	if err != nil {
		return nil
	}
	r.cache[key] = dyn
	return dyn
}

func (r *Registry) writeIndex(org, project string, list []Cluster) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return r.kms.Set(indexRef(org, project), raw)
}

// --- helpers ---

func openKMS(brand string) *kms.Client {
	nodesCSV := strings.TrimSpace(os.Getenv("CLOUD_KMS_NODES"))
	pass := os.Getenv("CLOUD_KMS_PASSPHRASE")
	if nodesCSV == "" || pass == "" {
		return nil
	}
	var nodes []string
	for n := range strings.SplitSeq(nodesCSV, ",") {
		if t := strings.TrimSpace(n); t != "" {
			nodes = append(nodes, t)
		}
	}
	org := cmp.Or(strings.TrimSpace(os.Getenv("CLOUD_KMS_ORG")), brand, "hanzo")
	threshold := len(nodes)
	if v := os.Getenv("CLOUD_KMS_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(nodes) {
			threshold = n
		}
	}
	// cloud.OrgNamespace is the one door a string becomes a tenant name at; the
	// MPC client takes the name so it never has to fold a slug itself.
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil
	}
	client, err := kms.NewClient(kms.Config{Nodes: nodes, Namespace: ns, Threshold: threshold})
	if err != nil {
		return nil
	}
	if err := client.Unlock(pass); err != nil {
		return nil
	}
	return client
}

func dynFromKubeconfig(kubeconfig []byte) (dynamic.Interface, string, error) {
	restCfg, err := SafeRESTConfig(kubeconfig)
	if err != nil {
		return nil, "", err
	}
	restCfg.UserAgent = "hanzo-cloud-fleet"
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, "", err
	}
	return dyn, restCfg.Host, nil
}

// SafeRESTConfig parses a kubeconfig into a REST config and is the ONE fold-safety
// gate every attach path funnels through (discovery-folded clusters AND hand-pasted
// BYO kubeconfigs). It REFUSES:
//   - credential PLUGINS — restCfg.ExecProvider (an exec credential plugin runs a
//     local binary with the pod's full environment inherited = RCE) and
//     restCfg.AuthProvider (auth-provider plugins likewise shell out);
//   - SSRF TARGETS — the apiserver host (restCfg.Host, the ACTUAL dial target the
//     inventory List hits) must be https and publicly routable, never loopback /
//     private / link-local / IMDS / unspecified / multicast.
//
// This closes the exec-kubeconfig RCE and the "guard the endpoint we don't dial"
// SSRF for every caller at once — the kubeconfig's server is what gets dialed, so
// it is what gets guarded.
func SafeRESTConfig(kubeconfig []byte) (*rest.Config, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	if restCfg.ExecProvider != nil {
		return nil, fmt.Errorf("kubeconfig uses an exec credential plugin, which is not permitted")
	}
	if restCfg.AuthProvider != nil {
		return nil, fmt.Errorf("kubeconfig uses an auth-provider plugin, which is not permitted")
	}
	if err := guardHost(restCfg.Host); err != nil {
		return nil, err
	}
	return restCfg, nil
}

// guardHost rejects an apiserver URL that is not https or resolves to a
// non-routable address. Bypassable via allowPrivateHostsEnv for loopback test
// apiservers. (A pure DNS-rebind after this check is the same accepted caveat as
// the git mirror SSRF guard — client-go re-resolves at dial.)
func guardHost(rawHost string) error {
	u, err := url.Parse(strings.TrimSpace(rawHost))
	if err != nil || u.Host == "" {
		return fmt.Errorf("cluster apiserver endpoint is not a valid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("cluster apiserver endpoint must be https")
	}
	if os.Getenv(allowPrivateHostsEnv) != "" {
		return nil
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return fmt.Errorf("cluster apiserver host is not permitted")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		r, rerr := net.LookupIP(host)
		if rerr != nil || len(r) == 0 {
			return fmt.Errorf("cluster apiserver host does not resolve")
		}
		ips = r
	}
	if slices.ContainsFunc(ips, blockedIP) {
		return fmt.Errorf("cluster apiserver resolves to a non-routable address")
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

type inv struct{ nodes, nvidia, amd int }

func inventoryOf(ctx context.Context, dyn dynamic.Interface) (inv, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ul, err := dyn.Resource(nodeListGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return inv{}, err
	}
	out := inv{nodes: len(ul.Items)}
	for _, n := range ul.Items {
		alloc, found, _ := unstructured.NestedMap(n.Object, "status", "allocatable")
		if !found {
			continue
		}
		out.nvidia += quantityInt(alloc["nvidia.com/gpu"])
		out.amd += quantityInt(alloc["amd.com/gpu"])
	}
	return out, nil
}

func quantityInt(v any) int {
	if v == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
	if err != nil {
		return 0
	}
	return n
}

func upsert(list []Cluster, rec Cluster) []Cluster {
	for i, cl := range list {
		if cl.Name == rec.Name {
			list[i] = rec
			return list
		}
	}
	return append(list, rec)
}
