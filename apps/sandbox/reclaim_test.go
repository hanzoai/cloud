package sandbox

// What makes a project disk reclaimable.
//
// volume_test.go pins the two halves of the disk's LIFETIME — a lease never costs
// a tenant their disk, and every lease records the facts a reclaim would need.
// This pins what reads those facts. The asymmetry is the whole design and it runs
// one way: a disk wrongly kept costs 20Gi a month, a disk wrongly deleted costs a
// tenant their checkout. So every case below that ends in KEEP is the interesting
// one, and the single DROP case is the only shape allowed through.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// svc is a Service holding nothing but the runtime and a silent log — the only
// two things a reclaim touches.
func svc(r *runtime) *cloud.Service[state] {
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{rt: r},
	}
}

// disk is a PVC as ensureVolume writes one: the project label, and the day it was
// last leased.
func disk(name, leased string) *unstructured.Unstructured {
	m := map[string]any{
		"name":      name,
		"namespace": "hanzo-sandboxes",
		"labels":    map[string]any{labOrg: "acme", labProject: "site"},
	}
	if leased != "" {
		m["annotations"] = map[string]any{annLeased: leased}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": m,
	}}
}

// pod mounts a claim, which is the fact that outranks every stamp.
func pod(name, claim string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": "hanzo-sandboxes",
			"labels": map[string]any{labSandbox: "sbx-1"},
		},
		"spec": map[string]any{"volumes": []any{
			map[string]any{"name": "work", "persistentVolumeClaim": map[string]any{"claimName": claim}},
		}},
	}}
}

func day(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(time.DateOnly)
}

// A COLD, UNMOUNTED, DATED DISK IS THE ONLY THING RECLAIMED — and everything that
// differs from it in exactly one way is kept.
func TestReclaim(t *testing.T) {
	cold := diskCold + 48*time.Hour

	for _, c := range []struct {
		name string
		disk *unstructured.Unstructured
		pods []k8sruntime.Object
		gone bool
		why  string
	}{
		{
			name: "cold and unmounted", disk: disk("m-acme-site-a1", day(cold)), gone: true,
			why: "unleased for longer than diskCold with nothing holding it — the case this exists for",
		},
		{
			name: "leased today", disk: disk("m-acme-site-a1", day(0)),
			why: "in use; a disk leased today is not a day closer to reclaim",
		},
		{
			name: "just inside the bound", disk: disk("m-acme-site-a1", day(diskCold-48*time.Hour)),
			why: "cold is a boundary, and a disk short of it is kept — no rounding toward deletion",
		},
		{
			name: "cold but MOUNTED", disk: disk("m-acme-site-a1", day(cold)),
			pods: []k8sruntime.Object{pod("m-live", "m-acme-site-a1")},
			why:  "a mounted disk is in use whatever its stamp says; the stamp is a hint, the mount is a fact",
		},
		{
			name: "undated", disk: disk("m-acme-site-a1", ""),
			why: "predates the stamp, so its last use is unrecorded — a disk that cannot be SHOWN dead is kept",
		},
		{
			name: "unparseable date", disk: disk("m-acme-site-a1", "last tuesday"),
			why: "a date we cannot read is not a date in the past",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := fakeRuntime(append([]k8sruntime.Object{c.disk}, c.pods...)...)
			reclaim(context.Background(), svc(r))

			_, alive := disks(t, r)["m-acme-site-a1"]
			if c.gone && alive {
				t.Fatalf("disk survived but should have been reclaimed: %s", c.why)
			}
			if !c.gone && !alive {
				t.Fatalf("DISK DELETED and must not have been: %s", c.why)
			}
		})
	}
}

// AN UNREADABLE POD LIST RECLAIMS NOTHING.
//
// "no pod mounts this disk" and "we could not find out" are the same sentence to a
// caller and opposite facts to a tenant. Treating the second as the first deletes
// every mounted disk in the namespace at once, which is the one mistake here with
// no upper bound on its cost — so it is pinned rather than left to the reading of
// a nil check.
func TestReclaimKeepsEverythingWhenPodsUnreadable(t *testing.T) {
	r := fakeRuntime(disk("m-acme-site-a1", day(diskCold+48*time.Hour)))
	r.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "pods",
		func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, errors.New("apiserver unavailable")
		})

	reclaim(context.Background(), svc(r))

	if _, alive := disks(t, r)["m-acme-site-a1"]; !alive {
		t.Fatal("a cold disk was reclaimed while the pod list was unreadable — " +
			"an unanswerable question was treated as the answer 'nothing mounts it'")
	}
}

// A DISK OUTSIDE THE BOUND IS NOT TOUCHED, even when it is cold and unmounted.
// The namespace half and the label half are each load-bearing (bound.go); this
// pins the label half, which is the one a disk wears.
func TestReclaimIgnoresUnlabelledClaims(t *testing.T) {
	d := disk("m-acme-site-a1", day(diskCold+48*time.Hour))
	unstructured.RemoveNestedField(d.Object, "metadata", "labels")
	r := fakeRuntime(d)

	reclaim(context.Background(), svc(r))

	if _, alive := disks(t, r)["m-acme-site-a1"]; !alive {
		t.Fatal("reclaimed a claim carrying no sandbox-project label — the sweep " +
			"reached outside its own objects")
	}
}
