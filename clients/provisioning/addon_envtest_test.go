package provisioning

// Real-apiserver proof for the addon injection sink (Red low-3). The fake
// orchestrator MODELS strategic-merge as an additive map write; this test runs
// the ACTUAL k8sOrchestrator.PatchAddonSecret / RemoveAddonSecretKey against a
// real kube-apiserver (controller-runtime envtest) so the sibling-key
// preservation is proven by the server, not asserted by a stand-in:
//
//   - a StrategicMergePatch that folds stringData->data must PRESERVE every
//     <KIND>_URL already projected (enabling a 2nd add-on never drops the 1st);
//   - a JSON-merge delete (data[key]=null) must remove EXACTLY one key and keep
//     the siblings; and it is idempotent on an absent key / absent Secret.
//
// Skips unless KUBEBUILDER_ASSETS points at the envtest control-plane binaries
// (fetch once: `go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
// use -p path`), so the default `go test ./clients/provisioning/...` stays green
// without a cluster; CI (and the release gate) export it and this runs for real.

import (
	"context"
	"encoding/base64"
	"github.com/hanzoai/cloud/clients/k8s"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestPatchAddonSecret_RealAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset — run `setup-envtest use` and export it to exercise the real-apiserver addon test")
	}

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	o := &k8sOrchestrator{dyn: dyn}
	ctx := context.Background()

	const ns = "tenant-acme"
	if _, err := dyn.Resource(k8s.Namespaces).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": ns},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	const name = "commerce-addons"
	const kvURL = "kv://default:pw@kv:6379"
	const sqlURL = "postgres://admin:pw@sql:5432/db?sslmode=disable"

	// data reads the live Secret and base64-decodes .data into plain strings.
	data := func() map[string]string {
		t.Helper()
		sec, err := dyn.Resource(secretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get secret: %v", err)
		}
		raw, _, _ := unstructured.NestedMap(sec.Object, "data")
		out := make(map[string]string, len(raw))
		for k, v := range raw {
			s, _ := v.(string)
			b, derr := base64.StdEncoding.DecodeString(s)
			if derr != nil {
				t.Fatalf("decode data[%s]: %v", k, derr)
			}
			out[k] = string(b)
		}
		return out
	}

	// 1. First add-on: create-if-absent.
	if err := o.PatchAddonSecret(ctx, ns, name, "acme", "KV_URL", kvURL); err != nil {
		t.Fatalf("inject KV_URL (create): %v", err)
	}
	if got := data(); got["KV_URL"] != kvURL {
		t.Fatalf("KV_URL = %q after first inject, want %q", got["KV_URL"], kvURL)
	}

	// 2. Second add-on on the SAME Secret: the REAL strategic-merge stringData
	// fold must PRESERVE the sibling KV_URL. This is the exact behavior the fake
	// can only assume.
	if err := o.PatchAddonSecret(ctx, ns, name, "acme", "SQL_URL", sqlURL); err != nil {
		t.Fatalf("inject SQL_URL (merge): %v", err)
	}
	got := data()
	if got["SQL_URL"] != sqlURL {
		t.Fatalf("SQL_URL = %q after second inject, want %q", got["SQL_URL"], sqlURL)
	}
	if got["KV_URL"] != kvURL {
		t.Fatalf("real strategic-merge DROPPED sibling KV_URL: data=%v", got)
	}

	// 2.5. The inject path stamped the reloader opt-in + a non-empty revision.
	sec, _ := dyn.Resource(secretsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	ann, _, _ := unstructured.NestedStringMap(sec.Object, "metadata", "annotations")
	if ann[addonReloaderKey] != addonReloaderVal || ann[addonRevKey] == "" {
		t.Fatalf("reloader/rev annotations missing after inject: %v", ann)
	}

	// 3. Remove ONE key: the JSON-merge delete drops KV_URL, keeps SQL_URL.
	if err := o.RemoveAddonSecretKey(ctx, ns, name, "KV_URL"); err != nil {
		t.Fatalf("remove KV_URL: %v", err)
	}
	got = data()
	if _, present := got["KV_URL"]; present {
		t.Fatalf("KV_URL still present after remove: data=%v", got)
	}
	if got["SQL_URL"] != sqlURL {
		t.Fatalf("remove dropped the sibling SQL_URL: data=%v", got)
	}

	// 4. Idempotent: removing an absent key, and removing on an absent Secret,
	// are both success (drop must never fail because a revert already happened).
	if err := o.RemoveAddonSecretKey(ctx, ns, name, "KV_URL"); err != nil {
		t.Fatalf("idempotent remove of absent key: %v", err)
	}
	if err := o.RemoveAddonSecretKey(ctx, ns, "does-not-exist-addons", "SQL_URL"); err != nil {
		t.Fatalf("idempotent remove on absent Secret: %v", err)
	}
}
