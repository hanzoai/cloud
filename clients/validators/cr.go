package validators

// cr.go — the cluster-facing half: it materializes a NEW luxd validator node by
// writing ONE `node.lux.cloud/v1` LuxNetwork CR (+ the KMS→Secret sync) into a
// dedicated namespace. The Lux operator's LuxNetwork reconciler turns that CR
// into a single-replica luxd StatefulSet whose staking keys are mounted from KMS
// at /staking-keys.
//
// ★ MAINNET SAFETY (non-negotiable) ★. This writer is structurally incapable of
// touching the live hand-managed `luxd` StatefulSets. THREE independent guards:
//
//  1. DISTINCT GROUP — the CR is `node.lux.cloud/v1`, never `lux.network`
//     (the legacy operator that owns live luxd) and never `lux.cloud` (the shim).
//     The legacy operator and the shim never see these objects.
//  2. DEDICATED NAMESPACE — objects land in `lux-validators` (default); the
//     reserved live namespaces (lux-mainnet/testnet/devnet, lux-system) are
//     HARD-REFUSED.
//  3. RESERVED NAME — the CR name is `val-<org>-<tokenId>`; the literal `luxd`
//     and any `luxd-*` name (the live StatefulSet identity) is HARD-REFUSED.
//
// A guard failure returns an error and writes NOTHING. cloud never creates a CR
// that could reconcile onto a live luxd StatefulSet or bind a live PVC.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// reservedNamespaces are the live luxd namespaces a validator node CR may NEVER
// be written into. The dedicated `lux-validators` namespace is the only allowed
// home (plus any operator-configured non-reserved namespace).
var reservedNamespaces = map[string]bool{
	"lux-mainnet": true,
	"lux-testnet": true,
	"lux-devnet":  true,
	"lux-system":  true,
	"default":     true,
}

// reservedNameRE matches the live hand-managed StatefulSet identity (`luxd`,
// `luxd-0`, …). A CR whose name matches is refused — cloud's names are
// `val-<org>-<tokenId>`, so this is a defense-in-depth backstop.
var reservedNameRE = regexp.MustCompile(`^luxd(-.*)?$`)

// slugRE bounds the org component of a CR name to a DNS label so the assembled
// name is always a valid k8s object name (and can never contain a '/').
var slugRE = regexp.MustCompile(`[^a-z0-9-]`)

// luxNetworkGVR is the operator LuxNetwork kind under the DEDICATED group. NOT
// lux.network (legacy operator → live luxd) and NOT lux.cloud (shim). The
// operator's LuxNetwork reconciler, wired opt-in + namespace-scoped, watches
// exactly this group in exactly the dedicated namespace.
func luxNetworkGVR(group string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: "v1", Resource: "luxnetworks"}
}

// kmsSecretsGVR is the kms-operator KMSSecret CR — the bridge from cloud's KMS
// scope to the namespaced Secret mounted at /staking-keys. Same kind
// clients/platform declares for app secrets, so there is ONE KMS→Secret path.
var kmsSecretsGVR = schema.GroupVersionResource{Group: "secrets.lux.network", Version: "v1alpha1", Resource: "kmssecrets"}

// provisionRequest is the node-materialization input, all server-derived from a
// VALIDATED claim (org from JWT, tokenID/nodeID from the proven entitlement).
type provisionRequest struct {
	Org        string
	TokenID    uint64
	NodeID     string
	KMSBaseRef string // orgs/<org>/validators/<tokenId> — where keygen sealed the artifacts
	NetworkID  int32  // 1 mainnet, 2 testnet, 3 devnet, 1337 localnet
}

// nodeProvisioner materializes (or reports unavailable) a validator node. The
// seam keeps the orchestration in validators.go testable without a cluster.
type nodeProvisioner interface {
	// Provision writes the node CR + KMS→Secret sync and returns the CR name +
	// namespace. It MUST enforce the three mainnet-safety guards.
	Provision(ctx context.Context, p provisionRequest) (crName, namespace string, err error)
	// Available reports whether a cluster is resolved (so the handler can degrade
	// honestly to "node pending: no cluster" instead of failing the whole claim).
	Available() bool
}

// crConfig is the node-provisioning configuration (env-driven).
type crConfig struct {
	Group     string // CR group (default node.lux.cloud)
	Namespace string // dedicated namespace (default lux-validators)
	NodeImage string // ghcr.io/luxfi/node:<tag>
	KMSHost   string // cloud KMS root the kms-operator reads keys from
	KMSCreds  string // per-tenant universalAuth creds Secret name in the namespace
	StorageGi int    // PVC size (default 200)
}

// k8sProvisioner writes CRs via the dynamic client. nil dyn ⇒ no cluster
// resolved; every write fails closed with initErr (surfaced honestly).
type k8sProvisioner struct {
	dyn     dynamic.Interface
	initErr string
	cfg     crConfig
}

// newK8sProvisioner builds the dynamic client from the in-cluster service
// account, falling back to KUBECONFIG for local/dev — identical to
// clients/platform.newK8sClient.
func newK8sProvisioner(cfg crConfig) *k8sProvisioner {
	p := &k8sProvisioner{cfg: cfg}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		restCfg, err = cc.ClientConfig()
		if err != nil {
			p.initErr = fmt.Sprintf("no in-cluster config and no kubeconfig: %v", err)
			return p
		}
	}
	restCfg.UserAgent = "hanzo-cloud-validators"
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		p.initErr = fmt.Sprintf("dynamic client: %v", err)
		return p
	}
	p.dyn = dyn
	return p
}

func (p *k8sProvisioner) Available() bool { return p != nil && p.dyn != nil }

// crName is the deterministic node name: val-<orgSlug>-<tokenId>. The org is
// folded to a DNS label; the tokenId disambiguates. Never `luxd*`.
func crName(org string, tokenID uint64) string {
	slug := slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(org)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "org"
	}
	name := "val-" + slug + "-" + strconv.FormatUint(tokenID, 10)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// guard enforces the three mainnet-safety guards. Returns the validated
// (name, namespace) or an error that writes nothing.
func (p *k8sProvisioner) guard(org string, tokenID uint64) (name, ns string, err error) {
	ns = p.cfg.Namespace
	if reservedNamespaces[ns] {
		return "", "", fmt.Errorf("refusing to write validator node into reserved namespace %q (live luxd lives here)", ns)
	}
	if p.cfg.Group == "lux.network" || p.cfg.Group == "lux.cloud" {
		return "", "", fmt.Errorf("refusing group %q: validator node CRs MUST use a dedicated group, never the legacy/shim group", p.cfg.Group)
	}
	name = crName(org, tokenID)
	if reservedNameRE.MatchString(name) {
		return "", "", fmt.Errorf("refusing reserved node name %q (live luxd StatefulSet identity)", name)
	}
	return name, ns, nil
}

// Provision writes the KMSSecret (KMS→Secret sync) and the LuxNetwork CR for a
// new node, both owner-guarded. Idempotent: re-provisioning the same slot
// create-or-updates the same named objects.
func (p *k8sProvisioner) Provision(ctx context.Context, req provisionRequest) (string, string, error) {
	if !p.Available() {
		reason := "kubernetes client not configured"
		if p.initErr != "" {
			reason = p.initErr
		}
		return "", "", fmt.Errorf("%s", reason)
	}
	name, ns, err := p.guard(req.Org, req.TokenID)
	if err != nil {
		return "", "", err
	}
	secretName := name + "-staking"

	// 1) KMS→Secret sync: declare the KMSSecret CR so the kms-operator
	// materializes the staking Secret from cloud's KMS scope. Best-effort — a
	// missing CRD leaves the node pending (honest), never a failed claim.
	if err := p.applyKMSSecret(ctx, ns, req, secretName); err != nil {
		return name, ns, fmt.Errorf("declare staking KMSSecret: %w", err)
	}

	// 2) The LuxNetwork CR — a single-replica luxd node under the dedicated group.
	if err := p.applyLuxNetwork(ctx, ns, name, secretName, req); err != nil {
		return name, ns, fmt.Errorf("apply LuxNetwork CR: %w", err)
	}
	return name, ns, nil
}

// applyKMSSecret create-or-updates the KMSSecret CR that syncs the 5 sealed
// staking artifacts from cloud's KMS scope into the namespaced Secret mounted at
// /staking-keys.
func (p *k8sProvisioner) applyKMSSecret(ctx context.Context, ns string, req provisionRequest, secretName string) error {
	// secretsPath is the sub-path UNDER the org (projectSlug carries the org),
	// matching where keygen.seal wrote the artifacts: orgs/<org>/validators/<id>.
	secretsPath := "validators/" + strconv.FormatUint(req.TokenID, 10)
	keys := make([]any, len(stakingArtifacts))
	for i, k := range stakingArtifacts {
		keys[i] = k
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "secrets.lux.network/v1alpha1",
		"kind":       "KMSSecret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": ns,
			"labels":    p.labels(req.Org, req.TokenID),
		},
		"spec": map[string]any{
			"hostAPI":        p.cfg.KMSHost,
			"resyncInterval": int64(300),
			"authentication": map[string]any{
				"universalAuth": map[string]any{
					"credentialsRef": map[string]any{
						"secretName":      p.cfg.KMSCreds,
						"secretNamespace": ns,
					},
					"secretsScope": map[string]any{
						"projectSlug": req.Org,
						"envSlug":     "default",
						"secretsPath": secretsPath,
						"keys":        keys,
					},
				},
			},
			"managedSecretReference": map[string]any{
				"secretName":      secretName,
				"secretNamespace": ns,
				"secretType":      "Opaque",
				"creationPolicy":  "Owner",
			},
		},
	}}
	return p.applyUnstructured(ctx, kmsSecretsGVR, ns, secretName, desired)
}

// applyLuxNetwork create-or-updates the single-replica LuxNetwork CR. The spec's
// `staking` block points the operator at the materialized Secret (mounted
// read-only at /staking-keys by the operator's LuxNetwork reconciler).
func (p *k8sProvisioner) applyLuxNetwork(ctx context.Context, ns, name, secretName string, req provisionRequest) error {
	one := int64(1)
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": p.cfg.Group + "/v1",
		"kind":       "LuxNetwork",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels":    p.labels(req.Org, req.TokenID),
			"annotations": map[string]any{
				// Provenance: this node was provisioned for a proven NFT slot.
				"validators.lux.cloud/token-id": strconv.FormatUint(req.TokenID, 10),
				"validators.lux.cloud/node-id":  req.NodeID,
			},
		},
		"spec": map[string]any{
			"networkName": name,
			"networkID":   int64(req.NetworkID),
			"validators":  one,
			"image": map[string]any{
				"repository": imageRepo(p.cfg.NodeImage),
				"tag":        imageTag(p.cfg.NodeImage),
				"pullPolicy": "Always",
			},
			"storage": map[string]any{
				"size": strconv.Itoa(p.cfg.StorageGi) + "Gi",
			},
			// The staking block the operator's reconciler reads to (a) declare the
			// KMSSecret sync and (b) mount secretName read-only at /staking-keys.
			"staking": map[string]any{
				"secretName": secretName,
				"kms": map[string]any{
					"hostAPI":        p.cfg.KMSHost,
					"projectSlug":    req.Org,
					"envSlug":        "default",
					"secretsPath":    "validators/" + strconv.FormatUint(req.TokenID, 10),
					"resyncInterval": int64(300),
					"credentialsRef": map[string]any{
						"name":      p.cfg.KMSCreds,
						"namespace": ns,
					},
					"managedSecretName": secretName,
				},
			},
		},
	}}
	return p.applyUnstructured(ctx, luxNetworkGVR(p.cfg.Group), ns, name, desired)
}

// applyUnstructured is create-if-absent, else merge-patch spec+labels. Idempotent
// and byte-stable for a stable input. Scoped to the dedicated namespace only.
func (p *k8sProvisioner) applyUnstructured(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, desired *unstructured.Unstructured) error {
	_, err := p.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cErr := p.dyn.Resource(gvr).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(cErr) {
			return nil
		}
		return cErr
	}
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"labels": desired.Object["metadata"].(map[string]any)["labels"]},
		"spec":     desired.Object["spec"],
	})
	_, err = p.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (p *k8sProvisioner) labels(org string, tokenID uint64) map[string]any {
	return map[string]any{
		"app.kubernetes.io/managed-by": "hanzo-cloud-validators",
		"validators.lux.cloud/org":     slugRE.ReplaceAllString(strings.ToLower(org), "-"),
		"validators.lux.cloud/slot":    strconv.FormatUint(tokenID, 10),
	}
}

// imageRepo/imageTag split "repo:tag" (tag defaults to latest). Mirrors
// clients/platform.splitImageRef without importing it.
func imageRepo(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}

func imageTag(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return "latest"
}
