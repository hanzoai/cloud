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

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// identity is what the kernel and the kubelet say about a caller on the
// socket, and it is the only thing a secret read is scoped by.
//
// A caller inside a pod is that pod: its namespace decides the tenant and its
// service account names the program. A caller outside any pod is a process on
// this host, admitted only when it runs as this process's user or as root —
// the same rule a 0600 socket enforced when no other user could connect.
type identity struct {
	// Org is the tenant whose material the caller may read.
	Org string
	// Platform is true for a caller in the deployment's own namespace, which
	// may also read material that belongs to no tenant.
	Platform bool
	// Account is the pod's service account, or "" outside a pod.
	Account string
}

const tenantPrefix = "tenant-"

// attest resolves a peer to an identity. It watches the pods scheduled on this
// node and indexes them by UID, so a lookup is a map read on the request path.
type attest struct {
	mu    sync.RWMutex
	pods  map[string]corev1.Pod
	ready bool
	uid   int
}

// newAttest starts watching pods when this process runs in a cluster. Outside
// one there is nothing to watch, and every peer is a host process.
func newAttest(ctx context.Context) *attest {
	a := &attest{pods: map[string]corev1.Pod{}, uid: os.Getuid()}
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

// of answers who is calling, or false when the peer cannot be placed: no
// kernel credential at all, a pod this node has not learned yet, or a host
// process under another user.
func (a *attest) of(ctx context.Context) (identity, bool) {
	peer := zip.PeerOf(ctx)
	if peer == nil {
		return identity{}, false
	}
	uid := peer.PodUID()
	if uid == "" {
		if peer.UID == a.uid || peer.UID == 0 {
			return identity{Org: cloud.Brand(), Platform: true}, true
		}
		return identity{}, false
	}
	a.mu.RLock()
	p, ok := a.pods[uid]
	a.mu.RUnlock()
	if !ok {
		return identity{}, false
	}
	return fromPod(p.Namespace, p.Spec.ServiceAccountName), true
}

// fromPod derives the identity a pod carries from where it was scheduled. A
// tenant namespace is `tenant-<org>`; every other namespace is the deployment's
// own, whose tenant is the brand.
func fromPod(namespace, account string) identity {
	if org, ok := strings.CutPrefix(namespace, tenantPrefix); ok && org != "" {
		return identity{Org: org, Account: account}
	}
	return identity{Org: cloud.Brand(), Platform: true, Account: account}
}

// admit decides one read. The ref names a tenant or it names the deployment;
// the caller was attested or it was not; and the org the call SAYS it acts for
// — a header, so a claim — may only ever narrow what the attestation allows.
func admit(ref string, stated string, who identity, attested bool) error {
	if ref == "" {
		return zip.ErrBadRequest("kms: empty ref")
	}
	if !attested {
		return zip.ErrForbidden("kms: caller could not be attested")
	}
	if stated != "" && stated != who.Org {
		return zip.ErrForbidden("kms: the call acts for " + stated + " but the caller is " + who.Org)
	}
	org, tenant := tenantOf(ref)
	if !tenant {
		if !who.Platform {
			return zip.ErrForbidden("kms: ref " + ref + " is the deployment's; the caller is a tenant")
		}
		return nil
	}
	if org != who.Org {
		return zip.ErrForbidden("kms: ref " + ref + " belongs to " + org + " but the caller is " + who.Org)
	}
	return nil
}
