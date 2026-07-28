// Package membership is the LIVE writer-membership source: the K8s Endpoints
// poll that plugs into internal/org's Source seam so the horizontally-scaled
// cloud tracks its CHANGING pod set instead of a static list.
//
// It lives OUT of package cloud on purpose. Every subsystem imports cloud for
// Deps, so cloud's import graph is the floor under all of them; the k8s client
// pulls 263 packages that only this one file needed. cloud declares the seam
// (cloud.SetLiveSource) and apps/ installs K8s into it, so a subsystem — and
// cloud's own tests — compile without the Kubernetes client present at all.
//
// It is the fix for the rolling-upgrade outage: with a static peer set, ha.Owner
// keeps electing a pod that is draining or already gone, and the shard router
// forwards an org's requests to a dead pod; a live, READY-gated set drops that pod
// the moment it starts terminating, so ha.Owner re-elects a live successor and the
// org stays served.
//
// It is a bounded LIST poll driven by internal/org.Membership's existing refresh
// loop (which retains the last-good set on a transient error and serves the
// hot-path AmOwner check lock-free from an atomic snapshot). Each poll re-lists,
// so a dropped connection self-heals on the next tick — no watch state to wedge.
package membership


import (
	"context"
	"os"
	"strings"

	"github.com/hanzoai/cloud/internal/org"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// membershipSource returns the writer-membership Source for the durability fencer AND
// the shard router. self and staticPeers are the fallback (self is always a member of
// the static set); selector is CLOUD_PEER_SELECTOR; port is the http port stamped onto
// each live peer's Addr so the shard router can dial it. When the process is in a
// cluster and selector != "", it yields the live Ready pod set; otherwise it yields the
// static set, so the exact same wiring runs everywhere.

// K8s returns a live membership Source for the given label selector, plus the
// namespace it is watching. A non-nil error means "not in a cluster" (dev,
// native-Go) and the caller falls back to its static peer set.
func K8s(selector, port string) (org.Source, string, error) {
	client, ns, err := inClusterPods()
	if err != nil {
		return nil, "", err
	}
	return func(ctx context.Context) ([]org.Member, error) {
		list, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			// Fail closed: org.Membership keeps the last-good set, never flapping to
			// empty (an empty set would strand every org with no owner).
			return nil, err
		}
		return readyMembers(list.Items, port), nil
	}, ns, nil
}

func inClusterPods() (kubernetes.Interface, string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	return client, podNamespace(), nil
}

// readyMembers converts a pod list to the writer-eligible member set: each Ready,
// non-terminating pod becomes a Member{ID: pod name (stable for the pod's life — the HRW
// weight), Addr: podIP:port (the shard router dials it directly)}. A pod with no IP yet
// (just scheduled) keeps an empty Addr; it is still HRW-eligible only once Ready, by
// which point the IP is set.
func readyMembers(pods []corev1.Pod, port string) []org.Member {
	out := make([]org.Member, 0, len(pods))
	for i := range pods {
		p := &pods[i]
		if !podWriterEligible(p) {
			continue
		}
		addr := p.Status.PodIP
		if addr != "" && port != "" {
			addr = addr + ":" + port
		}
		out = append(out, org.Member{ID: p.Name, Addr: addr})
	}
	return out
}

// podWriterEligible reports whether a pod is a live writer candidate: Running and Ready,
// and NOT terminating. The DeletionTimestamp gate is the fast drain signal — the instant
// K8s marks a pod for deletion (a rolling upgrade's first step), it leaves the writer set
// so ha.Owner re-elects a live successor BEFORE the pod stops serving; the Ready gate
// covers an unready-but-not-terminating pod (starting up, or a failing readiness probe).
// This is the ONE ready-gating predicate — identical to visor/object/coordinator.go's,
// the twin the shared github.com/hanzoai/ha/k8s source will fold together.
func podWriterEligible(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil {
		return false // terminating — drain it from the writer set immediately.
	}
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false // no Ready condition yet.
}

// podNamespace resolves this pod's namespace for the peer list: POD_NAMESPACE (Downward
// API) when set, else the in-cluster service-account namespace file. Empty only outside a
// cluster, where inClusterPods has already returned the static fallback.
func podNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return "default"
}
