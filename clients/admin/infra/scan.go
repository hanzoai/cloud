package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// nodeStatsFanout bounds the per-cluster kubelet fan-out. Each read is one small JSON
// document through the apiserver's node proxy, so this bounds pressure on the apiserver,
// not local work.
const nodeStatsFanout = 16

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

	svcs, err := cs.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Err = fmt.Errorf("list services: %w", err)
		return s
	}
	for _, sv := range svcs.Items {
		if sv.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		s.Services = append(s.Services, serviceRefOf(sv))
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

	// Fill is read LAST and cannot fail the scan — see volumeUsage. Every read the
	// safety verdict depends on has already succeeded by this point, so a slow or
	// missing kubelet costs a metric, never a mutation.
	s.Usage = volumeUsage(ctx, cs, s.Nodes)
	return s
}

// volumeUsage reads every kubelet's stats/summary and returns one row per mounted PVC.
//
// This is the ONLY source of fill on this board. DigitalOcean does not expose how full a
// volume is, and metrics-server is absent from most of our clusters — but every kubelet
// serves stats/summary unconditionally, so this works on all of them.
//
// ERRORS ARE DROPPED, DELIBERATELY. Fill is advisory: no safety verdict depends on it, so
// a kubelet that will not answer must not fail the scan — that would block every mutation
// on the fleet over a metric. Its volumes simply get NO ROW, and a volume with no row is
// carried to the screen as unmeasured, which is a different fact from empty.
func volumeUsage(ctx context.Context, cs *kubernetes.Clientset, nodes []NodeState) []VolumeUsage {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		used = map[[2]string]int64{}
		sem  = make(chan struct{}, nodeStatsFanout)
	)
	for _, n := range nodes {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			raw, err := cs.CoreV1().RESTClient().Get().
				Resource("nodes").Name(node).SubResource("proxy").
				Suffix("stats", "summary").DoRaw(ctx)
			if err != nil {
				return
			}
			var sum struct {
				Pods []struct {
					Volume []struct {
						PVCRef *struct {
							Namespace string `json:"namespace"`
							Name      string `json:"name"`
						} `json:"pvcRef"`
						UsedBytes int64 `json:"usedBytes"`
					} `json:"volume"`
				} `json:"pods"`
			}
			if json.Unmarshal(raw, &sum) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range sum.Pods {
				for _, v := range p.Volume {
					if v.PVCRef == nil {
						continue
					}
					k := [2]string{v.PVCRef.Namespace, v.PVCRef.Name}
					// Two kubelets can report the same claim — ReadWriteMany, or a rollout
					// with both pods briefly alive. Keep the LARGEST reading: more used
					// means less waste claimed, the same conservative direction the rest of
					// this package takes.
					if v.UsedBytes > used[k] {
						used[k] = v.UsedBytes
					}
				}
			}
		}(n.Name)
	}
	wg.Wait()

	out := make([]VolumeUsage, 0, len(used))
	for k, b := range used {
		out = append(out, VolumeUsage{Namespace: k[0], Name: k[1], UsedBytes: b})
	}
	// Map order is not stable; the scan's output is. Analyze folds this into a map and
	// does not care, but a reproducible scan is worth one sort.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ExpandPVC grows a PersistentVolumeClaim.
//
// This is the ONE correct way to grow a volume Kubernetes manages: the resize controller
// acts on the CLAIM, growing the backing cloud device and then the filesystem on it, so
// claim, PV, device and filesystem all end up agreeing. Growing the device through the
// DigitalOcean API instead would leave the claim and the PV declaring the old capacity
// and the filesystem never grown at all — three sources of truth, two of them wrong.
//
// The StorageClass must set allowVolumeExpansion; when it does not, the apiserver refuses
// the patch and its message is surfaced verbatim rather than worked around.
func ExpandPVC(ctx context.Context, do *digitalocean.Client, clusterID, ns, name string, gib int) error {
	cs, err := kube(ctx, do, clusterID)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%dGi"}}}}`, gib)
	if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("expand pvc %s/%s: %w", ns, name, err)
	}
	return nil
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

// doLBIDAnnotation is the annotation the DOKS cloud-controller stamps on a Service once
// it has provisioned a load balancer for it. It is the strongest link between the two.
const doLBIDAnnotation = "kubernetes.digitalocean.com/load-balancer-id"

// serviceRefOf reduces a type=LoadBalancer Service to every identity by which it can be
// matched to a DO load balancer. Both the DOKS annotation and the addresses are read,
// and either matching is enough: a broad match means MORE load balancers are treated as
// in use, which is the safe direction — the same stance volumeHandle takes.
func serviceRefOf(sv corev1.Service) ServiceRef {
	r := ServiceRef{
		Namespace: sv.Namespace, Name: sv.Name,
		LBID: strings.TrimSpace(sv.Annotations[doLBIDAnnotation]),
	}
	if ip := strings.TrimSpace(sv.Spec.LoadBalancerIP); ip != "" {
		r.IPs = append(r.IPs, ip)
	}
	for _, in := range sv.Status.LoadBalancer.Ingress {
		if ip := strings.TrimSpace(in.IP); ip != "" {
			r.IPs = append(r.IPs, ip)
		}
	}
	return r
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
	// The controlling owner names the workload that has to be edited to right-size this
	// pod's volumes, and its KIND decides how: a StatefulSet's volumeClaimTemplates are
	// immutable, so the workload itself must be recreated around the swap. See
	// shrinkRecipe, which is the only consumer.
	for _, o := range p.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			r.Controller = o.Kind + "/" + o.Name
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
