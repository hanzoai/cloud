// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package fleet is your own compute, attached: bring a Kubernetes cluster, a GPU
// box or bare metal and run work on it.
//
// It is the ONE per-org registry of that attached compute (BYO k8s clusters /
// BYO GPU / bare metal), the single source of truth consumed by BOTH the fleet
// surface (apps/visor, which serves /v1/compute/clusters — managed clusters from Visor
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
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/cloud/internal/environ"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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
// disabled: Register fails closed (never plaintext) and List no-ops.
type Registry struct {
	kms types.KMSClient
	log luxlog.Logger
	// dynCache holds federated per-cluster clients (key "org/name") so a registered
	// cluster becomes the org's compute plane without rebuilding on every request.
	cache map[string]dynamic.Interface
}

// New takes the deployment's KMS rather than opening one. build.go constructs it
// once, from the one bootstrap env, and every subsystem that custodies a secret is
// handed that — so a second opener cannot disagree with it about which org's
// namespace, which quorum, or what to do when the passphrase is absent.
//
// A nil client makes Enabled() false and the registry a graceful no-op, which is
// the same answer it gave when it opened its own and the env was unset.
func New(kms types.KMSClient, log luxlog.Logger) *Registry {
	return &Registry{kms: kms, log: log, cache: map[string]dynamic.Interface{}}
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
func (r *Registry) List(ctx context.Context, org, project string) ([]Cluster, error) {
	if !r.Enabled() {
		return nil, nil
	}
	raw, err := r.kms.GetSecret(ctx, indexRef(org, project))
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
	if err := r.kms.PutSecret(ctx, configRef(org, project, name), kubeBytes); err != nil {
		return Cluster{}, fmt.Errorf("seal kubeconfig: %w", err)
	}
	list, err := r.List(ctx, org, project)
	if err != nil {
		return Cluster{}, err
	}
	rec := Cluster{
		Name: name, Org: org, Kind: "byo", Provider: cmp.Or(strings.TrimSpace(provider), "byo"),
		Endpoint: endpoint, Nodes: inv.nodes, NvidiaGPU: inv.nvidia, AmdGPU: inv.amd,
		Registered: time.Now().UTC().Format(time.RFC3339), Default: isDefault,
	}
	list = upsert(list, rec)
	if err := r.writeIndex(ctx, org, project, list); err != nil {
		return Cluster{}, err
	}
	r.cache[scopeRef(org, project)+"/"+name] = dyn
	return rec, nil
}

// Deregister detaches a BYO cluster from the org+project fleet (index + sealed
// kubeconfig + cached client).
func (r *Registry) Deregister(ctx context.Context, org, project, name string) (bool, error) {
	if !r.Enabled() {
		return false, nil
	}
	list, err := r.List(ctx, org, project)
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
	if err := r.writeIndex(ctx, org, project, out); err != nil {
		return false, err
	}
	_ = r.kms.DeleteSecret(ctx, configRef(org, project, name))
	delete(r.cache, scopeRef(org, project)+"/"+name)
	return true, nil
}

// ErrNoCluster says the org+project shard holds no attached cluster by that
// name. A sentinel rather than prose, so a caller can answer it as an absence
// (a 404) and every other failure as the fault it is.
var ErrNoCluster = errors.New("no such cluster")

// RESTForOrgCluster returns the REST config that reaches ONE of the org+project's
// registered clusters, by name: the kubeconfig unsealed from the org's KMS and
// passed back through the same SafeRESTConfig gate that admitted it at attach.
// The caller names the cluster (a sandbox leased onto it) rather than taking the
// org's default, and it reads the same index and the same sealed key.
func (r *Registry) RESTForOrgCluster(ctx context.Context, org, project, name string) (*rest.Config, error) {
	if !r.Enabled() {
		return nil, ErrNoCluster
	}
	list, err := r.List(ctx, org, project)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(list, func(cl Cluster) bool { return cl.Name == name }) {
		return nil, ErrNoCluster
	}
	raw, err := r.kms.GetSecret(ctx, configRef(org, project, name))
	if err != nil {
		return nil, fmt.Errorf("unseal kubeconfig for %s: %w", name, err)
	}
	if len(raw) == 0 {
		return nil, ErrNoCluster
	}
	return restFromKubeconfig(raw)
}

func (r *Registry) writeIndex(ctx context.Context, org, project string, list []Cluster) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return r.kms.PutSecret(ctx, indexRef(org, project), raw)
}

// restFromKubeconfig is SafeRESTConfig plus the client identity every fleet
// connection carries — one place, so the registry's consumers (attach
// validation, RESTForOrgCluster) cannot disagree about either.
func restFromKubeconfig(kubeconfig []byte) (*rest.Config, error) {
	restCfg, err := SafeRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	restCfg.UserAgent = "hanzo-cloud-fleet"
	return restCfg, nil
}

func dynFromKubeconfig(kubeconfig []byte) (dynamic.Interface, string, error) {
	restCfg, err := restFromKubeconfig(kubeconfig)
	if err != nil {
		return nil, "", err
	}
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
	// A ".zt" apiserver is a fabric service, not an address: the connection to
	// it goes through the cloud's own fabric identity (zt.go), and every other
	// host keeps client-go's ordinary TCP dial.
	if fabricHost(hostOf(restCfg.Host)) {
		restCfg.Dial = fabricDial
	}
	return restCfg, nil
}

// hostOf extracts the hostname from an apiserver URL, empty when it has none.
func hostOf(rawHost string) string {
	u, err := url.Parse(strings.TrimSpace(rawHost))
	if err != nil {
		return ""
	}
	return u.Hostname()
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
	// A host on the fabric's own namespace is accepted WITHOUT resolving: nothing
	// resolves ".zt", and the SSRF classes this guard refuses are addresses,
	// which an authenticated overlay dial never touches (zt.go).
	if fabricHost(u.Hostname()) {
		return nil
	}
	if environ.Or(allowPrivateHostsEnv, "") != "" {
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
