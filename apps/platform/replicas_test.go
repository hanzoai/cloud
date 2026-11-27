package platform

// Platform runs on every replica. These tests stand a second replica beside the first:
// on hanzo-sql a second service with a store pool and a lock of its own over the same
// schema, which is what another pod is; on SQLite the same service, because a file
// belongs to one process.

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// holdDyn is a cluster that holds a patch where the test says to. The fake client holds
// its own lock through its reactors, so a hold has to be before the call: a real
// apiserver takes writes from many clients at once.
type holdDyn struct {
	dynamic.Interface
	hold func(patch []byte)
}

func (d holdDyn) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return holdResource{d.Interface.Resource(gvr), d.hold}
}

type holdResource struct {
	dynamic.NamespaceableResourceInterface
	hold func(patch []byte)
}

func (r holdResource) Namespace(ns string) dynamic.ResourceInterface {
	return holdNamespaced{r.NamespaceableResourceInterface.Namespace(ns), r.hold}
}

type holdNamespaced struct {
	dynamic.ResourceInterface
	hold func(patch []byte)
}

func (n holdNamespaced) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	n.hold(data)
	return n.ResourceInterface.Patch(ctx, name, pt, data, opts, subresources...)
}

// An older deploy of an app, held in its Service CR write on one replica, does not write
// after a newer deploy of the same app that another replica carries to live: the app's
// apply-and-finalize is one step across replicas, so the CR ends at the version the app
// records as live.
func TestTwoReplicasApplyOneAppInOrder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		_, first := mountSvcK8s(t, k)
		second := first
		if dialect.Of(first.State.store.db).Postgres() {
			_, second = mountSvcK8s(t, k)
		}
		const org, project = "racer", "web"
		ctx := context.Background()
		app := Application{
			ID: "app_race", Org: org, ProjectID: project, Slug: "api", Name: "api", Environment: "production",
			Source: "image", ImageRepo: "ghcr.io/hanzoai/nginx", ImageTag: "v0", Port: 8080, Replicas: 1,
			EnvJSON: "[]", DomainsJSON: "[]", Status: "created", Namespace: tenantNamespace(org), CreatedAt: 1, UpdatedAt: 1,
		}
		if err := first.State.store.CreateApplication(ctx, app); err != nil {
			t.Fatal(err)
		}
		if err := k.applyService(ctx, org, project, app, "ghcr.io/hanzoai/nginx:v0"); err != nil {
			t.Fatal(err)
		}

		// The older deploy's CR write is held until the newer one has gone live, or half a
		// second has passed (the lock held the newer one out).
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		k.dyn = holdDyn{k.dyn, func(patch []byte) {
			if bytes.Contains(patch, []byte(`"v1"`)) {
				once.Do(func() { close(entered) })
				<-release
			}
		}}

		var wg sync.WaitGroup
		deploy := func(s *cloud.Service[state], dep string, version int, tag string) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := deployTagCore(s, ctx, org, project, app, dep, version, time.Now().Unix(), "ghcr.io/hanzoai/nginx:"+tag, tag, "image", "", nil); err != nil {
					t.Errorf("deploy %s: %v", tag, err)
				}
			}()
		}
		deploy(first, "dep_old", 1, "v1")
		<-entered
		deploy(second, "dep_new", 2, "v2")
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if a, err := first.State.store.GetApplicationByID(ctx, org, app.ID); err == nil && a.CurrentDeploy == "dep_new" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(release)
		wg.Wait()

		a, err := first.State.store.GetApplicationByID(ctx, org, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := crImage(t, k, tenantNamespace(org), "api"); got != "ghcr.io/hanzoai/nginx:v2" || a.CurrentDeploy != "dep_new" {
			t.Fatalf("the Service CR is %s while the app records %s live, want the newer deploy in both", got, a.CurrentDeploy)
		}
	})
}
