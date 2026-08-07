package sandbox

// The lifetime of a project disk.
//
// A disk is the one thing this service makes that OUTLIVES everything that made
// it: the pod is gone in an hour, the row is gone with the lease, and the 20Gi
// stays until somebody deletes it on purpose. So the two properties below are
// not "does the code work" — they are the two halves of the only decision an
// operator staring at a namespace full of disks has to get right, and getting it
// wrong in either direction is expensive:
//
//	KEEP  a disk still in use is a tenant's checkout and their uncommitted work.
//	      Deleting one is unrecoverable, and no lease ending may ever imply it.
//	DROP  a disk nobody will ever ask for again bills forever. It cannot be
//	      identified — and therefore cannot be dropped — unless it SAYS what it
//	      is for and when it was last wanted.
//
// Measured 2026-08-07 on the live namespace: 15 disks, 300GiB, and the project
// each one belonged to was recoverable ONLY by brute-forcing sha256 over guessed
// names, because the name is a hash and the object carried nothing else. That is
// what makes reclaim undecidable in practice — not the absence of a policy, the
// absence of the fact a policy would read.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// fakeRuntime is a runtime whose apiserver is in memory. Only the disk paths are
// exercised, so nothing here needs a pod, a stream, or a cluster.
func fakeRuntime(objs ...k8sruntime.Object) *runtime {
	return &runtime{
		ns: "hanzo-sandboxes",
		dyn: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			k8sruntime.NewScheme(),
			map[schema.GroupVersionResource]string{
				k8s.Volumes: "PersistentVolumeClaimList",
				k8s.Pods:    "PodList",
			}, objs...),
		bound: Bound{Namespace: "hanzo-sandboxes", Selector: labSandbox},
	}
}

// disks is every PVC the fake cluster holds, by name.
func disks(t *testing.T, r *runtime) map[string]*unstructured.Unstructured {
	t.Helper()
	list, err := r.dyn.Resource(k8s.Volumes).Namespace(r.ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list disks: %v", err)
	}
	out := map[string]*unstructured.Unstructured{}
	for i := range list.Items {
		out[list.Items[i].GetName()] = &list.Items[i]
	}
	return out
}

// A LEASE ENDING MUST NEVER COST A TENANT THEIR DISK, and a second lease for the
// same project must find the FIRST disk rather than mint another.
//
// Both directions are asserted together because they are the same property seen
// from either side: the name is a pure function of (org, project), so reuse and
// survival are the same fact. If this ever fails as "two disks", the service is
// leaking 20Gi per lease; if it fails as "no disk", it has just destroyed a
// checkout. Neither is visible in a request — both are only visible here.
func TestProjectDiskOutlivesItsLeaseAndIsReusedNotRemade(t *testing.T) {
	r := fakeRuntime()
	ctx := context.Background()
	const org, project = "acme", "checkout"

	lease := func(id string) Sandbox {
		m := Sandbox{ID: id, Org: org, Project: project, Class: "dev", Pod: podName(id)}
		m.Volume = volumeName(org, project)
		if err := r.ensureVolume(ctx, m); err != nil {
			t.Fatalf("ensureVolume(%s): %v", id, err)
		}
		return m
	}

	first := lease("m_1")
	if got := disks(t, r); len(got) != 1 || got[first.Volume] == nil {
		t.Fatalf("first lease made %d disk(s) %v, want exactly %q", len(got), keys(got), first.Volume)
	}

	// The lease ends the way every lease ends — the reaper's `end`, which stops the
	// pod and keeps the disk. `stop` is what it calls; purge is a different verb the
	// caller has to ask for.
	if err := r.stop(ctx, first); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := disks(t, r); got[first.Volume] == nil {
		t.Fatalf("ending a lease destroyed the project disk %q — that is a tenant's uncommitted work", first.Volume)
	}

	// A SECOND lease, a new sandbox id, the same project. One disk, still.
	second := lease("m_2")
	if second.Volume != first.Volume {
		t.Fatalf("second lease addressed %q, want the same disk %q", second.Volume, first.Volume)
	}
	if got := disks(t, r); len(got) != 1 {
		t.Fatalf("two leases on one project left %d disks %v, want 1 — this is the 20Gi-per-lease leak",
			len(got), keys(got))
	}

	// And purge, which is the ONLY thing that may take it, still does.
	if err := r.purge(ctx, second); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if got := disks(t, r); len(got) != 0 {
		t.Fatalf("purge left %v", keys(got))
	}
}

// A DISK SAYS WHAT IT IS FOR AND WHEN IT WAS LAST WANTED.
//
// Without this a disk is an opaque 20Gi charge whose owner is a hash: the org
// label narrows it to a tenant and nothing narrows it further, so the only
// honest answer to "can this be deleted" is "no". Reclaim is not blocked by the
// absence of a policy — it is blocked by the absence of the two facts any policy
// would have to read. They are written where the disk is already being ensured,
// so a disk cannot come into existence unlabelled.
func TestProjectDiskSaysWhoseItIsAndWhenItWasLastLeased(t *testing.T) {
	r := fakeRuntime()
	ctx := context.Background()
	const org, project = "acme", "Deep Research"

	m := Sandbox{ID: "m_1", Org: org, Project: slug(project), Class: "dev"}
	m.Volume = volumeName(org, slug(project))
	if err := r.ensureVolume(ctx, m); err != nil {
		t.Fatalf("ensureVolume: %v", err)
	}

	d := disks(t, r)[m.Volume]
	if d == nil {
		t.Fatalf("no disk %q", m.Volume)
	}
	if got := d.GetLabels()[labOrg]; got != "acme" {
		t.Fatalf("%s = %q, want %q", labOrg, got, "acme")
	}
	// The project, PLAINLY. The name carries it only as a sha256 tail, so an
	// operator holding a namespace of disks cannot get back to a project without
	// guessing the string that made it.
	if got := d.GetLabels()[labProject]; got != "deep-research" {
		t.Fatalf("%s = %q, want %q — the project is otherwise recoverable only by brute force",
			labProject, got, "deep-research")
	}
	today := time.Now().UTC().Format(time.DateOnly)
	if got := d.GetAnnotations()[annLeased]; got != today {
		t.Fatalf("%s = %q, want %q", annLeased, got, today)
	}

	// A LATER LEASE REFRESHES IT. A stamp written once at creation dates the disk
	// and not its use, which reads a project worked on daily for a year as a year
	// old — exactly backwards, and it is the reading a reclaim would act on.
	stale := d.DeepCopy()
	stale.SetAnnotations(map[string]string{annLeased: "2026-01-01"})
	if _, err := r.dyn.Resource(k8s.Volumes).Namespace(r.ns).
		Update(ctx, stale, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("age the disk: %v", err)
	}
	if err := r.ensureVolume(ctx, Sandbox{ID: "m_2", Org: org, Project: slug(project), Volume: m.Volume}); err != nil {
		t.Fatalf("second ensureVolume: %v", err)
	}
	if got := disks(t, r)[m.Volume].GetAnnotations()[annLeased]; got != today {
		t.Fatalf("%s = %q after a second lease, want %q — a disk in daily use must not look abandoned",
			annLeased, got, today)
	}
}

func keys(m map[string]*unstructured.Unstructured) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ", ")
}
