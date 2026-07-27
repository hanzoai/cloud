package infra

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/fleet"
)

// clusterScanTimeout bounds ONE cluster's read. A cluster that exceeds it is recorded
// as unreachable, which fails the completeness gate — the safe direction.
const clusterScanTimeout = 45 * time.Second

// kube opens an authenticated client for one DOKS cluster. DO hands back a
// token-based kubeconfig against the cluster's public https endpoint; it still goes
// through fleet.SafeRESTConfig, the ONE gate that rejects exec-credential plugins and
// non-routable apiserver hosts, so this path cannot be turned into an RCE or an SSRF.
func kube(ctx context.Context, do *digitalocean.Client, clusterID string) (*kubernetes.Clientset, error) {
	raw, err := do.Kubeconfig(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	cfg, err := fleet.SafeRESTConfig(raw)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = clusterScanTimeout
	return kubernetes.NewForConfig(cfg)
}

// Scan reads every cluster's Kubernetes state, bounded-parallel. It ALWAYS returns
// one row per cluster: a cluster that failed comes back with Err set rather than
// being omitted, because a missing row and a healthy row must never be confusable —
// that confusion is exactly what would condemn live data.
func Scan(ctx context.Context, do *digitalocean.Client, clusters []digitalocean.Cluster) []ClusterScan {
	out := make([]ClusterScan, len(clusters))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, c := range clusters {
		wg.Add(1)
		go func(i int, c digitalocean.Cluster) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, clusterScanTimeout)
			defer cancel()
			out[i] = scanOne(cctx, do, c.ID)
		}(i, c)
	}
	wg.Wait()
	return out
}

// scanOne reads one cluster. Any error short-circuits with Err set.
func scanOne(ctx context.Context, do *digitalocean.Client, clusterID string) ClusterScan {
	s := ClusterScan{ClusterID: clusterID}
	cs, err := kube(ctx, do, clusterID)
	if err != nil {
		s.Err = err
		return s
	}

	pvs, err := cs.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Err = fmt.Errorf("list persistentvolumes: %w", err)
		return s
	}
	for _, pv := range pvs.Items {
		s.PVs = append(s.PVs, PVRef{
			Name:         pv.Name,
			Phase:        string(pv.Status.Phase),
			VolumeHandle: volumeHandle(pv),
			ClaimNS:      claimNS(pv),
			ClaimName:    claimName(pv),
		})
	}

	pvcs, err := cs.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Err = fmt.Errorf("list persistentvolumeclaims: %w", err)
		return s
	}
	for _, p := range pvcs.Items {
		s.PVCs = append(s.PVCs, PVCRef{
			Namespace: p.Namespace, Name: p.Name,
			Phase: string(p.Status.Phase), Volume: p.Spec.VolumeName,
		})
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Err = fmt.Errorf("list pods: %w", err)
		return s
	}
	for _, p := range pods.Items {
		s.Pods = append(s.Pods, podRefOf(p))
	}

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Err = fmt.Errorf("list nodes: %w", err)
		return s
	}
	for _, n := range nodes.Items {
		s.Nodes = append(s.Nodes, NodeState{
			Name:        n.Name,
			Ready:       nodeReady(n),
			Schedulable: !n.Spec.Unschedulable,
		})
	}
	return s
}

// volumeHandle extracts the backing DO volume ID a PV claims. Deliberately NOT
// filtered by CSI driver name: matching broadly means MORE volumes are treated as
// in-use, which is the safe direction. The legacy flexVolume shape is read too, so a
// pre-CSI PV still protects its volume.
func volumeHandle(pv corev1.PersistentVolume) string {
	if pv.Spec.CSI != nil && strings.TrimSpace(pv.Spec.CSI.VolumeHandle) != "" {
		return pv.Spec.CSI.VolumeHandle
	}
	if pv.Spec.FlexVolume != nil {
		if v := strings.TrimSpace(pv.Spec.FlexVolume.Options["volumeID"]); v != "" {
			return v
		}
	}
	return ""
}

func claimNS(pv corev1.PersistentVolume) string {
	if pv.Spec.ClaimRef == nil {
		return ""
	}
	return pv.Spec.ClaimRef.Namespace
}

func claimName(pv corev1.PersistentVolume) string {
	if pv.Spec.ClaimRef == nil {
		return ""
	}
	return pv.Spec.ClaimRef.Name
}

// podRefOf reduces a pod to the board's needs: placement, health, mounted claims and
// images.
func podRefOf(p corev1.Pod) PodRef {
	r := PodRef{
		Namespace: p.Namespace, Name: p.Name,
		Phase: string(p.Status.Phase), Reason: p.Status.Reason, Node: p.Spec.NodeName,
	}
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			r.Claims = append(r.Claims, v.PersistentVolumeClaim.ClaimName)
		}
	}
	for _, c := range p.Spec.InitContainers {
		r.Images = append(r.Images, c.Image)
	}
	for _, c := range p.Spec.Containers {
		r.Images = append(r.Images, c.Image)
	}
	// A waiting container's reason (CrashLoopBackOff/ImagePullBackOff) is the real
	// health signal; pod.status.reason stays empty for those.
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && r.Reason == "" {
			r.Reason = cs.State.Waiting.Reason
		}
	}
	return r
}

func nodeReady(n corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetSchedulable cordons or uncordons a node, optionally draining it. Returns the
// number of pods evicted.
//
// Drain uses the Eviction API, not delete: eviction respects PodDisruptionBudgets, so
// a drain that would break a quorum is REFUSED by the apiserver rather than silently
// taking a service down. DaemonSet and mirror pods are skipped — they are rescheduled
// onto the same node by definition and evicting them is a no-op loop.
func SetSchedulable(ctx context.Context, do *digitalocean.Client, clusterID, node string, schedulable, drain bool) (int, error) {
	cs, err := kube(ctx, do, clusterID)
	if err != nil {
		return 0, err
	}
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, !schedulable)
	if _, err := cs.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return 0, fmt.Errorf("cordon %s: %w", node, err)
	}
	if schedulable || !drain {
		return 0, nil
	}
	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return 0, fmt.Errorf("list pods on %s: %w", node, err)
	}
	evicted := 0
	for _, p := range pods.Items {
		if skipEviction(p) {
			continue
		}
		ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: p.Name}}
		if err := cs.CoreV1().Pods(p.Namespace).EvictV1(ctx, ev); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			// A PDB refusal is the system working. Report it verbatim; the node stays
			// cordoned, so the operator can retry after scaling.
			return evicted, fmt.Errorf("evict %s/%s: %w", p.Namespace, p.Name, err)
		}
		evicted++
	}
	return evicted, nil
}

// skipEviction reports pods that must not be evicted: DaemonSet-owned and static
// (mirror) pods, which the node recreates immediately, and pods already terminal.
func skipEviction(p corev1.Pod) bool {
	if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return true
	}
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			return true
		}
	}
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}
