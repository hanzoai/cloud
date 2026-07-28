// Package infra is the platform's DigitalOcean fleet board: the physical inventory
// (DOKS clusters, droplets, block-storage volumes, load balancers) cross-referenced
// against what every cluster's Kubernetes actually claims, with the cost of each and
// an orphan analysis that is safe BY CONSTRUCTION.
//
// THE RULE THIS PACKAGE EXISTS TO ENFORCE. A volume being detached, or carrying a
// `k8s:<cluster-uuid>` tag for some other cluster, does NOT make it garbage. Those two
// signals together would have condemned 4.39 TiB of live data belonging to running
// clusters. The ONLY sound liveness test is a cross-reference against the
// `spec.csi.volumeHandle` of every PersistentVolume in EVERY cluster — and it is only
// a valid test when every cluster answered. So:
//
//   - a volume is deletable only when NO PV in ANY cluster names it, and
//   - if even one cluster failed to scan, NOTHING is deletable (Complete=false).
//
// Absence of evidence is not evidence of absence: an unreachable cluster is treated as
// a cluster that might be holding the volume. The analysis fails CLOSED.
//
// "No pod mounts it" is a REVIEW signal, never a delete signal — an idle Bound PVC is
// an idle database, not garbage. Idle volumes are surfaced as a queue for a human and
// are never counted as reclaimable.
//
// THE SAME DISCIPLINE GOVERNS EVERY MUTATION, not just volume deletion. A droplet, a
// load balancer and a node pool each get a (allowed, reason) verdict derived HERE, from
// the same scan, behind the same completeness gate — see Snapshot.verdict. Handlers
// carry no policy: they read the verdict this file already reached.
//
// Analyze is a pure function of (DO inventory, cluster scans) so every rule above is
// unit-testable without a network or a cluster.
package infra

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/admin/money"
)

// volumeGiBCents is DO's block-storage rate: $0.10 per GiB per month. Droplet LOCAL
// disk is NOT billed at this rate (or at all, separately) — it is included in the
// droplet's own price. Conflating the two invents terabytes of phantom cost.
const volumeGiBCents = money.Cents(10)

// outlierShareBP flags any single resource costing at least this share of total fleet
// spend, in basis points (500bp = 5%). One explicable rule, no magic thresholds.
const outlierShareBP = money.Cents(500)

const gib = 1 << 30

// maxVolumeGiB is DigitalOcean's hard ceiling on a block-storage volume, 16 TiB.
const maxVolumeGiB = 16 * 1024

// The right-sizing rule, stated once. A volume is worth flagging when it is at least
// half empty AND the empty part is big enough that a migration pays for itself; the
// suggested size doubles what is actually stored, so the workload keeps as much room
// again as it has ever used. All three are rules of thumb, which is why they produce a
// FINDING for a human to judge and never an action.
const (
	wasteShareBP  = 5000 // 5000bp = 50% — at least half the volume is empty.
	wasteFloorGiB = 32   // $3.20/mo. Below this the migration costs more than it saves.
	headroomMult  = 2    // Suggest 2x measured usage...
	minTargetGiB  = 32   // ...never below this, because this board sees ONE instantaneous
	// sample and cannot see a growth RATE. A chain node holding 0.5 GiB today is not a
	// 1 GiB workload, and suggesting it be squeezed to 1 GiB would be advice that causes
	// the outage it was meant to prevent. shrinkRecipe states the limitation too.
)

// Volume states. The state machine is total and ordered: attachment beats reference,
// reference beats absence. Only Unreferenced is ever deletable.
const (
	// StateAttached — DO reports the volume attached to a droplet. In use, now.
	StateAttached = "attached"
	// StateBound — detached, but a PV in some cluster claims it and that PV is Bound
	// to a PVC. This is live data between mounts; deleting it destroys a database.
	StateBound = "bound"
	// StateReleased — a PV claims it but is no longer Bound (Released/Available/Failed).
	// A genuine cleanup candidate, but the PV still exists, so a human retires the PV.
	StateReleased = "released"
	// StateUnreferenced — no PV in ANY scanned cluster names it. The ONLY deletable state.
	StateUnreferenced = "unreferenced"
)

// Finding severities.
const (
	SevCritical = "critical"
	SevWarn     = "warn"
	SevInfo     = "info"
)

// firstParty are our own registries: anything here is ours by construction.
var firstParty = []string{
	"ghcr.io/hanzoai/", "ghcr.io/luxfi/", "ghcr.io/zooai/",
	"registry.hanzo.ai/", "registry.lux.network/", "registry.zoo.network/",
	"registry.digitalocean.com/hanzo/",
}

// knownVendors is the REVIEWED third-party set — upstream images we deliberately run.
// Kept deliberately short: an image outside both lists is reported for a human to
// judge, which is the point. Growing this list is a review decision, not a reflex.
var knownVendors = []string{
	"docker.io/library/", "library/", "registry.k8s.io/", "k8s.gcr.io/", "quay.io/",
	"grafana/", "prom/", "prometheus/", "bitnami/", "minio/", "moby/",
	"digitalocean/", "docker.digitalocean.com/", "acmglobaltech/", "hanzozt/",
}

// Inventory is the DigitalOcean account read — the half of the analysis input that
// needs no cluster.
type Inventory struct {
	Clusters      []digitalocean.Cluster
	Droplets      []digitalocean.Droplet
	Volumes       []digitalocean.Volume
	LoadBalancers []digitalocean.LoadBalancer
}

// PVRef is one PersistentVolume's identity: which DO volume it claims, and whether
// that claim is still live.
type PVRef struct {
	Name         string
	Phase        string
	VolumeHandle string
	ClaimNS      string
	ClaimName    string
}

// PVCRef is one PersistentVolumeClaim.
type PVCRef struct {
	Namespace string
	Name      string
	Phase     string
	Volume    string
}

// PodRef is one pod, reduced to what the board needs: where it runs, whether it is
// healthy, which PVCs it mounts, what images it runs, and the workload that controls it.
type PodRef struct {
	Namespace string
	Name      string
	Phase     string
	Reason    string
	Node      string
	Claims    []string
	Images    []string
	// Controller is the controlling owner as `Kind/Name` ("StatefulSet/luxd"), or "" for
	// a bare pod. Its kind decides how this pod's volumes can be right-sized.
	Controller string
}

// VolumeUsage is one PersistentVolumeClaim's REAL filesystem usage, as measured by the
// kubelet that has it mounted.
//
// A reading exists ONLY for a volume a running pod has mounted on a node that answered.
// Everything else has NO reading — a different fact from "empty", carried as such all the
// way to the screen. See Volume.HasUsage.
type VolumeUsage struct {
	Namespace string
	Name      string
	UsedBytes int64
}

// NodeState is one Kubernetes node's own view of itself.
type NodeState struct {
	Name        string
	Ready       bool
	Schedulable bool
}

// ServiceRef is one Service of type LoadBalancer: the identities by which it claims a
// DO load balancer. It is to load balancers exactly what PVRef is to volumes — the only
// sound liveness test, because a DO load balancer carries no back-reference of its own.
type ServiceRef struct {
	Namespace string
	Name      string
	LBID      string
	IPs       []string
}

// ClusterScan is ONE cluster's Kubernetes truth. Err non-nil means the cluster did
// not answer — which forces the whole analysis incomplete.
type ClusterScan struct {
	ClusterID string
	Err       error
	Nodes     []NodeState
	PVs       []PVRef
	PVCs      []PVCRef
	Pods      []PodRef
	Services  []ServiceRef
	// Usage is fill, per claim. PARTIAL BY NATURE and never an error: a claim absent here
	// was not measured, which this analysis reports as unknown rather than as zero.
	Usage []VolumeUsage
}

// Snapshot is the whole board in one value.
type Snapshot struct {
	At               string              `json:"at"`
	Complete         bool                `json:"complete"`
	IncompleteReason string              `json:"incompleteReason"`
	Sources          []core.SourceStatus `json:"sources"`
	Totals           Totals              `json:"totals"`
	Cost             Cost                `json:"cost"`
	Clusters         []Cluster           `json:"clusters"`
	Nodes            []Node              `json:"nodes"`
	Volumes          []Volume            `json:"volumes"`
	LoadBalancers    []LoadBalancer      `json:"loadBalancers"`
	Findings         []Finding           `json:"findings"`
}

// Totals are fleet counts. LocalDiskGiB is broken out precisely so it can be shown
// as NOT separately billed.
type Totals struct {
	Clusters            int `json:"clusters"`
	Nodes               int `json:"nodes"`
	Volumes             int `json:"volumes"`
	LoadBalancers       int `json:"loadBalancers"`
	VolumeGiB           int `json:"volumeGiB"`
	AttachedVolumes     int `json:"attachedVolumes"`
	AttachedGiB         int `json:"attachedGiB"`
	DetachedVolumes     int `json:"detachedVolumes"`
	DetachedGiB         int `json:"detachedGiB"`
	UnreferencedVolumes int `json:"unreferencedVolumes"`
	UnreferencedGiB     int `json:"unreferencedGiB"`
	IdlePVCs            int `json:"idlePVCs"`
	LocalDiskGiB        int `json:"localDiskGiB"`

	// Fill. MeasuredVolumes/UnmeasuredVolumes are the honesty denominator: UsedGiB and
	// WastedGiB describe the measured set ONLY, so a board showing waste must show how
	// much of the fleet the figure was computed from. Unmeasured capacity contributes
	// nothing to either — it is not assumed empty, and it is not assumed full.
	MeasuredVolumes   int `json:"measuredVolumes"`
	MeasuredGiB       int `json:"measuredGiB"`
	UnmeasuredVolumes int `json:"unmeasuredVolumes"`
	UnmeasuredGiB     int `json:"unmeasuredGiB"`
	UsedGiB           int `json:"usedGiB"`
	WastedGiB         int `json:"wastedGiB"`
}

// Cost is monthly spend in cents. Reclaimable counts ONLY unreferenced volumes —
// never idle ones, which are live data awaiting a human verdict.
type Cost struct {
	DropletsMonthly      money.Cents `json:"dropletsMonthly"`
	VolumesMonthly       money.Cents `json:"volumesMonthly"`
	LoadBalancersMonthly money.Cents `json:"loadBalancersMonthly"`
	TotalMonthly         money.Cents `json:"totalMonthly"`
	ReclaimableMonthly   money.Cents `json:"reclaimableMonthly"`
	// WastedMonthly is what the fleet pays every month for provisioned-but-empty space on
	// the volumes a kubelet actually measured.
	//
	// It is NOT ReclaimableMonthly and must never be added to it. Reclaimable is money a
	// button on this board collects, by deleting volumes proven to belong to no one.
	// Wasted is money locked inside volumes that are IN USE and holding live data:
	// DigitalOcean can only ever grow a volume, so collecting it means copying a database
	// onto a smaller one. See shrinkRecipe.
	//
	// It is also a LOWER BOUND — unmeasured volumes contribute nothing.
	WastedMonthly money.Cents `json:"wastedMonthly"`
}

// Cluster is one DOKS cluster with its scanned Kubernetes rollup.
type Cluster struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Region       string      `json:"region"`
	Version      string      `json:"version"`
	Status       string      `json:"status"`
	NodePools    int         `json:"nodePools"`
	Pools        []NodePool  `json:"pools"`
	Nodes        int         `json:"nodes"`
	Pods         int         `json:"pods"`
	PVs          int         `json:"pvs"`
	PVCs         int         `json:"pvcs"`
	IdlePVCs     int         `json:"idlePVCs"`
	Scanned      bool        `json:"scanned"`
	ScanError    string      `json:"scanError"`
	MonthlyCents money.Cents `json:"monthlyCents"`
}

// NodePool is one DOKS node pool — the only correct place to change a cluster's node
// count. ClusterSchedulable is the whole cluster's schedulable-and-ready node count,
// carried on the row so the shrink verdict is a pure method of it.
type NodePool struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Size               string `json:"size"`
	Count              int    `json:"count"`
	ClusterID          string `json:"clusterId"`
	Cluster            string `json:"cluster"`
	ClusterSchedulable int    `json:"clusterSchedulable"`
	Scalable           bool   `json:"scalable"`
	BlockedReason      string `json:"blockedReason"`
}

// ScaleTo answers whether this pool may be set to count.
//
// PROVEN HERE: a pool keeps at least one node, and a shrink never leaves the cluster
// with zero schedulable nodes — with none, every pod the removed nodes carried is
// unschedulable, guaranteed.
//
// NOT PROVEN, and deliberately not pretended: DOKS picks WHICH nodes it removes, so no
// particular pod can be shown to survive a shrink that leaves capacity behind. Node
// affinity, taints and resource requests decide that, and PodDisruptionBudgets are
// enforced by the cluster during DOKS's own drain — not by this board. A shrink that
// merely MIGHT not fit is allowed, and the response says so.
func (p NodePool) ScaleTo(count int) (bool, string) {
	switch {
	case !p.Scalable:
		return false, p.BlockedReason
	case count < 1:
		return false, "A node pool must keep at least one node — scaling to zero destroys every node in the pool and strands its pods."
	case count < p.Count && p.ClusterSchedulable-(p.Count-count) < 1:
		return false, fmt.Sprintf("Removing %d node(s) would leave %s with no schedulable node.", p.Count-count, p.Cluster)
	}
	return true, ""
}

// Node is one droplet, joined to the Kubernetes node of the same name.
type Node struct {
	ID           int         `json:"id"`
	Name         string      `json:"name"`
	Cluster      string      `json:"cluster"`
	ClusterID    string      `json:"clusterId"`
	Region       string      `json:"region"`
	Status       string      `json:"status"`
	SizeSlug     string      `json:"sizeSlug"`
	VCPUs        int         `json:"vcpus"`
	MemoryMiB    int         `json:"memoryMiB"`
	LocalDiskGiB int         `json:"localDiskGiB"`
	MonthlyCents money.Cents `json:"monthlyCents"`
	CreatedAt    string      `json:"createdAt"`
	PrivateIP    string      `json:"privateIp"`
	PublicIP     string      `json:"publicIp"`
	Tags         []string    `json:"tags"`
	Ready        bool        `json:"ready"`
	Schedulable  bool        `json:"schedulable"`
	Pods         int         `json:"pods"`
	Volumes      int         `json:"volumes"`
	// Mutable reports whether this droplet may be changed DIRECTLY — deleted or resized.
	// One predicate covers both because one fact decides both: a DOKS node belongs to a
	// node pool, and the pool is the only thing allowed to change it.
	Mutable       bool   `json:"mutable"`
	BlockedReason string `json:"blockedReason"`
}

// Volume is one block-storage volume with its PROVEN cluster ownership.
type Volume struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Region       string      `json:"region"`
	SizeGiB      int         `json:"sizeGiB"`
	MonthlyCents money.Cents `json:"monthlyCents"`
	State        string      `json:"state"`
	DropletIDs   []int       `json:"dropletIds"`
	NodeName     string      `json:"nodeName"`
	// Cluster/ClusterID are the PROVEN owner — resolved through a PV that names this
	// volume, never through the tag.
	Cluster   string `json:"cluster"`
	ClusterID string `json:"clusterId"`
	// TagCluster is the `k8s:<uuid>` tag. ADVISORY ONLY: it outlives the cluster that
	// set it. Shown so the operator can see tag-vs-truth disagree, never acted on.
	TagCluster   string   `json:"tagCluster"`
	PV           string   `json:"pv"`
	PVPhase      string   `json:"pvPhase"`
	PVCNamespace string   `json:"pvcNamespace"`
	PVCName      string   `json:"pvcName"`
	MountedBy    []string `json:"mountedBy"`
	Idle         bool     `json:"idle"`
	CreatedAt    string   `json:"createdAt"`
	// Controller is the workload owning the pod that mounts this volume
	// ("StatefulSet/luxd"), or "" when nothing mounts it. It names who has to act.
	Controller string `json:"controller"`

	// HasUsage reports whether a kubelet actually MEASURED this volume's filesystem.
	//
	// False means NOT MEASURED. It does NOT mean empty, and the three fields below are
	// meaningless — not zero — when it is false. A reading exists only while a running pod
	// has the volume mounted on a node that answered; a detached, idle or unreferenced
	// volume has none. Rendering an unmeasured volume as "0 used / 100% wasted" would
	// invent the single most expensive lie this board could tell, so every consumer must
	// branch on this flag and show unknown.
	HasUsage bool `json:"hasUsage"`
	// UsedBytes is the measured filesystem usage. BYTES, not GiB: the volumes this exists
	// to catch hold a fraction of a GiB in 200, and rounding that to an integer GiB would
	// print the very 0 the flag above exists to prevent.
	UsedBytes int64 `json:"usedBytes"`
	// WastedGiB is provisioned minus measured, in the unit DigitalOcean BILLS: whole GiB
	// of the volume's own size, never the filesystem's capacity — a 200 GiB volume carries
	// a 196 GiB filesystem after format overhead, and the invoice says 200.
	WastedGiB          int         `json:"wastedGiB"`
	WastedMonthlyCents money.Cents `json:"wastedMonthlyCents"`

	Deletable     bool   `json:"deletable"`
	BlockedReason string `json:"blockedReason"`
	// Expandable/ExpandBlockedReason are the GROW verdict, kept separate from Deletable
	// because the two ask opposite questions: a volume is deletable when nothing uses it,
	// and expandable when something uses it in a way this board can grow completely.
	Expandable          bool   `json:"expandable"`
	ExpandBlockedReason string `json:"expandBlockedReason"`
}

// ExpandTo answers whether this volume may be grown to gib, mirroring NodePool.ScaleTo:
// the row carries the standing verdict, and the method judges the number asked for.
//
// GROW ONLY. DigitalOcean can never shrink a block-storage volume, so a smaller target is
// not a slow operation — it is an impossible one, and pretending otherwise behind a button
// is how a chain node loses its data. Shrinking is a migration; see shrinkRecipe.
func (v Volume) ExpandTo(gib int) (bool, string) {
	switch {
	case !v.Expandable:
		return false, v.ExpandBlockedReason
	case gib <= v.SizeGiB:
		return false, fmt.Sprintf(
			"%d GiB is not larger than the current %d GiB. DigitalOcean volumes can only grow — "+
				"reclaiming space means copying the data to a smaller volume and swapping it in, "+
				"which this board deliberately does not do for you.", gib, v.SizeGiB)
	case gib > maxVolumeGiB:
		return false, fmt.Sprintf("%d GiB exceeds DigitalOcean's %d GiB maximum for one volume.", gib, maxVolumeGiB)
	}
	return true, ""
}

// LoadBalancer is one DO load balancer, attributed to a cluster via the Service that
// claims it, or failing that via its member droplets.
type LoadBalancer struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Region       string      `json:"region"`
	Status       string      `json:"status"`
	IP           string      `json:"ip"`
	SizeUnit     int         `json:"sizeUnit"`
	MonthlyCents money.Cents `json:"monthlyCents"`
	Droplets     int         `json:"droplets"`
	Cluster      string      `json:"cluster"`
	// Service is the `namespace/name` of the live type=LoadBalancer Service that claims
	// this load balancer, proven from the cluster scan. Non-empty means IN USE.
	Service       string `json:"service"`
	Deletable     bool   `json:"deletable"`
	BlockedReason string `json:"blockedReason"`
}

// Finding is one audit result — the "is anything bad" surface.
type Finding struct {
	ID           string      `json:"id"`
	Severity     string      `json:"severity"`
	Kind         string      `json:"kind"`
	Title        string      `json:"title"`
	Detail       string      `json:"detail"`
	Resource     string      `json:"resource"`
	Cluster      string      `json:"cluster"`
	MonthlyCents money.Cents `json:"monthlyCents"`
}

// pvHit is a PV that claims a given DO volume, plus the cluster it lives in.
type pvHit struct {
	cluster   string
	clusterID string
	pv        PVRef
}

// svcHit is a Service that claims a given DO load balancer, plus its cluster.
type svcHit struct {
	cluster string
	svc     ServiceRef
}

// verdict is the ONE place a mutation's answer is decided: the completeness gate every
// mutation shares, then the resource's own rule (blocked == "" meaning its rule is
// satisfied). The gate comes first and applies to ALL of them — droplets, load balancers
// and node pools fail closed on a partial scan for the same reason volumes do: a fleet
// we cannot fully see is a fleet whose live parts we cannot fully name.
//
// Every (Deletable|Mutable|Scalable, BlockedReason) pair on this board comes from here,
// so "allowed carries no reason, blocked always names one" is stated once.
func (s *Snapshot) verdict(blocked string) (bool, string) {
	switch {
	case !s.Complete:
		return false, s.IncompleteReason
	case blocked != "":
		return false, blocked
	}
	return true, ""
}

// Analyze folds the DO inventory and the per-cluster Kubernetes scans into the board.
// PURE: no clock, no network, no cluster — `at` is passed in so the result is
// byte-reproducible in tests.
func Analyze(inv Inventory, scans []ClusterScan, sources []core.SourceStatus, at time.Time) Snapshot {
	snap := Snapshot{
		At:      at.UTC().Format(time.RFC3339),
		Sources: sources,
	}
	if snap.Sources == nil {
		snap.Sources = []core.SourceStatus{}
	}

	scanByID := make(map[string]ClusterScan, len(scans))
	for _, s := range scans {
		scanByID[s.ClusterID] = s
	}
	nameByID := make(map[string]string, len(inv.Clusters))
	for _, c := range inv.Clusters {
		nameByID[c.ID] = c.Name
	}

	// ---- completeness gate -------------------------------------------------------
	// Every cluster must have answered. One silent gap and no volume may be condemned.
	var unreachable []string
	for _, c := range inv.Clusters {
		s, ok := scanByID[c.ID]
		if !ok || s.Err != nil {
			unreachable = append(unreachable, c.Name)
		}
	}
	switch {
	case len(inv.Clusters) == 0:
		snap.IncompleteReason = "DigitalOcean returned no clusters — the set of places a volume could be in use is unknown."
	case len(unreachable) > 0:
		snap.IncompleteReason = fmt.Sprintf(
			"%d of %d clusters did not answer (%s) — a volume they hold would look unreferenced, so nothing is classified as deletable.",
			len(unreachable), len(inv.Clusters), strings.Join(unreachable, ", "))
	default:
		snap.Complete = true
	}

	// ---- cross-cluster PV index --------------------------------------------------
	// THE safety index: every volume handle claimed by any PV in any cluster.
	byHandle := make(map[string]pvHit)
	// mounted[clusterID/ns/pvc] -> pods currently mounting it.
	mounted := make(map[string][]string)
	// ctrlByClaim[clusterID/ns/pvc] -> the workload owning the mounting pod, and
	// usedByClaim[same] -> its measured fill. Both are keyed identically to mounted and
	// both are populated by the same fact — a running pod — so a volume that has a fill
	// reading always has a named owner to act on it. ABSENT means unmeasured, never zero,
	// which is why this is a map lookup with a comma-ok and not a zero value.
	ctrlByClaim := make(map[string]string)
	usedByClaim := make(map[string]int64)
	// THE load-balancer safety index, keyed by BOTH identities a Service can claim one
	// by — the DOKS load-balancer-id annotation and every address it holds. Either
	// matching counts, because a broad match means MORE load balancers are treated as in
	// use, which is the safe direction.
	lbClaims := make(map[string]svcHit)
	for _, s := range scans {
		if s.Err != nil {
			continue
		}
		cname := nameByID[s.ClusterID]
		for _, pv := range s.PVs {
			if h := strings.TrimSpace(pv.VolumeHandle); h != "" {
				byHandle[h] = pvHit{cluster: cname, clusterID: s.ClusterID, pv: pv}
			}
		}
		for _, p := range s.Pods {
			for _, claim := range p.Claims {
				k := claimKey(s.ClusterID, p.Namespace, claim)
				mounted[k] = append(mounted[k], p.Namespace+"/"+p.Name)
				if p.Controller != "" {
					ctrlByClaim[k] = p.Controller
				}
			}
		}
		for _, u := range s.Usage {
			usedByClaim[claimKey(s.ClusterID, u.Namespace, u.Name)] = u.UsedBytes
		}
		for _, sv := range s.Services {
			hit := svcHit{cluster: cname, svc: sv}
			for _, key := range append([]string{sv.LBID}, sv.IPs...) {
				if key = strings.TrimSpace(key); key != "" {
					lbClaims[key] = hit
				}
			}
		}
	}

	// ---- nodes -------------------------------------------------------------------
	nodeByName := make(map[string]NodeState)
	podsPerNode := make(map[string]int)
	for _, s := range scans {
		if s.Err != nil {
			continue
		}
		for _, n := range s.Nodes {
			nodeByName[n.Name] = n
		}
		for _, p := range s.Pods {
			if p.Node != "" {
				podsPerNode[p.Node]++
			}
		}
	}
	volsPerDroplet := make(map[int]int)
	for _, v := range inv.Volumes {
		for _, id := range v.DropletIDs {
			volsPerDroplet[id]++
		}
	}
	clusterByDroplet := make(map[int]string, len(inv.Droplets))
	snap.Nodes = make([]Node, 0, len(inv.Droplets))
	for _, d := range inv.Droplets {
		cid := clusterIDFromTags(d.Tags)
		clusterByDroplet[d.ID] = cid
		ks := nodeByName[d.Name]
		n := Node{
			ID: d.ID, Name: d.Name, Cluster: nameByID[cid], ClusterID: cid,
			Region: d.Region, Status: d.Status, SizeSlug: d.SizeSlug,
			VCPUs: d.VCPUs, MemoryMiB: d.MemoryMiB, LocalDiskGiB: d.LocalDiskGiB,
			MonthlyCents: d.MonthlyCents, CreatedAt: d.CreatedAt,
			PrivateIP: d.PrivateIP, PublicIP: d.PublicIP, Tags: nonNilStrings(d.Tags),
			Ready: ks.Ready, Schedulable: ks.Schedulable,
			Pods: podsPerNode[d.Name], Volumes: volsPerDroplet[d.ID],
		}
		n.Mutable, n.BlockedReason = snap.verdict(nodeBlock(n))
		snap.Nodes = append(snap.Nodes, n)
		snap.Cost.DropletsMonthly += d.MonthlyCents
		snap.Totals.LocalDiskGiB += d.LocalDiskGiB
	}
	dropletName := make(map[int]string, len(inv.Droplets))
	for _, d := range inv.Droplets {
		dropletName[d.ID] = d.Name
	}

	// ---- volumes: the state machine ----------------------------------------------
	// Fill totals accumulate in BYTES and convert once, so 178 sub-GiB readings do not
	// round themselves away one at a time.
	var usedBytes int64
	snap.Volumes = make([]Volume, 0, len(inv.Volumes))
	for _, v := range inv.Volumes {
		hit, referenced := byHandle[v.ID]
		vol := Volume{
			ID: v.ID, Name: v.Name, Region: v.Region, SizeGiB: v.SizeGiB,
			MonthlyCents: money.Cents(v.SizeGiB) * volumeGiBCents,
			DropletIDs:   nonNilInts(v.DropletIDs),
			TagCluster:   nameByID[clusterIDFromTags(v.Tags)],
			CreatedAt:    v.CreatedAt,
			MountedBy:    []string{},
		}
		if len(v.DropletIDs) > 0 {
			vol.NodeName = dropletName[v.DropletIDs[0]]
		}
		if referenced {
			vol.Cluster, vol.ClusterID = hit.cluster, hit.clusterID
			vol.PV, vol.PVPhase = hit.pv.Name, hit.pv.Phase
			vol.PVCNamespace, vol.PVCName = hit.pv.ClaimNS, hit.pv.ClaimName
			if hit.pv.ClaimName != "" {
				k := claimKey(hit.clusterID, hit.pv.ClaimNS, hit.pv.ClaimName)
				vol.MountedBy = nonNilStrings(mounted[k])
				vol.Controller = ctrlByClaim[k]
				// Comma-ok, not a zero value: a claim no kubelet reported is UNMEASURED,
				// and leaving HasUsage false is what makes the screen say so.
				if b, ok := usedByClaim[k]; ok {
					vol.HasUsage, vol.UsedBytes = true, b
					vol.WastedGiB = wastedGiB(vol.SizeGiB, b)
					vol.WastedMonthlyCents = money.Cents(vol.WastedGiB) * volumeGiBCents
				}
			}
		} else if len(v.DropletIDs) > 0 {
			// No PV names it, but it is physically mounted on a node — that node's
			// cluster owns it. Attachment is hard evidence, unlike the tag: it is the
			// live kernel state, so the cost rolls up to the right cluster.
			vol.ClusterID = clusterByDroplet[v.DropletIDs[0]]
			vol.Cluster = nameByID[vol.ClusterID]
		}

		// Attachment beats reference; reference beats absence.
		switch {
		case len(v.DropletIDs) > 0:
			vol.State = StateAttached
		case referenced && strings.EqualFold(hit.pv.Phase, "Bound"):
			vol.State = StateBound
		case referenced:
			vol.State = StateReleased
		default:
			vol.State = StateUnreferenced
		}

		// Idle is a REVIEW signal on live data, never a delete signal.
		vol.Idle = vol.State == StateBound && len(vol.MountedBy) == 0

		vol.Deletable, vol.BlockedReason = snap.verdict(volumeBlock(vol))
		vol.Expandable, vol.ExpandBlockedReason = snap.verdict(expandBlock(vol))

		snap.Cost.VolumesMonthly += vol.MonthlyCents
		snap.Totals.VolumeGiB += vol.SizeGiB
		if vol.HasUsage {
			snap.Totals.MeasuredVolumes++
			snap.Totals.MeasuredGiB += vol.SizeGiB
			usedBytes += vol.UsedBytes
			snap.Totals.WastedGiB += vol.WastedGiB
			snap.Cost.WastedMonthly += vol.WastedMonthlyCents
		} else {
			snap.Totals.UnmeasuredVolumes++
			snap.Totals.UnmeasuredGiB += vol.SizeGiB
		}
		switch vol.State {
		case StateAttached:
			snap.Totals.AttachedVolumes++
			snap.Totals.AttachedGiB += vol.SizeGiB
		default:
			snap.Totals.DetachedVolumes++
			snap.Totals.DetachedGiB += vol.SizeGiB
		}
		if vol.State == StateUnreferenced {
			snap.Totals.UnreferencedVolumes++
			snap.Totals.UnreferencedGiB += vol.SizeGiB
			// Reclaimable is exactly the unreferenced set — and only when the scan was
			// complete enough to have earned that verdict.
			if snap.Complete {
				snap.Cost.ReclaimableMonthly += vol.MonthlyCents
			}
		}
		if vol.Idle {
			snap.Totals.IdlePVCs++
		}
		snap.Volumes = append(snap.Volumes, vol)
	}
	snap.Totals.UsedGiB = int(usedBytes / gib)

	// ---- load balancers ----------------------------------------------------------
	snap.LoadBalancers = make([]LoadBalancer, 0, len(inv.LoadBalancers))
	for _, l := range inv.LoadBalancers {
		lb := LoadBalancer{
			ID: l.ID, Name: l.Name, Region: l.Region, Status: l.Status, IP: l.IP,
			SizeUnit: l.SizeUnit, MonthlyCents: l.MonthlyCents, Droplets: len(l.DropletIDs),
		}
		// The claiming Service is the strongest attribution there is; member droplets are
		// the fallback for a load balancer no scanned Service claims.
		hit, claimed := lbClaims[l.ID]
		if !claimed {
			hit, claimed = lbClaims[l.IP]
		}
		if claimed {
			lb.Service = hit.svc.Namespace + "/" + hit.svc.Name
			lb.Cluster = hit.cluster
		}
		// A member droplet belonging to no cluster carries a workload Kubernetes knows
		// nothing about, so the Service scan proves nothing about this load balancer.
		unmanaged := 0
		for _, id := range l.DropletIDs {
			if cid := clusterByDroplet[id]; cid == "" {
				unmanaged++
			} else if lb.Cluster == "" {
				lb.Cluster = nameByID[cid]
			}
		}
		lb.Deletable, lb.BlockedReason = snap.verdict(lbBlock(lb, unmanaged))
		snap.Cost.LoadBalancersMonthly += lb.MonthlyCents
		snap.LoadBalancers = append(snap.LoadBalancers, lb)
	}

	// ---- cluster rollup ----------------------------------------------------------
	idleByCluster := make(map[string]int)
	costByCluster := make(map[string]money.Cents)
	for _, v := range snap.Volumes {
		if v.ClusterID != "" {
			costByCluster[v.ClusterID] += v.MonthlyCents
			if v.Idle {
				idleByCluster[v.ClusterID]++
			}
		}
	}
	nodesByCluster := make(map[string]int)
	for _, n := range snap.Nodes {
		if n.ClusterID != "" {
			nodesByCluster[n.ClusterID]++
			costByCluster[n.ClusterID] += n.MonthlyCents
		}
	}
	snap.Clusters = make([]Cluster, 0, len(inv.Clusters))
	for _, c := range inv.Clusters {
		row := Cluster{
			ID: c.ID, Name: c.Name, Region: c.Region, Version: c.Version,
			Status: c.Status, NodePools: len(c.Pools), Nodes: nodesByCluster[c.ID],
			IdlePVCs: idleByCluster[c.ID], MonthlyCents: costByCluster[c.ID],
			Pools: make([]NodePool, 0, len(c.Pools)),
		}
		if s, ok := scanByID[c.ID]; ok {
			if s.Err != nil {
				row.ScanError = s.Err.Error()
			} else {
				row.Scanned = true
				row.Pods, row.PVs, row.PVCs = len(s.Pods), len(s.PVs), len(s.PVCs)
			}
		} else {
			row.ScanError = "not scanned"
		}
		// A node that is cordoned or NotReady cannot take a pod, so it does not count as
		// somewhere a shrink's evicted pods could land. Counting conservatively here
		// refuses more shrinks, which is the safe direction.
		schedulable := 0
		for _, n := range scanByID[c.ID].Nodes {
			if n.Ready && n.Schedulable {
				schedulable++
			}
		}
		for _, p := range c.Pools {
			pool := NodePool{
				ID: p.ID, Name: p.Name, Size: p.Size, Count: p.Count,
				ClusterID: c.ID, Cluster: c.Name, ClusterSchedulable: schedulable,
			}
			// A pool carries no rule of its own — what may be refused depends on the
			// COUNT asked for, which ScaleTo decides. Only the shared gate applies here.
			pool.Scalable, pool.BlockedReason = snap.verdict("")
			row.Pools = append(row.Pools, pool)
		}
		snap.Clusters = append(snap.Clusters, row)
	}

	snap.Totals.Clusters = len(snap.Clusters)
	snap.Totals.Nodes = len(snap.Nodes)
	snap.Totals.Volumes = len(snap.Volumes)
	snap.Totals.LoadBalancers = len(snap.LoadBalancers)
	snap.Cost.TotalMonthly = snap.Cost.DropletsMonthly + snap.Cost.VolumesMonthly + snap.Cost.LoadBalancersMonthly

	snap.Findings = findings(snap, scans, nameByID)
	return snap
}

// The per-resource rules. Each states, in the operator's language, exactly why ITS
// resource may not be mutated, and returns "" when its own rule is satisfied — the
// shared completeness gate in Snapshot.verdict has the final word either way.

// volumeBlock: only an unreferenced volume may be deleted.
func volumeBlock(v Volume) string {
	switch v.State {
	case StateUnreferenced:
		return ""
	case StateAttached:
		if v.NodeName != "" {
			return "Attached to " + v.NodeName + " and in use."
		}
		return "Attached to a droplet and in use."
	case StateBound:
		return fmt.Sprintf("Live data: PV %s is Bound to %s/%s in %s.", v.PV, v.PVCNamespace, v.PVCName, v.Cluster)
	case StateReleased:
		return fmt.Sprintf("PV %s in %s still references it (%s) — retire the PV first.", v.PV, v.Cluster, v.PVPhase)
	}
	return "Not eligible for deletion."
}

// expandBlock: a volume may be grown when this board can grow it COMPLETELY — device,
// filesystem, and every object that declares a capacity, all left agreeing.
//
//   - A PVC claims it: the claim is patched and the CSI driver does all three. Complete,
//     and it is exactly the volumes that are in use and filling up — the case that matters.
//   - A PV claims it but no PVC does: nothing can be patched, and growing the device
//     behind the PV's back leaves the PV declaring a capacity that is now wrong.
//   - Nothing claims it: the DigitalOcean API IS the whole truth, so there is nothing to
//     disagree with. The filesystem, if any, is the operator's to grow — expandVolume says
//     so in its response rather than implying the volume is bigger than it is usable.
func expandBlock(v Volume) string {
	if v.PVCName == "" && v.PV != "" {
		return fmt.Sprintf(
			"PV %s claims it but no PVC does (%s) — growing the device would leave the PV's "+
				"declared capacity wrong. Retire or rebind the PV first.", v.PV, v.PVPhase)
	}
	return ""
}

// wastedGiB is provisioned minus measured, in whole billed GiB.
//
// Usage rounds UP before subtracting so the waste is never overstated, and the result
// floors at zero: a filesystem reporting more used than DigitalOcean provisioned —
// reserved blocks, accounting skew — is not negative waste.
func wastedGiB(sizeGiB int, usedBytes int64) int {
	used := int((usedBytes + gib - 1) / gib)
	if w := sizeGiB - used; w > 0 {
		return w
	}
	return 0
}

// nodeBlock: a DOKS node may not be deleted OR resized directly. The node pool owns it
// — DOKS recreates a node deleted out from under it (so the delete costs an outage and
// changes nothing) and reverts a hand-resized one to the pool's declared size. The pool
// is the only lever that actually holds.
func nodeBlock(n Node) string {
	if n.ClusterID == "" {
		return ""
	}
	name := n.Cluster
	if name == "" {
		name = n.ClusterID
	}
	return fmt.Sprintf("Node of DOKS cluster %s — DOKS owns it via its node pool and will recreate it. Scale or edit the pool instead.", name)
}

// lbBlock: a load balancer a live Service still targets may not be deleted — doing so
// black-holes that Service's public address, and DOKS recreates the load balancer
// anyway. Nor may one that forwards to droplets outside every cluster: the Service scan
// is the only liveness evidence this board has, and it says nothing about a workload
// Kubernetes does not run. "Has member droplets" is NOT itself a liveness signal — a
// DOKS load balancer lists every node in its cluster, so a leaked one still looks busy.
func lbBlock(lb LoadBalancer, unmanagedMembers int) string {
	switch {
	case lb.Service != "":
		return fmt.Sprintf("Serving Kubernetes Service %s in %s — delete the Service first; DOKS recreates a load balancer its Service still wants.", lb.Service, lb.Cluster)
	case unmanagedMembers > 0:
		return fmt.Sprintf("Forwards to %d droplet(s) outside any cluster — no Kubernetes Service can vouch for it, so it cannot be proven unused.", unmanagedMembers)
	}
	return ""
}

// rightSize answers what a volume should have been provisioned at, and whether the gap is
// worth an operator's time. Both halves of one rule, in one place.
//
// The two tests are deliberately about different things. "Mostly empty" is a property of
// the VOLUME and is measured against the raw waste. "Worth doing" is a property of the
// ACTION and is measured against what right-sizing would actually save — which is smaller,
// because the suggestion keeps headroom. A volume can be half empty and still not worth
// migrating, and saying so is the honest answer.
func rightSize(v Volume) (int, bool) {
	if !v.HasUsage {
		return 0, false
	}
	target := headroomMult * int((v.UsedBytes+gib-1)/gib)
	if target < minTargetGiB {
		target = minTargetGiB
	}
	if v.WastedGiB*10000 < v.SizeGiB*wasteShareBP {
		return target, false
	}
	return target, v.SizeGiB-target >= wasteFloorGiB
}

// shrinkRecipe is the exact sequence that right-sizes one volume. It is TEXT, and that is
// the entire point.
//
// DigitalOcean can only ever GROW a block-storage volume. Reclaiming space therefore means
// provisioning a smaller one, copying the data across and swapping the claim over — a
// migration with a window in which the only copy of a live database is in flight. A
// StatefulSet makes it harder still: volumeClaimTemplates is immutable, so the workload
// itself must be deleted (orphaning its pods) and recreated around the swap, one ordinal at
// a time.
//
// No button on this board runs this, and none ever should. The board shows the money and
// the steps; a human with a maintenance window runs them.
func shrinkRecipe(v Volume, target int) string {
	ns, pvc := v.PVCNamespace, v.PVCName
	kind, name, _ := strings.Cut(v.Controller, "/")
	tmp := pvc + "-rightsize"

	stop := fmt.Sprintf("stop every pod writing to %s/%s", ns, pvc)
	if name != "" {
		stop = fmt.Sprintf("kubectl -n %s scale %s/%s --replicas=0", ns, strings.ToLower(kind), name)
	}
	steps := []string{
		fmt.Sprintf(" 1  %s", stop),
		fmt.Sprintf(" 2  kubectl -n %s apply -f -   # PVC %s, %dGi, same storageClassName", ns, tmp, target),
		fmt.Sprintf(" 3  copy: run one pod mounting %s at /from (readOnly) and %s at /to, then\n"+
			"     rsync -aHAX --numeric-ids --delete /from/ /to/", pvc, tmp),
		fmt.Sprintf(" 4  NEW=$(kubectl -n %s get pvc %s -o jsonpath='{.spec.volumeName}')\n"+
			"     kubectl patch pv $NEW %s -p '{\"spec\":{\"persistentVolumeReclaimPolicy\":\"Retain\"}}'\n"+
			"     # Retain BOTH so nothing is destroyed until step 9 verifies the copy.", ns, tmp, v.PV),
		fmt.Sprintf(" 5  kubectl -n %s delete pvc %s %s   # frees the name; both PVs survive, Retained\n"+
			"     kubectl patch pv $NEW -p '{\"spec\":{\"claimRef\":null}}'", ns, pvc, tmp),
		fmt.Sprintf(" 6  kubectl -n %s apply -f -   # PVC %s again — same name, %dGi, volumeName: $NEW", ns, pvc, target),
	}
	if kind == "StatefulSet" {
		steps = append(steps, fmt.Sprintf(
			" 7  kubectl -n %s delete statefulset %s --cascade=orphan   # volumeClaimTemplates are IMMUTABLE\n"+
				"     recreate it with volumeClaimTemplates storage: %dGi", ns, name, target))
	}
	steps = append(steps,
		fmt.Sprintf(" %d  scale back up. Verify the pod is Ready and serving from the copy.", len(steps)+1),
		fmt.Sprintf(" %d  ONLY THEN: kubectl delete pv %s, which releases DigitalOcean volume %s (%s).",
			len(steps)+2, v.PV, v.Name, v.ID))

	return fmt.Sprintf(
		"Right-size %d GiB → %d GiB (measured usage %s, kept doubled as headroom): saves %s/mo.\n\n"+
			"DigitalOcean cannot shrink a volume, so this is a copy-and-swap migration of live data, "+
			"not a setting. This board will not run it for you. Take a maintenance window.\n\n"+
			"SIZE IT YOURSELF. That %d GiB is arithmetic on ONE instantaneous reading — this board "+
			"keeps no history and cannot see how fast the data grows. Check the workload's growth "+
			"rate before committing to a number you can only increase again.\n\n%s",
		v.SizeGiB, target, gibLabel(v.UsedBytes),
		usd(money.Cents(v.SizeGiB-target)*volumeGiBCents), target,
		strings.Join(steps, "\n"))
}

// findings is the audit pass: what a human should look at, worst first.
func findings(s Snapshot, scans []ClusterScan, nameByID map[string]string) []Finding {
	out := []Finding{}

	if !s.Complete {
		out = append(out, Finding{
			ID: "scan-incomplete", Severity: SevCritical, Kind: "scan-incomplete",
			Title:  "Cluster scan incomplete — deletion disabled",
			Detail: s.IncompleteReason,
		})
	}

	for _, v := range s.Volumes {
		switch {
		case v.State == StateUnreferenced && s.Complete:
			out = append(out, Finding{
				ID: "unref/" + v.ID, Severity: SevWarn, Kind: "unreferenced-volume",
				Title: fmt.Sprintf("Unreferenced volume %s (%d GiB)", v.Name, v.SizeGiB),
				Detail: "No PersistentVolume in any cluster references this volume. " +
					"Verified against every cluster, so it is safe to snapshot and delete.",
				Resource: v.ID, MonthlyCents: v.MonthlyCents,
			})
		case v.State == StateReleased:
			out = append(out, Finding{
				ID: "released/" + v.ID, Severity: SevWarn, Kind: "released-pv",
				Title:    fmt.Sprintf("Released PV holding %s (%d GiB)", v.Name, v.SizeGiB),
				Detail:   fmt.Sprintf("PV %s is %s. Retire the PV to release the volume.", v.PV, v.PVPhase),
				Resource: v.ID, Cluster: v.Cluster, MonthlyCents: v.MonthlyCents,
			})
		case v.Idle:
			out = append(out, Finding{
				ID: "idle/" + v.ID, Severity: SevInfo, Kind: "idle-pvc",
				Title: fmt.Sprintf("Idle volume %s (%d GiB) — no pod mounts it", v.Name, v.SizeGiB),
				Detail: fmt.Sprintf("PVC %s/%s is Bound but no running pod mounts it. "+
					"REVIEW ONLY: this is live data (typically a stopped database), not garbage.",
					v.PVCNamespace, v.PVCName),
				Resource: v.ID, Cluster: v.Cluster, MonthlyCents: v.MonthlyCents,
			})
		}
		// Oversizing is orthogonal to the state machine above — the worst offenders are
		// ATTACHED and perfectly healthy, so this is its own test, not another case.
		// MonthlyCents carries what right-sizing would actually SAVE, not the raw waste:
		// the findings list sorts by money, and the money there has to be money you could
		// really get back.
		if target, worth := rightSize(v); worth {
			out = append(out, Finding{
				ID: "oversized/" + v.ID, Severity: SevWarn, Kind: "oversized-volume",
				Title: fmt.Sprintf("%s is %s empty — %s GiB provisioned, %s used",
					v.Name, shareLabel(money.Cents(v.WastedGiB), money.Cents(v.SizeGiB)),
					fmt.Sprint(v.SizeGiB), gibLabel(v.UsedBytes)),
				Detail:   shrinkRecipe(v, target),
				Resource: v.ID, Cluster: v.Cluster,
				MonthlyCents: money.Cents(v.SizeGiB-target) * volumeGiBCents,
			})
		}
	}

	// Unhealthy pods + unknown images, per cluster.
	type imgSeen struct {
		pods    int
		cluster string
	}
	unknown := map[string]*imgSeen{}
	for _, sc := range scans {
		if sc.Err != nil {
			continue
		}
		cname := nameByID[sc.ClusterID]
		for _, p := range sc.Pods {
			if bad := podProblem(p); bad != "" {
				out = append(out, Finding{
					ID: "pod/" + sc.ClusterID + "/" + p.Namespace + "/" + p.Name, Severity: SevWarn,
					Kind: "pod-unhealthy", Title: fmt.Sprintf("Pod %s/%s is %s", p.Namespace, p.Name, bad),
					Detail:   fmt.Sprintf("Phase %s%s on node %s.", p.Phase, reasonSuffix(p.Reason), p.Node),
					Resource: p.Namespace + "/" + p.Name, Cluster: cname,
				})
			}
			for _, img := range p.Images {
				if knownImage(img) {
					continue
				}
				repo := imageRepo(img)
				if e, ok := unknown[repo]; ok {
					e.pods++
				} else {
					unknown[repo] = &imgSeen{pods: 1, cluster: cname}
				}
			}
		}
	}
	for repo, e := range unknown {
		out = append(out, Finding{
			ID: "image/" + repo, Severity: SevWarn, Kind: "unknown-image",
			Title:    "Unrecognised container image: " + repo,
			Detail:   fmt.Sprintf("Run by %d pod(s), from neither our registries nor the reviewed vendor set.", e.pods),
			Resource: repo, Cluster: e.cluster,
		})
	}

	// Cost outliers: any single resource at or above outlierShareBP of total spend.
	if s.Cost.TotalMonthly > 0 {
		threshold := s.Cost.TotalMonthly * outlierShareBP / 10000
		for _, n := range s.Nodes {
			if n.MonthlyCents >= threshold {
				out = append(out, Finding{
					ID: "cost/node/" + n.Name, Severity: SevInfo, Kind: "cost-outlier",
					Title:    fmt.Sprintf("Node %s is %s of fleet spend", n.Name, shareLabel(n.MonthlyCents, s.Cost.TotalMonthly)),
					Detail:   fmt.Sprintf("%s, %d vCPU / %d MiB.", n.SizeSlug, n.VCPUs, n.MemoryMiB),
					Resource: n.Name, Cluster: n.Cluster, MonthlyCents: n.MonthlyCents,
				})
			}
		}
		for _, v := range s.Volumes {
			if v.MonthlyCents >= threshold {
				out = append(out, Finding{
					ID: "cost/volume/" + v.ID, Severity: SevInfo, Kind: "cost-outlier",
					Title:    fmt.Sprintf("Volume %s is %s of fleet spend", v.Name, shareLabel(v.MonthlyCents, s.Cost.TotalMonthly)),
					Detail:   fmt.Sprintf("%d GiB, %s.", v.SizeGiB, v.State),
					Resource: v.ID, Cluster: v.Cluster, MonthlyCents: v.MonthlyCents,
				})
			}
		}
	}

	rank := map[string]int{SevCritical: 0, SevWarn: 1, SevInfo: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].MonthlyCents != out[j].MonthlyCents {
			return out[i].MonthlyCents > out[j].MonthlyCents
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// podProblem names the failure a pod is in, or "" when it is fine.
func podProblem(p PodRef) string {
	switch {
	case strings.EqualFold(p.Reason, "Evicted"):
		return "Evicted"
	case strings.EqualFold(p.Phase, "Failed"):
		return "Failed"
	case strings.Contains(p.Reason, "CrashLoopBackOff"):
		return "CrashLoopBackOff"
	case strings.Contains(p.Reason, "ImagePullBackOff"), strings.Contains(p.Reason, "ErrImagePull"):
		return "ImagePullBackOff"
	}
	return ""
}

func reasonSuffix(r string) string {
	if strings.TrimSpace(r) == "" {
		return ""
	}
	return " (" + r + ")"
}

// knownImage reports whether an image comes from our registries or the reviewed
// vendor set.
func knownImage(img string) bool {
	l := strings.ToLower(strings.TrimSpace(img))
	l = strings.TrimPrefix(l, "docker.io/")
	for _, p := range firstParty {
		if strings.HasPrefix(l, strings.TrimPrefix(p, "docker.io/")) {
			return true
		}
	}
	for _, p := range knownVendors {
		if strings.HasPrefix(l, strings.TrimPrefix(p, "docker.io/")) {
			return true
		}
	}
	// A bare `name:tag` with no slash is an official Docker Hub library image.
	return !strings.Contains(strings.SplitN(l, ":", 2)[0], "/")
}

// imageRepo strips the tag/digest so findings group by repository, not by build.
func imageRepo(img string) string {
	s := strings.TrimSpace(img)
	if i := strings.Index(s, "@"); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s = s[:i]
	}
	return s
}

// usd renders cents as dollars WITHOUT going through a float — the same integer-cents
// discipline the arithmetic keeps, kept through formatting too.
func usd(c money.Cents) string { return fmt.Sprintf("$%d.%02d", c/100, c%100) }

// gibLabel renders a measured byte count with the sub-GiB precision that is the whole
// point of measuring: "0.5 GiB", never the "0 GiB" an integer conversion would print.
func gibLabel(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/gib) }

// shareLabel renders a cents-of-total share as a percentage.
func shareLabel(part, total money.Cents) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(total))
}

// clusterIDFromTags extracts the DOKS cluster UUID from a `k8s:<uuid>` resource tag.
// On droplets this is authoritative (DOKS owns the droplet); on VOLUMES it is
// advisory only — see the Volume.TagCluster doc.
func clusterIDFromTags(tags []string) string {
	for _, t := range tags {
		v := strings.TrimPrefix(t, "k8s:")
		if v == t || v == "" {
			continue
		}
		// Cluster tags are UUIDs; DOKS also stamps role tags like `k8s:worker`.
		if len(v) == 36 && strings.Count(v, "-") == 4 {
			return v
		}
	}
	return ""
}

func claimKey(clusterID, ns, name string) string { return clusterID + "/" + ns + "/" + name }

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilInts(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}
