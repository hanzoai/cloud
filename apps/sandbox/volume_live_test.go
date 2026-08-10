package sandbox

// THE DATA-LOSS PROOF, both ways, on a real cluster.
//
// kata-fc has no shared filesystem, and the way that fails is the reason this
// file exists: the write SUCCEEDS. Kubernetes reports a Bound PVC, the mount
// appears at the right path, `cat` returns what was just written — and the
// bytes are in a tmpfs inside the VM, which is gone the moment the lease ends.
// Nothing anywhere returns an error. A unit test cannot see that, because the
// lie is told by the kernel and not by the code.
//
// So the assertion is the only one that catches it: write a file, END THE
// LEASE, lease the same project again, and read it back. A tmpfs cannot survive
// that. Run it under each runtime — the runtime is one env var, so the same
// test is the proof and the negative control:
//
//	# the shared-filesystem way: the file MUST come back
//	SANDBOX_LIVE=1 \   # fleet runtime = gvisor
//	  go test ./apps/sandbox/ -run TestLiveLease -v
//
//	# the fast way: exec gets Firecracker, and no volume is anywhere near it
//	SANDBOX_LIVE=1 \   # fleet runtime = kata-fc
//	  go test ./apps/sandbox/ -run TestLiveLease -v
//
// It drives the PLANE routes — lease_sandbox, run_in_sandbox, write, read,
// end_sandbox — because those are the addresses an agent actually calls, and a
// proof that skips them proves the runtime rather than the product.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// nodes is read for ONE field — the node's kernel version, so the guest kernel
// below is compared against the machine actually underneath the pod. It lives
// here rather than in apps/k8s because nothing in production reads a node, and a
// test is a poor reason to widen the surface the service can reach.
var nodes = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

// post drives one plane route and decodes its answer, timing the round trip.
// The duration is as much the point as the body: these are the numbers the lease
// path actually costs, measured where a caller pays them rather than on a
// synthetic pod.
func post[T any](t *testing.T, app *zip.App, path, org string, in any) (T, time.Duration) {
	t.Helper()
	var out T
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	start := time.Now()
	code, b := req(t, app, http.MethodPost, path, org, string(body))
	d := time.Since(start)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST %s: %d %s", path, code, b)
	}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return out, d
}

// release ends a lease on the way out and does not care whether it was already
// ended. The test ends one lease DELIBERATELY — that is the whole experiment —
// so a deferred cleanup that insisted on 200 would fail every successful run.
func release(app *zip.App, org, id string, purge bool) {
	body, _ := json.Marshal(plane.EndIn{ID: id, Purge: purge})
	r := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/end", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", "u-"+org)
	if resp, err := app.Test(r, zip.TestConfig{Timeout: 120 * time.Second}); err == nil {
		_ = resp.Body.Close()
	}
}

func TestLiveLeaseKeepsWhatItPromisesToKeep(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	app := mountHTTP(t)
	rt := newRuntime()
	if err := rt.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	const org = "hanzo"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	project := fmt.Sprintf("fcproof%d", time.Now().Unix())
	want := fmt.Sprintf("survived-%d", time.Now().UnixNano())
	t.Logf("fleet runtime = %q (settings: sandbox.runtime)", rt.preference(ctx))

	// ---- THE VOLUME-BEARING WAY --------------------------------------------
	// A project gives the sandbox a volume, and the volume is what decides the
	// runtime. This half must survive the lease.
	first, dLease := post[plane.Leased](t, app, "/v1/sandboxes/lease", org,
		plane.LeaseIn{Class: "dev", Project: project, TTLSec: 900})
	if first.ID == "" {
		t.Fatal("lease returned no id")
	}
	t.Logf("LEASE dev/%s -> %s in %v", project, first.ID, dLease.Round(time.Millisecond))
	// purge: this project exists only for the test, so its disk goes too.
	defer release(app, org, first.ID, true)

	// The runtime it LANDED on, read off the pod rather than inferred from the
	// env — the pod is what the kubelet obeyed.
	got := runtimeOfPod(t, ctx, rt, first.ID)
	if !runtimes[got].shares {
		t.Fatalf("a sandbox mounting a volume landed on %q, which has no shared filesystem — "+
			"this is the silent-tmpfs case the derivation exists to prevent", got)
	}
	t.Logf("  runtimeClassName=%q  (shares a filesystem: yes)", got)

	// The PVC has to be real and Bound, or "it survived" would only mean the
	// pod never restarted.
	if phase := volumePhase(t, ctx, rt, volumeName(org, project)); phase != "Bound" {
		t.Fatalf("project volume %s is %q, want Bound", volumeName(org, project), phase)
	}

	_, dWrite := post[plane.Wrote](t, app, "/v1/sandboxes/write", org,
		plane.WriteIn{ID: first.ID, Path: "keep.txt", Data: []byte(want)})
	// Written THROUGH the sandbox's own filesystem, so the check below is not
	// reading back the same buffer it just sent.
	ran, dRun := post[plane.Ran](t, app, "/v1/sandboxes/run", org,
		plane.RunIn{ID: first.ID, Command: "sync; cat keep.txt; df -T " + first.Workdir + " | tail -1"})
	t.Logf("  write %v · run %v", dWrite.Round(time.Millisecond), dRun.Round(time.Millisecond))
	t.Logf("  in-sandbox view: %s", oneLine(ran.Stdout))

	// THE MOMENT THAT MATTERS. End the lease: the pod goes, the volume stays.
	_, dEnd := post[struct{}](t, app, "/v1/sandboxes/end", org, plane.EndIn{ID: first.ID})
	t.Logf("END %s in %v", first.ID, dEnd.Round(time.Millisecond))

	// Same project, new lease. If the write went into a tmpfs, this read fails.
	second, dRelease := post[plane.Leased](t, app, "/v1/sandboxes/lease", org,
		plane.LeaseIn{Class: "dev", Project: project, TTLSec: 900})
	t.Logf("RE-LEASE %s -> %s in %v", project, second.ID, dRelease.Round(time.Millisecond))
	defer release(app, org, second.ID, true)
	if second.ID == first.ID {
		t.Fatalf("re-lease returned the SAME sandbox %s — the lease never ended, so nothing was proven", second.ID)
	}
	blob, dRead := post[plane.Blob](t, app, "/v1/sandboxes/read", org,
		plane.PathIn{ID: second.ID, Path: "keep.txt"})
	if string(blob.Data) != want {
		t.Fatalf("DATA LOSS: wrote %q before the lease ended, read %q after it — "+
			"the volume did not survive, which means the sandbox ran on a runtime "+
			"that cannot share a filesystem", want, blob.Data)
	}
	t.Logf("  read %v — SURVIVED: %q", dRead.Round(time.Millisecond), blob.Data)

	// ---- THE VOLUMELESS WAY ------------------------------------------------
	// No project, so no volume, so nothing to lose — and therefore free to take
	// the fast runtime.
	ex, dExLease := post[plane.Leased](t, app, "/v1/sandboxes/lease", org,
		plane.LeaseIn{Class: "exec", TTLSec: 600})
	t.Logf("LEASE exec (no project) -> %s in %v", ex.ID, dExLease.Round(time.Millisecond))
	defer release(app, org, ex.ID, false)

	// THERE MUST BE NO PVC AT ALL. Not an empty one, not an unbound one — none,
	// so there is nothing for a runtime without a shared filesystem to drop.
	if phase := volumePhase(t, ctx, rt, volumeName(org, "")); phase != "" {
		t.Fatalf("a volumeless sandbox has a PVC (%q) — it should have none", phase)
	}
	exRC := runtimeOfPod(t, ctx, rt, ex.ID)
	t.Logf("  runtimeClassName=%q", exRC)
	if fleet := rt.preference(ctx); exRC != fleet {
		t.Fatalf("volumeless exec landed on %q, but the fleet states %q — "+
			"a sandbox with nothing to lose should take the fleet's runtime unchanged",
			exRC, fleet)
	}

	// THE KERNEL IS THE CONTROL. A runtimeClassName is a label; a different
	// kernel version from the node's is the VM actually existing. Compared
	// against the node this pod is on, read from the node object.
	kern, dKern := post[plane.Ran](t, app, "/v1/sandboxes/run", org,
		plane.RunIn{ID: ex.ID, Command: "uname -r"})
	guest, host := oneLine(kern.Stdout), hostKernel(t, ctx, rt, ex.ID)
	t.Logf("  guest kernel %s vs host %s (run %v)", guest, host, dKern.Round(time.Millisecond))
	// The table's `kernel` column, checked against the only thing that can
	// settle it. A boundary that claims a kernel of its own and reports the
	// node's has not got one — the runtimeClassName was accepted and nothing
	// behind it was installed. And a boundary that claims none must report the
	// node's, or the table is describing a runtime we are not running.
	if b, ok := runtimes[exRC]; ok {
		if b.kernel && guest == host {
			t.Fatalf("runtimeClassName=%q says it has a kernel of its own, and the guest kernel %s "+
				"equals the host's — the pod did not get one, so the label is not the boundary it ran on",
				exRC, guest)
		}
		if !b.kernel && guest != host {
			t.Fatalf("runtimeClassName=%q says it IS the node's kernel, and the guest reports %s "+
				"against the host's %s", exRC, guest, host)
		}
	}
}

// runtimeOfPod reads runtimeClassName off the running pod. The env said what we
// asked for; this says what the kubelet did.
func runtimeOfPod(t *testing.T, ctx context.Context, r *runtime, id string) string {
	t.Helper()
	u, err := r.pods().Get(ctx, podName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod for %s: %v", id, err)
	}
	rc, _, _ := unstructured.NestedString(u.Object, "spec", "runtimeClassName")
	return rc
}

// hostKernel reads the kernel of the NODE the sandbox landed on, so the guest
// comparison is against the machine underneath it and not a fleet average.
func hostKernel(t *testing.T, ctx context.Context, r *runtime, id string) string {
	t.Helper()
	u, err := r.pods().Get(ctx, podName(id), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	node, _, _ := unstructured.NestedString(u.Object, "spec", "nodeName")
	n, err := r.dyn.Resource(nodes).Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node %s: %v", node, err)
	}
	k, _, _ := unstructured.NestedString(n.Object, "status", "nodeInfo", "kernelVersion")
	return k
}

// volumePhase answers "" when the claim does not exist, which is the assertion a
// volumeless sandbox needs — absence, not an empty value.
func volumePhase(t *testing.T, ctx context.Context, r *runtime, name string) string {
	t.Helper()
	u, err := r.dyn.Resource(k8s.Volumes).Namespace(r.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("get pvc %s: %v", name, err)
	}
	p, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	return p
}

// THE COST OF THE LEASE PATH, per runtime, measured where a caller pays it.
//
// The microbenchmarks that made Firecracker look free measured the wrong thing.
// They timed filesystem work INSIDE an already-running sandbox — git status,
// copy a tree — and on that axis a guest kernel owning a block device beats
// proxying every lstat to the host by an order of magnitude. All true, and all
// irrelevant to a caller who pays for the pod to exist first.
//
// So this measures both halves separately, because they point in opposite
// directions: how long a lease takes to answer, and how fast the sandbox is once
// it does. Run it under each runtime and read the two columns together.
//
//	SANDBOX_LIVE=1 \   # fleet runtime = kata-fc
//	  go test ./apps/sandbox/ -run TestLiveLeaseCost -v -timeout 20m
func TestLiveLeaseCost(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	app := mountHTTP(t)
	rt := newRuntime()
	if err := rt.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	const org, rounds = "hanzo", 3

	// The workload is timed INSIDE the sandbox, so the number is the
	// filesystem's and not the exec channel's. git status over a few hundred
	// files is the shape of work a coding sandbox actually does.
	const work = `cd /mnt/data && rm -rf b && mkdir b && cd b && ` +
		`i=0; while [ $i -lt 300 ]; do echo x > f$i; i=$((i+1)); done && ` +
		`git init -q . && git add -A && ` +
		`s=$(date +%s%N) && git status --porcelain >/dev/null && e=$(date +%s%N) && ` +
		`echo "git_status_ms=$(( (e-s)/1000000 ))"`

	t.Logf("runtime=%q  rounds=%d", rt.preference(context.Background()), rounds)
	for i := range rounds {
		m, dLease := post[plane.Leased](t, app, "/v1/sandboxes/lease", org,
			plane.LeaseIn{Class: "exec", TTLSec: 600})
		ran, dRun := post[plane.Ran](t, app, "/v1/sandboxes/run", org,
			plane.RunIn{ID: m.ID, Command: work, TimeoutSec: 300})
		_, dEnd := post[struct{}](t, app, "/v1/sandboxes/end", org, plane.EndIn{ID: m.ID})
		t.Logf("  round %d on %s: lease %v · run %v · end %v · %s",
			i, runtimeOfPodOrGone(ctx0(), rt, m.ID),
			dLease.Round(time.Millisecond), dRun.Round(time.Millisecond),
			dEnd.Round(time.Millisecond), oneLine(ran.Stdout))
	}
}

func ctx0() context.Context { return context.Background() }

// runtimeOfPodOrGone reports the runtime the pod ran on, or why it cannot say.
// The pod is deleted by the time the round is logged, so this is best effort —
// the per-round runtime is confirmed by the test above, not by this line.
func runtimeOfPodOrGone(ctx context.Context, r *runtime, id string) string {
	u, err := r.pods().Get(ctx, podName(id), metav1.GetOptions{})
	if err != nil {
		return r.preference(ctx) + "(gone)"
	}
	rc, _, _ := unstructured.NestedString(u.Object, "spec", "runtimeClassName")
	return rc
}
