// secrets.go — KMS-sealed secret env vars for /v1/platform apps (closes the
// createApp/setEnv 501). A user env var flagged `secret:true` is NEVER stored in
// platform.db and NEVER rendered inline into the operator Service CR. Instead the
// ONE secret invariant holds end-to-end: a secret value lives ONLY in KMS and the
// operator-materialized k8s Secret; cloud's control-plane DB holds a masked
// placeholder and cloud never handles the plaintext at deploy time.
//
// The path a secret takes:
//
//  1. SEAL — cloud seals the value into its embedded KMS (deps.KMS, AES-256-GCM
//     envelope, clients/kms) at the ORG-SCOPED coordinate /orgs/<org>/platform/
//     <app>/<KEY> (kmsSecretRef). Plaintext is sealed before it touches disk,
//     never logged, never echoed back over the API (toAppView masks it). If KMS is
//     unavailable the write FAILS CLOSED — plaintext never lands in the DB.
//  2. PROVISION — on app-create/deploy, cloud ensures the tenant's PER-TENANT KMS
//     credential exists (ensureTenantKMSAuth): a machine identity whose token
//     carries owner=<org>, projected into tenant-<org> as the creds Secret the CR
//     references. It is the ONE privileged step; when unavailable it degrades to
//     an honest "pending" (never a shared reader), so no cross-tenant identity is
//     ever created.
//  3. DECLARE — cloud writes a canonical KMSSecret CR (secrets.lux.network/
//     v1alpha1) into tenant-<org> pointing the operator at /v1/kms/orgs/<org>/
//     secrets/platform/<app> (projectSlug=<org>, explicit keys). The operator logs
//     in with the per-tenant creds (/v1/kms/auth/login → IAM), reads each key back
//     from cloud's KMS, and materializes the managed Secret `<app>-env`. Cloud's
//     org-scope guard admits the read ONLY for owner==<org>, so a tenant can read
//     ONLY its own scope. Best-effort: a missing CRD/RBAC/credential degrades to a
//     "pending" status, never a failed deploy.
//  4. MOUNT — the Service CR renders each secret env as `valueFrom.secretKeyRef` →
//     that Secret (optional, so the pod boots even before the sync lands); the
//     hanzo operator mounts it into the Deployment env. The pod reads the secret
//     from the k8s Secret — cloud is never in the plaintext path at runtime.
//
// Everything is org-scoped: the KMS coordinate, the auth identity, the KMSSecret
// CR, and the managed Secret all live under the VALIDATED tenant, never a request
// value — the same cross-tenant boundary as the rest of /v1/platform, enforced at
// cloud's ONE auth boundary (SanitizeIdentity + the kmssvc org-scope guard).
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// kmsSecretsGVR is the kms-operator-canonical KMSSecret CR — the ONE operator-
// driven bridge from a KMS scope to a namespaced k8s Secret. cloud writes one per
// app that has secret env; the cluster-wide kms-operator reconciles it.
var kmsSecretsGVR = schema.GroupVersionResource{Group: "secrets.lux.network", Version: "v1alpha1", Resource: "kmssecrets"}

// kmsSyncConfig is the operator-facing config the KMSSecret CR carries: the KMS
// ROOT the operator talks to (hostAPI — the operator appends /v1/kms/auth/login
// and /v1/kms/orgs/{projectSlug}/secrets/… itself) and the per-tenant credentials
// Secret (in the tenant namespace) holding the universalAuth clientId/clientSecret.
// Both are env-overridable so a host change is config, never code.
//
// COHERENT END-TO-END on cloud's OWN embedded KMS. cloud seals each secret at the
// org-scoped store coordinate /orgs/<org>/platform/<app>/<KEY> (kmsSecretRef), and
// the CR points the operator back at the SAME record through cloud's org-scoped
// read surface: projectSlug=<org> selects /v1/kms/orgs/<org>/secrets, secretsPath
// = platform/<app>, keys = the exact secret names (luxfi/kms has no list endpoint,
// so the CRD requires ≥1 explicit key — kmsSecretCR fills it from secretEnvKeys).
//
// PER-TENANT SCOPING is enforced at cloud's ONE auth boundary, not by hope. The
// operator authenticates as a per-tenant IAM machine identity (owner=<org>) via
// /v1/kms/auth/login (kmssvc login broker → IAM client_credentials). cloud's KMS
// guard (clients/kmssvc.guard) admits a read of /orgs/<org>/… ONLY when the
// VALIDATED principal's owner == that org (SanitizeIdentity derives owner from the
// token, ignoring any client X-Org-Id). So tenant-A's credential can never read
// tenant-B's path: the guard answers 403 (proven in kmssvc cross-tenant tests).
// credsSecret is therefore a PER-TENANT name in the tenant namespace — never a
// shared platform-wide reader, which would be a cross-tenant hole.
//
// Until a tenant's per-scoped credential is provisioned (ensureTenantKMSAuth), the
// operator cannot log in and the KMSSecret reconciles to an honest pending/failed
// condition (observeSecretSync); the sealed secret + optional secretKeyRef keep
// the pod booting. No plaintext ever leaks and no cross-tenant identity exists.
type kmsSyncConfig struct {
	hostAPI     string // KMS ROOT the operator talks to (CLOUD_PLATFORM_KMS_HOST_API); operator appends /v1/kms/…
	credsSecret string // per-tenant creds Secret name (CLOUD_PLATFORM_KMS_CREDS_SECRET)
}

// newKMSSyncConfig resolves the KMSSecret operator config from env, defaulting to
// cloud's own in-cluster KMS ROOT and a per-tenant (scoped) credentials Secret.
// The host is the ROOT (no /v1/kms suffix): the operator's HTTP client builds the
// /v1/kms/auth/login and /v1/kms/orgs/… paths itself, so a suffixed host would
// double the prefix. projectSlug is NOT a static default — it is the tenant org,
// filled per-CR by kmsSecretCR, so one tenant's CR can never name another's scope.
func newKMSSyncConfig() kmsSyncConfig {
	return kmsSyncConfig{
		// :8000 is cloud's app listener (HIP-0113 §1 — the http port the Service
		// exposes; every in-cluster caller reaches cloud at cloud.hanzo.svc:8000,
		// NOT :80). The operator appends /v1/kms/auth/login and /v1/kms/orgs/… .
		hostAPI:     getenv("CLOUD_PLATFORM_KMS_HOST_API", "http://cloud.hanzo.svc.cluster.local:8000"),
		credsSecret: getenv("CLOUD_PLATFORM_KMS_CREDS_SECRET", "platform-kms-auth"),
	}
}

// managedSecretName is the k8s Secret the KMSSecret materializes AND the Service
// CR secretKeyRefs point at: "<app>-env" in tenant-<org>. It shares the app slug
// with the Service CR name (serviceCR), so the two objects stay paired per app.
func managedSecretName(appSlug string) string { return appSlug + "-env" }

// kmsSecretRef is the per-tenant/app KMS coordinate for one secret key, the flat
// ref cloud's embedded KMS parses (clients/kms.parseRef) to (path, name, env):
//
//	orgs/<org>/platform/<app>/<KEY>  →  path=/orgs/<org>/platform/<app>, name=<KEY>, env=default
//
// The /orgs/<org> prefix is DELIBERATE and load-bearing: it is the EXACT store
// path cloud's org-scoped /v1/kms read surface addresses (kmssvc.orgPath folds
// :org into /orgs/{org}), so the kms-operator — reading /v1/kms/orgs/<org>/secrets/
// platform/<app>/<KEY> — resolves the very record cloud sealed. Seal path and read
// path are one coordinate; drift between them was why the sync was inert.
//
// It is INJECTIVE in (org, app, key): the org is the validated IAM owner used
// verbatim (matching kmssvc's case-sensitive guard), the app slug (slugRE) and the
// key (envKeyRE) contain no '/' or '@', so two distinct (org,app,key) triples never
// collide on one record and one tenant can never address another's secret.
func kmsSecretRef(org, appSlug, key string) string {
	return "orgs/" + org + "/platform/" + appSlug + "/" + key
}

// kmsSecretsPath is the KMSSecret secretsScope.secretsPath: the sub-path UNDER the
// org (projectSlug carries the org). The operator requests
// /v1/kms/orgs/<projectSlug>/secrets/<secretsPath>/<KEY>, which cloud resolves to
// store path /orgs/<org>/platform/<app> — the same record kmsSecretRef sealed. It
// is relative (no leading '/'): the operator normalises it, and keeping the org
// OUT of it (it lives in projectSlug) is what makes the guard's org==projectSlug
// check the single tenant boundary.
func kmsSecretsPath(appSlug string) string {
	return "platform/" + appSlug
}

// secretEnvKeys returns the SORTED secret keys (secret:true) of an app's env — the
// set that renders as secretKeyRef and drives the managed Secret. Sorted so the
// rendered CR is byte-stable across deploys (no spurious patches).
func secretEnvKeys(envJSON string) []string {
	var keys []string
	for _, e := range parseEnv(envJSON) {
		if e.Secret {
			keys = append(keys, e.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

// parseEnv decodes the app's stored env JSON, tolerating an empty/blank column.
func parseEnv(envJSON string) []EnvVarJSON {
	envJSON = strings.TrimSpace(envJSON)
	if envJSON == "" {
		return nil
	}
	var kvs []EnvVarJSON
	if err := json.Unmarshal([]byte(envJSON), &kvs); err != nil {
		return nil
	}
	return kvs
}

// sealSecretEnv seals every secret:true value into cloud's KMS and returns the env
// with those values BLANKED for persistence (the value is in KMS, never the DB).
// Plain (secret:false) entries pass through untouched. It FAILS CLOSED: if KMS is
// not configured (nil client) or a seal errors, the whole set is refused and
// NOTHING is stored — a plaintext secret never lands in the DB as a fallback. The
// returned error never contains a secret value (only the key name), so a 4xx/5xx
// body cannot leak the plaintext.
func (s *svc) sealSecretEnv(ctx context.Context, org, appSlug string, env []EnvVarJSON) ([]EnvVarJSON, error) {
	out := make([]EnvVarJSON, 0, len(env))
	for _, e := range env {
		if !e.Secret {
			out = append(out, e)
			continue
		}
		if s.kms == nil {
			return nil, fmt.Errorf("secret env unavailable: KMS is not configured for this deployment")
		}
		if err := s.kms.PutSecret(ctx, kmsSecretRef(org, appSlug, e.Key), []byte(e.Value)); err != nil {
			// Fail closed on ANY seal error — never fall back to storing plaintext.
			// The error is KMS-internal (master key / store), it does not carry the
			// value; we still name only the key so nothing sensitive is echoed.
			return nil, fmt.Errorf("seal secret %q: %w", e.Key, err)
		}
		out = append(out, EnvVarJSON{Key: e.Key, Value: "", Secret: true}) // value now lives only in KMS
	}
	return out, nil
}

// ensureSecretSync declares (or tears down) an app's secret-env sync alongside its
// Service CR. It is BEST-EFFORT: it never fails a deploy — a missing CRD, RBAC
// grant, or per-tenant credential degrades to an honest "pending" secret-sync
// status while the sealed secret + optional secretKeyRef keep the pod booting.
// Called from applyLive (the ONE shared deploy choke point) so both the image and
// git paths declare it.
//
// Order matters: PROVISION the per-tenant credential first (so the operator has an
// identity to authenticate with), THEN declare the CR. Both are fail-closed — the
// CR reconciling to "pending" without a credential is the safe state, never a
// cross-tenant read.
func (s *svc) ensureSecretSync(ctx context.Context, org string, a Application) {
	keys := secretEnvKeys(a.EnvJSON)
	if len(keys) > 0 {
		s.ensureTenantKMSAuth(ctx, org)
	}
	if err := s.k8s.applyKMSSecret(ctx, org, a.Slug, keys); err != nil {
		s.log.Warn("declare secret sync (KMSSecret) — continuing; secrets show pending until the operator syncs",
			"org", org, "app", a.Slug, "keys", len(keys), "err", err)
	}
}

// ── per-tenant KMS auth identity (the ONE privileged provisioning step) ─────────

// tenantKMSIdentity provisions (or resolves) the PER-TENANT IAM machine identity
// whose client_credentials token carries owner=<org> — the credential the
// kms-operator authenticates with (via /v1/kms/auth/login) to read ONLY that
// tenant's KMS scope. It is the sole seam where a privileged IAM write happens, so
// it is an injected dependency, not inlined: a concrete impl is wired ONLY when a
// scoped IAM admin credential is configured and verified.
//
// nil ⇒ provisioning is UNAVAILABLE: secret sync degrades to an honest "pending"
// (observeSecretSync) — never a shared platform-wide reader (a cross-tenant hole),
// never plaintext. The per-tenant creds Secret may still be provisioned out of
// band (ops / a universe KMSSecret); ensureTenantKMSAuth detects that and proceeds.
type tenantKMSIdentity interface {
	// EnsureOrgIdentity returns the clientId/clientSecret of a machine identity
	// bound to org (its token's owner claim == org). MUST be idempotent per org and
	// MUST NOT return a credential for any other org. An error leaves sync pending.
	//
	// AUDIENCE CONTRACT — must match cloud's validator (auth_identity.go,
	// kmsMachineAudSuffix). The identity MUST be a DEDICATED, NON-SHARED IAM
	// application named "<org>-platform-kms" (IAM clientId == app name), owned by the
	// tenant (Organization=<org>), with the client_credentials grant enabled and its
	// OWN per-tenant clientSecret. IAM then mints owner=<org> and aud=<org>-platform-kms
	// (a non-shared app's aud is its clientId), which is exactly the owner-bound
	// audience cloud accepts on the KMS machine path — so the operator's token clears
	// SanitizeIdentity and the org-scope guard admits it to /orgs/<org> only. A SHARED
	// app (one secret + an "@org"/"-org-" selector) is FORBIDDEN here: it would share
	// one clientSecret across tenants — the cross-tenant hole this seam exists to
	// avoid. NEVER mint the identity in the admin/master org; owner MUST be the tenant.
	EnsureOrgIdentity(ctx context.Context, org string) (clientID, clientSecret string, err error)
}

// ensureTenantKMSAuth makes sure the tenant's per-scoped KMS credential exists in
// tenant-<org>, so the operator can log in as owner=<org> and no wider. It is
// best-effort + fail-closed: any gap leaves the credential ABSENT (operator cannot
// authenticate → the org's sync stays pending), never a shared or wrong-org
// identity. Never fails the deploy.
func (s *svc) ensureTenantKMSAuth(ctx context.Context, org string) {
	// Already present (a prior deploy, ops, or a universe KMSSecret provisioned it)?
	// Nothing to do — and no pending-spam. Existence is the readiness signal.
	if s.k8s.tenantKMSAuthExists(ctx, org) {
		return
	}
	if s.kmsIdentity == nil {
		// FLAGGED STEP: no scoped IAM admin credential is wired, so cloud will not
		// mint a per-tenant identity itself. Fail closed to pending — the operator
		// cannot authenticate, so it can read NOTHING (least privilege by default),
		// rather than fall back to any shared reader.
		s.log.Info("secret sync pending: per-tenant KMS identity provisioning not configured — provision a machine identity (owner=<org>) into the tenant creds Secret to activate; staying fail-closed",
			"org", org, "credsSecret", s.k8s.kmsSync.credsSecret, "namespace", tenantNamespace(org))
		return
	}
	clientID, clientSecret, err := s.kmsIdentity.EnsureOrgIdentity(ctx, org)
	if err != nil {
		s.log.Warn("secret sync pending: provision per-tenant KMS identity failed — staying fail-closed",
			"org", org, "err", err)
		return
	}
	if clientID == "" || clientSecret == "" {
		s.log.Warn("secret sync pending: per-tenant KMS identity provisioner returned empty credential — staying fail-closed", "org", org)
		return
	}
	if err := s.k8s.applyTenantKMSAuthSecret(ctx, org, clientID, clientSecret); err != nil {
		s.log.Warn("secret sync pending: project per-tenant KMS creds Secret failed — staying fail-closed",
			"org", org, "err", err)
	}
}

// tenantKMSAuthExists reports whether the per-tenant KMS-auth creds Secret is
// present in tenant-<org>. A missing cluster or a NotFound is "absent" (false), so
// the caller provisions or stays pending — never a false "ready".
func (k *k8sClient) tenantKMSAuthExists(ctx context.Context, org string) bool {
	if k.ready() != nil {
		return false
	}
	_, err := k.dyn.Resource(secretsGVR).Namespace(tenantNamespace(org)).
		Get(ctx, k.kmsSync.credsSecret, metav1.GetOptions{})
	return err == nil
}

// applyTenantKMSAuthSecret projects the per-tenant KMS-auth credential into
// tenant-<org> as the Opaque Secret the KMSSecret CR's credentialsRef names, with
// the operator's expected data keys (clientId/clientSecret). It is create-if-
// absent (the per-tenant ClusterRole grants get/create/delete on core secrets, not
// patch) — rotation is delete-then-recreate. The credential is org-bound by the
// provisioner; this method only lands it in the right namespace/shape.
func (k *k8sClient) applyTenantKMSAuthSecret(ctx context.Context, org, clientID, clientSecret string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	if _, err := k.dyn.Resource(secretsGVR).Namespace(ns).Get(ctx, k.kmsSync.credsSecret, metav1.GetOptions{}); err == nil {
		return nil // already present — leave it (rotation is an explicit delete+recreate)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	sec := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      k.kmsSync.credsSecret,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":        org,
				"hanzo.ai/managed-by": "platform",
			},
		},
		"type": "Opaque",
		"stringData": map[string]any{
			"clientId":     clientID,
			"clientSecret": clientSecret,
		},
	}}
	_, err := k.dyn.Resource(secretsGVR).Namespace(ns).Create(ctx, sec, metav1.CreateOptions{})
	return err
}

// ── cluster: the KMSSecret CR (operator-driven materialization) ────────────────

// kmsSecretCR renders the canonical KMSSecret CR for an app's secret env. It
// declares a managed k8s Secret `<app>-env` in tenant-<org>, sourced from the
// app's org-scoped KMS scope — the byte-for-byte shape the production kms-operator
// reconciles (universalAuth machine identity → hostAPI /v1/kms → managedSecret).
//
// The secretsScope is the tenant boundary made concrete:
//   - projectSlug = org      → the operator reads /v1/kms/orgs/<org>/secrets/…,
//     and cloud's guard admits it ONLY when the authenticated owner == <org>.
//   - secretsPath = platform/<app>   (sub-path under the org)
//   - keys       = the exact secret names (REQUIRED, MinItems=1: luxfi/kms has no
//     list endpoint, so every value is fetched by name). Passed in already sorted
//     for a byte-stable CR across deploys.
//
// credentialsRef points at the PER-TENANT creds Secret in THIS namespace — never a
// shared reader. hostAPI is the KMS root (operator appends /v1/kms/…).
func (k *k8sClient) kmsSecretCR(ns, org, appSlug string, keys []string) *unstructured.Unstructured {
	c := k.kmsSync
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "secrets.lux.network/v1alpha1",
		"kind":       "KMSSecret",
		"metadata": map[string]any{
			"name":      managedSecretName(appSlug),
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":               org,
				"hanzo.ai/managed-by":        "platform",
				"app.kubernetes.io/instance": appSlug,
			},
		},
		"spec": map[string]any{
			"hostAPI":        c.hostAPI,
			"resyncInterval": int64(60),
			"authentication": map[string]any{
				"universalAuth": map[string]any{
					"credentialsRef": map[string]any{
						"secretName":      c.credsSecret,
						"secretNamespace": ns,
					},
					"secretsScope": map[string]any{
						"projectSlug": org,
						"envSlug":     "default",
						"secretsPath": kmsSecretsPath(appSlug),
						"keys":        keysToAny(keys),
					},
				},
			},
			"managedSecretReference": map[string]any{
				"secretName":      managedSecretName(appSlug),
				"secretNamespace": ns,
				"secretType":      "Opaque",
				// Owner: the managed Secret is garbage-collected with the KMSSecret,
				// so deleting the app (which deletes this CR) reaps its secret too.
				"creationPolicy": "Owner",
			},
		},
	}}
}

// keysToAny converts the sorted secret-key slice to the []any unstructured
// requires (a []string would panic runtime.DeepCopyJSON on the dynamic client).
func keysToAny(keys []string) []any {
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// applyKMSSecret create-or-updates (keys>0) or deletes (keys==0) the app's
// KMSSecret CR in tenant-<org>. It is idempotent and org-scoped. A missing CRD
// (operator not installed) or a forbidden write (RBAC not yet granted) is returned
// so the caller can log it and degrade to "pending" — it is NEVER fatal to a
// deploy. The namespace is assumed to exist (applyService ensured it before this
// runs on the deploy path).
func (k *k8sClient) applyKMSSecret(ctx context.Context, org, appSlug string, keys []string) error {
	if err := k.ready(); err != nil {
		return err
	}
	ns := tenantNamespace(org)
	name := managedSecretName(appSlug)
	if len(keys) == 0 {
		// No secret env → ensure no stale KMSSecret lingers (a secret removed from
		// the app must stop syncing). NotFound is success.
		err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	desired := k.kmsSecretCR(ns, org, appSlug, keys)
	_, err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cErr := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		return cErr
	}
	if err != nil {
		return err
	}
	patch := map[string]any{
		"metadata": map[string]any{"labels": desired.Object["metadata"].(map[string]any)["labels"]},
		"spec":     desired.Object["spec"],
	}
	b, _ := json.Marshal(patch)
	_, err = k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, b, metav1.PatchOptions{})
	return err
}

// deleteKMSSecret removes an app's KMSSecret CR (app/project teardown). NotFound is
// success; a missing CRD/RBAC is swallowed (best-effort teardown).
func (k *k8sClient) deleteKMSSecret(ctx context.Context, org, appSlug string) error {
	if err := k.ready(); err != nil {
		return nil // no cluster → nothing to tear down
	}
	ns := tenantNamespace(org)
	err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Delete(ctx, managedSecretName(appSlug), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// observeSecretSync reports the HONEST secret-sync state of an app from its
// KMSSecret CR status — never a fabricated "ready". It reads the operator's
// conditions and maps them to a small, stable vocabulary the console can render:
//
//	""        no secret env (no KMSSecret) — nothing to sync
//	ready     the operator materialized the managed Secret (all keys present)
//	syncing   the CR exists but the operator has not yet reported ready
//	pending   the CR could not be read (CRD/RBAC not present, or app not deployed)
//	failed    the operator reported a failure (detail carries the reason)
//
// Best-effort: any read error yields "pending" (honest unknown), never a guess.
func (k *k8sClient) observeSecretSync(ctx context.Context, org, appSlug string, hasSecrets bool) (status, detail string) {
	if !hasSecrets {
		return "", ""
	}
	if k.ready() != nil {
		return "pending", "cluster unavailable"
	}
	ns := tenantNamespace(org)
	obj, err := k.dyn.Resource(kmsSecretsGVR).Namespace(ns).Get(ctx, managedSecretName(appSlug), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "pending", "secret sync not yet declared (deploy the app to sync its secrets)"
		}
		// A missing CRD or forbidden read (operator/RBAC not wired) is an honest
		// "pending", not a fabricated ready.
		return "pending", "secret sync status unavailable"
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		cmsg, _ := cm["message"].(string)
		// The kms-operator publishes a Ready/Synced-style condition; a False status
		// with a reason is a real failure we surface verbatim (never masked).
		if cstatus == "False" {
			if cmsg == "" {
				cmsg = "operator reported the secret sync is not ready"
			}
			return "failed", cmsg
		}
		if strings.EqualFold(ctype, "Ready") || strings.EqualFold(ctype, "Synced") {
			if cstatus == "True" {
				return "ready", ""
			}
		}
	}
	return "syncing", "operator is materializing the secret"
}
