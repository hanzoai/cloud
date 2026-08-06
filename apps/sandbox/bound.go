package sandbox

// bound.go — what a sweep is allowed to touch, decided ONCE and by construction.
//
// An agent's sandbox cleanup deleted every sandbox on a node, including
// kube-system DaemonSet pods. They self-healed. That is luck, not a property, and
// the reason it was possible is that "which pods are sandboxes" was a selector
// somebody wrote at the call site — so the answer was only ever as good as the
// person writing the flag, and a cleanup is written exactly when the writer is in a
// hurry.
//
// So the answer stops being a flag. There is ONE selector, it is built here, and it
// carries both halves of the bound:
//
//	NAMESPACE  the sandbox namespace, which may never be a system namespace
//	LABEL      hanzo.ai/sandbox, which only this package's pods ever carry
//
// Either half alone is not enough and that is the whole design. A label with no
// namespace reaches every namespace in the cluster, which is how a sweep meets
// kube-system. A namespace with no label reaches whatever else was scheduled
// there — the CNI's DaemonSet pod lands in your namespace too.
//
// And a delete is not a selector at all. Every pod this package removes is removed
// BY NAME, after reading it back and checking that it carries this package's label
// and is the same object version that was read. A name plus a precondition cannot
// widen; a selector always can.

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// system names the namespaces nothing here may ever address. The `kube-` prefix
// covers the ones Kubernetes reserves by convention and any a distribution adds
// later; the rest are named because they are not prefixed and are just as fatal.
//
// `hanzo` is on this list for a reason of our own: it holds the datastores, and a
// sandbox namespace is by definition the namespace whose policy DENIES the cluster.
// If those were ever the same namespace, the isolation would be gone and the sweep
// would be the second-worst consequence.
var system = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"default": true, "hanzo": true, "": true,
}

func protected(ns string) bool {
	ns = strings.TrimSpace(ns)
	return system[ns] || strings.HasPrefix(ns, "kube-")
}

// Bound is the ONE selector anything sweeping sandboxes may use, and the check that
// it is safe, in one value that cannot be had without the other.
//
// It is a type and not a pair of strings so that a caller cannot end up holding
// half of it. `hanzo.ai/sandbox` with no namespace is the cluster-wide selector that
// caused the incident; there is no way to spell that here, because the only
// constructor refuses a system namespace and the only field that names a label is
// written by this file.
type Bound struct {
	Namespace string
	Selector  string
}

// bindTo builds the bound for a namespace, or says why that namespace may not be
// swept. The error is returned rather than logged, so a caller that ignores it has
// to ignore it in writing.
func bindTo(ns string) (Bound, error) {
	ns = strings.TrimSpace(ns)
	if protected(ns) {
		return Bound{}, fmt.Errorf("sandbox: refusing to operate in %q — sandboxes live in "+
			"their own namespace, and a sweep that can reach a system namespace is a sweep that "+
			"deletes DaemonSets", ns)
	}
	// The label EXISTENCE form, not an equality on one id: this selects every pod
	// this package created and nothing else, which is exactly the set an orphan
	// sweep is about. Narrowing it to one id would make the sweep a lookup and
	// widening it past the label is what this file exists to prevent.
	return Bound{Namespace: ns, Selector: labSandbox}, nil
}

// list is the ONE way to enumerate sandbox pods. It takes no selector argument,
// because a selector a caller can pass is a selector a caller can widen.
func (b Bound) list() metav1.ListOptions { return metav1.ListOptions{LabelSelector: b.Selector} }

// covers reports whether an object is one this bound may delete: right namespace,
// and carrying this package's label. It is applied to the object READ BACK from the
// apiserver, never to the row that named it, because the row is our belief and the
// object is the fact.
func (b Bound) covers(obj *unstructured.Unstructured) bool {
	if obj == nil || obj.GetNamespace() != b.Namespace {
		return false
	}
	_, ok := obj.GetLabels()[labSandbox]
	return ok
}

// precondition pins a delete to the exact object that was inspected. Without it the
// check above is a TOCTOU: read a sandbox pod, have it deleted and its name reused
// by something else, delete the something else. The UID makes that unspellable —
// the apiserver refuses the delete rather than removing a different object wearing
// the same name.
func precondition(obj *unstructured.Unstructured) metav1.DeleteOptions {
	uid := obj.GetUID()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
}
