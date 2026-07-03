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
//     envelope, clients/kms) at a per-tenant/app coordinate. Plaintext is sealed
//     before it touches disk, never logged, never echoed back over the API
//     (toAppView masks it). If KMS is unavailable the write FAILS CLOSED — a
//     plaintext secret never lands in the DB as a fallback.
//  2. DECLARE — on deploy, cloud writes a canonical KMSSecret CR
//     (secrets.lux.network/v1alpha1, the kms-operator-canonical CRD) into
//     tenant-<org> declaring a managed k8s Secret `<app>-env` sourced from that
//     KMS scope; the kms-operator reconciles KMS → Secret. This is best-effort:
//     a not-yet-granted CRD/RBAC degrades to an honest "secret sync pending"
//     status, never a failed deploy.
//  3. MOUNT — the Service CR renders each secret env as
//     `valueFrom.secretKeyRef` → that Secret (optional, so the pod boots even
//     before the sync lands); the hanzo operator mounts it into the Deployment
//     env. The pod reads the secret from the k8s Secret — cloud is never in the
//     plaintext path at runtime.
//
// Everything is org-scoped: the KMS coordinate, the KMSSecret CR, and the managed
// Secret all live under the VALIDATED tenant (tenant-<org>), never a request
// value — the same cross-tenant boundary as the rest of /v1/platform.
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

// kmsSyncConfig is the operator-facing config the KMSSecret CR carries: which KMS
// the operator reads (hostAPI), the project it authenticates against, and the
// credentials Secret (in the tenant namespace) that holds the universalAuth
// clientId/clientSecret. All are env-overridable so activating live tenant-secret
// sync is a config change, never a code change.
//
// The defaults target CLOUD's own KMS (the same store sealSecretEnv writes to) so
// the design is coherent end-to-end: cloud seals to cloud KMS, and the CR reads
// cloud KMS back. Two pieces must land to make sync GO LIVE (flagged for CTO —
// security-sensitive, deliberately not silently half-wired):
//
//   - cloud's /v1/kms must expose the Infisical read surface the kms-operator's
//     universalAuth transport calls (or the operator gain a cloud-KMS transport),
//     so it can read the per-tenant scope cloud sealed.
//   - the credentials Secret must be provisioned PER TENANT and SCOPED to that
//     tenant's project only — never the shared machine identity in every tenant
//     namespace (that would let a compromised tenant pod read other tenants'
//     secrets). Hence the default credsSecret is a per-tenant name, not the shared
//     `universal-auth-credentials`.
//
// Until then the KMSSecret reconciles to an honest pending/failed condition,
// surfaced by observeSecretSync; the sealed secret + optional secretKeyRef keep
// the pod booting meanwhile. No plaintext ever leaks and no cross-tenant identity
// is provisioned by default.
type kmsSyncConfig struct {
	hostAPI     string // KMS the operator pulls from (CLOUD_PLATFORM_KMS_HOST_API)
	project     string // universalAuth projectSlug   (CLOUD_PLATFORM_KMS_PROJECT)
	credsSecret string // per-tenant creds Secret name (CLOUD_PLATFORM_KMS_CREDS_SECRET)
}

// newKMSSyncConfig resolves the KMSSecret operator config from env, defaulting to
// cloud's own in-cluster KMS surface and a per-tenant (scoped) credentials Secret.
func newKMSSyncConfig() kmsSyncConfig {
	return kmsSyncConfig{
		hostAPI:     getenv("CLOUD_PLATFORM_KMS_HOST_API", "http://cloud.hanzo.svc.cluster.local/v1/kms"),
		project:     getenv("CLOUD_PLATFORM_KMS_PROJECT", "platform"),
		credsSecret: getenv("CLOUD_PLATFORM_KMS_CREDS_SECRET", "platform-kms-auth"),
	}
}

// managedSecretName is the k8s Secret the KMSSecret materializes AND the Service
// CR secretKeyRefs point at: "<app>-env" in tenant-<org>. It shares the app slug
// with the Service CR name (serviceCR), so the two objects stay paired per app.
func managedSecretName(appSlug string) string { return appSlug + "-env" }

// kmsSecretRef is the per-tenant/app KMS coordinate for one secret key, a flat
// ref the embedded KMS parses to (path=/platform/<tenant-ns>/<app>, name=<KEY>,
// env=default):
//
//	platform/<tenant-ns>/<app>/<KEY>
//
// It is INJECTIVE in (org, app, key): the org slug (provisioning.SanitizeOrg),
// the app slug (slugRE) and the key (envKeyRE) contain no '/' or '@', so two
// distinct (org,app,key) triples never collide on one KMS record and one tenant
// can never address another's secret. The path mirrors the KMSSecret secretsPath
// so the operator reads back exactly what cloud sealed.
func kmsSecretRef(org, appSlug, key string) string {
	return "platform/" + tenantNamespace(org) + "/" + appSlug + "/" + key
}

// kmsSecretsPath is the KMS scope (a whole app's secrets) the KMSSecret CR syncs:
// /platform/<tenant-ns>/<app>. Every key sealed under kmsSecretRef lives here, so
// the operator materializes them as the managed Secret's data keys.
func kmsSecretsPath(org, appSlug string) string {
	return "/platform/" + tenantNamespace(org) + "/" + appSlug
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
// Service CR. It is BEST-EFFORT: it never fails a deploy — a missing CRD or RBAC
// grant degrades to an honest "pending" secret-sync status while the sealed secret
// + optional secretKeyRef keep the pod booting. Called from applyLive (the ONE
// shared deploy choke point) so both the image and git paths declare it.
func (s *svc) ensureSecretSync(ctx context.Context, org string, a Application) {
	keys := secretEnvKeys(a.EnvJSON)
	if err := s.k8s.applyKMSSecret(ctx, org, a.Slug, keys); err != nil {
		s.log.Warn("declare secret sync (KMSSecret) — continuing; secrets show pending until the operator syncs",
			"org", org, "app", a.Slug, "keys", len(keys), "err", err)
	}
}

// ── cluster: the KMSSecret CR (operator-driven materialization) ────────────────

// kmsSecretCR renders the canonical KMSSecret CR for an app's secret env. It
// declares a managed k8s Secret `<app>-env` in tenant-<org>, sourced from the
// app's KMS scope (kmsSecretsPath) — the byte-for-byte shape the production
// kms-operator reconciles (universalAuth machine identity → hostAPI KMS →
// managedSecretReference). hostAPI / project / credentials are operator config
// (env-overridable) so activating live sync is a config + RBAC change, never a
// code change.
func (k *k8sClient) kmsSecretCR(ns, org, appSlug string) *unstructured.Unstructured {
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
						"projectSlug": c.project,
						"envSlug":     "default",
						"secretsPath": kmsSecretsPath(org, appSlug),
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
	desired := k.kmsSecretCR(ns, org, appSlug)
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
