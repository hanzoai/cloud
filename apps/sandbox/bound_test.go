package sandbox

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// The incident this file is the gate for: a sandbox cleanup deleted every sandbox
// on a node, including kube-system DaemonSet pods. They self-healed. The next one
// might not, so what follows is not "we are careful" — it is that the widening
// cannot be spelled.

// TestSystemNamespacesAreRefused is the first half of the bound. A runtime that
// could ADDRESS a system namespace is a runtime one bad env var away from the
// incident, so the namespace is checked before a client is even built.
func TestSystemNamespacesAreRefused(t *testing.T) {
	for _, ns := range []string{
		"kube-system", "kube-public", "kube-node-lease", "default", "hanzo", "",
		"kube-anything-a-distro-adds-later", "  kube-system  ",
	} {
		if _, err := bindTo(ns); err == nil {
			t.Errorf("bindTo(%q) was allowed — a sweep that can reach %q is a sweep that "+
				"deletes DaemonSets", ns, strings.TrimSpace(ns))
		}
	}
	for _, ns := range []string{"hanzo-sandboxes", "lux-sandboxes", "sandboxes-staging"} {
		b, err := bindTo(ns)
		if err != nil {
			t.Fatalf("bindTo(%q) refused a legitimate sandbox namespace: %v", ns, err)
		}
		if b.Namespace != ns || b.Selector != labSandbox {
			t.Errorf("bindTo(%q) = %+v, want namespace %q and selector %q", ns, b, ns, labSandbox)
		}
	}
}

// TestNewRuntimeFailsClosedInASystemNamespace measures the refusal where it
// actually matters: through the constructor the process uses, so a deployment that
// sets SANDBOX_NAMESPACE=kube-system gets a subsystem that cannot start a pod, let
// alone delete one — not a warning in a log nobody reads.
func TestNewRuntimeFailsClosedInASystemNamespace(t *testing.T) {
	t.Setenv("SANDBOX_NAMESPACE", "kube-system")
	r := newRuntime()
	err := r.ready()
	if err == nil {
		t.Fatal("a runtime in kube-system reported ready; every call through it can delete " +
			"a DaemonSet pod by name")
	}
	if !strings.Contains(err.Error(), "kube-system") {
		t.Errorf("ready() = %v, want a refusal naming the namespace", err)
	}
	if r.dyn != nil {
		t.Error("a refused namespace still built a Kubernetes client — the refusal must " +
			"come before the capability, not after it")
	}
}

// TestTheSelectorIsNeverJustALabel is the second half. A label with no namespace is
// the cluster-wide selector that caused the incident; there must be no way to hold
// one, so the only constructor returns both halves together and the list options are
// built from the pair rather than from an argument.
func TestTheSelectorIsNeverJustALabel(t *testing.T) {
	b, err := bindTo("hanzo-sandboxes")
	if err != nil {
		t.Fatalf("bindTo: %v", err)
	}
	if got := b.list().LabelSelector; got != labSandbox {
		t.Errorf("list selector = %q, want %q", got, labSandbox)
	}
	if b.Namespace == "" {
		t.Error("a bound with an empty namespace is the cluster-wide selector by another name")
	}
}

func podObj(ns, name string, labels map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns, "uid": "uid-" + name}
	if labels != nil {
		meta["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": meta}}
}

// TestCoversRefusesEverythingThatIsNotASandbox is the check applied to the object
// READ BACK from the apiserver, and it is the one that would have stopped the
// incident: a kube-system DaemonSet pod fails it on the namespace, and a neighbour
// scheduled into the sandbox namespace fails it on the label.
func TestCoversRefusesEverythingThatIsNotASandbox(t *testing.T) {
	b, err := bindTo("hanzo-sandboxes")
	if err != nil {
		t.Fatalf("bindTo: %v", err)
	}
	refused := []struct {
		why string
		obj *unstructured.Unstructured
	}{
		{"a kube-system DaemonSet pod — THE incident",
			podObj("kube-system", "cilium-abcde", map[string]any{"k8s-app": "cilium"})},
		{"a kube-system pod that happens to carry our label",
			podObj("kube-system", "cilium-abcde", map[string]any{labSandbox: "m_deadbeef"})},
		{"a neighbour in the sandbox namespace with no sandbox label",
			podObj("hanzo-sandboxes", "csi-node-xyz", map[string]any{"app": "csi"})},
		{"a pod in another tenant namespace",
			podObj("hanzo", "sql-0", map[string]any{labSandbox: "m_deadbeef"})},
		{"nothing at all", nil},
	}
	for _, tc := range refused {
		if b.covers(tc.obj) {
			t.Errorf("covers() accepted %s — a delete would follow", tc.why)
		}
	}
	ours := podObj("hanzo-sandboxes", "m-deadbeef", map[string]any{labSandbox: "m_deadbeef"})
	if !b.covers(ours) {
		t.Error("covers() refused an actual sandbox pod; the bound is now a wall around nothing")
	}
}

// TestDeletePinsTheExactObject: the label check above is a TOCTOU on its own — read
// a sandbox pod, have it deleted and its name reused, delete the replacement. The
// UID precondition is what makes that unspellable, so it has to be on every delete.
func TestDeletePinsTheExactObject(t *testing.T) {
	obj := podObj("hanzo-sandboxes", "m-deadbeef", map[string]any{labSandbox: "m_deadbeef"})
	opts := precondition(obj)
	if opts.Preconditions == nil || opts.Preconditions.UID == nil {
		t.Fatal("delete carries no UID precondition; a name is a claim, not an identity")
	}
	if got := *opts.Preconditions.UID; got != types.UID("uid-m-deadbeef") {
		t.Errorf("precondition UID = %q, want the UID of the object that was inspected", got)
	}
	var bare metav1.DeleteOptions
	if opts.Preconditions == bare.Preconditions {
		t.Error("precondition() produced a bare DeleteOptions")
	}
}

// TestExecSandboxesHaveACeiling. The single-attach rule bounds `dev` and `desktop`
// because they carry a project; an `exec` sandbox carries none, so nothing bounded
// how many an org could hold. The code tool sends no session_id, so every call mints
// a fresh pod on a 15-minute lease — a loop of 40 calls took 40 pods, and each is a
// real 250m/512Mi/2Gi reservation on a node.
//
// The reaper cannot fix this: it ends leases that are over, so it bounds the steady
// state and not the burst, and the burst is what fills a node.
func TestExecSandboxesHaveACeiling(t *testing.T) {
	st := memStore(t)
	ctx := context.Background()
	for i := range maxLiveExec {
		if err := st.Put(ctx, Sandbox{
			ID: fmt.Sprintf("m_%02d", i), Org: "acme", Class: "exec", Status: "running",
			CreatedAt: 1, LastUsedAt: 1,
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	n, err := st.LiveOfClass(ctx, "acme", "exec")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != maxLiveExec {
		t.Fatalf("counted %d live exec sandboxes, want %d — a cap read through a LIMITed "+
			"query stops counting exactly where refusing starts to matter", n, maxLiveExec)
	}

	// The count is per (org, class): another org is unaffected, and this org's `dev`
	// sandboxes are not what the exec ceiling is about.
	if n, _ := st.LiveOfClass(ctx, "other", "exec"); n != 0 {
		t.Errorf("another org counted %d, want 0 — the ceiling is per tenant", n)
	}
	if n, _ := st.LiveOfClass(ctx, "acme", "dev"); n != 0 {
		t.Errorf("dev counted %d, want 0 — dev is bounded by single-attach, not by this", n)
	}

	// An ENDED sandbox stops counting, so the ceiling is a live-set bound and not a
	// lifetime quota: a tenant that finishes its work can always start more.
	if err := st.Delete(ctx, "acme", "m_00"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.LiveOfClass(ctx, "acme", "exec"); n != maxLiveExec-1 {
		t.Errorf("after ending one, counted %d, want %d", n, maxLiveExec-1)
	}
}

// memStore opens a real per-test store — the same openStore the service uses, so the
// count above is measured against the actual schema and not a stand-in.
func memStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
