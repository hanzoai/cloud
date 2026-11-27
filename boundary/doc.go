// Package boundary names the isolation boundaries this cloud runs a sandbox on.
//
// It is a LEAF: it imports nothing, so both ends of the contract can hold it
// without either dragging the other's graph. That is the whole reason it
// exists. The scheduler (apps/sandbox) knows two FACTS about each boundary —
// whether it has a kernel of its own and whether it can back a volume — and it
// reaches Kubernetes to act on them, 784 packages deep. The installer (the
// `hanzo` CLI) knows a different pair — which handler runs it and which file
// proves it is installed — and is a client binary that must not link an
// apiserver client to read four strings.
//
// What they SHARE is the closed set of names, and a name is what a
// RuntimeClass is addressed by, so the two must agree exactly: a machine that
// installs a handler the scheduler will not schedule has wasted a download, and
// a scheduler naming a class the machine cannot run is a pod that waits Pending
// with no explanation. Neither failure has a symptom where it happens, which is
// why the set is written once, here, rather than in the two places that would
// each be right on their own.
package boundary
