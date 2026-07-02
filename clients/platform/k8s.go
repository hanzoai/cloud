// k8s.go — the cluster-facing half of /v1/platform: the ONE deploy path.
//
// A user application is deployed by writing an operator hanzo.ai/v1 `Service`
// CR into the caller's OWN tenant namespace; the Hanzo operator reconciles it
// into a Deployment + Service + Ingress (+ HPA/PDB) on DOKS. cloud never
// reimplements a deployer — it writes one CR, exactly like clients/paassvc
// patches system CRs, but here every object lives in `tenant-<org>` where the
// org is the gateway-minted, IAM-VALIDATED tenant (c.Org()), never a value from
// the request body or path. That derivation is the whole cross-tenant isolation
// boundary: a caller cannot name another org's namespace because the namespace
// is not an input.
//
// Builds (git-source apps) launch an in-cluster BuildKit Job (the arcd model,
// buildkit-job.ts) via client-go — no GitHub builders. When the cluster / CI
// prerequisites are absent the subsystem fails CLOSED with the real reason
// (never status-theater), matching paassvc.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/provisioning"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// servicesGVR is the operator Service CR — the same GVR clients/paassvc drives.
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

// errTooManyBuilds is returned by launchBuildJob when the caller's org already
// has the maximum number of concurrent build Jobs in flight (MED-3, shared-build
// DoS). The deploy path maps it to HTTP 429.
var errTooManyBuilds = errors.New("platform: too many concurrent builds for this org")

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
	dyn         dynamic.Interface
	initErr     string
	imagePrefix string
	buildNS     string         // namespace CI Jobs run in (default "hanzo")
	limits      resourceLimits // per-tenant replica/quota/build bounds (MED-3)
}

// newK8sClient builds the dynamic client from the in-cluster service account,
// falling back to KUBECONFIG for local/dev — identical to paassvc.newDynamic.
func newK8sClient(imagePrefix, buildNS string) *k8sClient {
	c := &k8sClient{imagePrefix: imagePrefix, buildNS: buildNS, limits: newResourceLimits()}
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

// ensureNamespace creates tenant-<org> if it does not exist and ALWAYS ensures
// the tenant's ResourceQuota + LimitRange are present (idempotent). Applying the
// bounds on every call — not only at first create — means older tenant
// namespaces are brought under quota too, and a deleted quota is re-created on
// the next deploy (MED-3). The namespace is labeled with the org so cluster
// tooling can attribute it.
func (k *k8sClient) ensureNamespace(ctx context.Context, ns, org string) error {
	if err := k.ready(); err != nil {
		return err
	}
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
	} else if err != nil {
		return err
	}
	// Bound the tenant's total footprint (idempotent create-or-update).
	if err := k.ensureBoundObject(ctx, resourceQuotasGVR, ns, tenantQuotaName, k.limits.resourceQuota(ns)); err != nil {
		return fmt.Errorf("ensure resourcequota: %w", err)
	}
	if err := k.ensureBoundObject(ctx, limitRangesGVR, ns, tenantLimitRangeName, k.limits.limitRange(ns)); err != nil {
		return fmt.Errorf("ensure limitrange: %w", err)
	}
	return nil
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
	}
	if env := envList(a.EnvJSON); len(env) > 0 {
		spec["env"] = env
	}
	if hosts := domainList(a.DomainsJSON); len(hosts) > 0 {
		spec["ingress"] = map[string]any{
			"enabled":          true,
			"hosts":            toAnySlice(hosts),
			"ingressClassName": "hanzo",
			"tls":              true,
			"clusterIssuer":    "letsencrypt-prod",
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
	command := []any{
		"buildctl-daemonless.sh", "build",
		"--frontend=dockerfile.v0",
		"--opt", "context=" + buildCtx,
		"--opt", "filename=" + dockerfile,
		"--output", "type=image,name=" + image + ",push=true",
	}

	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      jobName,
			"namespace": k.buildNS,
			"labels": map[string]any{
				"hanzo.ai/org":         org,
				"hanzo.ai/managed-by":  "platform",
				"hanzo.ai/application": a.Slug,
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
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return jobName, nil
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

// jobFinished reports whether a batch/v1 Job has completed (succeeded or failed),
// so it no longer counts toward the per-org concurrent-build cap.
func jobFinished(job *unstructured.Unstructured) bool {
	if succeeded, ok := nestedInt(mustStatus(job), "succeeded"); ok && succeeded > 0 {
		return true
	}
	if failed, ok := nestedInt(mustStatus(job), "failed"); ok && failed > 0 {
		return true
	}
	conds, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		if (t == "Complete" || t == "Failed") && st == "True" {
			return true
		}
	}
	return false
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

func envList(envJSON string) []any {
	var kvs []EnvVarJSON
	if envJSON == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(envJSON), &kvs)
	out := make([]any, 0, len(kvs))
	for _, kv := range kvs {
		if kv.Secret {
			continue // secret env is KMS-sealed (phase 2); never rendered inline
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
