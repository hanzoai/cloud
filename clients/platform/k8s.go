// k8s.go — the cluster-facing half of /v1/platform: the ONE deploy path.
//
// A user application is deployed by writing an operator hanzo.ai/v1 `Service`
// CR into the caller's OWN tenant namespace; the Hanzo operator reconciles it
// into a Deployment + Service + Ingress (+ HPA/PDB) on DOKS. cloud never
// reimplements a deployer — it writes one CR, exactly like clients/paas
// patches system CRs, but here every object lives in `tenant-<org>` where the
// org is the gateway-minted, IAM-VALIDATED tenant (c.Org()), never a value from
// the request body or path. That derivation is the whole cross-tenant isolation
// boundary: a caller cannot name another org's namespace because the namespace
// is not an input.
//
// Builds (git-source apps) launch an in-cluster BuildKit Job (the arcd model,
// buildkit-job.ts) via client-go — no GitHub builders. When the cluster / CI
// prerequisites are absent the subsystem fails CLOSED with the real reason
// (never status-theater), matching paas.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/provisioning"
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

// servicesGVR is the operator Service CR — the same GVR clients/paas drives.
// A user app is one of these CRs in its tenant namespace.
var servicesGVR = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "services"}

// jobsGVR is the batch/v1 Job used to launch an in-cluster BuildKit build.
var jobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}

// namespacesGVR lets the deploy path ensure the tenant namespace exists.
var namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

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
// KMS-synced source), NEVER by cloud-api. The cloud-api ServiceAccount holds no
// `secrets` grant and issues no Secrets API call — secret provisioning is the
// operator's job, exclusively. Until the operator has projected it the reference
// degrades to the namespace default rather than hard-failing the deploy.
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
	buildNS     string         // namespace CI Jobs run in (default "hanzo")
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
// falling back to KUBECONFIG for local/dev — identical to paas.newDynamic.
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
// hardened, INJECTIVE org normalizer (provisioning.SanitizeOrg — identity on a
// clean DNS label, else fold + a SHA-256 suffix of the raw owner), so two
// distinct owners can NEVER collapse onto the same namespace (CRIT-2). Reusing
// that single function keeps cloud's whole tenant→namespace/bucket/DB boundary
// consistent, rather than forking a third, lossy slug rule.
func tenantNamespace(org string) string {
	org = provisioning.SanitizeOrg(org)
	if org == "" {
		org = "unknown"
	}
	return "tenant-" + org
}

// buildImageRef is the deterministic per-tenant output image for a git build:
// <prefix>/tenant-<org>/<app>:<tag>. org and app live in SEPARATE path
// components, joined by '/', which neither an org slug (provisioning.SanitizeOrg
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
	return fmt.Sprintf("%s/tenant-%s/%s:%s", strings.TrimRight(prefix, "/"), provisioning.SanitizeOrg(org), app, tag)
}

// ensureNamespaceExists creates tenant-<org> if it does not exist (idempotent).
// Creating the namespace is what TRIGGERS the operator's tenant-RBAC controller to
// project cloud-api's `cloud-api-platform` RoleBinding into it — so this is the ONE
// precondition for tenant RBAC ever becoming ready. Pure namespace mechanism: no
// RBAC wait, no quota. Both the synchronous image path (ensureNamespace, which then
// BLOCKS on the RoleBinding) and the async build reconciler (ensureTenantReady,
// which only PROBES) build on it, so there is exactly one namespace-create rule.
func (k *k8sClient) ensureNamespaceExists(ctx context.Context, ns, org string) error {
	_, err := k.dyn.Resource(namespacesGVR).Get(ctx, ns, metav1.GetOptions{})
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
		if _, cErr := k.dyn.Resource(namespacesGVR).Create(ctx, obj, metav1.CreateOptions{}); cErr != nil && !apierrors.IsAlreadyExists(cErr) {
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
		sleep := backoff
		if sleep > tenantRBACPollMax {
			sleep = tenantRBACPollMax
		}
		if sleep > remaining {
			sleep = remaining
		}
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
	if ing := ingressSpec(domainList(a.DomainsJSON)); ing != nil {
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
		"kind":       "Service",
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
	desired := serviceCR(ns, org, project, a, image)
	existing, err := k.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, a.Slug, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cErr := k.dyn.Resource(servicesGVR).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		return cErr
	}
	if err != nil {
		return err
	}
	// Redeploy: merge-patch only .spec (+ labels), leaving the operator status
	// and resourceVersion intact.
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"labels": desired.Object["metadata"].(map[string]any)["labels"]},
		"spec":     desired.Object["spec"],
	})
	_ = existing
	_, err = k.dyn.Resource(servicesGVR).Namespace(ns).Patch(ctx, a.Slug, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
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
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": int64(replicas)}})
	_, err := k.dyn.Resource(servicesGVR).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return errNotFound
	}
	return err
}

// deleteService removes an app's Service CR (best-effort teardown on app/project
// delete). A NotFound is success (already gone).
func (k *k8sClient) deleteService(ctx context.Context, org, name string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	err := k.dyn.Resource(servicesGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// observeService reads the operator-reconciled status of an app's Service CR to
// derive a live health/phase signal for the app view. Best-effort: any error
// yields empty signals (honest unknown), never a fabricated status.
func (k *k8sClient) observeService(ctx context.Context, org, name string) (phase, health string) {
	if k.ready() != nil {
		return "", ""
	}
	ns := tenantNamespace(org)
	obj, err := k.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
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
	_, err := k.dyn.Resource(servicesGVR).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
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
	obj, err := k.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
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
	cleanURL, cleanDockerfile, cleanRef, err := validateBuildInputs(a.RepoURL, a.Dockerfile, ref)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	dockerfile := cleanDockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// (MED-3) Cap concurrent builds per org so a tenant cannot spawn unbounded
	// privileged Jobs in the shared build namespace. Counted just-in-time from the
	// live Jobs the org owns; refuse with errTooManyBuilds (→ HTTP 429) when at
	// the ceiling.
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
	jobName := truncate("pf-build-"+provisioning.SanitizeOrg(org)+"-"+a.Slug+"-"+jobIDSuffix(buildID), 63)

	// buildctl-daemonless git frontend context: https://<repo>.git#<ref>.
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef

	// (CRIT-1) Emit buildctl as EXEC-FORM argv — NEVER a `sh -c` string. Each
	// validated value is a single argv element handed to execve, so no shell ever
	// parses it: a ';', '|', or '#' in an input can neither chain a command nor
	// smuggle a flag. The output image ref is FORCED server-side (the `image`
	// param, computed by buildImageRef from the validated tenant) and appears as
	// its own fixed argv element, so a client can never override --output/--opt to
	// push to another tenant's repo.
	command := buildFrontendCmd(buildCtx, dockerfile, image)
	job := k.buildJobSpec(jobName, org, a.Slug, command)
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
func buildFrontendCmd(buildCtx, dockerfile, image string) []any {
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
	return append(cmd, "--output", "type=image,name="+image+",push=true")
}

// buildJobSpec is the shared moby/buildkit Job (arcd model): privileged buildkit
// on the CI runner pool, pushing to GHCR via the kaniko-ghcr pull secret. Both
// the tenant build (launchBuildJob) and the direct build (launchDirectBuild)
// construct their Job through here — one spec, one place.
func (k *k8sClient) buildJobSpec(jobName, org, app string, command []any) *unstructured.Unstructured {
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
			"backoffLimit":            int64(0),
			"ttlSecondsAfterFinished": int64(3600),
			"template": map[string]any{
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"nodeSelector":                 map[string]any{"runner-pool": "32g"},
					"tolerations":                  []any{map[string]any{"key": "dedicated", "operator": "Equal", "value": "ci-runner", "effect": "NoSchedule"}},
					"imagePullSecrets":             []any{map[string]any{"name": "kaniko-ghcr"}},
					"automountServiceAccountToken": false,
					"containers": []any{map[string]any{
						"name":            "buildkit",
						"image":           "moby/buildkit:v0.16.0",
						"command":         command,
						"securityContext": map[string]any{"privileged": true},
						"env": []any{
							map[string]any{"name": "DOCKER_CONFIG", "value": "/ghcr"},
						},
						"volumeMounts": []any{
							map[string]any{"name": "ghcr", "mountPath": "/ghcr", "readOnly": true},
						},
					}},
					"volumes": []any{map[string]any{
						"name":   "ghcr",
						"secret": map[string]any{"secretName": "kaniko-ghcr", "items": []any{map[string]any{"key": ".dockerconfigjson", "path": "config.json"}}},
					}},
				},
			},
		},
	}}
}

// launchDirectBuild launches a privileged /v1/runner build. It takes explicit
// (repo, ref, image, dockerfile) rather than a tenant Application, validates
// them at this single choke point (validateBuildInputs), and launches the same
// moby/buildkit Job with the caller's forced output image. Frontend defaults to
// hanzoai/pack; a non-empty dockerfile is the escape hatch. buildID is the
// idempotency key: a retry of the same build collides on the Job name (409)
// rather than spawning a duplicate.
func (k *k8sClient) launchDirectBuild(ctx context.Context, repoURL, ref, image, dockerfile, buildID string) (string, error) {
	if err := k.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	cleanURL, cleanDockerfile, cleanRef, err := validateBuildInputs(repoURL, dockerfile, ref)
	if err != nil {
		return "", fmt.Errorf("invalid build input: %w", err)
	}
	active, err := k.countActiveBuilds(ctx, platformBuildOrg)
	if err != nil {
		return "", fmt.Errorf("count active builds: %w", err)
	}
	if active >= k.limits.maxConcurrentBuilds() {
		return "", errTooManyBuilds
	}
	jobName := truncate("pf-runner-"+jobIDSuffix(buildID), 63)
	buildCtx := strings.TrimSuffix(cleanURL, ".git") + ".git#" + cleanRef
	command := buildFrontendCmd(buildCtx, cleanDockerfile, image)
	job := k.buildJobSpec(jobName, platformBuildOrg, "runner", command)
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
// asserts it reaches "listening" with no startup-crash signature — the in-container
// mirror of release.yml's docker-run smoke. It exits 0 (Job succeeds) only on a
// clean boot; 1 (Job fails) on any crash signature, a missing "listening" line, or a
// process that died after logging it. backoffLimit 0 + restartPolicy Never make the
// Job's terminal state the smoke verdict, read by waitForJob/jobResult.
const smokeScript = `set -u
/cloud >/tmp/boot.log 2>&1 &
pid=$!
listening=0
for _ in $(seq 1 60); do
  if grep -q '"message":"listening"' /tmp/boot.log 2>/dev/null; then listening=1; break; fi
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
// and the throwaway KMS master key. It pulls from GHCR with the same kaniko-ghcr secret
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
			"activeDeadlineSeconds":   int64(120),
			"template": map[string]any{
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"nodeSelector":                 map[string]any{"runner-pool": "32g"},
					"tolerations":                  []any{map[string]any{"key": "dedicated", "operator": "Equal", "value": "ci-runner", "effect": "NoSchedule"}},
					"imagePullSecrets":             []any{map[string]any{"name": "kaniko-ghcr"}},
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
					"volumes": []any{map[string]any{"name": "data", "emptyDir": map[string]any{}}},
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
// provisioning.SanitizeOrg (see tenantNamespace / buildImageRef). A second,
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

func domainList(domainsJSON string) []string {
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
