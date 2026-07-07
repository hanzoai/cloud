package provisioning

// Instance binding — the ONE uniform <KIND>_URL injection mechanism.
//
// Every product runs on Base/SQLite by default. When an org ENABLES an add-on
// (Hanzo KV / SQL / DocDB / Datastore) for one app instance, the dedicated
// provision projects the assembled DSN as the canonical env var <KIND>_URL
// (KV_URL / SQL_URL / DOCDB_URL / DATASTORE_URL) into the Secret
// "<instance>-addons" in that org's namespace (tenant-<org>). The instance reads
// <KIND>_URL and switches off Base onto the backend; DISABLING (drop) removes
// the key and the instance reverts to Base. ONE derivation, four kinds — the
// selection is parameterized by kind, never four bespoke systems.
//
// The addons Secret is the injection SINK. Its keys are the <KIND>_URL env vars;
// the instance mounts it with `envFrom: [{secretRef: {name: <instance>-addons,
// optional: true}}]`, so an absent Secret / key means the instance stays on
// Base. The DSN carries a password, so the sink is a k8s Secret (RBAC-scoped) —
// NEVER a plaintext row/log; the provisioned_resources row keeps only the KMS
// secret_ref. Multiple add-ons on one instance coexist because injection MERGES
// a single key and removal deletes a single key, never rewriting the Secret.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// addonReloader* opt the addons Secret into Stakater Reloader watching: the
// instance's workload carries the matching reloader.stakater.com/match
// annotation, so when Reloader is deployed any change to this Secret rolls the
// workload to re-read the <KIND>_URL env. addonRevKey is bumped on every write so
// the change is always visible (and, once Reloader is present, always triggers a
// reload) even if a value is unchanged. Absent Reloader the key still lands
// durably and is picked up on the next restart — the mechanism is correct
// regardless; Reloader only makes the re-read automatic.
const (
	addonReloaderKey = "reloader.stakater.com/match"
	addonReloaderVal = "true"
	addonRevKey      = "hanzo.ai/addons-rev"
)

// addonKey is the canonical env var a backend kind is injected under:
// KV_URL, SQL_URL, DATASTORE_URL, DOCDB_URL. ONE uniform derivation.
func addonKey(kind string) string { return strings.ToUpper(kind) + "_URL" }

// addonsSecretName is the Secret an instance reads its add-on <KIND>_URL keys
// from: "<instance>-addons".
func addonsSecretName(instance string) string { return instance + "-addons" }

// injectAddonURL projects dsn as <KIND>_URL into the instance's addons Secret in
// tenant-<org>, creating the Secret if absent and MERGING the key so a second
// add-on on the same instance never clobbers the first. No-op when the request
// is not instance-bound (instance == "") — the pre-instance-binding behavior, so
// every existing provision is unchanged.
func (s *svc) injectAddonURL(ctx context.Context, org, instance, kind, dsn string) error {
	if instance == "" {
		return nil
	}
	ns := tenantNamespace(org)
	return s.orch.PatchAddonSecret(ctx, ns, addonsSecretName(instance), org, addonKey(kind), dsn)
}

// removeAddonURL deletes the <KIND>_URL key from the instance's addons Secret so
// the instance reverts to Base. Idempotent: a missing Secret/key is success.
// No-op when the resource is not instance-bound.
func (s *svc) removeAddonURL(ctx context.Context, org, instance, kind string) error {
	if instance == "" {
		return nil
	}
	ns := tenantNamespace(org)
	return s.orch.RemoveAddonSecretKey(ctx, ns, addonsSecretName(instance), addonKey(kind))
}

// ----- k8sOrchestrator addon impls ------------------------------------------

// PatchAddonSecret merges key=value into the addons Secret's stringData
// (create-if-absent). A strategic-merge patch on a Secret folds stringData into
// data key-by-key, so every OTHER <KIND>_URL an instance already has survives —
// enabling a second add-on never drops the first.
func (o *k8sOrchestrator) PatchAddonSecret(ctx context.Context, ns, name, org, key, value string) error {
	if err := o.Ready(); err != nil {
		return err
	}
	rev := fmt.Sprint(time.Now().UnixNano())
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{
			addonReloaderKey: addonReloaderVal,
			addonRevKey:      rev,
		}},
		"stringData": map[string]any{key: value},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = o.dyn.Resource(secretsGVR).Namespace(ns).Patch(ctx, name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		_, err = o.dyn.Resource(secretsGVR).Namespace(ns).Create(ctx, addonsSecretObj(ns, name, org, key, value, rev), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			// Lost a create race — merge onto the now-existing Secret instead.
			_, err = o.dyn.Resource(secretsGVR).Namespace(ns).Patch(ctx, name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
		}
	}
	return err
}

// RemoveAddonSecretKey deletes one <KIND>_URL from the addons Secret via a JSON
// merge patch (RFC 7386: data[key]=null removes just that key, preserving the
// others). A missing Secret is a no-op success, so drop is idempotent.
func (o *k8sOrchestrator) RemoveAddonSecretKey(ctx context.Context, ns, name, key string) error {
	if err := o.Ready(); err != nil {
		return err
	}
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{addonRevKey: fmt.Sprint(time.Now().UnixNano())}},
		"data":     map[string]any{key: nil},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = o.dyn.Resource(secretsGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// addonsSecretObj is the create-if-absent form of the addons Secret: one
// <KIND>_URL key plus the reloader/revision annotations. Created at runtime via
// the API (stringData), so no plaintext DSN ever lands in git. The org label
// keeps it attributable; managed-by marks it control-plane-owned.
func addonsSecretObj(ns, name, org, key, value, rev string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":        org,
				"hanzo.ai/managed-by": "provisioning",
				"hanzo.ai/addons":     "true",
			},
			"annotations": map[string]any{
				addonReloaderKey: addonReloaderVal,
				addonRevKey:      rev,
			},
		},
		"type":       "Opaque",
		"stringData": map[string]any{key: value},
	}}
}
