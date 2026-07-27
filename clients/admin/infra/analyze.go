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
// healthy, which PVCs it mounts, and what images it runs.
type PodRef struct {
	Namespace string
	Name      string
	Phase     string
	Reason    string
	Node      string
	Claims    []string
	Images    []string
}

// NodeState is one Kubernetes node's own view of itself.
type NodeState struct {
	Name        string
	Ready       bool
	Schedulable bool
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
}

// Cost is monthly spend in cents. Reclaimable counts ONLY unreferenced volumes —
// never idle ones, which are live data awaiting a human verdict.
type Cost struct {
	DropletsMonthly      money.Cents `json:"dropletsMonthly"`
	VolumesMonthly       money.Cents `json:"volumesMonthly"`
	LoadBalancersMonthly money.Cents `json:"loadBalancersMonthly"`
	TotalMonthly         money.Cents `json:"totalMonthly"`
	ReclaimableMonthly   money.Cents `json:"reclaimableMonthly"`
}

// Cluster is one DOKS cluster with its scanned Kubernetes rollup.
type Cluster struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Region       string      `json:"region"`
	Version      string      `json:"version"`
	Status       string      `json:"status"`
	NodePools    int         `json:"nodePools"`
	Nodes        int         `json:"nodes"`
	Pods         int         `json:"pods"`
	PVs          int         `json:"pvs"`
	PVCs         int         `json:"pvcs"`
	IdlePVCs     int         `json:"idlePVCs"`
	Scanned      bool        `json:"scanned"`
	ScanError    string      `json:"scanError"`
	MonthlyCents money.Cents `json:"monthlyCents"`
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
	TagCluster    string   `json:"tagCluster"`
	PV            string   `json:"pv"`
	PVPhase       string   `json:"pvPhase"`
	PVCNamespace  string   `json:"pvcNamespace"`
	PVCName       string   `json:"pvcName"`
	MountedBy     []string `json:"mountedBy"`
	Idle          bool     `json:"idle"`
	CreatedAt     string   `json:"createdAt"`
	Deletable     bool     `json:"deletable"`
	BlockedReason string   `json:"blockedReason"`
}

// LoadBalancer is one DO load balancer, attributed to a cluster via its members.
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
		snap.Nodes = append(snap.Nodes, Node{
			ID: d.ID, Name: d.Name, Cluster: nameByID[cid], ClusterID: cid,
			Region: d.Region, Status: d.Status, SizeSlug: d.SizeSlug,
			VCPUs: d.VCPUs, MemoryMiB: d.MemoryMiB, LocalDiskGiB: d.LocalDiskGiB,
			MonthlyCents: d.MonthlyCents, CreatedAt: d.CreatedAt,
			PrivateIP: d.PrivateIP, PublicIP: d.PublicIP, Tags: nonNilStrings(d.Tags),
			Ready: ks.Ready, Schedulable: ks.Schedulable,
			Pods: podsPerNode[d.Name], Volumes: volsPerDroplet[d.ID],
		})
		snap.Cost.DropletsMonthly += d.MonthlyCents
		snap.Totals.LocalDiskGiB += d.LocalDiskGiB
	}
	dropletName := make(map[int]string, len(inv.Droplets))
	for _, d := range inv.Droplets {
		dropletName[d.ID] = d.Name
	}

	// ---- volumes: the state machine ----------------------------------------------
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
				vol.MountedBy = nonNilStrings(mounted[claimKey(hit.clusterID, hit.pv.ClaimNS, hit.pv.ClaimName)])
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

		vol.Deletable = snap.Complete && vol.State == StateUnreferenced
		vol.BlockedReason = blockedReason(vol, snap.Complete, snap.IncompleteReason)

		snap.Cost.VolumesMonthly += vol.MonthlyCents
		snap.Totals.VolumeGiB += vol.SizeGiB
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

	// ---- load balancers ----------------------------------------------------------
	snap.LoadBalancers = make([]LoadBalancer, 0, len(inv.LoadBalancers))
	for _, l := range inv.LoadBalancers {
		lb := LoadBalancer{
			ID: l.ID, Name: l.Name, Region: l.Region, Status: l.Status, IP: l.IP,
			SizeUnit: l.SizeUnit, MonthlyCents: l.MonthlyCents, Droplets: len(l.DropletIDs),
		}
		for _, id := range l.DropletIDs {
			if cid := clusterByDroplet[id]; cid != "" {
				lb.Cluster = nameByID[cid]
				break
			}
		}
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
			Status: c.Status, NodePools: c.NodePools, Nodes: nodesByCluster[c.ID],
			IdlePVCs: idleByCluster[c.ID], MonthlyCents: costByCluster[c.ID],
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

// blockedReason states, in the operator's language, exactly why a volume may not be
// deleted. An empty string means it may.
func blockedReason(v Volume, complete bool, incomplete string) string {
	if v.Deletable {
		return ""
	}
	switch {
	case !complete:
		return incomplete
	case v.State == StateAttached:
		if v.NodeName != "" {
			return "Attached to " + v.NodeName + " and in use."
		}
		return "Attached to a droplet and in use."
	case v.State == StateBound:
		return fmt.Sprintf("Live data: PV %s is Bound to %s/%s in %s.", v.PV, v.PVCNamespace, v.PVCName, v.Cluster)
	case v.State == StateReleased:
		return fmt.Sprintf("PV %s in %s still references it (%s) — retire the PV first.", v.PV, v.Cluster, v.PVPhase)
	}
	return "Not eligible for deletion."
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
