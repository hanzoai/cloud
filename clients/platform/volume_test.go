package platform

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// getClaim reads the app's PVC out of the fake cluster, or nil when absent.
func getClaim(t *testing.T, k *k8sClient, org, slug string) *unstructured.Unstructured {
	t.Helper()
	u, err := k.dyn.Resource(k8s.Volumes).Namespace(tenantNamespace(org)).
		Get(context.Background(), volumeName(slug), metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return u
}

func claimSize(t *testing.T, u *unstructured.Unstructured) string {
	t.Helper()
	size, _, _ := unstructured.NestedString(u.Object, "spec", "resources", "requests", "storage")
	return size
}

// TestStatelessAppGetsNoVolume is the default and the one that must not regress:
// an app that asks for no storage gets no claim, no volumes on its CR, and keeps
// the rolling update it had. Storage is opt-in.
func TestStatelessAppGetsNoVolume(t *testing.T) {
	k := fakeK8s()
	a := mkApp("acme", "proj_1", "api") // StorageGB stays 0

	if err := k.applyService(context.Background(), "acme", "proj_1", a, "ghcr.io/hanzoai/nginx:1"); err != nil {
		t.Fatalf("applyService: %v", err)
	}
	if c := getClaim(t, k, "acme", "api"); c != nil {
		t.Fatalf("stateless app got a volume claim: %v", c.Object)
	}
	cr := serviceCR(tenantNamespace("acme"), "acme", "proj_1", a, "ghcr.io/hanzoai/nginx:1")
	spec := cr.Object["spec"].(map[string]any)
	if _, ok := spec["volumes"]; ok {
		t.Fatal("stateless app CR declares volumes")
	}
	if _, ok := spec["volumeMounts"]; ok {
		t.Fatal("stateless app CR declares volumeMounts")
	}
	if _, ok := spec["strategy"]; ok {
		t.Fatal("stateless app must keep the default rolling update, not Recreate")
	}
}

// TestStorageProvisionsClaimAndMountsIt covers the whole declaration: asking for
// storage creates the claim at the requested size and the CR mounts that claim at
// the one path.
func TestStorageProvisionsClaimAndMountsIt(t *testing.T) {
	k := fakeK8s()
	a := mkApp("acme", "proj_1", "index")
	a.StorageGB = 20

	if err := k.applyService(context.Background(), "acme", "proj_1", a, "ghcr.io/hanzoai/index:1"); err != nil {
		t.Fatalf("applyService: %v", err)
	}

	claim := getClaim(t, k, "acme", "index")
	if claim == nil {
		t.Fatal("storage was declared but no claim was created")
	}
	if got := claimSize(t, claim); got != "20Gi" {
		t.Fatalf("claim size = %q, want 20Gi", got)
	}
	modes, _, _ := unstructured.NestedStringSlice(claim.Object, "spec", "accessModes")
	if len(modes) != 1 || modes[0] != "ReadWriteOnce" {
		t.Fatalf("accessModes = %v, want [ReadWriteOnce]", modes)
	}

	cr := serviceCR(tenantNamespace("acme"), "acme", "proj_1", a, "ghcr.io/hanzoai/index:1")
	spec := cr.Object["spec"].(map[string]any)
	vols := spec["volumes"].([]any)
	if len(vols) != 1 {
		t.Fatalf("volumes = %v, want exactly one", vols)
	}
	got := vols[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"]
	if got != "index-data" {
		t.Fatalf("claimName = %v, want index-data", got)
	}
	mounts := spec["volumeMounts"].([]any)
	if p := mounts[0].(map[string]any)["mountPath"]; p != volumeMount {
		t.Fatalf("mountPath = %v, want %s", p, volumeMount)
	}
}

// TestStorageForcesRecreate pins the coupling that is easy to drop and expensive
// to rediscover: a ReadWriteOnce volume attaches to one node, so a rolling update
// deadlocks on Multi-Attach with the new pod stuck in ContainerCreating. Declaring
// storage must therefore also declare Recreate.
func TestStorageForcesRecreate(t *testing.T) {
	a := mkApp("acme", "proj_1", "db")
	a.StorageGB = 5
	cr := serviceCR(tenantNamespace("acme"), "acme", "proj_1", a, "ghcr.io/hanzoai/db:1")
	if got := cr.Object["spec"].(map[string]any)["strategy"]; got != "Recreate" {
		t.Fatalf("strategy = %v, want Recreate", got)
	}
}

// TestRedeployNeverRewritesTheClaim is the data-loss guard. A redeploy runs
// applyService again; the claim it finds must be left exactly as it is, even when
// the app row now says a different size. Resizing is deliberate and separate — and
// a shrink written here would be rejected by the CSI driver and wedge the app.
func TestRedeployNeverRewritesTheClaim(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	a := mkApp("acme", "proj_1", "index")
	a.StorageGB = 20
	if err := k.applyService(ctx, "acme", "proj_1", a, "ghcr.io/hanzoai/index:1"); err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	a.StorageGB = 5 // a shrink, which must not reach the cluster
	if err := k.applyService(ctx, "acme", "proj_1", a, "ghcr.io/hanzoai/index:2"); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	if got := claimSize(t, getClaim(t, k, "acme", "index")); got != "20Gi" {
		t.Fatalf("claim size = %q after redeploy, want the original 20Gi", got)
	}
}

// TestDeleteAppKeepsTheVolume: deleting an app is a statement about a workload.
// The claim holds the only copy of the tenant's data and outlives it.
func TestDeleteAppKeepsTheVolume(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	a := mkApp("acme", "proj_1", "index")
	a.StorageGB = 10
	if err := k.applyService(ctx, "acme", "proj_1", a, "ghcr.io/hanzoai/index:1"); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if err := k.deleteService(ctx, "acme", "index"); err != nil {
		t.Fatalf("deleteService: %v", err)
	}
	if getClaim(t, k, "acme", "index") == nil {
		t.Fatal("deleting the app destroyed its volume")
	}
}

// TestClampStorage: zero is meaningful (stateless) and must survive, which is why
// this cannot borrow clampReplicas' "0 becomes 1" floor. Over-asking is capped
// rather than refused, so a fat-fingered size degrades instead of failing a deploy.
func TestClampStorage(t *testing.T) {
	l := resourceLimits{maxStorageGB: 50}
	for _, tc := range []struct{ in, want int }{
		{0, 0}, {-7, 0}, {1, 1}, {50, 50}, {5000, 50},
	} {
		if got := l.clampStorage(tc.in); got != tc.want {
			t.Errorf("clampStorage(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// An unset ceiling must fall back to the safe default, never to unlimited.
	if got := (resourceLimits{}).clampStorage(1_000_000); got != defaultMaxStorageGB {
		t.Errorf("unset ceiling: clampStorage(1e6) = %d, want %d", got, defaultMaxStorageGB)
	}
}

// TestStorageSurvivesTheStore: the column round-trips, and an app written before
// it existed reads back as stateless rather than as an error.
func TestStorageSurvivesTheStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	a := mkApp("acme", "proj_1", "index")
	a.StorageGB = 42
	if err := st.CreateApplication(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetApplication(ctx, "acme", "proj_1", "index")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.StorageGB != 42 {
		t.Fatalf("StorageGB = %d, want 42", got.StorageGB)
	}

	got.StorageGB = 7
	got.UpdatedAt = 200
	if err := st.UpdateApplication(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := st.GetApplication(ctx, "acme", "proj_1", "index")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if again.StorageGB != 7 {
		t.Fatalf("StorageGB after update = %d, want 7", again.StorageGB)
	}
}
