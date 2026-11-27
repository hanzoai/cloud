// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"context"
	"os"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// identity is what the kernel and the kubelet say about a caller on the
// socket, and it is the only thing a secret read is scoped by.
//
// There are two standings and no third. The CUSTODIAN is this process and the
// plugin processes it hosts: a peer the kernel can name in this process's own
// PID namespace, running as this process's user or as root — the rule a 0600
// socket enforced when no other user could connect — or one whose cgroup names
// this very pod. A TENANT is a pod in `tenant-<org>`, which reads its own org's
// material and nothing else. Everything else has no standing here: another pod,
// including one in the deployment's own namespace, and any process the kernel
// cannot name at all.
//
// WHY THE PID NAMESPACE. Inside a container the cgroup lookup names no pod — a
// private cgroup namespace reads "0::/" for every process, this one included —
// so the pod UID is not what separates this pod from the node. The PID is:
// SO_PEERCRED translates the peer into the reader's PID namespace and reports 0
// for a process outside it, which is every other pod and every host process.
// Plugins are children of this process and always visible.
type identity struct {
	// Org is the tenant whose material the caller may read; for the custodian,
	// the org it acts for when a call states none.
	Org string
	// Platform is true for the custodian, which may act for any tenant it
	// states and may read material that belongs to no tenant.
	Platform bool
	// Account is the pod's service account, or "" outside a pod.
	Account string
}

// custodian is the identity of this process and the plugin processes it hosts.
func custodian() identity { return identity{Org: cloud.Brand(), Platform: true} }

const tenantPrefix = "tenant-"

// attest resolves a peer to an identity. It watches the pods scheduled on this
// node and indexes them by UID, so a lookup is a map read on the request path.
type attest struct {
	mu    sync.RWMutex
	pods  map[string]corev1.Pod
	ready bool
	uid   int
	// self is this process's own pod UID, read from its cgroup exactly the way
	// zip reads a peer's, or "" outside a pod.
	self string
}

// newAttest starts watching pods when this process runs in a cluster. Outside
// one there is nothing to watch, and a peer is placed by its user alone.
func newAttest(ctx context.Context) *attest {
	a := &attest{pods: map[string]corev1.Pod{}, uid: os.Getuid(), self: (&zip.Peer{PID: os.Getpid()}).PodUID()}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return a
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return a
	}
	var opts []informers.SharedInformerOption
	if node := os.Getenv("NODE_NAME"); node != "" {
		opts = append(opts, informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", node).String()
		}))
	}
	f := informers.NewSharedInformerFactoryWithOptions(cs, 0, opts...)
	inf := f.Core().V1().Pods().Informer()
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { a.put(o) },
		UpdateFunc: func(_, o any) { a.put(o) },
		DeleteFunc: func(o any) { a.drop(o) },
	})
	f.Start(ctx.Done())
	go func() {
		cache.WaitForCacheSync(ctx.Done(), inf.HasSynced)
		a.mu.Lock()
		a.ready = true
		a.mu.Unlock()
	}()
	return a
}

func (a *attest) put(o any) {
	p, ok := o.(*corev1.Pod)
	if !ok {
		return
	}
	a.mu.Lock()
	a.pods[string(p.UID)] = *p
	a.mu.Unlock()
}

func (a *attest) drop(o any) {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	p, ok := o.(*corev1.Pod)
	if !ok {
		return
	}
	a.mu.Lock()
	delete(a.pods, string(p.UID))
	a.mu.Unlock()
}

// unattested refuses a peer that cannot be placed at all.
func unattested() error { return zip.ErrForbidden("kms: caller could not be attested") }

// of answers who is calling: the custodian, a tenant, or a refusal — no kernel
// credential at all, a process outside this PID namespace, a pod this node has
// not learned yet, a process under another user, or a pod with no standing on
// this plane.
func (a *attest) of(ctx context.Context) (identity, error) {
	peer := zip.PeerOf(ctx)
	if peer == nil {
		return identity{}, unattested()
	}
	return a.place(peer.UID, peer.PID, peer.PodUID())
}

// place is of's decision over the three facts the kernel supplies: the peer's
// user, its pid in this PID namespace (0 when it lives outside it), and the pod
// its cgroup names ("" when none is visible).
func (a *attest) place(uid, pid int, pod string) (identity, error) {
	if pod == "" {
		if pid > 0 && (uid == a.uid || uid == 0) {
			return custodian(), nil
		}
		return identity{}, unattested()
	}
	if pod == a.self {
		return custodian(), nil
	}
	a.mu.RLock()
	p, ok := a.pods[pod]
	a.mu.RUnlock()
	if !ok {
		return identity{}, unattested()
	}
	return fromPod(p.Namespace, p.Spec.ServiceAccountName)
}

// fromPod derives the standing a pod OTHER than this one carries from where it
// was scheduled. A tenant namespace is `tenant-<org>` and reads that org's
// material. Any other namespace has none: the custodian is identified by its
// pod, never by the namespace it happens to share with other workloads.
func fromPod(namespace, account string) (identity, error) {
	if org, ok := strings.CutPrefix(namespace, tenantPrefix); ok && org != "" {
		return identity{Org: org, Account: account}, nil
	}
	return identity{}, zip.ErrForbidden("kms: pod " + namespace + "/" + account + " has no standing on the internal plane")
}

// admit decides one call. The ref names a tenant or it names the deployment;
// the org the call SAYS it acts for — a header, so a claim — is honoured from
// the custodian, which acts for every tenant it hosts, and from a tenant may
// only ever narrow what the attestation allows. v is what the call does with the
// value: a read opens it, anything else writes, removes or signs with it.
func admit(ref string, stated string, who identity, v Verb) error {
	if ref == "" {
		return zip.ErrBadRequest("kms: empty ref")
	}
	act := who.Org
	if stated != "" {
		if !who.Platform && stated != who.Org {
			return zip.ErrForbidden("kms: the call acts for " + stated + " but the caller is " + who.Org)
		}
		act = stated
	}
	org, tenant := tenantOf(ref)
	if !tenant {
		if !who.Platform {
			return zip.ErrForbidden("kms: ref " + ref + " is the deployment's; the caller is a tenant")
		}
		return nil
	}
	if org != act {
		return zip.ErrForbidden("kms: ref " + ref + " belongs to " + org + " but the call acts for " + act)
	}
	// What the deployment seals on the org's behalf is its custodian's
	// (custody.go). A tenant pod is the org's own code, deployed by any member
	// who may deploy, so it reads only what the org's deployments are sealed for
	// and touches nothing else there.
	if keeper, held := custodyOfRef(ref); held && !who.Platform && !(keeper.sync && v == Read) {
		return zip.ErrForbidden("kms: ref " + ref + " is held by " + keeper.app + " on the org's behalf; the caller is a tenant")
	}
	return nil
}
