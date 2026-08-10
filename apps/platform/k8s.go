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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/k8s"
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

// errTooManyBuilds is returned by launchBuildJob when the caller's org already
// has the maximum number of concurrent build Jobs in flight (MED-3, shared-build
// DoS). The deploy path maps it to HTTP 429.
var errTooManyBuilds = errors.New("platform: too many concurrent builds for this org")

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
	buildNS     string         // ISOLATED namespace CI Jobs run in (default defaultBuildNamespace, off the platform ns)
	limits      resourceLimits // per-tenant replica/quota/build bounds (MED-3)
	kmsSync     kmsSyncConfig  // KMSSecret CR operator config (secrets.go)
	// First-deploy tenant-RBAC readiness wait (waitForTenantRBAC). Zero ⇒ the
	// production constants (tenantRBACReadyTimeout / tenantRBACPollInitial); tests
	// shrink them for fast, deterministic coverage. Not env-configurable: these do
	// not vary between environments.
	rbacReadyTimeout time.Duration
	rbacPollInitial  time.Duration
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

func serviceCR(ns, org, project string, a Application, image string) *unstructured.Unstructured {
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
	if ing := ingressSpec(activeHosts(a.DomainsJSON)); ing != nil {
		spec["ingress"] = ing
	}
	// Container-serverless autoscaling: the /v1/run path sets MaxScale>0 to declare an
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
	desired := serviceCR(ns, org, project, a, image)
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

// ingressSpec renders the operator Service CR ingress block for a set of hosts,
// or nil when there are none. It is the ONE place the ingress + cert-manager TLS
// shape is defined, shared by serviceCR (full-CR apply on deploy) and applyIngress
// (the incremental domain-change patch) so the TLS wiring can never diverge
// between the two write paths. The operator's build_ingress materializes one k8s
// Ingress per host with `cert-manager.io/cluster-issuer: letsencrypt-prod`, so a
// verified host added here gets its ACME cert for free.
func ingressSpec(hosts []string) map[string]any {
	if len(hosts) == 0 {
		return nil
	}
	return map[string]any{
		"enabled":          true,
		"hosts":            toAnySlice(hosts),
		"ingressClassName": "hanzo",
		"tls":              true,
		"clusterIssuer":    "letsencrypt-prod",
	}
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
	if ing := ingressSpec(hosts); ing != nil {
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
// success.
func (k *k8sClient) launchBuildJob(ctx context.Context, org string, a Application, image, gitRef, buildID string) (string, error) {
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
		ref = firstNonEmpty(a.RepoBranch, "main")
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

	// (MED-3) Cap concurrent builds per org so a tenant cannot spawn unbounded
	// privileged Jobs in the shared build namespace. Counted just-in-time from the
	// live Jobs the org owns; refuse with errTooManyBuilds (→ HTTP 429) when at
	// the ceiling.
	// The ceiling is SOFT, and that is a decision rather than an oversight.
	//
	// This is check-then-act: two requests that count concurrently both see room
	// and both create, so the true bound is the ceiling plus the number of
	// requests in flight. Making it hard needs an atomic reservation, and the only
	// atom available here is the Job NAME — which is already spent on idempotency
	// (jobIDSuffix(buildID), so a retry of one build collides at 409 instead of
	// building twice). A name cannot carry both properties, and a counter object
	// with optimistic concurrency would be a second source of truth about how many
	// builds are running, which drifts from the Jobs it counts.
	//
	// The overrun is bounded and CONFINED: the label is the caller's own org, so
	// an org can only ever exceed ITS OWN share and never take another's. What a
	// soft ceiling does not do is bound the cluster, and that bound belongs where
	// bounds are enforced atomically — a ResourceQuota on the build namespace,
	// which the apiserver applies at admission and no race can widen. This check
	// stays as the fast, attributable refusal; the quota is the wall behind it.
	active, err := k.countActiveBuilds(ctx, org)
	if err != nil {
		return "", fmt.Errorf("count active builds: %w", err)
	}
	if active >= k.limits.maxConcurrentBuilds() {
		return "", errTooManyBuilds
	}

	// Deterministic Job name (arcd model): pf-build-<org>-<app>-<buildID[:12]>.
	// Because the build ID is unique per attempt, a retry of the SAME build
	// collides (409 AlreadyExists) rather than spawning a duplicate — the
	// idempotency key, no monotonic counter. The org slug is the INJECTIVE
	// normalizer so two orgs' identically-named apps never share a Job name.
	jobName := truncate("pf-build-"+namespace.Sanitize(org)+"-"+a.Slug+"-"+jobIDSuffix(buildID), 63)

	// buildctl-daemonless git frontend context: https://<repo>.git#<ref>.
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef

	// (CRIT-1) Emit buildctl as EXEC-FORM argv — NEVER a `sh -c` string. Each
	// validated value is a single argv element handed to execve, so no shell ever
	// parses it: a ';', '|', or '#' in an input can neither chain a command nor
	// smuggle a flag. The output image ref is FORCED server-side (the `image`
	// param, computed by buildImageRef from the validated tenant) and appears as
	// its own fixed argv element, so a client can never override --output/--opt to
	// push to another tenant's repo.
	command := buildFrontendCmdRev(buildCtx, dockerfile, image, cleanRef)
	pushSecret, err := buildPushSecret(image)
	if err != nil {
		return "", err
	}
	job := k.buildJobSpec(jobName, org, a.Slug, pushSecret, command)
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return jobName, nil
}

// packFrontendImage is hanzoai/pack — the one canonical zero-config packer, a
// BuildKit gateway frontend that detects any project (Go, Node, Python, Rust,
// static, …) and emits a runnable OCI image. It is the default builder; a
// Dockerfile is the explicit escape hatch.
const packFrontendImage = "ghcr.io/hanzoai/pack:latest"

// platformBuildOrg labels fabric-owned (non-tenant) builds — the /v1/runner
// direct builds share this pool for the concurrency cap.
const platformBuildOrg = "platform"

// buildFrontendCmd is the ONE way a build chooses its BuildKit frontend: an
// explicit Dockerfile uses dockerfile.v0; otherwise hanzoai/pack detects and
// builds with zero config. The output image is FORCED here as its own argv
// element, so a caller can never redirect --output to another repo.
//
// The GIT_AUTH_TOKEN build secret (sourced from the Job env, which buildJobSpec
// wires from the gitTokenSecret Secret when present) is what lets the git
// context fetch PRIVATE repos — BuildKit's gitsource presents it as the HTTPS
// credential. Public repos ignore it; without the Secret the env is empty and
// fetches are anonymous, exactly as before.
func buildFrontendCmd(buildCtx, dockerfile, image string) []any {
	return buildFrontendCmdRev(buildCtx, dockerfile, image, "")
}

// buildFrontendCmdArgs is buildFrontendCmdRev plus the image's declared
// `--build-arg`s. They are appended AFTER the derived ones, and that order is
// the policy: buildkit takes the LAST value for a repeated build-arg, so a
// declaration cannot overwrite... which is exactly why VERSION and REVISION are
// filtered out of the declared set instead of relying on position. Those two are
// receipts — the tag the image is published under and the commit it was built
// from — and an image that can name a different commit than it was built from is
// the one lie the whole digest-pinned lane exists to prevent.
func buildFrontendCmdArgs(buildCtx, dockerfile, image, revision string, args map[string]string) ([]any, error) {
	// AN ABBREVIATED SHA IS A MISTAKE; A BRANCH NAME IS NOT.
	//
	// A build context may legitimately name a branch, and a branch is not a
	// revision — stamping "main" would make the label look populated while
	// answering a different question, so those builds correctly ship an image that
	// says `revision: unknown`. That is honest and stays.
	//
	// A value that is hex and SHORTER than a commit is a different thing: a caller
	// who believes it is stamping a revision and is not. It silently produced an
	// image that cannot say what it is — /v1/health answers "unknown" forever, and
	// nobody can ask a running fleet which commit it serves. That is how a rollback
	// went unnoticed here: the tag said one thing, the image knew nothing, and only
	// a per-file check told the truth. Refusing names the reason at the door.
	if isAbbreviatedCommit(revision) {
		return nil, fmt.Errorf("revision %q is an abbreviated commit, so the image it builds could not name itself: pass the full 40-character sha", revision)
	}
	cmd := buildFrontendCmdRev(buildCtx, dockerfile, image, revision)
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
// isAbbreviatedCommit reports a value that is hex but too short to BE a commit —
// the shape of a caller who meant to pass a revision and passed a prefix of one.
//
// Deliberately narrow. A branch name is not caught (it is a legitimate build
// context, and cloud.IsCommit already declines to stamp it), and neither is the
// empty string. Only the mistake is.
func isAbbreviatedCommit(s string) bool {
	if len(s) < 7 || len(s) >= 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func buildFrontendCmdRev(buildCtx, dockerfile, image, revision string) []any {
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
	cmd = append(cmd, "--secret", "id=GIT_AUTH_TOKEN,env=GIT_AUTH_TOKEN")
	// Hand the image's own tag to the build as VERSION, so a binary can stamp the
	// version it was published under (cloud's Dockerfile links it into
	// cloud.Version, the X-Api-Version header) instead of shipping the "dev"
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
	}
	// A build context may name a BRANCH, and a branch is not a revision: stamping
	// "main" here would make the label and the binary look populated while
	// answering a different question than the one anybody reads them for. Empty
	// only for callers that genuinely have no commit; a Dockerfile with no
	// `ARG REVISION` ignores it either way.
	//
	// cloud.IsCommit, not a private copy, because this is one END of a wire whose
	// other end applies the same rule to decide what it will REPORT (see
	// cloud.Revision). Two copies of "what is a commit" that drift apart would let
	// the builder pass a value the process silently downgrades to "unknown" — the
	// same silent failure, one layer over.
	if cloud.IsCommit(revision) {
		cmd = append(cmd, "--opt", "build-arg:REVISION="+revision)
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
	if repo, tag := splitImageRef(image); tag != "" && !strings.Contains(tag, ":") {
		for _, a := range cacheArgs(repo) {
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
	return append(cmd, "--output", "type=image,name="+image+",push=true,compression=zstd,force-compression=true,oci-mediatypes=true")
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
func cacheArgs(repo string) []string {
	if ep := strings.TrimSpace(getenv("BUILD_CACHE_S3_ENDPOINT", "")); ep != "" {
		common := "type=s3,bucket=" + getenv("BUILD_CACHE_S3_BUCKET", cacheBucket) +
			",region=" + getenv("S3_REGION", "us-east-1") +
			",endpoint_url=" + s3CacheEndpoint(ep) +
			",use_path_style=true,name=" + cacheKey(repo)
		// mode=min, for the reason spelled out on the registry branch below: max
		// re-compresses and re-uploads the whole build stage to buy back a few
		// seconds of `apk add`.
		return []string{"--import-cache", common, "--export-cache", common + ",mode=min,compression=zstd"}
	}
	ref := repo + ":buildcache"
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
	if strings.EqualFold(getenv("S3_ADMIN_SECURE", "false"), "true") {
		return "https://" + ep
	}
	return "http://" + ep
}

// gitTokenSecret is the Secret (same namespace as the build Jobs) whose `token`
// key authenticates the fabric's git fetches for private repos. Optional by
// design: absent Secret ⇒ empty env ⇒ anonymous fetch (public repos only).
const gitTokenSecret = "console-git-token"

// buildkitRootlessImage is the UNPRIVILEGED buildkit variant. The build Job runs
// it rootless (uid 1000, no privileged, process sandbox disabled) so a hostile
// Dockerfile has no host-root / device / privileged-syscall surface to escape
// from (H2). It is a public image on Docker Hub — no pull secret needed.
const buildkitRootlessImage = "moby/buildkit:v0.16.0-rootless"

// buildCacheLimit bounds the buildkitd emptyDir — the build's whole working set,
// and the only thing in the pod that grows without bound.
//
// It is a bound, not a measurement: half of a runner node's ~105G, which also
// carries the node's images, its snapshots and any concurrent build. The number
// matters far less than having one, because the failure it prevents is not this
// build running out of room — it is the NODE running out, which the kubelet
// answers by evicting every pod on it.
//
// The two consts below bound this package's OTHER two emptyDirs for the same
// reason, and they are stated here together because "how much node disk may a
// job take" is one question with three answers, not three unrelated numbers. An
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

	// smokeDataLimit bounds the smoke job's /data. This one is scratch for a
	// single 120s boot test — a fresh SQLite and whatever the image writes at
	// startup — so it is small on purpose. It is capped anyway because the
	// failure mode is not this pod's: an image that loops writing on boot would
	// fill the runner's rootfs and evict its NEIGHBOURS while staying well
	// inside its own resource limits.
	smokeDataLimit = "10Gi"
)

// buildPushSecretPrefix + buildPushSecret select the PER-ORG push credential a
// build mounts. The Secret is named push-<namespace> (push-hanzoai / push-luxfi /
// push-zooai) and holds ONLY that one org's registry write token, so a build for
// one brand can never read another brand's push credential (H2). Combined with H1
// (a non-SuperAdmin build's target namespace already equals the caller's own org),
// a malicious Dockerfile can exfiltrate at most the SAME org's push token it was
// authorized to push with — never the shared 3-org credential. Fail-closed: an
// image not on an owned namespace yields an error and NO Job is launched
// (imageAllowed has already passed on every live path, so this only fires on a
// logic error, and it fails closed rather than falling back to a broad cred).
// buildPullSecret is the registry READ credential in the build namespace, used by
// the smoke Job to pull the image it just built. It named `kaniko-ghcr`, a Secret
// that does not exist there — so every smoke pulled anonymously and a private
// image could only fail on a pull nobody attributed to a credential.
const buildPullSecret = "ghcr-pull"

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
//   - pushSecret is the caller-resolved PER-ORG push credential (push-<namespace>),
//     mounted read-only as the only registry credential in the pod. NOTE the mount
//     is per-org but the token VALUE must be a per-org minimal-scope token for this
//     to bound blast radius (else a hostile build reads a still-multi-org token) —
//     an egress allowlist on the build ns shrinks the exfil surface meanwhile.
//   - runs in the ISOLATED build namespace (k.buildNS, defaulted off the main
//     platform namespace) with automountServiceAccountToken=false and pinned to the
//     dedicated CI runner pool (taint + nodeSelector) — retained from before.
func (k *k8sClient) buildJobSpec(jobName, org, app, pushSecret string, command []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
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
			},
		},
		"spec": map[string]any{
			"backoffLimit": int64(0),
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
					// on the Job above stay on the Job, where countActiveBuilds reads them.
					// So the build pod carried no label of ours at all, and hanzo-build's
					// Cilium policy selects PODS: build-egress-deny-internal denies every
					// internal CIDR and artifact-publish-egress reopens s3:9000 only for
					// hanzo.ai/publish=artifact. Unlabelled, the pod could not reach the
					// object store — which is where its layer cache belongs. Without it the
					// cache is a registry blob pulled WHOLE onto the node's 105GB disk
					// beside the images, the snapshots and the build's own working set, and
					// builds are evicted mid-run.
					"labels": map[string]any{
						"hanzo.ai/publish": "artifact",
					},
					// AppArmor unconfined for the rootless worker. The beta annotation is
					// honored on older nodes; securityContext.appArmorProfile (below) is the
					// GA form on k8s ≥1.30 — both set so ONE spec is correct across versions.
					"annotations": map[string]any{
						"container.apparmor.security.beta.kubernetes.io/buildkit": "unconfined",
					},
				},
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"nodeSelector":                 map[string]any{"runner-pool": "32g"},
					"tolerations":                  []any{map[string]any{"key": "dedicated", "operator": "Equal", "value": "ci-runner", "effect": "NoSchedule"}},
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
							"requests": map[string]any{"ephemeral-storage": "12Gi"},
							"limits":   map[string]any{"ephemeral-storage": "80Gi"},
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
							map[string]any{"name": "DOCKER_CONFIG", "value": "/ghcr"},
							// Private-repo fetch credential, surfaced to the solve as the
							// GIT_AUTH_TOKEN build secret (buildFrontendCmd). optional: a
							// cluster without the Secret builds public repos exactly as before.
							map[string]any{"name": "GIT_AUTH_TOKEN", "valueFrom": map[string]any{
								"secretKeyRef": map[string]any{"name": gitTokenSecret, "key": "token", "optional": true},
							}},
							// Object-store credential for the layer cache (cacheArgs).
							// buildkit reads the S3 backend's keys from the environment, so
							// they never appear on argv or in a log line. OPTIONAL by the
							// same rule the artifact publisher uses: absent, buildkit reports
							// a cache miss and the build runs uncached rather than the Job
							// being unschedulable — a cache accelerates, it never gates.
							map[string]any{"name": "AWS_ACCESS_KEY_ID", "valueFrom": map[string]any{
								"secretKeyRef": map[string]any{"name": artifactS3Secret, "key": "access-key", "optional": true},
							}},
							map[string]any{"name": "AWS_SECRET_ACCESS_KEY", "valueFrom": map[string]any{
								"secretKeyRef": map[string]any{"name": artifactS3Secret, "key": "secret-key", "optional": true},
							}},
						},
						"volumeMounts": []any{
							map[string]any{"name": "ghcr", "mountPath": "/ghcr", "readOnly": true},
							// Rootless buildkitd state (worker cache + snapshots) under the
							// buildkit user's home — a writable emptyDir, required rootless.
							map[string]any{"name": "buildkitd", "mountPath": "/home/user/.local/share/buildkit"},
						},
					}},
					"volumes": []any{
						map[string]any{
							"name":   "ghcr",
							"secret": map[string]any{"secretName": pushSecret, "items": []any{map[string]any{"key": ".dockerconfigjson", "path": "config.json"}}},
						},
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
	}}
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
// concurrency ceiling is per-org: the Job carries hanzo.ai/org=<org> and
// countActiveBuilds selects on it, so one org's builds can only ever exhaust its
// own share. It was the constant platformBuildOrg for every caller, which put
// fabric builds and every tenant's builds in ONE pool of 3 — so a single org
// looping deploys locked every other org out of building, with no attribution in
// the Job labels to see it by. /v1/runner still passes platformBuildOrg (its
// builds ARE the fabric's); a tenant deploy passes the tenant.
func (k *k8sClient) launchDirectBuild(ctx context.Context, org, repoURL, ref, image, dockerfile, buildID string, args map[string]string) (string, error) {
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
	image = cleanImage
	// The ceiling is SOFT, and that is a decision rather than an oversight.
	//
	// This is check-then-act: two requests that count concurrently both see room
	// and both create, so the true bound is the ceiling plus the number of
	// requests in flight. Making it hard needs an atomic reservation, and the only
	// atom available here is the Job NAME — which is already spent on idempotency
	// (jobIDSuffix(buildID), so a retry of one build collides at 409 instead of
	// building twice). A name cannot carry both properties, and a counter object
	// with optimistic concurrency would be a second source of truth about how many
	// builds are running, which drifts from the Jobs it counts.
	//
	// The overrun is bounded and CONFINED: the label is the caller's own org, so
	// an org can only ever exceed ITS OWN share and never take another's. What a
	// soft ceiling does not do is bound the cluster, and that bound belongs where
	// bounds are enforced atomically — a ResourceQuota on the build namespace,
	// which the apiserver applies at admission and no race can widen. This check
	// stays as the fast, attributable refusal; the quota is the wall behind it.
	active, err := k.countActiveBuilds(ctx, org)
	if err != nil {
		return "", fmt.Errorf("count active builds: %w", err)
	}
	if active >= k.limits.maxConcurrentBuilds() {
		return "", errTooManyBuilds
	}
	jobName := truncate("pf-runner-"+jobIDSuffix(buildID), 63)
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef
	command, err := buildFrontendCmdArgs(buildCtx, cleanDockerfile, image, cleanRef, args)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	pushSecret, err := buildPushSecret(image)
	if err != nil {
		return "", err
	}
	job := k.buildJobSpec(jobName, org, "runner", pushSecret, command)
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return jobName, nil
}

// jobPollInterval is how often waitForJob re-reads a Job's terminal state, and
// smokeJobDeadline bounds one smoke boot (a healthy boot is ~1-2s; the ceiling only
// guards a cold image pull on the node).
const (
	jobPollInterval  = 5 * time.Second
	smokeJobDeadline = 5 * time.Minute
)

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

// smokeScript boots the built image's /cloud entrypoint in the background and
// asserts it reaches "zip listening" with no startup-crash signature — the in-container
// mirror of release.yml's docker-run smoke. It exits 0 (Job succeeds) only on a
// clean boot; 1 (Job fails) on any crash signature, a missing "listening" line, or a
// process that died after logging it. backoffLimit 0 + restartPolicy Never make the
// Job's terminal state the smoke verdict, read by waitForJob/jobResult.
// smokeBootSeconds is the window the script waits for "zip listening", and it is
// spent TWICE: once by the loop below, and once by the Job's activeDeadlineSeconds.
// Those were two constants — 180 here, 120 there — so kubelet killed the pod a
// minute before the script had finished waiting, and every boot landing in that
// gap was reported as "never reached listening" by a script that never got to
// say so. The contract test asserts the SCRIPT's window and passed throughout,
// because the ceiling that actually applied was not in the script.
//
// One number, and the deadline derives from it with slack for scheduling and the
// image pull, which happen before the script starts and are not part of the boot
// this is measuring.
const smokeBootSeconds = 180

var smokeScript = `set -u
/cloud >/tmp/boot.log 2>&1 &
pid=$!
listening=0
for _ in $(seq 1 ` + strconv.Itoa(smokeBootSeconds) + `); do
  if grep -q '"message":"zip listening"' /tmp/boot.log 2>/dev/null; then listening=1; break; fi
  kill -0 "$pid" 2>/dev/null || break
  sleep 1
done
cat /tmp/boot.log
if grep -Eiq 'metrics\.Mount|mount metrics|panic|want \*zip\.App' /tmp/boot.log; then echo 'SMOKE FAIL: startup-crash signature'; exit 1; fi
if [ "$listening" -ne 1 ]; then echo 'SMOKE FAIL: never reached listening'; exit 1; fi
kill -0 "$pid" 2>/dev/null || { echo 'SMOKE FAIL: exited after listening'; exit 1; }
echo 'SMOKE PASS'
`

// launchSmokeJob boots image as an in-cluster Job that runs smokeScript — the native
// mirror of release.yml's smoke gate. kmsKey is a throwaway 32-byte master key so the
// KMS plane mounts on its normal ready path (no real secret) and the boot reaches
// "listening" exactly as prod does. buildID is the idempotency key (a retry collides
// on the Job name rather than spawning a duplicate). Returns the Job name to wait on.
func (k *k8sClient) launchSmokeJob(ctx context.Context, image, kmsKey, buildID string) (string, error) {
	if err := k.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("smoke: empty image")
	}
	jobName := truncate("pf-smoke-"+jobIDSuffix(buildID), 63)
	job := k.smokeJobSpec(jobName, image, kmsKey)
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return jobName, nil
}

// smokeJobSpec is the boot-test Job: the built image under a sh wrapper (smokeScript)
// with a production-representative boot env — a writable /data emptyDir, CLOUD_ENV=smoke,
// and the throwaway KMS master key. It pulls from GHCR with the build namespace's secret
// the build Job uses and runs on the same CI pool, so a green smoke proves the exact
// pushed image boots on the exact cluster it will deploy to.
func (k *k8sClient) smokeJobSpec(jobName, image, kmsKey string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      jobName,
			"namespace": k.buildNS,
			"labels": map[string]any{
				"hanzo.ai/org":        platformBuildOrg,
				"hanzo.ai/managed-by": "platform",
				"hanzo.ai/release":    "smoke",
			},
		},
		"spec": map[string]any{
			"backoffLimit":            int64(0),
			"ttlSecondsAfterFinished": int64(3600),
			"activeDeadlineSeconds":   int64(smokeBootSeconds + 120), // boot window + scheduling/pull slack
			"template": map[string]any{
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"nodeSelector":                 map[string]any{"runner-pool": "32g"},
					"tolerations":                  []any{map[string]any{"key": "dedicated", "operator": "Equal", "value": "ci-runner", "effect": "NoSchedule"}},
					"imagePullSecrets":             []any{map[string]any{"name": buildPullSecret}},
					"automountServiceAccountToken": false,
					"containers": []any{map[string]any{
						"name":    "smoke",
						"image":   image,
						"command": []any{"/bin/sh", "-c", smokeScript},
						"env": []any{
							map[string]any{"name": "CLOUD_DATA_DIR", "value": "/data"},
							map[string]any{"name": "CLOUD_ENV", "value": "smoke"},
							map[string]any{"name": "CLOUD_KMS_MASTER_KEY_REF", "value": kmsKey},
						},
						"volumeMounts": []any{map[string]any{"name": "data", "mountPath": "/data"}},
					}},
					"volumes": []any{map[string]any{"name": "data", "emptyDir": map[string]any{"sizeLimit": smokeDataLimit}}},
				},
			},
		},
	}}
}

// countActiveBuilds returns how many build Jobs the org currently has that are
// NOT finished (no succeeded/failed completion). Jobs are labeled
// hanzo.ai/org=<org> + hanzo.ai/build=true in the shared build namespace; a
// server-side label selector narrows the list and each candidate's status is
// re-checked client-side so a completed-but-not-yet-TTL'd Job does not count.
func (k *k8sClient) countActiveBuilds(ctx context.Context, org string) (int, error) {
	sel := "hanzo.ai/build=true,hanzo.ai/org=" + org
	list, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		// Client-side re-check of BOTH labels (defensive) and completion status.
		lbls, _, _ := unstructured.NestedStringMap(list.Items[i].Object, "metadata", "labels")
		if lbls["hanzo.ai/build"] != "true" || lbls["hanzo.ai/org"] != org {
			continue
		}
		if jobFinished(&list.Items[i]) {
			continue
		}
		n++
	}
	return n, nil
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "-")
}
