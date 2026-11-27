// k8s.go — the cluster-facing half of /v1/platform: the ONE deploy path.
//
// A user application is deployed by writing an operator hanzo.ai/v1 `App`
// CR into the caller's OWN tenant namespace; the Hanzo operator reconciles it
// into a Deployment + Service + Ingress (+ HPA/PDB) on DOKS. cloud never
// reimplements a deployer — it writes one CR, exactly like the fleet board
// reads system CRs, but here every object lives in `tenant-<org>` where the
// org is the gateway-minted, IAM-VALIDATED tenant (c.Org()), never a value from
// the request body or path. That derivation is the whole cross-tenant isolation
// boundary: a caller cannot name another org's namespace because the namespace
// is not an input.
//
// Builds (git-source apps) launch an in-cluster BuildKit Job (the arcd model,
// buildkit-job.ts) via client-go — no GitHub builders. When the cluster / CI
// prerequisites are absent the subsystem fails CLOSED with the real reason
// (never status-theater), matching the fleet board.

package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/namespace"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// k8s.Apps is the operator App CR — the workload kind a tenant app is written as.
// A role-less App dispatches to the operator's service profile
// (controllers/app.rs `classify("") => Dispatch::Service`), so an App carries a
// tenant workload with the default profile: one Deployment + one core Service.
// A user app is one of these CRs in its tenant namespace.

// resolveCR reports whether the named tenant App CR exists (and, for the caller's
// patch, its GVR). A lookup error other than not-found is returned as-is — a
// tenant write must never proceed on an unknown cluster state.
func (k *k8sClient) resolveCR(ctx context.Context, ns, name string) (schema.GroupVersionResource, bool, error) {
	_, err := k.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return k8s.Apps, true, nil
	}
	if !apierrors.IsNotFound(err) {
		return schema.GroupVersionResource{}, false, err
	}
	return schema.GroupVersionResource{}, false, nil
}

// getCR fetches the named tenant App CR. Callers are read-only observers, so a
// not-found is returned as the error — an honest "unknown", never an invented
// object.
func (k *k8sClient) getCR(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
	return k.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

// jobsGVR is the batch/v1 Job used to launch an in-cluster BuildKit build.
var jobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}

// k8s.Namespaces lets the deploy path ensure the tenant namespace exists.

// resourceQuotasGVR / limitRangesGVR let ensureNamespace bound a tenant's total
// footprint (MED-3). Both are core/v1 namespaced objects.
var resourceQuotasGVR = schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
var limitRangesGVR = schema.GroupVersionResource{Version: "v1", Resource: "limitranges"}

// secretsGVR is the core/v1 Secret. cloud writes exactly ONE kind here: the
// per-tenant KMS-auth creds Secret (secrets.go applyTenantKMSAuthSecret) the
// KMSSecret CR's credentialsRef points at. The per-tenant ClusterRole grants only
// get/create/delete on core secrets (no patch/update) — projection is create-if-
// absent, and rotation deletes-then-recreates. cloud NEVER reads the app's own
// managed <app>-env Secret (that is the operator's, materialized from KMS).
var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// selfSubjectAccessReviewsGVR is the authorization.k8s.io/v1 SelfSubjectAccessReview
// virtual resource. cloud-api POSTs one to ask the apiserver, as its OWN identity,
// "can I act in this namespace yet?" — the readiness probe that closes the
// first-deploy race with the operator's async per-tenant RoleBinding
// (waitForTenantRBAC). Creating a SSAR is granted to every authenticated identity by
// the built-in system:basic-user ClusterRole, so this probe succeeds even BEFORE the
// operator's RoleBinding lands: it is the one signal immune to the very window it
// detects.
var selfSubjectAccessReviewsGVR = schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}

// tenantPullSecretName is the GHCR image-pull Secret every tenant namespace
// carries so the pod can pull the PRIVATE per-tenant build image
// (ghcr.io/hanzoai/tenant-<org>/*). serviceCR REFERENCES it by name only; the
// Secret itself is provisioned by the OPERATOR's tenant-RBAC controller (from a
// KMS-synced source), NEVER by cloud-api — cloud references it by name only and
// never provisions it. (cloud's ONLY Secrets API write is the per-tenant KMS-auth
// creds Secret in a TENANT namespace, via its per-tenant get/create/delete grant;
// see secretsGVR.) The ISOLATED build namespace (hanzo-build) is deliberately kept
// OFF the operator's tenant-RBAC selector (it must NOT carry hanzo.ai/managed-by=
// platform) so that secrets grant is NEVER projected there — a build ns needs only
// the cloud-build-launcher jobs/pods grant, no secrets (R6). Until the operator has
// projected a tenant pull secret the reference degrades to the namespace default
// rather than hard-failing the deploy.
const tenantPullSecretName = "ghcr-pull"

// memberLabel marks a build Job as a member's, so the line starts an admin's build of the
// same org before it (dispatch.go).
const memberLabel = "hanzo.ai/member"

// errTooManyRunners is returned by admitRunner when the CLUSTER already holds
// the maximum number of live runner Jobs. Unlike builds this is not per org: a
// runner's memory is its node's, so the resource it exhausts is the node and
// every tenant shares it. Twelve were live here at once, 9-10 GB apiece as kata
// microVMs (they run under visor now), and the kernel started killing
// processes. The job path maps it to HTTP 429.
var errTooManyRunners = fmt.Errorf("platform: too many concurrent runners on this cluster: %w", cloud.ErrRunnerCapacity)

// errForgeShareFull is admitRunner's refusal when the forge already holds its
// half of the ceiling for the architecture a job needs. To any caller that only
// asks whether the cluster is full it IS errTooManyRunners; the queue loop tells
// them apart, because a job of the other architecture may still fit.
var errForgeShareFull = fmt.Errorf("platform: the forge holds its share of this architecture's runners: %w", errTooManyRunners)

// errTenantProvisioning is returned by waitForTenantRBAC when the operator has not
// projected cloud-api's per-tenant RoleBinding (cloud-api-platform) into a
// freshly-created namespace within the bounded wait. It is a RETRYABLE condition
// (deployErrStatus → HTTP 503): the operator finishes onboarding a moment later, so
// the client's next deploy finds the RoleBinding present. It is an HONEST
// "provisioning, retry" — never a fabricated success.
var errTenantProvisioning = errors.New("platform: tenant RBAC still provisioning")

const (
	// tenantRBACReadyTimeout bounds how long a FIRST deploy into a brand-new tenant
	// waits for the operator's tenant-RBAC controller to grant cloud-api access in
	// the new namespace. The operator reconciles the RoleBinding within ~1-3s of the
	// namespace appearing; this ceiling is only the safety net for a slow reconcile,
	// never the common case. An already-onboarded tenant returns on the first probe
	// (no wait), so the auto-glue fast path is unchanged.
	tenantRBACReadyTimeout = 45 * time.Second
	// tenantRBACPollInitial / tenantRBACPollMax bound the exponential back-off
	// between readiness probes while RBAC is not yet ready.
	tenantRBACPollInitial = 250 * time.Millisecond
	tenantRBACPollMax     = 2 * time.Second
)

const platformUserAgent = "hanzo-cloud-platform"

// buildImagePrefix is the GHCR namespace tenant build images are pushed under.
// Each org's images live at ghcr.io/hanzoai/tenant-<org>/<app>:<tag>; the org
// and app are SEPARATE '/'-joined path components derived from the validated
// tenant, so the (org,app)→image mapping is injective and images never collide
// or leak across orgs. Overridable by the operator via CLOUD_PLATFORM_IMAGE_PREFIX.
const defaultBuildImagePrefix = "ghcr.io/hanzoai"

// defaultBuildNamespace is the ISOLATED namespace privileged-input build Jobs run
// in when CLOUD_PLATFORM_BUILD_NS is unset. It is deliberately NOT the platform
// namespace: builds mount a per-org push credential + the git-fetch token and run
// attacker-authored Dockerfiles, so they are quarantined away from cloud's own
// secret set (H2). The operator provisions this namespace + its scoped creds.
const defaultBuildNamespace = "hanzo-build"

// k8sClient wraps the dynamic client + the resolved build image prefix + the
// per-tenant resource policy. nil dyn ⇒ no cluster resolved; every cluster op
// fails closed with initErr.
type k8sClient struct {
	dyn dynamic.Interface
	// clientset is the TYPED client, held ONLY for the pod-log subresource
	// (Pods().GetLogs streams a raw body the dynamic client cannot express). All CR
	// reads/writes stay on dyn; this is the one typed capability platform needs.
	// nil when the cluster is unresolved (logs then fall back to the recorded
	// timeline, like every other cluster op here degrades).
	clientset   kubernetes.Interface
	initErr     string
	imagePrefix string
	apex        string         // the apps apex (sitesHost): its one-label hosts ride the wildcard certificate
	buildNS     string         // ISOLATED namespace CI Jobs run in (default defaultBuildNamespace, off the platform ns)
	limits      resourceLimits // per-tenant replica/quota/build bounds (MED-3)
	kmsSync     kmsSyncConfig  // KMSSecret CR operator config (secrets.go)
	// First-deploy tenant-RBAC readiness wait (waitForTenantRBAC). Zero ⇒ the
	// production constants (tenantRBACReadyTimeout / tenantRBACPollInitial); tests
	// shrink them for fast, deterministic coverage. Not env-configurable: these do
	// not vary between environments.
	rbacReadyTimeout time.Duration
	rbacPollInitial  time.Duration
	// How long a checkout token's revoker waits for its build Job, and how often
	// it looks (revokeWhenDone). Zero ⇒ checkoutLife / jobPollInterval.
	checkoutWait, checkoutPoll time.Duration
}

// newK8sClient builds the dynamic client from the in-cluster service account,
// falling back to KUBECONFIG for local/dev — identical to newFleetDynamic.
func newK8sClient(imagePrefix, buildNS string) *k8sClient {
	c := &k8sClient{imagePrefix: imagePrefix, buildNS: buildNS, limits: newResourceLimits(), kmsSync: newKMSSyncConfig()}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			c.initErr = fmt.Sprintf("no in-cluster config and no kubeconfig: %v", err)
			return c
		}
	}
	cfg.UserAgent = platformUserAgent
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		c.initErr = fmt.Sprintf("dynamic client: %v", err)
		return c
	}
	c.dyn = dyn
	// Typed client for the pod-log subresource ONLY (deployment logs). Best-effort:
	// a failure here leaves clientset nil so logs fall back to the recorded timeline,
	// but it never disables the CR control plane (which is all on dyn). From the SAME
	// rest.Config, so it authenticates as the same cloud-api service account.
	if cs, csErr := kubernetes.NewForConfig(cfg); csErr == nil {
		c.clientset = cs
	}
	return c
}

func (k *k8sClient) ready() error {
	if k == nil || k.dyn == nil {
		reason := "kubernetes client not configured"
		if k != nil && k.initErr != "" {
			reason = k.initErr
		}
		return fmt.Errorf("%s", reason)
	}
	return nil
}

// tenantNamespace derives the physical namespace for an org. This is the
// cross-tenant isolation boundary: the org is the VALIDATED tenant (c.Org()),
// so the namespace is not attacker-controlled. The slug is produced by the ONE
// hardened, INJECTIVE org normalizer (namespace.Sanitize — identity on a
// clean DNS label, else fold + a SHA-256 suffix of the raw owner), so two
// distinct owners can NEVER collapse onto the same namespace (CRIT-2).
//
// ★ org MUST ALREADY BE THAT SLUG, and this only prepends the prefix.
//
// It used to sanitize again, and Sanitize is NOT idempotent — it is the identity
// only on a clean label, and re-folding its own <fold>-<hash> output appends a
// SECOND hash (looksSuffixed denies the fast path deliberately, so no name can
// squat on the rendered form of another). Every handler receives an
// already-sanitized slug from tenant(s, c), so for every org whose name is not
// already clean this wrote App CRs into tenant-<double> while apps/deploy
// (scope.go tenantNS) and apps/provisioning (dedicated.go) both scanned
// tenant-<single>. It fails closed — nothing crosses a tenant boundary — but the
// org's board is silently EMPTY and its CRs are orphaned in a namespace nothing
// lists. This was the outlier of three copies of one rule; the other two already
// took the slug, and provisioning's even documents why.
//
// Same class as the confinement asymmetry: Sanitize applied an unequal number of
// times on two sides of one comparison. TestTenantNamespaceIsDerivedExactlyOnce
// holds the three together.
//
// ⚠ MIGRATION: any dirty-org namespace created before this fix is already
// double-suffixed on the cluster and is NOT what this now derives. Those are
// orphans either way — nothing has ever read them — but they must be reaped
// deliberately, not left to look like a live tenant.
func tenantNamespace(org string) string {
	// FAIL CLOSED ON A CONTRACT VIOLATION. The input must already be a slug, and
	// "already sanitized" is NOT detectable by re-sanitizing — that is the very
	// non-idempotence this fix is about. What IS checkable is the property a
	// namespace must have anyway: a DNS-1123 label. Every namespace.Sanitize
	// output is one; a raw or hostile owner claim ("acme/../hanzo", "  spaced  ",
	// "org:with:colons") is not, and such a caller has skipped tenant(s, c).
	//
	// It resolves to the inert "unknown" rather than a malformed namespace, so a
	// missed sanitize can never render a path-bearing or space-bearing namespace
	// into a manifest — and, being inert, never lands in a real tenant's either.
	// TestSanitizeIsInjective holds this: the output is always a clean label.
	if org == "" || !appNameRE.MatchString(org) {
		org = "unknown"
	}
	// NAMING(gated): rename tenant-<org> → org-<org> requires migrating live
	// namespaces — and the coupled tenant-<org>/<app> registry image refs and the
	// tenant-quota / tenant-limits objects derived from this prefix. The literal
	// "tenant-" string is retained until that infrastructure migration ships; the
	// rest of cloud's identity vocabulary is org-native (see LLM.md doctrine).
	return "tenant-" + org
}

// buildImageRef is the deterministic per-tenant output image for a git build:
// <prefix>/tenant-<org>/<app>:<tag>. org and app live in SEPARATE path
// components, joined by '/', which neither an org slug (namespace.Sanitize
// output) nor an app slug (slugRE) can contain — so the (org,app) pair is
// UNIQUELY recoverable from the ref and the mapping is INJECTIVE (CRIT-2). The
// previous single-component "tenant-<org>-<app>" join was ambiguous: (org=a-b,
// app=c) and (org=a,app=b-c) both rendered "tenant-a-b-c", letting one tenant
// push to another's image. With an injective org slug AND a '/'-separated,
// slug-free boundary, distinct tenants always target distinct repositories.
func (k *k8sClient) buildImageRef(org, app, tag string) string {
	prefix := k.imagePrefix
	if prefix == "" {
		prefix = defaultBuildImagePrefix
	}
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s/tenant-%s/%s:%s", strings.TrimRight(prefix, "/"), namespace.Sanitize(org), app, tag)
}

// ensureNamespaceExists creates tenant-<org> if it does not exist (idempotent).
// Creating the namespace is what TRIGGERS the operator's tenant-RBAC controller to
// project cloud-api's `cloud-api-platform` RoleBinding into it — so this is the ONE
// precondition for tenant RBAC ever becoming ready. Pure namespace mechanism: no
// RBAC wait, no quota. Both the synchronous image path (ensureNamespace, which then
// BLOCKS on the RoleBinding) and the async build reconciler (ensureTenantReady,
// which only PROBES) build on it, so there is exactly one namespace-create rule.
func (k *k8sClient) ensureNamespaceExists(ctx context.Context, ns, org string) error {
	_, err := k.dyn.Resource(k8s.Namespaces).Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]any{
				"name": ns,
				"labels": map[string]any{
					"hanzo.ai/org":        org,
					"hanzo.ai/managed-by": "platform",
				},
			},
		}}
		if _, cErr := k.dyn.Resource(k8s.Namespaces).Create(ctx, obj, metav1.CreateOptions{}); cErr != nil && !apierrors.IsAlreadyExists(cErr) {
			return cErr
		}
		return nil
	}
	return err
}

// ensureNamespace is the SYNCHRONOUS (image-source) tenant preparation: create the
// namespace, BLOCK up to ~45s for the operator's async RoleBinding to land, then
// ensure the tenant's ResourceQuota + LimitRange (idempotent). Applying the bounds
// on every call — not only at first create — means older tenant namespaces are
// brought under quota too, and a deleted quota is re-created on the next deploy
// (MED-3). The caller is a single client deploy that must succeed without a manual
// retry, so the bounded wait is worth it here; the async build reconciler uses the
// NON-BLOCKING ensureTenantReady instead (its 10s tick is its retry loop, so it must
// never park mid-reconcile and head-of-line-block other orgs).
func (k *k8sClient) ensureNamespace(ctx context.Context, ns, org string) error {
	if err := k.ready(); err != nil {
		return err
	}
	if err := k.ensureNamespaceExists(ctx, ns, org); err != nil {
		return err
	}
	// A BRAND-NEW tenant namespace is created above, but the operator's tenant-RBAC
	// controller projects cloud-api's `cloud-api-platform` RoleBinding (the grant to
	// get/create resourcequotas/limitranges/services here) into it ASYNCHRONOUSLY, a
	// moment later. Wait for that grant to land before touching the quota objects, so
	// the first-ever deploy no longer races the RoleBinding and 403s. An already-
	// onboarded tenant is confirmed by a single fast probe (no sleep), so existing
	// deploys are not slowed.
	if err := k.waitForTenantRBAC(ctx, ns); err != nil {
		return err
	}
	// Bound the tenant's total footprint (idempotent create-or-update).
	if err := k.ensureBoundObject(ctx, resourceQuotasGVR, ns, tenantQuotaName, k.limits.resourceQuota(ns)); err != nil {
		return fmt.Errorf("ensure resourcequota: %w", err)
	}
	if err := k.ensureBoundObject(ctx, limitRangesGVR, ns, tenantLimitRangeName, k.limits.limitRange(ns)); err != nil {
		return fmt.Errorf("ensure limitrange: %w", err)
	}
	// The tenant's GHCR image-pull Secret (tenantPullSecretName) is provisioned by
	// the OPERATOR's tenant-RBAC controller — cloud-api touches NO K8s Secret and
	// holds no `secrets` grant. serviceCR references it by name only.
	return nil
}

// ensureTenantReady is the NON-BLOCKING readiness gate for the async build
// reconciler (reconcile.go). It creates the tenant namespace if absent (idempotent —
// the trigger for the operator's RoleBinding) and does exactly ONE readiness probe.
// It NEVER blocks on the operator's async RoleBinding the way the synchronous
// ensureNamespace does (bounded ~45s waitForTenantRBAC): the reconciler is itself a
// retry loop on a 10s tick, so parking in a per-deployment wait would head-of-line-
// block EVERY other org's go-live behind one slow tenant onboarding. Instead the
// reconciler probes once and, if not ready, leaves the deployment "building" and
// re-drives next tick.
//
//   - ready=true            → the RoleBinding has landed; the caller may applyService
//     now (its own waitForTenantRBAC resolves on the first probe, no sleep).
//   - ready=false, err=nil  → namespace exists but RBAC is still provisioning; retry
//     on a later tick (the namespace now exists, so the operator will project the
//     RoleBinding before then).
//   - err!=nil              → a real cluster error (namespace create / probe blip);
//     the reconciler retries it as transient too, failing only past the deadline.
func (k *k8sClient) ensureTenantReady(ctx context.Context, ns, org string) (bool, error) {
	if err := k.ready(); err != nil {
		return false, err
	}
	if err := k.ensureNamespaceExists(ctx, ns, org); err != nil {
		return false, err
	}
	return k.canGetResourceQuotas(ctx, ns)
}

// ensureBoundObject create-or-updates one namespaced policy object (ResourceQuota
// / LimitRange) so the tenant's declared bounds always match the operator's
// current config: it Creates when absent and merge-patches .spec when present
// (an operator raising the limits takes effect on the next deploy). Any other
// get error propagates.
func (k *k8sClient) ensureBoundObject(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, desired *unstructured.Unstructured) error {
	_, err := k.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cErr := k.dyn.Resource(gvr).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		if cErr != nil && !apierrors.IsAlreadyExists(cErr) {
			return cErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]any{"spec": desired.Object["spec"]})
	_, err = k.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// waitForTenantRBAC blocks until cloud-api can act in ns, or the bounded window
// elapses. It closes the first-deploy cold-start race: a brand-new tenant's
// namespace is created by ensureNamespace, but the operator's tenant-RBAC
// controller projects cloud-api's `cloud-api-platform` RoleBinding (ClusterRole
// hanzo-cloud-platform-tenant: get/create/patch on resourcequotas + limitranges +
// services.hanzo.ai) into it ASYNCHRONOUSLY. Without this gate the first deploy ran
// ahead of the RoleBinding and failed with `resourcequotas ... is forbidden`,
// self-healing only on a manual retry.
//
// The gate is a SelfSubjectAccessReview poll — "can I get resourcequotas in ns?":
//   - allowed  → the RoleBinding has landed; proceed. The already-onboarded tenant
//     returns on the FIRST probe with no sleep, so the auto-glue fast path is
//     unchanged (one lightweight, non-persisted apiserver call).
//   - denied   → poll again with bounded exponential back-off until allowed or the
//     window elapses.
//   - window elapsed → fail CLOSED with errTenantProvisioning (→ HTTP 503, honest
//     "provisioning, retry"); never a fabricated success.
//
// The probe's verb/resource/namespace mirror ensureNamespace's first privileged op
// (Get resourcequotas in ns) exactly, so "allowed" is a true precondition for the
// work that follows. A SSAR needs no tenant RBAC (system:basic-user), so the probe
// never itself hits the window it is closing. ctx cancellation (client disconnect /
// shutdown) aborts the wait promptly. Idempotent and restart-safe: it holds no
// state and re-derives readiness from the live cluster on every call.
func (k *k8sClient) waitForTenantRBAC(ctx context.Context, ns string) error {
	timeout := k.rbacReadyTimeout
	if timeout <= 0 {
		timeout = tenantRBACReadyTimeout
	}
	backoff := k.rbacPollInitial
	if backoff <= 0 {
		backoff = tenantRBACPollInitial
	}
	deadline := time.Now().Add(timeout)
	for {
		allowed, probeErr := k.canGetResourceQuotas(ctx, ns)
		if allowed {
			return nil
		}
		// A SSAR create error (rare apiserver blip) is treated as not-yet-ready and
		// retried within the window rather than failing the deploy on a flake; a
		// persistent error simply exhausts the window into an honest timeout below.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if probeErr != nil {
				return fmt.Errorf("%w: tenant %s not ready after %s (last probe error: %v)", errTenantProvisioning, ns, timeout, probeErr)
			}
			return fmt.Errorf("%w: tenant %s not ready after %s (retry deploy)", errTenantProvisioning, ns, timeout)
		}
		sleep := min(backoff, tenantRBACPollMax, remaining)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		backoff *= 2
	}
}

// canGetResourceQuotas asks the apiserver, as cloud-api's OWN identity, whether it
// may `get` resourcequotas in ns — the exact op ensureNamespace performs next. It
// POSTs a SelfSubjectAccessReview and reads .status.allowed. A create error (rare)
// is returned so the poller can retry-within-window rather than fail on a flake.
func (k *k8sClient) canGetResourceQuotas(ctx context.Context, ns string) (bool, error) {
	ssar := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec": map[string]any{
			"resourceAttributes": map[string]any{
				"namespace": ns,
				"verb":      "get",
				"group":     "", // core API group
				"resource":  "resourcequotas",
			},
		},
	}}
	out, err := k.dyn.Resource(selfSubjectAccessReviewsGVR).Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, _, _ := unstructured.NestedBool(out.Object, "status", "allowed")
	return allowed, nil
}

// serviceCR renders the operator hanzo.ai/v1 Service CR for an application. The
// CR name is the app slug; it lives in tenant-<org>; labels stamp the org for
// defense-in-depth (the namespace is already the boundary). `image` is the
// resolved ref to run (the app's image for image-source, or the build output).
// volumeName is the claim an app's storage lives under, and volumeMount is where
// it appears in the container. Both are DERIVED, never configurable: one app, one
// volume, one path. A per-app mount path would be a second way to say the same
// thing and would let two deploys of the same app disagree about where its data is.
func volumeName(slug string) string { return slug + "-data" }

const volumeMount = "/data"

// ensureVolume creates the app's PersistentVolumeClaim if it is absent, and
// otherwise leaves it EXACTLY as it is.
//
// It never patches and never deletes. A claim is the only copy of the tenant's
// data, so the two edits that look reasonable here are the two that lose it: a
// shrink is rejected by the CSI driver and leaves the app wedged, and a delete on
// app-removal would destroy data on what a user experiences as "undeploy". Growing
// a volume is a separate, explicit operation for the same reason.
func (k *k8sClient) ensureVolume(ctx context.Context, ns, org string, a Application) error {
	name := volumeName(a.Slug)
	_, err := k.dyn.Resource(k8s.Volumes).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get volume %s: %w", name, err)
	}
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":               org,
				"hanzo.ai/managed-by":        "platform",
				"app.kubernetes.io/instance": a.Slug,
			},
		},
		"spec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources": map[string]any{
				"requests": map[string]any{"storage": fmt.Sprintf("%dGi", a.StorageGB)},
			},
		},
	}}
	_, err = k.dyn.Resource(k8s.Volumes).Namespace(ns).Create(ctx, claim, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create volume %s: %w", name, err)
	}
	return nil
}

func serviceCR(ns, org, project string, a Application, image, apex string) *unstructured.Unstructured {
	repo, tag := splitImageRef(image)
	spec := map[string]any{
		"image": map[string]any{
			"repository": repo,
			"tag":        tag,
			"pullPolicy": "Always",
		},
		// a.Replicas is already clamped to [1,maxReplicas] at the applyService
		// boundary (and at createApp/start); max1 is the final floor guard.
		"replicas": int64(max1(a.Replicas)),
		"ports": []any{
			map[string]any{"name": "http", "containerPort": int64(portOr(a.Port))},
		},
		// The build output is a PRIVATE image under ghcr.io/hanzoai/tenant-<org>/*;
		// the operator propagates imagePullSecrets to the pod so it can pull it. The
		// referenced Secret is provisioned by the operator's tenant-RBAC controller
		// (from a KMS-synced source); cloud-api only names it here, never creates it.
		"imagePullSecrets": []any{
			map[string]any{"name": tenantPullSecretName},
		},
	}
	if env := renderEnv(managedSecretName(a.Slug), a.EnvJSON); len(env) > 0 {
		spec["env"] = env
	}
	// Declaring storage also decides the update strategy, because the two are one
	// fact: a ReadWriteOnce volume can be attached to a single node, so a rolling
	// update asks the new pod to mount what the old pod has not released yet and
	// the CSI driver deadlocks on Multi-Attach — the new pod sits in
	// ContainerCreating indefinitely. Recreate tears the old pod down first. That
	// is downtime, and it is the honest cost of one volume.
	if a.StorageGB > 0 {
		spec["strategy"] = "Recreate"
		spec["volumes"] = []any{
			map[string]any{
				"name":                  "data",
				"persistentVolumeClaim": map[string]any{"claimName": volumeName(a.Slug)},
			},
		}
		spec["volumeMounts"] = []any{
			map[string]any{"name": "data", "mountPath": volumeMount},
		}
	}
	if ing := ingressSpec(activeHosts(a.DomainsJSON), apex); ing != nil {
		spec["ingress"] = ing
	}
	// Container-serverless autoscaling: the /v1/platform/run path sets MaxScale>0 to declare an
	// HPA over [MinScale,MaxScale]. The operator makes autoscaling.minReplicas the
	// authoritative floor and passes None for Deployment.spec.replicas when enabled
	// (crs/gateway.yaml), so the two never fight. MaxScale==0 (the app-deploy path)
	// omits the block entirely and keeps the fixed-replicas behavior unchanged.
	if a.MaxScale > 0 {
		spec["autoscaling"] = map[string]any{
			"enabled":     true,
			"minReplicas": int64(max1(a.MinScale)),
			"maxReplicas": int64(a.MaxScale),
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "App",
		"metadata": map[string]any{
			"name":      a.Slug,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":               org,
				"hanzo.ai/managed-by":        "platform",
				"app.kubernetes.io/part-of":  project,
				"app.kubernetes.io/instance": a.Slug,
			},
		},
		"spec": spec,
	}}
}

// applyService create-or-updates the Service CR for an app in its tenant
// namespace and returns the effective image. On first deploy it Creates; on
// redeploy it merge-patches .spec (preserving operator-managed status). The
// namespace is ensured first. Every call is scoped to `ns == tenant-<org>`.
func (k *k8sClient) applyService(ctx context.Context, org, project string, a Application, image string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	if err := k.ensureNamespace(ctx, ns, org); err != nil {
		return fmt.Errorf("ensure namespace %s: %w", ns, err)
	}
	// Final replica clamp at the write boundary (defense in depth for MED-3: even
	// a row that somehow carried an out-of-bounds replica count is bounded here).
	a.Replicas = k.limits.clampReplicas(a.Replicas)
	a.StorageGB = k.limits.clampStorage(a.StorageGB)
	// The claim must exist before the CR that mounts it, or the first pod is
	// unschedulable until the next reconcile.
	if a.StorageGB > 0 {
		if err := k.ensureVolume(ctx, ns, org, a); err != nil {
			return err
		}
	}
	desired := serviceCR(ns, org, project, a, image, k.apex)
	gvr, found, err := k.resolveCR(ctx, ns, a.Slug)
	if err != nil {
		return err
	}
	if !found {
		_, cErr := k.dyn.Resource(k8s.Apps).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		return cErr
	}
	// Redeploy: merge-patch only .spec (+ labels), leaving the operator status and
	// resourceVersion intact.
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"labels": desired.Object["metadata"].(map[string]any)["labels"]},
		"spec":     desired.Object["spec"],
	})
	_, err = k.dyn.Resource(gvr).Namespace(ns).Patch(ctx, a.Slug, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// scaleService merge-patches .spec.replicas (stop == 0, start == declared). The
// operator reconciles the Deployment replica count.
func (k *k8sClient) scaleService(ctx context.Context, org, name string, replicas int) error {
	if err := k.ready(); err != nil {
		return err
	}
	// Bound the scale target: 0 (stop) passes through; a start clamps to
	// [1,maxReplicas] so scale-up can never exceed the per-app ceiling (MED-3).
	if replicas > 0 {
		replicas = k.limits.clampReplicas(replicas)
	}
	ns := tenantNamespace(org)
	gvr, found, err := k.resolveCR(ctx, ns, name)
	if err != nil {
		return err
	}
	if !found {
		return errNotFound
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": int64(replicas)}})
	_, err = k.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return errNotFound
	}
	return err
}

// deleteService removes an app's App CR (best-effort teardown on app/project
// delete). A NotFound is success (already gone).
// deleteService removes the App CR and NOT the app's volume. Deleting an app is a
// statement about a workload; the claim outlives it so that removing and
// redeploying an app under the same slug finds its data where it left it, and so
// that no misclick destroys a database. Reclaiming the storage is deliberate and
// separate.
func (k *k8sClient) deleteService(ctx context.Context, org, name string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	err := k.dyn.Resource(k8s.Apps).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// observeService reads the operator-reconciled status of an app's Service CR to
// derive a live health/phase signal for the app view. Best-effort: any error
// yields empty signals (honest unknown), never a fabricated status.
func (k *k8sClient) observeService(ctx context.Context, org, name string) (phase, health string) {
	if k.ready() != nil {
		return "", ""
	}
	ns := tenantNamespace(org)
	obj, err := k.getCR(ctx, ns, name)
	if err != nil {
		return "", ""
	}
	phase, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	return phase, healthFromStatus(status)
}

// appRoutePriority ranks an app's own route above the *.<apex> wildcard that
// sends every other name to the sites edge, and below the platform's own routes
// for names under the apex (100 and up). Unranked, a route's priority is its
// rule's length, and the wildcard's regexp outranks every short name: an app
// answered with the sites edge's 404.
const appRoutePriority = "90"

// ingressSpec renders the operator Service CR ingress block for a set of hosts,
// or nil when there are none. It is the ONE place the ingress + TLS shape is
// defined, shared by serviceCR (full-CR apply on deploy) and applyIngress (the
// incremental domain-change patch) so the two write paths can never diverge.
//
// A host one label under apex is covered by the apex's wildcard certificate,
// which the ingress serves for it by SNI, so an app answering only there asks for
// no certificate of its own: one the cluster never issues (cert-manager runs
// without its ingress-shim) names a secret that does not exist. Only a custom
// host asks for one.
//
// No class: the operator writes the cluster's own (DEFAULT_INGRESS_CLASS,
// "ingress"). A class named here wins over it, and "hanzo" names no IngressClass
// on the cluster.
func ingressSpec(hosts []string, apex string) map[string]any {
	if len(hosts) == 0 {
		return nil
	}
	spec := map[string]any{
		"enabled":     true,
		"hosts":       toAnySlice(hosts),
		"tls":         false,
		"annotations": map[string]any{"ingress.kubernetes.io/router.priority": appRoutePriority},
	}
	for _, h := range hosts {
		if apex == "" || !strings.HasSuffix(h, "."+apex) || strings.Contains(strings.TrimSuffix(h, "."+apex), ".") {
			spec["tls"], spec["clusterIssuer"] = true, "letsencrypt-prod"
			break
		}
	}
	return spec
}

// applyIngress merge-patches ONLY the Service CR's ingress block to match an
// app's current active host set — the operator-native way a domain add / remove /
// custom-verify takes effect BETWEEN full deploys (a deploy already renders the
// hosts via serviceCR). hosts>0 sets the cert-manager TLS ingress; hosts==0
// removes it (JSON null). If the app has no Service CR yet (never deployed), it
// is a no-op: the hosts persist in the app's DomainsJSON and render on the next
// deploy. Every call is scoped to ns == tenant-<org>.
func (k *k8sClient) applyIngress(ctx context.Context, org, name string, hosts []string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	// A JSON merge-patch replaces the ingress subtree when hosts>0 and removes the
	// key (null) when hosts==0 — so a removed last domain tears the Ingress down.
	var ingress any
	if ing := ingressSpec(hosts, k.apex); ing != nil {
		ingress = ing
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"ingress": ingress}})
	gvr, found, err := k.resolveCR(ctx, ns, name)
	if err != nil {
		return err
	}
	if !found {
		return nil // app not deployed yet — hosts render on the next deploy
	}
	_, err = k.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil // app not deployed yet — hosts render on the next deploy
	}
	return err
}

// observeDomains reads the operator-reconciled Service CR to report which of an
// app's ingress hosts are actually LIVE. The operator publishes status.endpoints
// (the URLs it materialized an Ingress + cert-manager cert for) and status.phase.
// This is the honest source for per-domain live/provisioning state in the console
// — never a fabricated "cert ready". Best-effort: any error yields empty (honest
// unknown / not-yet-deployed), never invented endpoints.
func (k *k8sClient) observeDomains(ctx context.Context, org, name string) (endpoints []string, phase string) {
	if k.ready() != nil {
		return nil, ""
	}
	ns := tenantNamespace(org)
	obj, err := k.getCR(ctx, ns, name)
	if err != nil {
		return nil, ""
	}
	phase, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
	eps, _, _ := unstructured.NestedStringSlice(obj.Object, "status", "endpoints")
	return eps, phase
}

// launchBuildJob creates an in-cluster BuildKit Job (arcd model) that builds the
// app's git source and pushes the output image to GHCR. Returns the Job name.
// This is the client-go port of buildkit-job.ts: moby/buildkit + buildctl over
// an HTTPS git context, output pushed to the per-tenant image ref. Runs in the
// central build namespace on the CI runner pool. If the cluster/CI prerequisites
// are absent the caller surfaces the error and the build lands FAILED — no fake
// success. l is whose build it is (laneOf), decided by the caller that
// authorized it.
func (k *k8sClient) launchBuildJob(ctx context.Context, l lane, org string, a Application, image, gitRef, buildID string) (string, error) {
	if err := k.ready(); err != nil {
		return "", err
	}

	// (CRIT-1) VALIDATE every attacker-controlled build input at the single choke
	// point that constructs the privileged Job. repo.url must be an https URL to
	// an allowlisted git host with no shell/flag metacharacters; a non-empty
	// dockerfile must be a safe relative path; a non-empty ref must be a safe
	// branch/tag/commit. This fails closed for a hostile persisted app row OR a
	// hostile deploy-body ref — the caller records the build FAILED and surfaces
	// the reason (never a fabricated success).
	ref := gitRef
	if strings.TrimSpace(ref) == "" {
		ref = cmp.Or(a.RepoBranch, "main")
	}
	cleanURL, cleanDockerfile, cleanRef, cleanImage, err := validateBuildInputs(a.RepoURL, a.Dockerfile, ref, image)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	image = cleanImage
	dockerfile := cleanDockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// (MED-3) An org's line is bounded, so a tenant cannot fill the apiserver with
	// Jobs in the shared build namespace; a build past its concurrency waits (dispatch.go).
	if err := k.admitBuild(ctx, org); err != nil {
		return "", err
	}

	// Deterministic Job name (arcd model): pf-build-<org>-<app>-<buildID[:12]>.
	// Because the build ID is unique per attempt, a retry of the SAME build
	// collides (409 AlreadyExists) rather than spawning a duplicate — the
	// idempotency key, no monotonic counter. The org slug is the INJECTIVE
	// normalizer so two orgs' identically-named apps never share a Job name.
	jobName := tenantBuildJobName(org, a.Slug, buildID)

	// buildctl-daemonless git frontend context: https://<repo>.git#<ref>.
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef

	// (CRIT-1) Emit buildctl as EXEC-FORM argv — NEVER a `sh -c` string. Each
	// validated value is a single argv element handed to execve, so no shell ever
	// parses it: a ';', '|', or '#' in an input can neither chain a command nor
	// smuggle a flag. The output image ref is FORCED server-side (the `image`
	// param, computed by buildImageRef from the validated tenant) and appears as
	// its own fixed argv element, so a client can never override --output/--opt to
	// push to another tenant's repo.
	command := buildFrontendCmdRev(l, buildCtx, dockerfile, image, image, cleanRef, nil)
	pushSecret, err := buildPushSecret(image)
	if err != nil {
		return "", err
	}
	job := k.buildJobSpec(l, jobName, buildID, org, a.Slug, pushSecret, image, command, defaultArch,
		solveSecrets(l, buildCtx, l == estate && githubRead(ctx, org, cleanURL, cleanRef)))
	if _, err := k.createBuildJob(ctx, job); err != nil {
		return "", err
	}
	return jobName, nil
}

// packFrontendImage is hanzoai/pack — the one canonical zero-config packer, a
// BuildKit gateway frontend that detects any project (Go, Node, Python, Rust,
// static, …) and emits a runnable OCI image. It is the default builder; a
// Dockerfile is the explicit escape hatch.
const packFrontendImage = "ghcr.io/hanzoai/pack:latest"

// buildFrontendCmd is the ONE way a build chooses its BuildKit frontend: an
// explicit Dockerfile uses dockerfile.v0; otherwise hanzoai/pack detects and
// builds with zero config. The output image is FORCED here as its own argv
// element, so a caller can never redirect --output to another repo.
//
// It declares no build secret: those are the Job's (solveSecrets, buildJobSpec).
func buildFrontendCmd(l lane, buildCtx, dockerfile, image string) []any {
	return buildFrontendCmdRev(l, buildCtx, dockerfile, image, image, "", nil)
}

// buildFrontendCmdArgs is buildFrontendCmdRev plus what one build selects: the
// stage it stops at (`target`), the repository directory that is its build
// context (`contextsubdir`), and the image's declared `--build-arg`s.
//
// contextsubdir scopes the build context alone. The dockerfile frontend still
// reads the Dockerfile from the repository root at `filename`, so a dockerfile
// path stays root-relative, the way hanzo.yml and `docker buildx build -f`
// spell it. Empty target and dir emit nothing.
//
// The build-args are appended AFTER the derived ones, and that order is
// the policy: buildkit takes the LAST value for a repeated build-arg, so a
// declaration cannot overwrite... which is exactly why VERSION and REVISION are
// filtered out of the declared set instead of relying on position. Those two are
// receipts — the tag the image is published under and the commit it was built
// from — and an image that can name a different commit than it was built from is
// the one lie the whole digest-pinned lane exists to prevent.
func buildFrontendCmdArgs(l lane, buildCtx, dockerfile, target, dir, image, push, revision string, args map[string]string, platforms []string, tags ...string) ([]any, error) {
	cmd := buildFrontendCmdRev(l, buildCtx, dockerfile, image, push, revision, platforms, tags...)
	if target != "" {
		cmd = append(cmd, "--opt", "target="+target)
	}
	if dir != "" {
		cmd = append(cmd, "--opt", "contextsubdir="+dir)
	}
	declared := make(map[string]string, len(args))
	for k, v := range args {
		switch k {
		case "VERSION", "GIT_VERSION", "REVISION":
			continue
		}
		declared[k] = v
	}
	extra, err := buildArgs(declared)
	if err != nil {
		return nil, err
	}
	return append(cmd, extra...), nil
}

// buildFrontendCmdRev is buildFrontendCmd plus the commit being built, which is
// the difference between an image that can be traced back to source and one that
// cannot.
//
// Every image this lane published carried `org.opencontainers.image.revision=
// unknown`. Not because the label was missing — cloud's Dockerfile declares
// `ARG REVISION=unknown` and stamps the label from it — but because nothing here
// ever passed REVISION, so the default won on every build. The other lane
// (docker/build-push-action) sets the label from the OUTSIDE, after the
// Dockerfile, so its images were fine and the gap was invisible unless you
// compared the two.
//
// It stopped being cosmetic the moment two lanes raced for one tag. With one
// image labelled 1b8b76ed and the other labelled `unknown`, "which of these is
// the release" had no answer that did not involve diffing layers — and the
// release that lost had already been pinned. A version is only a receipt if the
// image can name its own commit, so the arg is passed here and the label is true
// no matter which builder ran.

// `image` is what the build CLAIMS TO BE — the version it stamps into itself and
// the repository its layer cache is keyed on. `push` is where this Job writes it.
// The two are the same ref for every build but one half of a fan-out, which
// publishes at the release tag plus its architecture and would otherwise stamp
// that suffix into the binary's own version string.
//
// `tags` are more names for what `push` writes, in push's repository, written by
// the same export (imageNames). They never change what the build claims to be:
// VERSION and the cache key stay `image`'s.
//
// The lane decides where the solve's result goes. An estate solve pushes it and
// reads and writes the registry layer cache, with the registry credential in its
// own container. A customer solve holds no registry credential, so it neither
// pushes nor touches a cache: it exports the image to the Job's own volume
// (customerExport) and the Job's push container publishes `push` from there
// (customerJob).
func buildFrontendCmdRev(l lane, buildCtx, dockerfile, image, push, revision string, platforms []string, tags ...string) []any {
	cmd := []any{"buildctl-daemonless.sh", "build"}
	if strings.TrimSpace(dockerfile) != "" {
		cmd = append(cmd,
			"--frontend=dockerfile.v0",
			"--opt", "context="+buildCtx,
			"--opt", "filename="+dockerfile,
		)
	} else {
		cmd = append(cmd,
			"--frontend=gateway.v0",
			"--opt", "source="+packFrontendImage,
			"--opt", "context="+buildCtx,
		)
	}
	// The platform this solve targets. Unnamed, buildkit builds for the worker's
	// own architecture — which is the single-platform case, where the node has
	// already been chosen for the architecture asked for and naming it again would
	// say nothing. Named, it is what makes the output an image FOR something rather
	// than an image from wherever it happened to run.
	if len(platforms) > 0 {
		cmd = append(cmd, "--opt", "platform="+strings.Join(platforms, ","))
	}
	// No credential is declared here. Which fabric credentials a solve is handed
	// depends on whose build it is, which this argv does not know; solveSecrets
	// decides that at the choke point and buildJobSpec declares each one.
	//
	// Hand the image's own tag to the build as VERSION, so a binary can stamp the
	// version it was published under (cloud's Dockerfile links it into its host,
	// which serves it as the X-Api-Version header) instead of shipping the "dev"
	// default. The tag is already the authority here — deriving it means the
	// version can never drift from the ref that was pushed. A Dockerfile with no
	// `ARG VERSION` simply ignores it.
	// GIT_VERSION is the same value without the leading v, which is how a
	// Makefile that would otherwise run `git describe` writes it — a git context
	// carries no .git to describe from. Both come from ONE splitImageRef call so
	// they cannot disagree; a digest-pinned ref names no version, and
	// splitImageRef reports the digest in the tag position, so the colon check
	// suppresses both args rather than stamping a hash as a version.
	if _, tag := splitImageRef(image); tag != "" && tag != "latest" && !strings.Contains(tag, ":") {
		cmd = append(cmd, "--opt", "build-arg:VERSION="+tag)
		cmd = append(cmd, "--opt", "build-arg:GIT_VERSION="+strings.TrimPrefix(tag, "v"))
		cmd = append(cmd, "--opt", "label:org.opencontainers.image.version="+tag)
	}
	// Each receipt goes out TWICE — once as a build-arg the Dockerfile may read,
	// once as an image LABEL set from outside it. A build-arg reaches the image
	// only where the Dockerfile declares the matching ARG and stamps a LABEL from
	// it, and most do not: ci's declares neither, so its images name no commit at
	// all and a reader has to diff layers to learn which one is inside. `--opt
	// label:` needs nothing from the repository, so provenance stops being a
	// property of how carefully a Dockerfile was written.
	//
	// A build context may name a BRANCH, and a branch is not a revision: stamping
	// "main" here would make the label and the binary look populated while
	// answering a different question than the one anybody reads them for. Empty
	// only for callers that genuinely have no commit.
	//
	// provenance.IsCommit, not a private copy, because this is one END of a wire
	// whose other end applies the same rule to decide what it will REPORT (see
	// provenance.Revision). Two copies of "what is a commit" that drift apart would let
	// the builder pass a value the process silently downgrades to "unknown" — the
	// same silent failure, one layer over.
	if env.IsCommit(revision) {
		cmd = append(cmd, "--opt", "build-arg:REVISION="+revision)
		cmd = append(cmd, "--opt", "label:org.opencontainers.image.revision="+revision)
	}
	if l != estate {
		return append(cmd, "--output", customerExport)
	}
	// REGISTRY LAYER CACHE, both directions. Every build job is a fresh pod with an
	// empty local cache, so without this each one re-resolves and re-downloads its
	// whole dependency set from the network — for studio that is the entire torch
	// stack plus requirements on every push, and it is why a build takes tens of
	// minutes instead of a couple. The cache lives beside the image under a
	// `buildcache` tag (the standard convention) so it is per-repo, needs no extra
	// credentials, and is garbage-collected with the package.
	//
	// What the cache can and cannot buy is measured, not assumed — see cacheArgs.
	// Both flags are ADVISORY in buildkit: a missing or unreadable cache ref is a
	// cache miss, never a build failure, so a first build (or a registry hiccup)
	// behaves exactly as it does today. Skipped for a digest-pinned ref, which
	// names no tag to hang the cache off.
	// Keyed per architecture. Two halves of a fan-out build the same repository at
	// the same tag, so one cache ref would have each overwriting the other's index
	// on every build and neither ever reading its own.
	if repo, tag := splitImageRef(image); tag != "" && !strings.Contains(tag, ":") {
		for _, a := range cacheArgs(repo, cacheArch(platforms)) {
			cmd = append(cmd, a)
		}
	}
	// ZSTD, not buildkit's default gzip. This image is ONE 1.63GB layer — the
	// per-app plugin binaries, 98.8% of its 1.65GB — and gzip writes a layer as a
	// single stream on a single core, so the export ran at 23.7MB/s with seven of
	// the runner's eight CPUs idle: 175.0s and 174.7s on two consecutive builds.
	// Measured on those same bytes, both orderings, 2026-08-06: gzip 245.5s/256.1s
	// against zstd 58.1s/53.3s. The zstd result is also 1.2% SMALLER (1,626,561,616
	// vs 1,646,830,883 bytes), so every pull gets slightly cheaper too.
	//
	// oci-mediatypes is the enabling half, not decoration: a zstd layer is
	// application/vnd.oci.image.layer.v1.tar+zstd, and the Docker schema2 manifest
	// this lane emitted before has no media type that can name one. Setting the
	// compression without it would push layers no client could read.
	//
	// force-compression is load-bearing, not belt-and-braces. Without it buildkit
	// treats any already-compressed blob as good enough and reuses it: asked for
	// zstd over layers it had itself just written as gzip, it published an OCI
	// manifest whose layers were still ...layer.v1.tar+gzip, having done no
	// compression work at all. That failure is silent and lands in the "still
	// works, only slow" direction — the kind that survives review and quietly gives
	// back the whole saving. Forcing it means what ships is what was measured. The
	// price is re-compressing ~11MB of alpine base layers, under a second.
	//
	// Verified before shipping rather than assumed, because the blast radius is
	// "nothing in the fleet can pull our images": ghcr.io stores the zstd blobs,
	// and kubelet/containerd 1.7.28 pulled and RAN the resulting image on two
	// different node pools, including one that had never seen the bytes.
	//
	// One caller had to change WITH this and is not optional: imagePullable in
	// pin.go negotiates the manifest by Accept, and ghcr answers a type it was not
	// offered with 404. See the note there.
	// Do NOT pass --metadata-file /dev/termination-log: buildctl writes metadata
	// atomically via a sibling .tmp-* file in /dev/, which fails with permission
	// denied under rootless uid 1000 and readOnlyRootFilesystem. jobDigest reads
	// the pushed digest from BuildKit's exporting/pushing manifest log lines.
	return append(cmd,
		"--output", "type=image,"+imageNames(push, tags)+",push=true,"+imageEncoding,
	)
}

// imageEncoding is how every image this lane writes is encoded, pushed or
// exported: zstd layers under OCI media types, compressed by this build rather
// than reused (the reasons are on buildFrontendCmdRev's push). One value, so a
// customer image and an estate image are the same bytes for the same solve.
const imageEncoding = "compression=zstd,force-compression=true,oci-mediatypes=true"

// exportMount is a customer Job's export volume, and ociLayout the OCI image
// layout directory on it that the solve writes and the push container reads.
const (
	exportMount = "/out"
	ociLayout   = exportMount + "/image"
)

// customerExport is a customer solve's --output: the image as an OCI layout
// directory (tar=false) on the export volume, named nothing and pushed nowhere.
// The customer's build can rewrite anything on that volume before it exits, so
// the push container treats the layout as the customer's: it reads it
// read-only, and the registry re-hashes every blob it is sent (pushImage).
const customerExport = "type=oci,dest=" + ociLayout + ",tar=false," + imageEncoding

// imageNames is the image exporter's `name` attribute: ref, then each tag as a
// ref of the same repository. One export writes every name, so they name one
// manifest by construction rather than by a second push agreeing with the first.
//
// buildctl reads --output as CSV and the image exporter splits `name` on commas,
// so several names are ONE quoted field, `"name=a,b"`, and a single name is
// written bare, byte for byte as it always was. The quoting holds only because no
// name can carry a quote, a comma or an `=` of its own: every ref reaching here
// passed validateImageRef at a choke point — launchDirectBuild for a build,
// launchIndexBuild for an index — which is what keeps a tag from becoming an
// exporter attribute (M1).
func imageNames(ref string, tags []string) string {
	if len(tags) == 0 {
		return "name=" + ref
	}
	names := []string{ref}
	for _, t := range tags {
		names = append(names, tagRef(ref, t))
	}
	return `"name=` + strings.Join(names, ",") + `"`
}

// cacheArch names the architecture a cache belongs to, and nothing when the build
// did not say — which is the ref every cache in the registry is under today.
func cacheArch(platforms []string) string {
	if len(platforms) != 1 {
		return ""
	}
	return nativeArch[platforms[0]]
}

// cacheBucket is the object-store bucket the layer cache lives in. One bucket for
// the fabric, keyed inside by repository, so a new repo needs no provisioning.
const cacheBucket = "buildcache"

// cacheArgs points the layer cache at whichever backend this deployment can
// actually reach.
//
// THE OBJECT STORE IS THE RIGHT HOME and is selected by setting
// BUILD_CACHE_S3_ENDPOINT. A registry cache does not scale with the fabric:
// buildkit pulls the WHOLE cache onto the node before it can read any of it and
// pushes it back afterwards, so the cache's size lands on the same 105GB disk
// that holds the images, the snapshots and the build's own working set. That is
// what filled the runner pool — 79GB of retained layers with 26GB left — and
// evicted builds fifteen minutes in. S3 is read ranged and per-blob: the node
// holds the working set and nothing else.
//
// It is not the default because it is not yet REACHABLE. The build namespace has
// no network path to the object store on either endpoint (both time out), so
// selecting S3 today would point every build at a cache it cannot open. Which
// backend to use is therefore a deployment fact, not a code opinion: grant
// hanzo-build ingress to the s3 service, set the endpoint, and the cache moves
// with no code change. Until then the registry backend is what works.
func cacheArgs(repo, arch string) []string {
	if ep := env.Or("BUILD_CACHE_S3_ENDPOINT", ""); ep != "" {
		common := "type=s3,bucket=" + env.Or("BUILD_CACHE_S3_BUCKET", cacheBucket) +
			",region=" + env.Or("S3_REGION", "us-east-1") +
			",endpoint_url=" + s3CacheEndpoint(ep) +
			",use_path_style=true,name=" + cacheKey(repo) + suffix(arch)
		// mode=min, for the reason spelled out on the registry branch below: max
		// re-compresses and re-uploads the whole build stage to buy back a few
		// seconds of `apk add`.
		return []string{"--import-cache", common, "--export-cache", common + ",mode=min,compression=zstd"}
	}
	ref := repo + ":buildcache" + suffix(arch)
	// mode=min. The claim this used to carry — that max keeps the Go compiles warm
	// — is not what the builds do. Two independent cloud builds imported this cache
	// and got the SAME eight cached steps: `apk add`, `adduser`, the libsqlcipher
	// symlink and WORKDIR, once per stage. Every expensive step missed both times
	// and had to: `COPY . .` sits in front of them and its digest changes with
	// every commit, and the compiles write into `--mount=type=cache`, which is
	// worker-local and never travels in an exported cache at all — a build pod is
	// fresh, so it starts them cold no matter what this flag says.
	//
	// What max charged for those eight steps: it exports every intermediate stage,
	// and the build stage holds the same 4.2GB of plugin binaries the image does,
	// so buildkit compressed them a SECOND time and pushed a second 1.6GB blob.
	// Measured 2026-08-06 — 221.3s and 225.2s of a 17-minute build, reproduced at
	// 212.1s on a synthetic build of the same shape, against 1.8s for min.
	//
	// Four of the eight steps are stage-2, i.e. layers of the FINAL image, so min
	// still exports them. Only the four build-stage records are given up, and those
	// are worth 4.6s cold (measured: 4.4 + 0.1 + 0.1 + 0.0).
	//
	// compression matches the image's, which is what keeps min cheap: min exports
	// the final image's layers, so if the two settings disagree buildkit has to
	// re-compress all of them here and the saving is handed straight back.
	//
	// Both flags stay ADVISORY in buildkit — a missing or unreadable cache ref is a
	// cache miss, never a build failure — so --import-cache keeps working unchanged
	// against a ref written either way, including one written by the old mode.
	return []string{
		"--import-cache", "type=registry,ref=" + ref,
		"--export-cache", "type=registry,ref=" + ref + ",mode=min,compression=zstd",
	}
}

// cacheKey names one repository's cache inside the shared bucket, so a new repo
// needs no provisioning and two repos never read each other's layers.
func cacheKey(repo string) string {
	return strings.ReplaceAll(strings.TrimPrefix(repo, "ghcr.io/"), "/", "-")
}

// s3CacheEndpoint normalizes the configured address to a URL. A bare host takes
// http unless S3_ADMIN_SECURE says otherwise — the same convention artifactPutBase
// uses for the internal write path.
func s3CacheEndpoint(ep string) string {
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		return ep
	}
	if strings.EqualFold(env.Or("S3_ADMIN_SECURE", "false"), "true") {
		return "https://" + ep
	}
	return "http://" + ep
}

// forgeTokenSecret is the Secret (same namespace as the build Jobs) whose `token`
// key authenticates reads of the fabric's OWN forge. Two lanes present it: the
// artifact lane's prepare step as GIT_TOKEN (artifact.go), which clones
// git.hanzo.ai and nothing else, and the BuildKit git context as GIT_AUTH_TOKEN,
// for an estate build whose context is the forge (solveSecrets). It reconciles
// from KMS orgs/hanzo/deploy/FORGE_TOKEN@prod — universe
// infra/k8s/hanzo-build/forge-token-kmssecret.yaml.
//
// NAMED FOR THE HOST IT AUTHENTICATES TO. git.hanzo.ai and github.com both speak
// git and take different credentials, so a name saying only "git" leaves the two
// interchangeable, and the forge answers a credential it does not know by asking
// for a username — which reads as no credential rather than the wrong one. A
// foreign host in the allowlist serves anonymously what it serves anonymously and
// is never challenged for this one.
//
// NOT optional, unlike registryTokenSecret below. REQUIRE_SIGNIN_VIEW is on, so
// the forge refuses an anonymous fetch even of a public repo, and the fabric
// builds its own repositories. A required mount stops the pod on the Secret and
// the key it wanted; an optional one starts it and leaves the absence to surface
// as a clone asking for a username.
const forgeTokenSecret = "forge-token"

// registryTokenSecret is the Secret (same namespace as the build Jobs) whose
// `token` key reads the fabric's own package registry — git.hanzo.ai serves npm
// and Go modules, and REQUIRE_SIGNIN_VIEW gates them, so an anonymous install
// 401s even against a public repo's packages. Kept apart from forgeTokenSecret
// because the two authenticate to different SERVICES on that host and rotate on
// their own schedules; folding them into one credential is how a package read
// starts depending on a git-fetch rotation. Optional, and here that is real
// rather than residual: a build with no package credential installs from public
// registries exactly as before, which is a working build, not a silent one.
const registryTokenSecret = "forge-registry-token"

// githubReadSecret is the Secret (same namespace as the build Jobs) whose
// `gh_token` key reads private GitHub repositories — the C++ and GPU sources
// under lux-cpp and lux-gpu that the forge does not mirror and that a Lux or Zoo
// image compiles from source. The solve receives it as the GH_READ_TOKEN build
// secret, which a RUN step mounts (`--mount=type=secret,id=GH_READ_TOKEN`) and
// which never enters a layer, a build-arg or the image history.
const githubReadSecret = "gh-read"

// githubReadRepos names, per org, the EXACT GitHub repositories whose builds
// may be handed githubReadSecret. It is keyed on BOTH the org (the build's
// credential names it — runnerOrg or the plane's stated caller, never a request
// field) AND the repository being built, and it denies by default: a build gets
// the secret only when its org appears here and the repository it clones is in
// that org's list.
//
// Org alone is not enough. The secret reads Lux's private C++/GPU sources, so an
// org that may build ONE repo needing them must not thereby hand the token to
// every other repo it can build — a zoo admin building a repo of its own would
// otherwise walk off with a token for lux-cpp/*. So zoo gets zooai/node, which
// compiles lux-cpp/node, and nothing else; lux gets its own node and lux-cpp/node.
//
// Every entry is a repository ON github.com. githubReadRepo matches the host as
// well as the owner/name pair, so the same two segments on another host — a forge
// mirror, another provider, a subgroup path ending in them — name a different
// repository and get nothing.
//
// FOLLOW-UP (not this change): mint a short-lived GitHub App installation token
// per build, scoped to exactly the repos that build reads, so the credential is
// per-build rather than a standing all-of-lux-private token. This allowlist and
// the trusted-ref rule (githubRead) are the containment until that exists.
var githubReadRepos = map[string][]string{
	"hanzo": {"hanzo-inc/engine", "hanzo-inc/kai", "hanzo-inc/zen", "hanzoai/git", "hanzoai/bot-hub", "hanzoai/console", "hanzo-apps/console", "hanzoai/playground", "hanzoai/studio-v1"},
	"lux":   {"luxfi/node", "lux-cpp/node"},
	"zoo":   {"zooai/node"},
}

// githubReadRepo is the owner/name a build for org clones when that repository is
// one githubReadRepos admits: https on github.com exactly, no credentials, port,
// query or fragment, a path of exactly owner/name (a trailing .git or slash
// allowed), and that pair — case-folded — in org's list. Anything else is "".
func githubReadRepo(org, repoURL string) (owner, name string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != "github.com" ||
		u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	for _, allowed := range githubReadRepos[org] {
		if strings.EqualFold(allowed, parts[0]+"/"+parts[1]) {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

// githubRead decides whether a build for org, cloning repoURL at ref, is handed
// githubReadSecret.
//
// The repository must be one githubReadRepo admits, AND the ref must be one that
// repository publishes from: its default branch, a protected branch, a tag, or a
// full commit one of those branches contains. The build runs the Dockerfile AT
// that ref with the secret mounted, so the ref decides whose code holds the
// credential — a pull request's ref, an unprotected branch or a commit only a fork
// holds is written by whoever opened it, not by the repository's maintainers.
//
// The ref is judged by GitHub, through the App's own installation on the
// repository (provider's integrations_github_ref), and every doubt withholds: an
// unreachable provider, a refusal and a timeout all mean no secret. A build that
// is not handed it still starts; its RUN steps find no secret mounted.
func githubRead(ctx context.Context, org, repoURL, ref string) bool {
	owner, name, ok := githubReadRepo(org, repoURL)
	if !ok || strings.TrimSpace(ref) == "" {
		return false
	}
	return githubTrusts(ctx, org, owner, name, ref)
}

// githubTrusts asks GitHub, through the platform App's own installation on
// owner/name (provider's integrations_github_ref), whether ref is the default
// branch, a protected branch, a tag, or a full commit one of those contains. An
// unreachable provider, a refusal and a timeout are all no.
func githubTrusts(ctx context.Context, org, owner, name, ref string) bool {
	ctx, cancel := context.WithTimeout(cloud.For(ctx, org), grantBudget)
	defer cancel()
	out, err := providerpeer.IntegrationsGitHubRef(ctx, &client.GitHubRefIn{Owner: owner, Repo: name, Ref: ref})
	return err == nil && out != nil && out.Trusted
}

// solveSecret is one fabric credential a build's solve can be handed. id is the
// build-secret id a RUN step mounts (`--mount=type=secret,id=<id>`) and the env
// var buildctl reads it from; secret and key name the Secret that env var is
// sourced from. buildJobSpec renders the declaration and the env var from the one
// value, so a Job never holds a credential its solve does not declare, and never
// declares one its env does not hold.
//
// perBuild marks a credential minted for ONE build and held in a Secret that
// build's Job owns (checkoutSecret). It is the only kind a customer's solve is
// handed (buildJobSpec).
type solveSecret struct {
	id, secret, key string
	optional        bool
	perBuild        bool
}

// handSecret declares s to the solve (`--secret id=<id>,env=<env>`) and sources
// that env var from s's Secret in c, from the one value, and answers the argv.
// The value only ever arrives by secretKeyRef: never argv, never a build-arg.
func handSecret(c map[string]any, argv []any, s solveSecret) []any {
	ref := map[string]any{"name": s.secret, "key": s.key}
	if s.optional {
		ref["optional"] = true
	}
	c["env"] = append(c["env"].([]any), map[string]any{"name": s.env(), "valueFrom": map[string]any{"secretKeyRef": ref}})
	return append(argv, "--secret", "id="+s.id+",env="+s.env())
}

// env is the variable buildctl reads s from: the id with every character a shell
// name cannot hold made '_'. A per-host id carries dots, and buildctl-daemonless.sh
// is a shell that execs buildctl, which may drop a variable whose name is not one.
func (s solveSecret) env() string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s.id)
}

var (
	// forgeRead opens the forge's repositories to the git context: buildkit's
	// gitsource presents GIT_AUTH_TOKEN as the HTTPS credential. Required, for
	// the reason stated on forgeTokenSecret.
	forgeRead = solveSecret{id: "GIT_AUTH_TOKEN", secret: forgeTokenSecret, key: "token"}

	// Both names carry the same KMS-backed value; no second credential exists.

	// registryRead reads the fabric's own package registry, for a build that
	// installs the estate's npm or Go packages. Optional in the strong sense:
	// buildkit's secretsprovider skips its stat when `env` is named and answers a
	// missing variable with an EMPTY secret rather than an error (v0.16.0 store.go
	// NewStore/GetSecret), so a build with no Secret behind it installs from
	// public registries.
	registryRead = solveSecret{id: "REGISTRY_TOKEN", secret: registryTokenSecret, key: "token", optional: true}

	// githubSource reads the private GitHub sources githubReadRepos names.
	githubSource = solveSecret{id: "GH_READ_TOKEN", secret: githubReadSecret, key: "gh_token", optional: true}

	// githubCheckout is the same credential presented to the git CONTEXT. The
	// frontend clones the context before any step runs, so a private repository
	// named by a github.com context is read with this or not at all: without it
	// the solve dies on "could not read Username for 'https://github.com'". The
	// id is buildkit's per-host name, so the token goes to github.com alone and
	// the forge context keeps forgeRead.
	githubCheckout = solveSecret{id: "GIT_AUTH_TOKEN.github.com", secret: githubReadSecret, key: "gh_token", optional: true}
)

// inEstate reports whether org is one the forge's closed table names
// (forge.Owner): the estate's own orgs, whose builds are the fabric's and are
// handed its credentials. Every other org is a customer.
//
// The org goes through namespace.Sanitize first because forge.Owner folds case
// and this answer must not. IAM orgs are byte-exact, so `Hanzo` is a tenant of
// its own and not the estate; Sanitize is the identity on a clean slug and gives
// any other spelling a hashed suffix, so only the table's own spelling matches.
func inEstate(org string) bool {
	_, err := forge.Owner(namespace.Sanitize(org))
	return err == nil
}

// lane is whose build one BuildKit build is, and it decides what the build's
// Jobs hold. It is decided once, by the handler that authorized the build
// (laneOf), and carried to every Job the build launches.
//
// The zero value is customer, so a build nobody decided for holds nothing.
type lane int

const (
	// customer is every build the estate's staff did not ask for. Its RUN steps
	// share buildkitd's user and PID namespaces (buildJobSpec), so nothing the
	// solve's container holds is kept from them: the container holds no fabric
	// credential and no registry credential, the image leaves it as an OCI layout
	// on a volume, and a separate container that runs no customer code pushes it
	// (customerJob).
	customer lane = iota
	// estate is a build of an estate org asked for by that org's staff. Its
	// Dockerfile is the fabric's own, so the solve holds the push credential, the
	// layer cache and the forge credentials solveSecrets names.
	estate
)

// laneOf is the lane of a build charged to org, asked for by a caller that is,
// or is not, staff of it: a SuperAdmin or an admin of org (cloud.Admin, the
// platform's one rule), or org's own machine identity where the entry point
// admits one. Both halves are required. The org name alone is not enough: a
// member of an estate org acts as that org (an IAM invitation makes one) and is
// no more the estate's staff for it than a customer is.
func laneOf(org string, staff bool) lane {
	if staff && inEstate(org) {
		return estate
	}
	return customer
}

// solveSecrets is every fabric credential one build is handed, decided at the
// choke point that launches it (launchBuildJob, launchDirectBuild) from the
// build's lane, the git context it clones, and the caller's githubRead verdict.
//
// A declared build secret belongs to whoever wrote the Dockerfile: any RUN step
// can mount it (`RUN --mount=type=secret,id=REGISTRY_TOKEN cat
// /run/secrets/REGISTRY_TOKEN`), and buildkit presents GIT_AUTH_TOKEN to any git
// source the Dockerfile names. So the fabric's credentials go to an estate build
// only. A customer's build is handed neither the declaration nor the env var:
// buildkitd runs with --oci-worker-no-process-sandbox, which buildkit documents
// as letting a build step signal and, seccomp permitting, ptrace the daemon's
// processes, so an env var the solve never declared is still not one to leave
// in the pod. A customer that needs the estate's private packages is not served
// by this credential.
//
// Within the estate, the forge's git credential goes to the forge and nowhere
// else. buildkit presents GIT_AUTH_TOKEN to whatever host the git CONTEXT names,
// and github.com answers a credential it does not know with 401 — which git
// reports as `could not read Username for 'https://github.com'`, so a PUBLIC
// repository would not clone while holding a credential it never needs. Naming
// the host's own GIT_AUTH_TOKEN.<host> with an empty value does not help:
// buildkit treats an empty per-host token as absent and falls back to the
// forge's. The only anonymous fetch is one with no credential declared at all.
//
// The package-registry credential is separate from the git one because they
// authenticate to different services on the forge and rotate independently.
//
// GH_READ_TOKEN and its checkout twin have their own admission besides, by org,
// repository and ref (githubRead), and only estate orgs appear in githubReadRepos.
func solveSecrets(l lane, buildCtx string, github bool) []solveSecret {
	if l != estate {
		return nil
	}
	var out []solveSecret
	if h := gitHost(buildCtx); h == "" || h == forgeHost {
		out = append(out, forgeRead)
	}
	out = append(out, registryRead)
	if github {
		out = append(out, githubSource, githubCheckout)
	}
	return out
}

// buildkitRootlessImage is the UNPRIVILEGED buildkit variant. The build Job runs
// it rootless (uid 1000, no privileged, process sandbox disabled) so a hostile
// Dockerfile has no host-root / device / privileged-syscall surface to escape
// from (H2). It is a public image on Docker Hub — no pull secret needed.
//
// v0.26 takes a tag that moved under a kept checkout (moby/buildkit#6259); older
// ones fail the fetch. universe's warm builder runs the same release.
const buildkitRootlessImage = "moby/buildkit:v0.26.3-rootless"

// buildCacheLimit bounds the buildkitd emptyDir — the build's whole working set,
// and the only thing in the pod that grows without bound.
//
// It is a bound, not a measurement: half of a runner node's ~105G, which also
// carries the node's images, its snapshots and any concurrent build. The number
// matters far less than having one, because the failure it prevents is not this
// build running out of room — it is the NODE running out, which the kubelet
// answers by evicting every pod on it.
//
// The consts below bound this package's OTHER emptyDirs for the same reason,
// and they are stated here together because "how much node disk may a job
// take" is one question with several answers, not unrelated numbers. An
// uncapped emptyDir is the default, and the default is the bug: nothing about
// `emptyDir: {}` says it is charged to the node's ephemeral storage, so every
// one of these read as harmless scratch until a runaway filled a runner rootfs.
const (
	buildCacheLimit = "50Gi"

	// artifactWorkspaceLimit bounds the artifact job's /w — the clone, HOME,
	// GOPATH, the npm cache and every produced binary for every platform, for
	// every recipe entry in turn, all in one volume. Same half-a-node bound as
	// the build cache, and for the same reason: a recipe that loops is bounded
	// by this or by the node.
	artifactWorkspaceLimit = "50Gi"

	// exportLimit bounds a customer build's export volume (customerJob): the
	// finished image as an OCI layout, compressed layers and manifests only,
	// held from the end of the solve until the push container has sent it. The
	// estate's largest image, cloud, is 1.6G compressed; this is room for an
	// image an order of magnitude larger, and a ceiling on the node's disk.
	exportLimit = "20Gi"
)

// pushImage is the push container of a customer build (customerJob): crane,
// go-containerregistry's registry client, at the version cloud's own go.mod
// requires, pinned by the digest of its multi-architecture index. It is a
// static Go binary on a distroless base with no shell, so the container can run
// exactly the argv it is given and nothing else. Its layout reader refuses a
// blob that is a symlink or not a regular file, and the registry re-hashes
// every blob it is sent against the digest named for it, so a layout the
// customer's build tampered with cannot make it upload a file of the push
// container's own.
const pushImage = "gcr.io/go-containerregistry/crane:v0.21.7@sha256:3a62904867999848c5b16bb9b1eb8b2a7bbaa6061203a90037c91bf33b6b567e"

const buildPushSecretPrefix = "push-"

func buildPushSecret(image string) (string, error) {
	ns, ok := imageRegistryNamespace(image)
	if !ok || !ownedNamespaces[ns] {
		return "", fmt.Errorf("no owned registry namespace for image %q", image)
	}
	return buildPushSecretPrefix + ns, nil
}

// buildJobSpec is the shared build Job for both the tenant build (launchBuildJob)
// and the direct build (launchDirectBuild) — one spec, one place. It is ROOTLESS
// and defense-in-depth hardened (H2):
//
//   - moby/buildkit:*-rootless as uid/gid 1000, runAsNonRoot, privileged=false,
//     seccomp/AppArmor Unconfined + --oci-worker-no-process-sandbox — it
//     user-namespaces the build instead of relying on host privilege, the
//     documented moby/buildkit k8s rootless posture with NO host-root escape.
//     allowPrivilegeEscalation and the default capability set are LEFT at k8s
//     defaults on purpose: rootlesskit's setuid newuidmap/newgidmap need them
//     (dropping ALL caps or no-new-privs breaks the sub-uid mapping — proven by
//     an on-cluster canary). See the container securityContext below.
//   - pushSecret is the caller-resolved push credential for push's registry
//     namespace (push-<namespace>), mounted read-only as the only registry
//     credential in the pod. It writes that WHOLE namespace — push-hanzoai
//     writes every ghcr.io/hanzoai image, the fleet's own included — so where it
//     is mounted is the lane's decision: in the solve's own container on the
//     estate lane, and only in the push container of a customer Job
//     (customerJob), which runs no customer code.
//   - runs in the ISOLATED build namespace (k.buildNS, defaulted off the main
//     platform namespace) with automountServiceAccountToken=false, and only on the
//     CI pool — the nodes labelled hanzo.ai/visor=true (placeBuild).
//   - secrets are the build secrets the solve is handed (solveSecrets); the Job
//     declares and sources exactly those. A customer Job declares none of the
//     fabric's: at most its own build's checkout token (perBuild, checkout.go).
//
// push is the ref a customer Job's push container publishes; an estate solve
// names its own in command.
func (k *k8sClient) buildJobSpec(l lane, jobName, buildID, org, app, pushSecret, push string, command []any, arch string, secrets []solveSecret) *unstructured.Unstructured {
	spec := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      jobName,
			"namespace": k.buildNS,
			"labels": map[string]any{
				"hanzo.ai/org":         org,
				"hanzo.ai/managed-by":  "platform",
				"hanzo.ai/application": app,
				"hanzo.ai/build":       "true",
				// WHICH BUILD THIS JOB IS HALF OF. A fan-out is several Jobs asked for
				// once, so the concurrency ceiling has to count this and not Jobs — a
				// two-platform default against a Job count would halve every org's
				// ceiling the day it landed.
				"hanzo.ai/build-id": jobIDSuffix(buildID),
			},
		},
		"spec": map[string]any{
			// Created suspended: the Job is its place in the line, and dispatchBuilds
			// resumes it when the org and the cluster have room (dispatch.go).
			"suspend":          true,
			"backoffLimit":     int64(0),
			"podFailurePolicy": lostNodeRetries(),
			// A finished build holds its buildkitd emptyDir until the POD is deleted,
			// so this TTL is disk time, not just record-keeping. At an hour, with a
			// build every few minutes, six finished pods sat on tens of gigabytes
			// each and the next build was evicted off a node they had filled. Ten
			// minutes is long past the terminal-state read (waitForJob polls every
			// 5s) and long enough to pull logs from a failure.
			"ttlSecondsAfterFinished": int64(600),
			"template": map[string]any{
				"metadata": map[string]any{
					// A Job copies only its POD TEMPLATE's labels onto the pod — the four
					// on the Job above stay on the Job, where the line reads them (dispatch.go).
					// So the build pod carried no label of ours at all, and hanzo-build's
					// Cilium policy selects PODS: build-egress-deny-internal denies every
					// internal CIDR and artifact-publish-egress reopens s3:9000 only for
					// hanzo.ai/publish=artifact. Unlabelled, the pod could not reach the
					// object store — which is where its layer cache belongs. Without it the
					// cache is a registry blob pulled WHOLE onto the node's 105GB disk
					// beside the images, the snapshots and the build's own working set, and
					// builds are evicted mid-run.
					//
					// A customer pod carries none (podLabels): it holds no object-store
					// credential, so its build has no use for the object store and its pod
					// keeps no path to it.
					"labels": podLabels(l),
					// AppArmor unconfined for the rootless worker. The beta annotation is
					// honored on older nodes; securityContext.appArmorProfile (below) is the
					// GA form on k8s ≥1.30 — both set so ONE spec is correct across versions.
					"annotations": map[string]any{
						"container.apparmor.security.beta.kubernetes.io/buildkit": "unconfined",
					},
				},
				"spec": map[string]any{
					"restartPolicy": "Never",
					// Builds yield. hanzo-ci sits below hanzo-infra-critical and
					// hanzo-money-critical, so a queue of them can never push a serving
					// workload off a node, and raising a single run's class is how one
					// build gets moved to the front without stopping the others.
					"priorityClassName":            "hanzo-ci",
					"automountServiceAccountToken": false,
					// Pod-level: run the whole pod as the non-root buildkit user.
					"securityContext": map[string]any{
						"runAsUser":      int64(1000),
						"runAsGroup":     int64(1000),
						"runAsNonRoot":   true,
						"seccompProfile": map[string]any{"type": "Unconfined"},
					},
					"containers": []any{map[string]any{
						"name":    "buildkit",
						"image":   buildkitRootlessImage,
						"command": command,
						// A failed solve's log tail becomes its termination message,
						// which is where jobReason reads why the build failed.
						"terminationMessagePolicy": "FallbackToLogsOnError",
						// Rootless (user-namespaced) — NOT privileged. This is the documented
						// moby/buildkit rootless posture for Kubernetes: no host root, no
						// device access; the build is confined to uid 1000 and its sub-uid
						// range. allowPrivilegeEscalation and the default capability set are
						// LEFT at the k8s defaults on purpose — rootlesskit's setuid
						// newuidmap/newgidmap (which set up the sub-uid mapping) require them;
						// dropping ALL caps or setting no-new-privs breaks the user-namespace
						// setup (proven by an on-cluster canary). The isolation win over the
						// previous privileged=true is already decisive.
						// A build's working set is DISK: the git clone, the module cache,
						// 112 plugin binaries, the exported layer cache. With no request the
						// pod is best-effort for ephemeral-storage, so the scheduler will
						// place it on a node that has no room and the kubelet evicts it
						// FIRST when that node fills — which is how a release died with
						// "The node was low on resource: ephemeral-storage … request is 0"
						// after ten minutes of work. Declaring the request is what lets the
						// scheduler avoid a full node; the limit bounds a runaway build
						// instead of letting it take the node down for everything else.
						"resources": map[string]any{
							// 12Gi, not 50 and not 32: the request has to FIT beside buildkitd
							// on a pool that is BUSY. Each
							// runner-pool-32g node allocates ~88Gi of ephemeral storage and
							// the buildkitd DaemonSet reserves 48Gi of it on every one, so a
							// 50Gi request left no node able to hold the pod and every build
							// sat Pending forever — "0/34 nodes are available … 3 Insufficient
							// ephemeral-storage", with the autoscaler declining to scale up
							// because no larger node matched either. Six builds queued for
							// five days and a whole repo stopped publishing images, with
							// nothing failing anywhere to say so.
							//
							// The reason the request exists is unchanged and still right: a
							// best-effort pod gets placed on a full node and is evicted first.
							// It just has to be a number the pool can actually satisfy WHILE
							// other builds are running. 32Gi fitted only an idle pool: one
							// in-flight build takes 24Gi of the ~40Gi left beside buildkitd,
							// so the next one went Pending — and a pool whose builds never
							// schedule looks idle, so the autoscaler deleted a node and
							// cordoned another. The shortage feeds itself. 12Gi schedules
							// against a busy node and is ample for a clone plus a module
							// cache. The limit stays 80Gi so a big build still bursts.
							//
							// CPU and memory are DECLARED for the same reason and a different
							// failure. The build namespace carries a LimitRange, so a container
							// that names neither takes its 2-CPU default — and a multi-stage
							// Dockerfile runs its stages in PARALLEL, so two cross-compiles and
							// an `npm ci` share those two cores. The install then leaves its
							// pooled sockets idle past the package registry's keep-alive window,
							// measured between 360s and 420s, and ends on ECONNRESET, which
							// reads as a broken build network and is a starved one. Measured on
							// hanzoai/team, same commit and same builder: the install died at
							// 1130s under the default and finished in 396s at 8 CPU, the whole
							// two-platform build in fifteen minutes. Requests sit well under the
							// limits because CPU is compressible — a build bursts into an idle
							// node and yields when one is busy.
							"requests": map[string]any{"cpu": "2", "memory": "4Gi", "ephemeral-storage": "12Gi"},
							"limits":   map[string]any{"cpu": "8", "memory": "16Gi", "ephemeral-storage": "80Gi"},
						},
						"securityContext": map[string]any{
							"privileged":      false,
							"runAsUser":       int64(1000),
							"runAsGroup":      int64(1000),
							"runAsNonRoot":    true,
							"seccompProfile":  map[string]any{"type": "Unconfined"},
							"appArmorProfile": map[string]any{"type": "Unconfined"},
						},
						"env": []any{
							// --oci-worker-no-process-sandbox lets buildkitd run rootless with
							// no privileged process sandbox (it user-namespaces the build).
							map[string]any{"name": "BUILDKITD_FLAGS", "value": "--oci-worker-no-process-sandbox"},
							// The registry credential and the object-store cache credential
							// are added below, ESTATE ONLY.
						},
						"volumeMounts": []any{
							// Rootless buildkitd state (worker cache + snapshots) under the
							// buildkit user's home — a writable emptyDir, required rootless.
							map[string]any{"name": "buildkitd", "mountPath": "/home/user/.local/share/buildkit"},
						},
					}},
					"volumes": []any{
						// buildkitd's worker cache + snapshots — the whole of a build's
						// working set, and the only thing in this pod that grows without
						// bound. An emptyDir is charged to the NODE's ephemeral storage,
						// so uncapped, one runaway build fills the node rootfs, trips
						// DiskPressure, and the kubelet evicts every pod on that node
						// rather than the build that caused it. A cap makes an overrun
						// evict THIS pod only, which is the one that can be retried.
						map[string]any{"name": "buildkitd", "emptyDir": map[string]any{"sizeLimit": buildCacheLimit}},
					},
				},
			},
		},
	}
	placeBuild(podSpec(spec), arch)
	pod := podSpec(spec)
	c := pod["containers"].([]any)[0].(map[string]any)
	if l != estate {
		// A customer's solve holds no credential of the fabric's. The one it may
		// hold is its own build's (perBuild): a token minted for this build alone,
		// in a Secret its Job owns (checkout.go). Anything else in secrets is
		// dropped here rather than trusted to every caller to have left out.
		declared := slices.Clone(command)
		for _, s := range secrets {
			if s.perBuild {
				declared = handSecret(c, declared, s)
			}
		}
		c["command"] = declared
		return customerJob(spec, pushSecret, push)
	}
	// The registry credential, for the push and the layer cache: buildkitd reads
	// DOCKER_CONFIG for both. On this lane alone, because the solve's RUN steps
	// can read everything its container holds.
	c["env"] = append(c["env"].([]any), map[string]any{"name": "DOCKER_CONFIG", "value": registryConfig})
	c["volumeMounts"] = append([]any{registryMount()}, c["volumeMounts"].([]any)...)
	pod["volumes"] = append([]any{registryVolume(pushSecret)}, pod["volumes"].([]any)...)
	// Each fabric credential the build was handed (solveSecrets): declared to the
	// solve and sourced from the Job env, both from the one value. A credential
	// not in the list is in neither, so its Secret is never referenced by this Job.
	// The value only ever arrives by secretKeyRef — never argv, never a build-arg.
	if nativeForgeBuilds() && l == estate {
		addForgeIdentity(pod)
		c["volumeMounts"] = append(c["volumeMounts"].([]any), forgeIdentityMount(true))
	}
	declared := slices.Clone(command)
	for _, s := range secrets {
		if nativeForgeBuilds() && l == estate && forgeReaderSecret(s) {
			declared = append(declared, "--secret", "id="+s.id+",src="+readerTokenPath)
			continue
		}
		declared = handSecret(c, declared, s)
	}
	// The object-store cache credential (artifact-s3) is the object store's ADMIN
	// key, and buildkitd holds it in its process environment. The image lane runs
	// buildkitd rootless with --oci-worker-no-process-sandbox under seccomp
	// Unconfined, so a build step shares the daemon's namespaces and can read its
	// /proc/<pid>/environ — a customer's Dockerfile would walk off with the admin
	// key. So it is handed to an ESTATE build only, whose Dockerfile is the
	// fabric's own. The credential is optional (absent ⇒ a cache miss, never a
	// failed build), and the S3 cache backend is selected by
	// BUILD_CACHE_S3_ENDPOINT (cacheArgs). The scoped fix is a bucket-read-only
	// key; until one exists, no admin key rides in a customer pod.
	c["env"] = append(c["env"].([]any),
		map[string]any{"name": "AWS_ACCESS_KEY_ID", "valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": artifactS3Secret, "key": "access-key", "optional": true},
		}},
		map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": artifactS3Secret, "key": "secret-key", "optional": true},
		}},
	)
	c["command"] = declared
	return &unstructured.Unstructured{Object: spec}
}

// registryConfig is where a registry credential is mounted, as the directory
// DOCKER_CONFIG names: buildkitd reads it on the estate lane, crane in a
// customer Job's push container.
const registryConfig = "/ghcr"

// registryVolume is the push credential as a docker config.json, and
// registryMount mounts it read-only at registryConfig. Which container of a
// build Job mounts it is the lane's decision (buildJobSpec, customerJob).
func registryVolume(pushSecret string) map[string]any {
	return map[string]any{
		"name":   "ghcr",
		"secret": map[string]any{"secretName": pushSecret, "items": []any{map[string]any{"key": ".dockerconfigjson", "path": "config.json"}}},
	}
}

func registryMount() map[string]any {
	return map[string]any{"name": "ghcr", "mountPath": registryConfig, "readOnly": true}
}

// podLabels are the build pod's own labels, which hanzo-build's Cilium policy
// selects on (buildJobSpec). An estate pod may reach the object store for its
// layer cache; a customer pod holds no credential for it and is given no path.
func podLabels(l lane) map[string]any {
	if l != estate {
		return map[string]any{}
	}
	return map[string]any{"hanzo.ai/publish": "artifact"}
}

// customerJob is a customer build's Job: BUILD and PUSH in two containers that
// never run at the same time and share nothing but the export volume.
//
// The solve runs rootless buildkitd with --oci-worker-no-process-sandbox under
// seccomp Unconfined, so a RUN step shares the daemon's user and PID namespaces
// and uid, and can read /proc/<buildkitd>/root and /proc/<buildkitd>/environ:
// whatever the solve's container holds, the customer's Dockerfile holds. So that
// container holds nothing of the fabric's. It is an INIT container with no
// registry credential, no DOCKER_CONFIG and no build secret of the fabric's (a
// private GitHub repository's build holds its own one-repository read token,
// checkout.go), it pulls only what is public, and it ends by writing the image as
// an OCI layout onto the export volume (customerExport).
//
// The push container starts only once the solve's container has exited, and
// with it every process it started, since each container has a PID namespace of
// its own (shareProcessNamespace stays unset). It runs no customer code: a
// fixed crane argv whose one input from the build is the layout, mounted
// read-only, and whose destination is `push`, forced server-side like every
// build output. It alone mounts the push credential, and it runs non-root with
// no capabilities, no privilege escalation, a read-only root and the runtime's
// default seccomp and AppArmor profiles.
func customerJob(spec map[string]any, pushSecret, push string) *unstructured.Unstructured {
	pod := podSpec(spec)
	build := pod["containers"].([]any)[0].(map[string]any)
	build["volumeMounts"] = append(build["volumeMounts"].([]any),
		map[string]any{"name": "export", "mountPath": exportMount})
	pod["initContainers"] = []any{build}
	pod["containers"] = []any{map[string]any{
		"name":                     "push",
		"image":                    pushImage,
		"command":                  []any{"/ko-app/crane", "push", ociLayout, push},
		"terminationMessagePolicy": "FallbackToLogsOnError",
		"env":                      []any{map[string]any{"name": "DOCKER_CONFIG", "value": registryConfig}},
		"volumeMounts": []any{
			registryMount(),
			map[string]any{"name": "export", "mountPath": exportMount, "readOnly": true},
		},
		// A push is a stream of already-compressed blobs: it needs a core and
		// little memory, and its disk is the export volume's.
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "250m", "memory": "256Mi", "ephemeral-storage": "64Mi"},
			"limits":   map[string]any{"cpu": "2", "memory": "1Gi", "ephemeral-storage": "1Gi"},
		},
		"securityContext": map[string]any{
			"privileged":               false,
			"runAsUser":                int64(65532),
			"runAsGroup":               int64(65532),
			"runAsNonRoot":             true,
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   true,
			"capabilities":             map[string]any{"drop": []any{"ALL"}},
			"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
			"appArmorProfile":          map[string]any{"type": "RuntimeDefault"},
		},
	}}
	pod["volumes"] = append(pod["volumes"].([]any),
		map[string]any{"name": "export", "emptyDir": map[string]any{"sizeLimit": exportLimit}},
		registryVolume(pushSecret),
	)
	return &unstructured.Unstructured{Object: spec}
}

// lostNodeRetries is the pod failure policy every build Job carries: a pod that
// fails because its node went away is not counted as the build failing, so the Job
// controller starts it again on another node.
//
// The CI pools are mostly spot. When EC2 reclaims a node, the eviction and the
// garbage collection of its pods both mark the pod DisruptionTarget. With
// backoffLimit 0 and no policy, that one lost node failed the whole build, and
// the caller waited out its deadline for an image that was never coming. A build
// that fails on its own is still one attempt.
func lostNodeRetries() map[string]any {
	return map[string]any{"rules": []any{map[string]any{
		"action":          "Ignore",
		"onPodConditions": []any{map[string]any{"type": "DisruptionTarget", "status": "True"}},
	}}}
}

// podSpec reaches the pod template's spec inside a Job object.
func podSpec(job map[string]any) map[string]any {
	return job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}

// ciPool is the node label that defines the CI pool. Every build pod requires it
// with the value "true", so a build runs on the CI pool and nowhere else.
const ciPool = "hanzo.ai/visor"

// placeBuild sets where a build pod may run, and is the only code that does:
// every Job the build door creates (buildJobSpec, artifactJobSpec) passes its pod
// spec through here.
//
// The node selector REQUIRES the CI pool (hanzo.ai/visor=true). A toleration only
// permits a tainted node and never excludes an untainted one, so the selector is
// what keeps a build off the serving nodes.
//
// arch adds kubernetes.io/arch, so the builder runs on a node of the architecture
// it builds for and its compiler is the machine's own. It is empty when the output
// does not depend on the machine — the index solve, which executes nothing and
// only joins two published images, and the artifact lane, which cross-compiles —
// and the pod then takes whichever pool node has room.
//
// The tolerations admit the pool's hanzo.ai/visor taint, which the sandbox nodes
// also carry, and dedicated=ci-runner. hanzo-ci yields to the sandboxes by
// priority, so a build borrows idle capacity on a shared node and never displaces
// a sandbox.
//
// And never on a node under disk pressure (k8s.AvoidPressure): a build writes its
// layers to the node's disk.
func placeBuild(pod map[string]any, arch string) {
	selector := map[string]any{ciPool: "true"}
	if arch != "" {
		selector["kubernetes.io/arch"] = arch
	}
	pod["nodeSelector"] = selector
	pod["tolerations"] = []any{
		map[string]any{"key": "dedicated", "operator": "Equal", "value": "ci-runner", "effect": "NoSchedule"},
		map[string]any{"key": ciPool, "operator": "Exists", "effect": "NoSchedule"},
	}
	k8s.AvoidPressure(pod)
}

// launchDirectBuild launches a privileged build. It takes explicit
// (repo, ref, image, dockerfile) rather than a tenant Application, validates
// them at this single choke point (validateBuildInputs), and launches the same
// moby/buildkit Job with the caller's forced output image. Frontend defaults to
// hanzoai/pack; a non-empty dockerfile is the escape hatch. buildID is the
// idempotency key: a retry of the same build collides on the Job name (409)
// rather than spawning a duplicate.
//
// `org` is WHO THE BUILD IS CHARGED TO, and it is a parameter because the
// concurrency ceiling is per-org: the Job carries hanzo.ai/org=<org> and the line
// reads it, so one org's builds can only ever exhaust its own share. Every caller passes the org its credential names — /v1/build, the
// plane's PlatformBuild and a tenant deploy alike — because no build is org-less:
// a constant here puts every org's builds in one pool, and one org's release
// train then holds the ceiling against all the others.
//
// `tags` are more tags of image's repository (imageTags), validated here with the
// rest. A single-platform build writes them from its one Job; a fan-out writes
// them from the index Job, which the reconciler launches from the stored build.
//
// `l` is whose build it is (laneOf), decided by the caller that authorized it. A
// customer build is one platform at one ref, and asking for more is refused: its
// solve pushes nothing, so it has no export to write extra tags from, and a
// fan-out's index Job reads the halves through the dockerfile frontend with the
// push credential in its own container, where a customer image's ONBUILD
// triggers would run.
func (k *k8sClient) launchDirectBuild(ctx context.Context, l lane, org, member, repoURL, ref, image, dockerfile, target, dir, buildID string, args map[string]string, platforms []string, tags ...string) (string, error) {
	return k.launchCheckedBuild(ctx, l, org, member, repoURL, ref, image, dockerfile, target, dir, buildID, nil, args, platforms, tags...)
}

// launchCheckedBuild is launchDirectBuild handing the solve checkout, the one
// credential a customer's build of a private GitHub repository needs to clone
// it (checkout.go). nil is launchDirectBuild. It is refused on the estate lane,
// whose GitHub credential is the standing one githubRead admits, and for any
// context but the repository it was minted for.
func (k *k8sClient) launchCheckedBuild(ctx context.Context, l lane, org, member, repoURL, ref, image, dockerfile, target, dir, buildID string, checkout *gitCheckout, args map[string]string, platforms []string, tags ...string) (string, error) {
	if strings.TrimSpace(org) == "" {
		return "", fmt.Errorf("a build must be attributed to an org")
	}
	if err := k.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	cleanURL, cleanDockerfile, cleanRef, cleanImage, err := validateBuildInputs(repoURL, dockerfile, ref, image)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	if target, err = validateTarget(target); err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	if dir, err = validateContextDir(dir); err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	// Both are dockerfile frontend options. The pack frontend reads neither, so
	// naming either without a Dockerfile is refused instead of ignored.
	if cleanDockerfile == "" && (target != "" || dir != "") {
		return "", fmt.Errorf("invalid build input: dockerTarget and context select within a Dockerfile build; name the dockerfile")
	}
	image = cleanImage
	if tags, err = imageTags(image, tags); err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	if l != estate && (len(platforms) > 1 || len(tags) > 0) {
		return "", fmt.Errorf("invalid build input: a customer build publishes one platform at one tag")
	}
	if checkout != nil && !checkout.clones(cleanURL) {
		return "", fmt.Errorf("a checkout credential is only for the repository it was minted for")
	}
	ghStanding := l == estate && githubRead(ctx, org, cleanURL, cleanRef)
	if checkout != nil && l == estate && (ghStanding || !repoOwnerInOrg(cleanURL, org)) {
		return "", fmt.Errorf("a checkout credential on the estate lane is only for an org-owned repository without a standing credential")
	}
	if err := k.admitBuild(ctx, org); err != nil {
		return "", err
	}
	// The build's credentials are decided here, once, and passed down to every Job
	// the build launches: the forge's by whose build it is and what it clones
	// (solveSecrets), the GitHub read credential by the org, the repo AND the ref
	// (githubRead). Deciding them once at the choke point keeps a fan-out's two
	// Jobs from disagreeing, and the index Job — which executes nothing — is
	// handed none of them.
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef
	secrets := solveSecrets(l, buildCtx, ghStanding)

	// ONE PLATFORM, ONE JOB — the shape every build has had, now saying out loud
	// which architecture it is for.
	if len(platforms) < 2 {
		arch := defaultArch
		if len(platforms) == 1 {
			arch = nativeArch[platforms[0]]
		}
		builder := k.builderFor(ctx, l, org, cleanURL, cleanRef, cleanDockerfile, target, dir, args, platforms)
		return k.launchBuild(ctx, l, org, member, buildID, "", image, image, buildCtx, cleanDockerfile, target, dir, cleanRef, args, platforms, arch, builder, secrets, checkout, tags...)
	}

	// SEVERAL PLATFORMS, ONE JOB EACH, EACH ON A NODE OF ITS OWN ARCHITECTURE.
	// The alternative — one solve naming both platforms — builds the foreign half
	// under qemu, and measured on our own images that is not a tax worth paying:
	// cloud, iam, ci, team, sql and base declare no $BUILDPLATFORM stage, and
	// cloud's plugin stage is CGO_ENABLED=1 against libsqlcipher, so its foreign
	// half is 118 emulated C-linked compiles rather than a cross-compile. We own a
	// machine of each architecture; this spends them.
	//
	// Each half publishes at its own tag and is a real, pullable image. The index
	// that joins them is composed FROM those tags (launchIndexBuild) once both have
	// landed, which is also why a failed join leaves two working images behind
	// instead of nothing.
	//
	// One admission, several Jobs: the line counts BUILDS and a fan-out is one build
	// asked for once, so its Jobs start together (dispatch.go).
	for _, p := range platforms {
		arch := nativeArch[p]
		if _, err := k.launchBuild(ctx, l, org, member, buildID, arch, image, archTag(image, arch), buildCtx, cleanDockerfile, target, dir, cleanRef, args, []string{p}, arch, "", secrets, checkout); err != nil {
			return "", err
		}
	}
	return buildJobName(buildID, indexJobSuffix), nil
}

// launchBuild creates one BuildKit Job: the argv, the push credential its image
// implies, and a node of the architecture it targets. It derives the Job name
// from (buildID, nameSuffix) so the name and the hanzo.ai/build-id label can
// never disagree about which build this Job belongs to. The credential is push's:
// every tag is a ref of push's own repository.
//
// A non-empty builder (builderFor) runs the solve on that warm builder, and the
// Job is its client, placed on any CI node: the compiler runs in the builder.
//
// A non-nil checkout is handed to the solve from a Secret named for the Job and
// owned by it (checkoutSecret), written once the Job exists because the owner
// reference needs its uid. Until then the pod waits in CreateContainerConfigError
// and the kubelet retries; a Secret that cannot be written takes the Job with it.
func (k *k8sClient) launchBuild(ctx context.Context, l lane, org, member, buildID, nameSuffix, image, push, buildCtx, dockerfile, target, dir, revision string, args map[string]string, platforms []string, arch, builder string, secrets []solveSecret, checkout *gitCheckout, tags ...string) (string, error) {
	jobName := buildJobName(buildID, nameSuffix)
	if checkout != nil {
		secrets = append(slices.Clone(secrets), checkoutSecret(jobName))
	}
	command, err := buildFrontendCmdArgs(l, buildCtx, dockerfile, target, dir, image, push, revision, args, platforms, tags...)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	pushSecret, err := buildPushSecret(push)
	if err != nil {
		return "", err
	}
	if builder != "" {
		command, arch = onBuilder(command, builder, k.buildNS), ""
	}
	job := k.buildJobSpec(l, jobName, buildID, org, "runner", pushSecret, push, command, arch, secrets)
	if nameSuffix != "" {
		// A half's Job is how the join learns it landed, so it outlives the other
		// half (a build is stuck at stuckAfter); releasePods frees its disk.
		_ = unstructured.SetNestedField(job.Object, int64(2*buildDeadline/time.Second), "spec", "ttlSecondsAfterFinished")
	}
	if builder != "" {
		asClient(job, builder)
	}
	if member != "" {
		labels := job.GetLabels()
		labels[memberLabel] = "true"
		job.SetLabels(labels)
	}
	created, err := k.createBuildJob(ctx, job)
	if err != nil {
		return "", err
	}
	if checkout != nil {
		if err := k.writeCheckout(ctx, created, checkout); err != nil {
			policy := metav1.DeletePropagationBackground
			_ = k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Delete(context.WithoutCancel(ctx), jobName,
				metav1.DeleteOptions{PropagationPolicy: &policy})
			return "", err
		}
	}
	return jobName, nil
}

// launchIndexBuild publishes `image` as a manifest index over the per-architecture
// images the fan-out pushed. The reconciler calls it once every one of those has
// succeeded — an index naming an image that is not there is a broken tag, so the
// order is the whole contract.
//
// It executes nothing of its own. The build is a single FROM resolved once per
// platform, so both halves are read from the registry as they were built and
// neither is run, which is what lets one node write an index for two
// architectures honestly.
//
// It writes the index under image and each of `tags`. Both come off a stored
// build row and are joined into one exporter attribute here, so this is a choke
// point like launchDirectBuild and validates them the same way.
//
// It is an ESTATE Job, because only an estate build fans out (launchDirectBuild):
// buildkitd holds the push credential here, and the dockerfile frontend runs a
// base image's ONBUILD triggers when it resolves the FROM, so the halves it joins
// must be the estate's own.
func (k *k8sClient) launchIndexBuild(ctx context.Context, org, image, buildID string, platforms []string, tags ...string) (string, error) {
	if err := k.ready(); err != nil {
		return "", err
	}
	image, err := validateImageRef(image)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	if tags, err = imageTags(image, tags); err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	pushSecret, err := buildPushSecret(image)
	if err != nil {
		return "", err
	}
	jobName := buildJobName(buildID, indexJobSuffix)
	// The index build executes nothing — a FROM resolved per platform — so it is
	// handed no build secret.
	job := k.buildJobSpec(estate, jobName, buildID, org, "runner", pushSecret, image, indexCmd(image, platforms, tags...), "", nil)
	if _, err := k.createBuildJob(ctx, job); err != nil {
		return "", err
	}
	return jobName, nil
}

// indexDir is where the index build's one-line Dockerfile is written, and its
// build context. Both are the same directory because the context is empty: a FROM
// with nothing after it copies nothing in.
const indexDir = "/tmp/index"

// indexJobSuffix names the Job that joins a fan-out. It is derived from the build
// id like every other Job name here, so a retry collides at 409 rather than
// writing the index twice.
const indexJobSuffix = "index"

// indexScript writes the join Dockerfile and hands buildctl the rest of the argv.
//
// The script is a CONSTANT and every value the caller influenced arrives as a
// separate argv element through "$@", so the exec-form rule holds exactly as it
// does for a build: nothing a request can reach is ever parsed by a shell. The
// Dockerfile it writes has to be a file because buildctl reads its context off
// disk, and a constant written by a constant is the cheapest file there is.
//
// TARGETARCH is a predefined build arg the dockerfile frontend places in the
// global scope, so each half of the solve resolves ITS OWN architecture's image.
const indexScript = `set -eu
mkdir -p ` + indexDir + `
printf '%s\n' 'ARG SRC' 'FROM ${SRC}-${TARGETARCH}' > ` + indexDir + `/Dockerfile
exec buildctl-daemonless.sh build "$@"`

// indexCmd is the index build's argv. No layer cache and no compression override:
// the layers already exist in this repository exactly as the fan-out wrote them,
// so the push is manifests and nothing else. The halves are read from image's
// tag alone; `tags` only name the index it writes.
func indexCmd(image string, platforms []string, tags ...string) []any {
	return []any{"/bin/sh", "-c", indexScript, indexJobSuffix,
		"--frontend=dockerfile.v0",
		"--local", "context=" + indexDir,
		"--local", "dockerfile=" + indexDir,
		"--opt", "build-arg:SRC=" + image,
		"--opt", "platform=" + strings.Join(platforms, ","),
		"--output", "type=image," + imageNames(image, tags) + ",push=true,oci-mediatypes=true",
	}
}

// buildJobName is the ONE name a build's Jobs are known by: derived from the build
// id, so a retry of one build collides rather than duplicating, and suffixed by
// what the Job is for when a build has more than one.
func buildJobName(buildID, suffix string) string {
	name := "pf-runner-" + jobIDSuffix(buildID)
	if suffix != "" {
		name += "-" + suffix
	}
	return truncate(name, 63)
}

// tenantBuildJobName is a tenant build's Job name: the org and the app, cut to leave room for
// the build's own id, which comes last because it is what makes the name unique. Cutting the
// whole name at 63 dropped the id from an org with a long name (every wallet org's is 49
// characters), so two apps whose slugs share a prefix, or two builds of one app, named the same
// Job and the second deploy failed with "already exists".
func tenantBuildJobName(org, slug, buildID string) string {
	id := jobIDSuffix(buildID)
	return truncate("pf-build-"+namespace.Sanitize(org)+"-"+slug, 63-1-len(id)) + "-" + id
}

// archTag is where one half of a fan-out publishes: the requested tag plus the
// architecture that built it. Each is a real image a caller can pull on its own,
// and the index is composed from them by name.
func archTag(image, arch string) string {
	repo, tag := splitImageRef(image)
	return repo + ":" + tag + "-" + arch
}

// suffix prefixes a non-empty discriminator with a dash, so an absent one leaves
// the name it qualifies exactly as it was.
func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "-" + s
}

// defaultArch is where a build that names no platform runs — unchanged behaviour
// for every caller that does not use the field.
const defaultArch = "amd64"

// jobPollInterval is how often waitForJob re-reads a Job's terminal state.
const jobPollInterval = 5 * time.Second

// waitForJob blocks until a Job reaches a terminal state (succeeded → nil, failed →
// error) or deadline elapses, reading the SAME jobResult the build reconciler uses —
// one definition of "a Job is done". It re-reads immediately on entry, so an
// already-terminal Job returns without waiting a tick. ctx cancellation aborts.
func (k *k8sClient) waitForJob(ctx context.Context, jobName string, deadline time.Duration) error {
	t := time.NewTicker(jobPollInterval)
	defer t.Stop()
	end := time.Now().Add(deadline)
	for {
		if done, ok, err := k.jobResult(ctx, jobName); err == nil && done {
			if ok {
				return nil
			}
			return fmt.Errorf("job %s failed", jobName)
		}
		if !time.Now().Before(end) {
			return fmt.Errorf("job %s did not complete within %s", jobName, deadline)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// admitRunner refuses a new runner when the cluster's nodes already run as many
// runners as its ceiling, and a forge runner when the forge already holds its
// half.
//
// Counted just-in-time from the live Jobs and their pods rather than tracked in
// memory, for the same reason admitBuild counts: the Jobs outlive the request
// that made them and several replicas make them, so the only honest count is
// the cluster's own.
//
// The ceiling counts runners a node has taken. One still waiting for a node
// holds nothing, and counting it let a queue of amd64 runners, all waiting on
// the one amd64 visor node, fill the ceiling while the arm64 node sat idle —
// every arm64 leg refused behind work that could not start. For GitHub the
// waiting pod is also the only durable queue there is: a refused workflow_job
// is delivered again only a few times, and GitHub fails it after a day.
//
// The forge loses nothing when refused — it keeps the job queued and the queue
// loop starts it on a later tick — so it is held to half the ceiling counting
// every runner it has, waiting or not, and never piles pods up. cloud's own
// train keeps forge jobs queued most of the day; without that, the forge,
// drained first, held every slot and no GitHub workflow ran.
//
// That half is held PER ARCHITECTURE, and per class within it (runnerLane). A runner is pinned to the architecture
// its job's labels name (arch, from runnerNode; "" for a job that names none),
// and one waiting for a node of that architecture takes nothing from a node of
// the other. Counted across both, arm64 legs waiting on a full arm64 node held
// the forge's whole half and refused every amd64 job while the amd64 nodes sat
// idle. Per architecture, waiting pods still cannot pile up past the half, and
// the ceiling above still bounds what the nodes run.
func (k *k8sClient) admitRunner(ctx context.Context, ns, provider, arch, class string) error {
	r, err := k.activeRunners(ctx, ns)
	if err != nil {
		return fmt.Errorf("count active runners: %w", err)
	}
	max := k.limits.maxConcurrentRunners()
	if r.bound.total() >= max {
		return errTooManyRunners
	}
	if provider == "forge" && max > 1 && r.lane["forge"][runnerLane(arch, class)] >= max-max/2 {
		return errForgeShareFull
	}
	return nil
}

// runnerLane is what the forge's half is held PER: an architecture and a class. A class is
// a pool of its own, so a pod Pending on it waits for that pool and holds nothing of another's.
func runnerLane(arch, class string) string { return arch + "/" + class }

// reclaimSpentRunner deletes the runner Job named for a forge job when that Job
// has FINISHED, and answers whether it did.
//
// A runner is ephemeral and takes whatever task the forge hands it, so the Job
// named for one forge job routinely runs another repo's — and exits. The forge
// then still holds the job queued while a finished Job sits under its name, and
// [launch] answers AlreadyExists for it. Read as "the job has its runner" that is
// a deadlock: nothing relaunches until the Job's TTL expires ten minutes later,
// and a release waits that long for no reason. Measured twice in one evening on
// hanzo-inc/cloud, both times on the gate of a release that was otherwise ready.
//
// It deletes only a Job that is DONE, so a runner that is genuinely working is
// never taken out from under its task, and the next tick launches a fresh one.
func (k *k8sClient) reclaimSpentRunner(ctx context.Context, ns, name string) (bool, error) {
	if err := k.ready(); err != nil {
		return false, err
	}
	job, err := k.dyn.Resource(jobsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if !jobFinished(job) {
		return false, nil
	}
	policy := metav1.DeletePropagationBackground
	if err := k.dyn.Resource(jobsGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	}); err != nil {
		return false, err
	}
	_ = k.dyn.Resource(secretsGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	return true, nil
}

// held is how many runners each provider has, keyed by the hanzo.ai/runner label
// job.go stamps.
type held map[string]int

func (h held) total() int {
	n := 0
	for _, v := range h {
		n += v
	}
	return n
}

// runners is every unfinished runner Job in a namespace, those of them whose pod
// a node has taken, and every one of them by the lane its pod is pinned to, the
// architecture and the class (provider → runnerLane → count).
type runners struct {
	all, bound held
	lane       map[string]held
}

// activeRunners counts the runner Jobs in ns that are NOT finished, by provider,
// and which of them are bound to a node. Each Job's status is re-checked so a
// completed-but-not-yet-TTL'd Job does not hold a slot.
func (k *k8sClient) activeRunners(ctx context.Context, ns string) (runners, error) {
	jobs, err := k.dyn.Resource(jobsGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "hanzo.ai/managed-by=platform,hanzo.ai/runner",
	})
	if err != nil {
		return runners{}, err
	}
	pods, err := k.dyn.Resource(k8s.Pods).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "hanzo.ai/runner"})
	if err != nil {
		return runners{}, err
	}
	placed := map[string]bool{}
	for i := range pods.Items {
		if node, _, _ := unstructured.NestedString(pods.Items[i].Object, "spec", "nodeName"); node != "" {
			l := pods.Items[i].GetLabels()
			placed[l["batch.kubernetes.io/job-name"]], placed[l["job-name"]] = true, true
		}
	}
	r := runners{all: held{}, bound: held{}, lane: map[string]held{}}
	for i := range jobs.Items {
		if jobFinished(&jobs.Items[i]) {
			continue
		}
		provider := jobs.Items[i].GetLabels()["hanzo.ai/runner"]
		r.all[provider]++
		if placed[jobs.Items[i].GetName()] {
			r.bound[provider]++
		}
		arch, _, _ := unstructured.NestedString(jobs.Items[i].Object, "spec", "template", "spec", "nodeSelector", "kubernetes.io/arch")
		class, _, _ := unstructured.NestedString(jobs.Items[i].Object, "spec", "template", "metadata", "labels", classLabel)
		if r.lane[provider] == nil {
			r.lane[provider] = held{}
		}
		r.lane[provider][runnerLane(arch, class)]++
	}
	return r, nil
}

// githubRunners is every runner Job in ns that platform made for GitHub, finished
// or not, with when it was created — what a reconcile of GitHub's queue counts
// against.
func (k *k8sClient) githubRunners(ctx context.Context, ns string) ([]client.RunnerJob, error) {
	jobs, err := k.dyn.Resource(jobsGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "hanzo.ai/managed-by=platform,hanzo.ai/runner=github",
	})
	if err != nil {
		return nil, err
	}
	out := make([]client.RunnerJob, 0, len(jobs.Items))
	for i := range jobs.Items {
		j := &jobs.Items[i]
		out = append(out, client.RunnerJob{Name: j.GetName(), Done: jobFinished(j), Created: j.GetCreationTimestamp().Unix()})
	}
	return out, nil
}

// jobOutcome classifies a fetched batch/v1 Job: done reports completion
// (succeeded OR failed); succeeded distinguishes which. It is the ONE place that
// reads Job terminal state, shared by jobFinished (the concurrent-build cap) and
// jobResult (the build reconciler) so both agree on when a build is over.
func jobOutcome(job *unstructured.Unstructured) (done, succeeded bool) {
	if n, ok := nestedInt(mustStatus(job), "succeeded"); ok && n > 0 {
		return true, true
	}
	if n, ok := nestedInt(mustStatus(job), "failed"); ok && n > 0 {
		return true, false
	}
	conds, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		if st != "True" {
			continue
		}
		if t == "Complete" {
			return true, true
		}
		if t == "Failed" {
			return true, false
		}
	}
	return false, false
}

// jobFinished reports whether a batch/v1 Job has completed (succeeded or failed),
// so it no longer counts toward the per-org concurrent-build cap.
func jobFinished(job *unstructured.Unstructured) bool {
	done, _ := jobOutcome(job)
	return done
}

// releaseUnscheduled deletes each of b's unfinished Jobs whose pod never reached
// a node, and reports how many it deleted.
//
// An unfinished Job holds one of its org's places, and a pod that never schedules
// never finishes. Past the deadline the reconciler records the build failed, and
// without this the Job went on holding its place until a node came free. A Job
// that never started did no work, so deleting it loses nothing. A Job that did
// start is left alone: slow is not stuck, and an artifact build runs past
// buildDeadline and still publishes.
func (k *k8sClient) releaseUnscheduled(ctx context.Context, b Build) (int, error) {
	if err := k.ready(); err != nil {
		return 0, err
	}
	names := []string{b.JobName}
	if len(b.Platforms) > 1 {
		for _, p := range b.Platforms {
			names = append(names, buildJobName(b.ID, nativeArch[p]))
		}
	}
	jobs := k.dyn.Resource(jobsGVR).Namespace(k.buildNS)
	pods := k.dyn.Resource(k8s.Pods).Namespace(k.buildNS)
	policy := metav1.DeletePropagationBackground
	released := 0
	for _, name := range names {
		if name == "" {
			continue
		}
		job, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return released, err
		}
		if jobFinished(job) {
			continue
		}
		list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + name})
		if err != nil {
			return released, err
		}
		if slices.ContainsFunc(list.Items, func(p unstructured.Unstructured) bool {
			node, _, _ := unstructured.NestedString(p.Object, "spec", "nodeName")
			return node != ""
		}) {
			continue
		}
		if err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
			return released, err
		}
		released++
	}
	return released, nil
}

// releasePods deletes the pods of the named build Jobs and leaves the Jobs: a
// finished pod holds its buildkitd volume on its node's disk until it is deleted,
// and the Job keeps the result the reconciler reads.
func (k *k8sClient) releasePods(ctx context.Context, jobs ...string) error {
	if len(jobs) == 0 {
		return nil
	}
	if err := k.ready(); err != nil {
		return err
	}
	pods := k.dyn.Resource(k8s.Pods).Namespace(k.buildNS)
	policy := metav1.DeletePropagationBackground
	for _, name := range jobs {
		list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + name})
		if err != nil {
			return err
		}
		for _, p := range list.Items {
			if err := pods.Delete(ctx, p.GetName(), metav1.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// jobResult fetches a build Job and classifies it for the reconciler. A NotFound
// (e.g. the Job's TTL elapsed) propagates as an error so the caller can decide
// whether the deployment is merely old or truly orphaned.
func (k *k8sClient) jobResult(ctx context.Context, jobName string) (done, succeeded bool, err error) {
	if rErr := k.ready(); rErr != nil {
		return false, false, rErr
	}
	obj, gErr := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(ctx, jobName, metav1.GetOptions{})
	if gErr != nil {
		return false, false, gErr
	}
	done, succeeded = jobOutcome(obj)
	return done, succeeded, nil
}

// mustStatus returns the Job's status map (or nil), a small helper for jobFinished.
func mustStatus(job *unstructured.Unstructured) map[string]any {
	s, _, _ := unstructured.NestedMap(job.Object, "status")
	return s
}

// ── pure helpers ─────────────────────────────────────────────────────────────
//
// The org→slug normalizer is NOT here: cloud has exactly ONE, the injective
// namespace.Sanitize (see tenantNamespace / buildImageRef). A second,
// lossy copy previously lived here and was the CRIT-2 collision — deleted.

func splitImageRef(ref string) (repo, tag string) {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[:at], ref[at+1:]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

func healthFromStatus(status map[string]any) string {
	if status == nil {
		return ""
	}
	desired, hasDesired := nestedInt(status, "replicas")
	ready, _ := nestedInt(status, "readyReplicas")
	if !hasDesired {
		if avail, ok := nestedInt(status, "availableReplicas"); ok {
			if avail > 0 {
				return "green"
			}
			return "red"
		}
		return ""
	}
	if desired == 0 {
		return "yellow"
	}
	if ready >= desired {
		return "green"
	}
	if ready > 0 {
		return "yellow"
	}
	return "red"
}

func nestedInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// renderEnv builds the operator Service CR `spec.env` for an app. A plain env var
// renders as `{name, value}`; a secret:true var renders as a Kubernetes
// `valueFrom.secretKeyRef` → the operator-materialized Secret `secretName`, key =
// the env key (secrets.go). The secret's PLAINTEXT is NEVER rendered here — the
// value lived only in KMS and reaches the pod via the k8s Secret. `optional:true`
// so the pod boots even before the KMSSecret sync lands (the env var is simply
// absent until the operator materializes the Secret) — honest degradation, never a
// crash-loop. A secret var must carry ONLY valueFrom (never a `value`), which the
// hanzo operator asserts before rendering the container env.
func renderEnv(secretName, envJSON string) []any {
	kvs := parseEnv(envJSON)
	out := make([]any, 0, len(kvs))
	for _, kv := range kvs {
		if kv.Secret {
			out = append(out, map[string]any{
				"name": kv.Key,
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{
						"name":     secretName,
						"key":      kv.Key,
						"optional": true,
					},
				},
			})
			continue
		}
		out = append(out, map[string]any{"name": kv.Key, "value": kv.Value})
	}
	return out
}

// activeHosts is the app's ACTIVE ingress host set, decoded from the record. It
// is named for what it returns rather than for the column it reads, so the type
// that answers the domains route can be called domainList without the two
// colliding.
func activeHosts(domainsJSON string) []string {
	var hosts []string
	if domainsJSON == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(domainsJSON), &hosts)
	return hosts
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func portOr(p int) int {
	if p <= 0 || p > 65535 {
		return 8080
	}
	return p
}

// jobIDSuffix derives a DNS-1123-safe, ≤12-char suffix from a build ID for a
// deterministic Job name. Strips the "bld_" prefix and any non-label chars.
func jobIDSuffix(buildID string) string {
	s := strings.ToLower(strings.TrimPrefix(buildID, "bld_"))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "build"
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// truncate caps a DNS-1123 name at n chars, trimming a trailing '-'.
func truncate(s string, n int) string {
	return strings.TrimRight(text.Cut(s, n), "-")
}
