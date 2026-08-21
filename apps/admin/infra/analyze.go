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

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/admin/money"
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
	// At is when the scan ran, RFC3339 in UTC. Analyze takes it as an argument instead of
	// reading a clock, which is what makes the whole fold byte-reproducible in a test.
	At string `json:"at"`
	// Complete reports that EVERY cluster answered. It is the gate in front of every
	// destructive verdict on this board: while it is false nothing is deletable,
	// expandable or scalable, because the cluster that stayed silent is exactly the one
	// that might be holding the volume.
	Complete bool `json:"complete"`
	// IncompleteReason names the clusters that did not answer and what their silence
	// costs, in the operator's language. Empty exactly when Complete is true, and it is
	// the same sentence every blocked row then repeats as its own BlockedReason.
	IncompleteReason string `json:"incompleteReason"`
	// Sources is one freshness row per upstream read: `do.clusters`, `do.droplets`,
	// `do.volumes`, `do.loadBalancers`, and `k8s.<cluster name>` for each cluster scanned.
	// A row with ok=false is usually why Complete is false.
	Sources []core.SourceStatus `json:"sources"`
	// Totals are the fleet counts — how many of each thing, and how much capacity.
	Totals Totals `json:"totals"`
	// Cost is what the fleet bills per month, in cents, plus what could be recovered.
	Cost Cost `json:"cost"`
	// Clusters is every DOKS cluster in the account, each with the rollup of its scan.
	// A cluster DO reports appears here even when its Kubernetes never answered.
	Clusters []Cluster `json:"clusters"`
	// Nodes is EVERY droplet in the account, not only cluster members. A droplet no
	// cluster claims has an empty cluster and is the only kind this board will delete.
	Nodes []Machine `json:"nodes"`
	// Volumes is every block-storage volume, each carrying the state machine's verdict
	// on who owns it. It is the list the safety rule exists for.
	Volumes []Volume `json:"volumes"`
	// LoadBalancers is every DO load balancer, attributed to a cluster by the Service
	// that claims it, or failing that by its member droplets.
	LoadBalancers []LoadBalancer `json:"loadBalancers"`
	// Findings is the audit pass over everything above, sorted worst first: severity,
	// then money, then id. Empty means the fold found nothing worth a human's attention.
	Findings []Finding `json:"findings"`
}

// Totals are fleet counts. LocalDiskGiB is broken out precisely so it can be shown
// as NOT separately billed.
type Totals struct {
	// Clusters is how many DOKS clusters DigitalOcean reports, scanned or not. It is the
	// denominator the completeness gate counts unanswered clusters against.
	Clusters int `json:"clusters"`
	// Nodes is every droplet in the account, cluster member or not — the same population
	// as Snapshot.nodes, which is a superset of the machines Kubernetes knows about.
	Nodes int `json:"nodes"`
	// Volumes is every block-storage volume in the account.
	Volumes int `json:"volumes"`
	// LoadBalancers is every DO load balancer in the account.
	LoadBalancers int `json:"loadBalancers"`
	// VolumeGiB is the PROVISIONED block-storage capacity, in whole GiB — the number
	// DigitalOcean bills for. It excludes droplet local disk entirely; see LocalDiskGiB.
	VolumeGiB int `json:"volumeGiB"`
	// AttachedVolumes counts the volumes DO reports attached to a droplet right now.
	AttachedVolumes int `json:"attachedVolumes"`
	// AttachedGiB is the provisioned capacity of the attached volumes, in GiB.
	AttachedGiB int `json:"attachedGiB"`
	// DetachedVolumes is the exact complement of AttachedVolumes — bound, released and
	// unreferenced together, so the two always sum to Volumes. Detached is NOT idle and
	// NOT garbage: most of these are live data between mounts.
	DetachedVolumes int `json:"detachedVolumes"`
	// DetachedGiB is the provisioned capacity of the detached volumes, in GiB. With
	// AttachedGiB it sums to VolumeGiB.
	DetachedGiB int `json:"detachedGiB"`
	// UnreferencedVolumes counts the volumes no PV in any cluster names — the deletable
	// set, and the only one. It is a count of CANDIDATES: whether the button is live
	// still depends on Complete.
	UnreferencedVolumes int `json:"unreferencedVolumes"`
	// UnreferencedGiB is the provisioned capacity of the unreferenced volumes, in GiB —
	// the space a delete would hand back.
	UnreferencedGiB int `json:"unreferencedGiB"`
	// IdlePVCs counts volumes whose PVC is Bound but which no running pod mounts. A
	// review queue, never a delete queue — the usual member is a stopped database — and
	// deliberately excluded from ReclaimableMonthly.
	IdlePVCs int `json:"idlePVCs"`
	// LocalDiskGiB is the droplets' own disk, summed. Broken out precisely so it is never
	// added to VolumeGiB: it is included in each droplet's own monthly price and DO does
	// not bill it as block storage.
	LocalDiskGiB int `json:"localDiskGiB"`

	// Fill. MeasuredVolumes/UnmeasuredVolumes are the honesty denominator: UsedGiB and
	// WastedGiB describe the measured set ONLY, so a board showing waste must show how
	// much of the fleet the figure was computed from. Unmeasured capacity contributes
	// nothing to either — it is not assumed empty, and it is not assumed full.
	MeasuredVolumes int `json:"measuredVolumes"`
	// MeasuredGiB is the PROVISIONED capacity of the measured volumes — how much of the
	// fleet UsedGiB and WastedGiB were computed from, not how much of it is full.
	MeasuredGiB int `json:"measuredGiB"`
	// UnmeasuredVolumes counts volumes no kubelet reported a reading for. Not measured is
	// a different fact from empty, so these contribute to neither UsedGiB nor WastedGiB.
	UnmeasuredVolumes int `json:"unmeasuredVolumes"`
	// UnmeasuredGiB is the provisioned capacity of the unmeasured volumes, in GiB — the
	// size of the blind spot behind UsedGiB and WastedGiB.
	UnmeasuredGiB int `json:"unmeasuredGiB"`
	// UsedGiB is what the measured filesystems actually hold. Summed in BYTES and
	// converted once at the end, so hundreds of sub-GiB readings do not each round to
	// zero on the way in.
	UsedGiB int `json:"usedGiB"`
	// WastedGiB is provisioned minus measured across the measured set, in billed GiB. A
	// LOWER BOUND on fleet waste, and not the same money as ReclaimableMonthly: this
	// space sits inside volumes holding live data. See Cost.wastedMonthly.
	WastedGiB int `json:"wastedGiB"`
}

// Cost is monthly spend in cents. Reclaimable counts ONLY unreferenced volumes —
// never idle ones, which are live data awaiting a human verdict.
type Cost struct {
	// DropletsMonthly is DigitalOcean's list price for every droplet's size, summed.
	// The droplet's local disk is part of that price and is never billed again.
	DropletsMonthly money.Cents `json:"dropletsMonthly"`
	// VolumesMonthly is block storage at DO's rate of 10 cents per provisioned GiB per
	// month, summed over every volume — attached, idle and orphaned alike, because DO
	// bills for capacity, not for use.
	VolumesMonthly money.Cents `json:"volumesMonthly"`
	// LoadBalancersMonthly is $12 per billed node per month, summed. DO's API does not
	// price load balancers, so this is derived from the size_unit it does report.
	LoadBalancersMonthly money.Cents `json:"loadBalancersMonthly"`
	// TotalMonthly is droplets + volumes + load balancers. It is the fleet's recurring
	// bill, not the DigitalOcean invoice: bandwidth, snapshots, spaces and registry are
	// not inventory this board reads. WastedMonthly is already inside VolumesMonthly and
	// is never added again.
	TotalMonthly money.Cents `json:"totalMonthly"`
	// ReclaimableMonthly is what deleting the unreferenced volumes would stop costing —
	// the one figure on this board that a button here can actually collect. Zero while
	// Complete is false, because an unproven orphan is not an orphan.
	ReclaimableMonthly money.Cents `json:"reclaimableMonthly"`
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
	// ID is the DOKS cluster UUID — the same UUID that appears in a resource's
	// `k8s:<uuid>` tag, and the key every scan and every proven ownership joins on.
	ID string `json:"id"`
	// Name is the cluster's DO name ("hanzo-k8s"). It is what every human-facing message
	// on this board names a cluster by, including the refusal reasons.
	Name string `json:"name"`
	// Region is DO's region slug ("nyc3"). A volume can only ever attach to a droplet in
	// its own region, which is why the region rides along on the row.
	Region string `json:"region"`
	// Version is the DOKS Kubernetes version DO reports ("1.31.1-do.0").
	Version string `json:"version"`
	// Status is DOKS's own lifecycle state for the control plane ("running",
	// "provisioning", "degraded", "error", "deleting"). It says nothing about whether
	// this board reached the cluster — Scanned answers that.
	Status string `json:"status"`
	// NodePools is how many pools the cluster has, which is len(pools).
	NodePools int `json:"nodePools"`
	// Pools is the pools themselves, each carrying its own scale verdict.
	Pools []NodePool `json:"pools"`
	// Nodes is how many droplets carry this cluster's tag. It comes from the DO
	// inventory, so it is populated even for a cluster that never answered.
	Nodes int `json:"nodes"`
	// Pods is how many pods the scan listed, all namespaces. Zero when Scanned is false —
	// unknown, not empty.
	Pods int `json:"pods"`
	// PVs is how many PersistentVolumes the scan listed. These are the claims that make a
	// DO volume live; zero here with Scanned false is what forces the whole board
	// incomplete.
	PVs int `json:"pvs"`
	// PVCs is how many PersistentVolumeClaims the scan listed.
	PVCs int `json:"pvcs"`
	// IdlePVCs is how many of this cluster's volumes are Bound with no pod mounting them
	// — its share of the review queue, not of the delete queue.
	IdlePVCs int `json:"idlePVCs"`
	// Scanned reports that this cluster's Kubernetes answered. False makes the whole
	// board incomplete, not just this row: a cluster that did not answer might hold any
	// volume on it.
	Scanned bool `json:"scanned"`
	// ScanError is why the scan failed, or "not scanned" when the cluster was never
	// reached at all. Empty exactly when Scanned is true.
	ScanError string `json:"scanError"`
	// MonthlyCents is this cluster's share of the bill: its droplets plus the volumes
	// PROVEN to belong to it. Load balancers are not included — a Service's load balancer
	// is attributed on the load-balancer row, not folded in here, so cluster costs never
	// double-count against Cost.totalMonthly.
	MonthlyCents money.Cents `json:"monthlyCents"`
}

// NodePool is one DOKS node pool — the only correct place to change a cluster's node
// count. ClusterSchedulable is the whole cluster's schedulable-and-ready node count,
// carried on the row so the shrink verdict is a pure method of it.
type NodePool struct {
	// ID is the DO node pool UUID. The scale route accepts it or Name interchangeably,
	// since both are unique within a cluster.
	ID string `json:"id"`
	// Name is the pool's name ("workers") — what an operator reads off the board and
	// what every message about a scale names.
	Name string `json:"name"`
	// Size is the DigitalOcean size slug every node in this pool is created at
	// ("s-4vcpu-8gb"). One slug per pool: it is the pool, not the droplet, that declares
	// what its machines are, which is why a hand-resized node gets reverted.
	Size string `json:"size"`
	// Count is how many nodes the pool is currently set to. It is the DECLARED count, so
	// it can lead reality while DOKS creates or drains machines.
	Count int `json:"count"`
	// ClusterID is the DOKS cluster this pool belongs to.
	ClusterID string `json:"clusterId"`
	// Cluster is that cluster's name, carried on the row so a refusal can say which
	// cluster a shrink would strand.
	Cluster string `json:"cluster"`
	// ClusterSchedulable is how many nodes in the WHOLE cluster are both Ready and
	// schedulable — not just this pool's. Cordoned and NotReady nodes are excluded,
	// because a shrink's evicted pods cannot land on them. It rides on the row so
	// ScaleTo is a pure method of the row and needs no second lookup.
	ClusterSchedulable int `json:"clusterSchedulable"`
	// Scalable is the standing verdict: whether a scale may be attempted at all. It
	// reflects the shared completeness gate ONLY. Whether a PARTICULAR count is allowed
	// depends on the number asked for, which ScaleTo decides at request time.
	Scalable bool `json:"scalable"`
	// BlockedReason is why not, in the operator's language. Empty exactly when Scalable
	// is true — allowed carries no reason, blocked always names one.
	BlockedReason string `json:"blockedReason"`
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

// Machine is one droplet, joined to the Kubernetes node of the same name.
type Machine struct {
	// ID is the DigitalOcean droplet id. Numeric, and the id every droplet route takes.
	ID int `json:"id"`
	// Name is the droplet name. For a DOKS node it is also the Kubernetes node name, and
	// that equality is the join: it is how Ready, Schedulable and Pods below get filled.
	Name string `json:"name"`
	// Cluster is the DOKS cluster's name, or "" for a droplet no cluster owns — the only
	// kind this board will ever delete.
	Cluster string `json:"cluster"`
	// ClusterID is that cluster's UUID, read from the droplet's own `k8s:<uuid>` tag. On
	// a DROPLET the tag is authoritative, unlike on a volume: DOKS creates the droplet
	// and destroys it with the pool, so the tag cannot outlive what it describes.
	ClusterID string `json:"clusterId"`
	// Region is DO's region slug ("nyc3").
	Region string `json:"region"`
	// Status is DO's droplet state ("new", "active", "off", "archive"). A powered-off
	// droplet still bills, which is why "off" here and a cost row are not in tension.
	Status string `json:"status"`
	// SizeSlug is the DigitalOcean plan ("s-4vcpu-8gb"). It decides MonthlyCents and is
	// what a resize sets.
	SizeSlug string `json:"sizeSlug"`
	// VCPUs is the plan's virtual CPU count.
	VCPUs int `json:"vcpus"`
	// MemoryMiB is the plan's RAM, in MiB as DO reports it.
	MemoryMiB int `json:"memoryMiB"`
	// LocalDiskGiB is the droplet's own disk, in GiB. Its price is already inside
	// MonthlyCents — DO does not bill it as block storage, and adding it to the volume
	// totals is how a fleet appears to hold terabytes it never pays for.
	LocalDiskGiB int `json:"localDiskGiB"`
	// MonthlyCents is DO's list price for SizeSlug, in cents per month. It is the plan's
	// price, not a metered bill, so it does not move with how hard the machine works.
	MonthlyCents money.Cents `json:"monthlyCents"`
	// CreatedAt is when DO created the droplet, RFC3339.
	CreatedAt string `json:"createdAt"`
	// PrivateIP is the droplet's VPC address, or "" when it has none.
	PrivateIP string `json:"privateIp"`
	// PublicIP is the droplet's internet-facing address, or "" when it has none.
	PublicIP string `json:"publicIp"`
	// Tags are DO's resource tags verbatim, including the DOKS stamps `k8s:<uuid>` and
	// `k8s:worker`. Never null — an untagged droplet carries an empty list.
	Tags []string `json:"tags"`
	// Ready is the Kubernetes node's own Ready condition, joined by Name. False for a
	// droplet that is not a node at all, and for a node in a cluster that did not answer
	// — unknown reads as not-ready, which refuses more and is the safe direction.
	Ready bool `json:"ready"`
	// Schedulable is the same node's cordon state, inverted: false means unschedulable.
	// It is what the cordon route sets, and only a Ready AND schedulable node counts
	// toward NodePool.clusterSchedulable.
	Schedulable bool `json:"schedulable"`
	// Pods is how many pods the scan found scheduled on this node.
	Pods int `json:"pods"`
	// Volumes is how many block-storage volumes DO reports attached to this droplet.
	Volumes int `json:"volumes"`
	// Mutable reports whether this droplet may be changed DIRECTLY — deleted or resized.
	// One predicate covers both because one fact decides both: a DOKS node belongs to a
	// node pool, and the pool is the only thing allowed to change it.
	Mutable bool `json:"mutable"`
	// BlockedReason is why not, in the operator's language, and it names the lever that
	// DOES work — scale or edit the node pool. Empty exactly when Mutable is true.
	BlockedReason string `json:"blockedReason"`
}

// Volume is one block-storage volume with its PROVEN cluster ownership.
type Volume struct {
	// ID is the DigitalOcean volume id. It is also the `spec.csi.volumeHandle` a
	// PersistentVolume names, which is what makes the cross-cluster liveness test an
	// exact match rather than a guess.
	ID string `json:"id"`
	// Name is the DO volume name. DOKS-provisioned volumes are named `pvc-<uuid>`, which
	// is a naming convention and NOT evidence of ownership — only a PV reference is.
	Name string `json:"name"`
	// Region is DO's region slug ("nyc3"). A volume can only ever attach to a droplet in
	// its own region, so this bounds where it could possibly be in use.
	Region string `json:"region"`
	// SizeGiB is the PROVISIONED size — what DigitalOcean bills. It is not the
	// filesystem's capacity, which is a few percent smaller after format overhead.
	SizeGiB int `json:"sizeGiB"`
	// MonthlyCents is SizeGiB at DO's rate of 10 cents per GiB per month. It is charged
	// on provisioned capacity, so an empty volume and a full one of the same size cost
	// exactly the same.
	MonthlyCents money.Cents `json:"monthlyCents"`
	// State is the ownership verdict, one of `attached`, `bound`, `released`,
	// `unreferenced`. The machine is total and ordered — attachment beats reference,
	// reference beats absence — and `unreferenced` is the ONLY deletable state.
	State string `json:"state"`
	// DropletIDs are the droplets DO reports this volume attached to. Non-empty is hard
	// kernel-level evidence the volume is in use right now. Never null.
	DropletIDs []int `json:"dropletIds"`
	// NodeName is the name of the first attached droplet, or "" when detached. Carried so
	// a refusal can name the machine an operator would have to go look at.
	NodeName string `json:"nodeName"`
	// Cluster/ClusterID are the PROVEN owner — resolved through a PV that names this
	// volume, never through the tag.
	Cluster string `json:"cluster"`
	// ClusterID is that cluster's UUID. Falls back to the cluster of the droplet the
	// volume is physically attached to, which is evidence of the same quality: live
	// kernel state, not a label someone wrote once.
	ClusterID string `json:"clusterId"`
	// TagCluster is the `k8s:<uuid>` tag. ADVISORY ONLY: it outlives the cluster that
	// set it. Shown so the operator can see tag-vs-truth disagree, never acted on.
	TagCluster string `json:"tagCluster"`
	// PV is the name of the PersistentVolume that claims this volume, or "" when no PV in
	// any scanned cluster does. Non-empty is the proof that makes it undeletable.
	PV string `json:"pv"`
	// PVPhase is that PV's phase — `Bound`, `Released`, `Available` or `Failed`. Bound
	// means live data; anything else means the claim is retired but the PV still exists,
	// so a human retires the PV before the volume can go.
	PVPhase string `json:"pvPhase"`
	// PVCNamespace is the namespace of the claim the PV is bound to, or "" when unbound.
	PVCNamespace string `json:"pvcNamespace"`
	// PVCName is that claim's name. It plus the namespace is what the fill reading and
	// the mounting pods are keyed by, so an empty PVCName means neither can exist.
	PVCName string `json:"pvcName"`
	// MountedBy lists the `namespace/name` of every running pod mounting this volume.
	// Empty on a Bound volume means idle — a review signal, never a delete signal.
	// Never null.
	MountedBy []string `json:"mountedBy"`
	// Idle is Bound with nothing mounting it: live data nobody is currently reading,
	// typically a stopped database. It is a queue for a human and is deliberately kept
	// out of Cost.reclaimableMonthly.
	Idle bool `json:"idle"`
	// CreatedAt is when DO created the volume, RFC3339. Age is corroboration for a human
	// judging an orphan, never an input to the deletable verdict.
	CreatedAt string `json:"createdAt"`
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
	WastedGiB int `json:"wastedGiB"`
	// WastedMonthlyCents is WastedGiB at the same 10 cents per GiB per month. Money the
	// fleet pays for empty space inside a volume that is IN USE — so it is not
	// collectable by any button here, only by the copy-and-swap migration the matching
	// finding spells out. Zero when HasUsage is false, which means unknown, not none.
	WastedMonthlyCents money.Cents `json:"wastedMonthlyCents"`

	// Deletable is the standing verdict: this volume is unreferenced AND the scan was
	// complete enough to prove it. It is the only thing the delete route consults, and it
	// is recomputed from a fresh scan at the moment the button is pressed.
	Deletable bool `json:"deletable"`
	// BlockedReason is why not, in the operator's language — which cluster's PV holds it,
	// or which clusters went unanswered. Empty exactly when Deletable is true.
	BlockedReason string `json:"blockedReason"`
	// Expandable/ExpandBlockedReason are the GROW verdict, kept separate from Deletable
	// because the two ask opposite questions: a volume is deletable when nothing uses it,
	// and expandable when something uses it in a way this board can grow completely.
	Expandable bool `json:"expandable"`
	// ExpandBlockedReason is why a grow is refused: a PV claims the volume but no PVC
	// does, so growing the device would leave the PV declaring a capacity that is wrong.
	// Empty exactly when Expandable is true.
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
	// ID is the DO load balancer UUID. DOKS writes it into the Service's
	// `kubernetes.digitalocean.com/load-balancer-id` annotation, which is one of the two
	// identities the liveness index is keyed by.
	ID string `json:"id"`
	// Name is DigitalOcean's own name for it. A DOKS-provisioned load balancer is named
	// after neither its Service nor its cluster, so this does not identify who uses it —
	// Service does.
	Name string `json:"name"`
	// Region is DO's region slug ("nyc3").
	Region string `json:"region"`
	// Status is DO's own state for it ("new", "active", "errored").
	Status string `json:"status"`
	// IP is the public address it answers on — the address that black-holes if it is
	// deleted while a Service still wants it. It is also the second identity the
	// liveness index matches on, for a Service that carries no annotation.
	IP string `json:"ip"`
	// SizeUnit is the number of billed load-balancer nodes DO reports. Never below 1: DO
	// omits the field on the legacy single-node size, which is one node, not zero.
	SizeUnit int `json:"sizeUnit"`
	// MonthlyCents is SizeUnit at $12 per node per month. Derived, because DO's API
	// prices droplets but not load balancers.
	MonthlyCents money.Cents `json:"monthlyCents"`
	// Droplets is how many droplets it forwards to. NOT a liveness signal: a DOKS load
	// balancer lists every node in its cluster, so a leaked one still looks busy.
	Droplets int `json:"droplets"`
	// Cluster is the owning cluster's name — from the claiming Service where there is
	// one, otherwise from the cluster its member droplets belong to.
	Cluster string `json:"cluster"`
	// Service is the `namespace/name` of the live type=LoadBalancer Service that claims
	// this load balancer, proven from the cluster scan. Non-empty means IN USE.
	Service string `json:"service"`
	// Deletable is the standing verdict: no live Service claims it, every member droplet
	// belongs to a cluster this board scanned, and the scan was complete.
	Deletable bool `json:"deletable"`
	// BlockedReason is why not — which Service still wants it, or how many of its member
	// droplets sit outside every cluster and so cannot be vouched for. Empty exactly when
	// Deletable is true.
	BlockedReason string `json:"blockedReason"`
}

// Finding is one audit result — the "is anything bad" surface.
type Finding struct {
	// ID is stable across scans and unique within one: `unref/<volume>`,
	// `oversized/<volume>`, `pod/<cluster>/<ns>/<name>`, `image/<repo>`,
	// `cost/node/<name>`. A consumer can key a dismissal off it.
	ID string `json:"id"`
	// Severity is one of `critical`, `warn`, `info`, and is the primary sort. Only an
	// incomplete scan is critical, because only that invalidates the board's other
	// answers.
	Severity string `json:"severity"`
	// Kind is the machine-readable class: `scan-incomplete`, `unreferenced-volume`,
	// `released-pv`, `idle-pvc`, `oversized-volume`, `pod-unhealthy`, `unknown-image`,
	// `cost-outlier`. Group and filter on this; Title is for a human.
	Kind string `json:"kind"`
	// Title is the one-line summary, already carrying the numbers that matter.
	Title string `json:"title"`
	// Detail is the full explanation. For an oversized volume it is the entire
	// copy-and-swap migration, written out as kubectl steps — this board prints the
	// recipe and deliberately refuses to run it.
	Detail string `json:"detail"`
	// Resource is what the finding is about, in that kind's own addressing: a volume id,
	// a `namespace/name` pod, an image repository. Empty on a fleet-wide finding.
	Resource string `json:"resource"`
	// Cluster is the cluster's name where the finding is scoped to one, "" otherwise.
	Cluster string `json:"cluster"`
	// MonthlyCents is the money at stake, and it is the secondary sort within a severity.
	// It carries what acting would actually SAVE, not the raw waste — an oversized volume
	// reports the saving after keeping headroom, since a list sorted by money has to be
	// sorted by money you could really get back. Zero when nothing is at stake.
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
	snap.Nodes = make([]Machine, 0, len(inv.Droplets))
	for _, d := range inv.Droplets {
		cid := clusterIDFromTags(d.Tags)
		clusterByDroplet[d.ID] = cid
		ks := nodeByName[d.Name]
		n := Machine{
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
func nodeBlock(n Machine) string {
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
	target := max(headroomMult*int((v.UsedBytes+gib-1)/gib), minTargetGiB)
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
