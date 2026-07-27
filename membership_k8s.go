package cloud

// membership_k8s.go is the LIVE writer-membership source: the discovery mechanism that
// plugs into internal/org's Source seam (membership.go names "a K8s Endpoints poll
// (prod)" as exactly this) so the horizontally-scaled cloud tracks its CHANGING pod set
// instead of a static list. It is the fix for the rolling-upgrade outage: with a static
// peer set, ha.Owner keeps electing a pod that is draining or already gone, and the
// shard router forwards an org's requests to a dead pod; a live, READY-gated set drops
// that pod the moment it starts terminating, so ha.Owner re-elects a live successor and
// the org stays served.
//
// Capability-detected, NO on/off flag (the CTO one-way rule): in-cluster AND a selector
// configured → live K8s membership; otherwise (dev, native-Go, no selector) → the
// STATIC ShardPeers/self set. Native-Go always works: no cluster, no problem.
//
// It is a bounded LIST poll driven by internal/org.Membership's existing refresh loop
// (which already retains the last-good set on a transient error and serves the hot-path
// AmOwner check lock-free from an atomic snapshot). Each poll re-lists, so a dropped
// connection self-heals on the next tick — no watch state to wedge. The ready-gating
// predicate (podWriterEligible) is the ONE copy the future github.com/hanzoai/ha/k8s
// shared source folds together with visor/object/coordinator.go's identical twin.

import (
	"context"
	"os"
	"strings"

	"github.com/hanzoai/cloud/internal/org"
	luxlog "github.com/luxfi/log"
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
func membershipSource(staticPeers []org.Member, selector, port string, log luxlog.Logger) org.Source {
	static := org.StaticSource(staticPeers...)
	if strings.TrimSpace(selector) == "" {
		return static // dev / native-Go / no selector: the static set is the whole writer set.
	}
	client, ns, err := inClusterPods()
	if err != nil {
		// Selector configured but not in a cluster (or the API is unreachable at boot):
		// fall back to static rather than fail — the deployment still serves, just
		// without live membership tracking. Logged so an in-cluster misconfig is visible.
		if log != nil {
			log.Warn("live K8s membership unavailable — using static peer set", "selector", selector, "err", err)
		}
		return static
	}
	if log != nil {
		log.Info("live K8s membership enabled", "namespace", ns, "selector", selector)
	}
	return func(ctx context.Context) ([]org.Member, error) {
		list, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			// Fail closed: org.Membership keeps the last-good set, never flapping to
			// empty (an empty set would strand every org with no owner).
			return nil, err
		}
		return readyMembers(list.Items, port), nil
	}
}

// inClusterPods builds a typed clientset from the in-cluster service-account, mirroring
// the access pattern visor/object/coordinator.go already uses. A non-nil error means
// "not in a cluster" (dev / native-Go), and the caller falls back to static membership.
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

// httpPortOf extracts the port from a listen address (":8080" → "8080", "0.0.0.0:8080" →
// "8080") for stamping onto a live peer's Addr. A value with no colon is returned as-is
// (already a bare port); empty stays empty.
func httpPortOf(listenAddr string) string {
	listenAddr = strings.TrimSpace(listenAddr)
	if i := strings.LastIndex(listenAddr, ":"); i >= 0 {
		return listenAddr[i+1:]
	}
	return listenAddr
}
