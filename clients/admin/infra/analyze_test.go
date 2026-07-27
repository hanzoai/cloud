package infra

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/admin/digitalocean"
)

// Two live clusters. The near-miss that motivated this package involved volumes tagged
// for one cluster whose PVs actually live in another, so every fixture here has two.
const (
	cidA = "aaaaaaaa-1111-2222-3333-444444444444"
	cidB = "bbbbbbbb-1111-2222-3333-444444444444"
)

func baseInventory() Inventory {
	return Inventory{
		Clusters: []digitalocean.Cluster{
			{ID: cidA, Name: "hanzo-k8s", Region: "sfo3", Status: "running"},
			{ID: cidB, Name: "lux-k8s", Region: "sfo3", Status: "running"},
		},
		Droplets: []digitalocean.Droplet{{
			ID: 101, Name: "node-a1", Region: "sfo3", Status: "active",
			SizeSlug: "s-8vcpu-16gb-amd", VCPUs: 8, MemoryMiB: 16384,
			LocalDiskGiB: 320, MonthlyCents: 11200,
			Tags: []string{"k8s", "k8s:" + cidA, "k8s:worker"},
		}},
	}
}

func scansOK() []ClusterScan {
	return []ClusterScan{{ClusterID: cidA}, {ClusterID: cidB}}
}

func volByID(t *testing.T, s Snapshot, id string) Volume {
	t.Helper()
	v, ok := findVolume(s, id)
	if !ok {
		t.Fatalf("volume %s missing from snapshot", id)
	}
	return v
}

func analyze(inv Inventory, scans []ClusterScan) Snapshot {
	return Analyze(inv, scans, nil, time.Unix(0, 0).UTC())
}

// TestCrossClusterPVProtectsMisTaggedVolume is THE regression test. A detached volume
// tagged `k8s:<cluster A>` whose PV actually lives in cluster B must be classified from
// the PV, not the tag. Trusting the tag here is what nearly destroyed 4.39 TiB.
func TestCrossClusterPVProtectsMisTaggedVolume(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{
		ID: "vol-mistagged", Name: "pvc-neon-pageserver", Region: "sfo3", SizeGiB: 50,
		DropletIDs: nil,                     // detached
		Tags:       []string{"k8s:" + cidA}, // tag says cluster A …
	}}
	scans := scansOK()
	// … but the PV that owns it lives in cluster B, and is Bound to a live PVC.
	scans[1].PVs = []PVRef{{
		Name: "pv-neon", Phase: "Bound", VolumeHandle: "vol-mistagged",
		ClaimNS: "neon", ClaimName: "pageserver-data",
	}}

	got := analyze(inv, scans)
	v := volByID(t, got, "vol-mistagged")

	if v.State != StateBound {
		t.Fatalf("state = %q, want %q — a detached, mis-tagged volume with a Bound PV is LIVE DATA", v.State, StateBound)
	}
	if v.Deletable {
		t.Fatal("volume marked deletable: this is the 4.39 TiB data-loss bug")
	}
	if v.Cluster != "lux-k8s" {
		t.Errorf("proven cluster = %q, want lux-k8s (from the PV, not the tag)", v.Cluster)
	}
	if v.TagCluster != "hanzo-k8s" {
		t.Errorf("tagCluster = %q, want hanzo-k8s (advisory, surfaced so tag-vs-truth is visible)", v.TagCluster)
	}
	if got.Cost.ReclaimableMonthly != 0 {
		t.Errorf("reclaimable = %d, want 0", got.Cost.ReclaimableMonthly)
	}
}

// TestIncompleteScanBlocksEveryDeletion: one unreachable cluster and NOTHING is
// deletable, even a volume no reachable cluster references. Absence of evidence is not
// evidence of absence.
func TestIncompleteScanBlocksEveryDeletion(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{ID: "vol-orphan", Name: "stray", SizeGiB: 100}}
	scans := scansOK()
	scans[1].Err = errors.New("dial tcp: i/o timeout")

	got := analyze(inv, scans)
	if got.Complete {
		t.Fatal("Complete = true with an unreachable cluster")
	}
	v := volByID(t, got, "vol-orphan")
	if v.State != StateUnreferenced {
		t.Errorf("state = %q, want %q (state is observable; the VERDICT is what is withheld)", v.State, StateUnreferenced)
	}
	if v.Deletable {
		t.Fatal("deletable with an incomplete scan — fail-closed violated")
	}
	if got.Cost.ReclaimableMonthly != 0 {
		t.Errorf("reclaimable = %d, want 0 when the scan is incomplete", got.Cost.ReclaimableMonthly)
	}
	if v.BlockedReason == "" || !strings.Contains(v.BlockedReason, "lux-k8s") {
		t.Errorf("blockedReason = %q, want it to name the unreachable cluster", v.BlockedReason)
	}
	if got.Findings[0].Kind != "scan-incomplete" || got.Findings[0].Severity != SevCritical {
		t.Errorf("first finding = %+v, want a critical scan-incomplete", got.Findings[0])
	}
}

// TestNoClustersIsIncomplete: an empty cluster list means the set of places a volume
// could be in use is unknown, which must not read as "referenced by nothing".
func TestNoClustersIsIncomplete(t *testing.T) {
	inv := Inventory{Volumes: []digitalocean.Volume{{ID: "v1", Name: "x", SizeGiB: 10}}}
	got := analyze(inv, nil)
	if got.Complete {
		t.Fatal("Complete = true with zero clusters")
	}
	if volByID(t, got, "v1").Deletable {
		t.Fatal("deletable with zero clusters known")
	}
}

// TestVolumeStateMachine covers the full ordering: attachment beats reference,
// reference beats absence, and only the unreferenced volume is ever deletable.
func TestVolumeStateMachine(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{
		{ID: "v-attached", Name: "attached", SizeGiB: 10, DropletIDs: []int{101}, Tags: []string{"k8s:" + cidA}},
		{ID: "v-bound", Name: "bound", SizeGiB: 20},
		{ID: "v-released", Name: "released", SizeGiB: 30},
		{ID: "v-unref", Name: "unref", SizeGiB: 40},
		// Attached AND referenced by a Bound PV: attachment wins.
		{ID: "v-both", Name: "both", SizeGiB: 50, DropletIDs: []int{101}},
	}
	scans := scansOK()
	scans[0].PVs = []PVRef{
		{Name: "pv-bound", Phase: "Bound", VolumeHandle: "v-bound", ClaimNS: "ns", ClaimName: "c1"},
		{Name: "pv-rel", Phase: "Released", VolumeHandle: "v-released", ClaimNS: "ns", ClaimName: "c2"},
		{Name: "pv-both", Phase: "Bound", VolumeHandle: "v-both", ClaimNS: "ns", ClaimName: "c3"},
	}
	scans[0].Pods = []PodRef{{Namespace: "ns", Name: "p1", Node: "node-a1", Claims: []string{"c1", "c3"}}}

	got := analyze(inv, scans)
	if !got.Complete {
		t.Fatalf("Complete = false, want true: %s", got.IncompleteReason)
	}
	for _, tc := range []struct {
		id, want  string
		deletable bool
	}{
		{"v-attached", StateAttached, false},
		{"v-bound", StateBound, false},
		{"v-released", StateReleased, false},
		{"v-unref", StateUnreferenced, true},
		{"v-both", StateAttached, false},
	} {
		v := volByID(t, got, tc.id)
		if v.State != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.id, v.State, tc.want)
		}
		if v.Deletable != tc.deletable {
			t.Errorf("%s: deletable = %v, want %v (reason %q)", tc.id, v.Deletable, tc.deletable, v.BlockedReason)
		}
		if !v.Deletable && v.BlockedReason == "" {
			t.Errorf("%s: not deletable but no reason given", tc.id)
		}
		if v.Deletable && v.BlockedReason != "" {
			t.Errorf("%s: deletable but carries reason %q", tc.id, v.BlockedReason)
		}
	}
	// Exactly one volume is reclaimable: 40 GiB × $0.10 = $4.00.
	if got.Cost.ReclaimableMonthly != 400 {
		t.Errorf("reclaimable = %d cents, want 400", got.Cost.ReclaimableMonthly)
	}
	// v-bound is mounted by p1; v-both is mounted by p1 too — neither is idle.
	if got.Totals.IdlePVCs != 0 {
		t.Errorf("idlePVCs = %d, want 0", got.Totals.IdlePVCs)
	}
}

// TestIdleIsReviewNotReclaimable: a Bound volume no pod mounts is flagged for review
// but is never deletable and never counted as money we can get back.
func TestIdleIsReviewNotReclaimable(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{ID: "v-idle", Name: "registry-data", SizeGiB: 50}}
	scans := scansOK()
	scans[1].PVs = []PVRef{{Name: "pv-idle", Phase: "Bound", VolumeHandle: "v-idle", ClaimNS: "registry", ClaimName: "registry-data"}}
	// No pod mounts registry-data.
	scans[1].Pods = []PodRef{{Namespace: "other", Name: "unrelated", Claims: []string{"something-else"}}}

	got := analyze(inv, scans)
	v := volByID(t, got, "v-idle")
	if !v.Idle {
		t.Fatal("idle = false, want true (Bound but unmounted)")
	}
	if v.Deletable {
		t.Fatal("an idle volume must never be deletable — it is a stopped database, not garbage")
	}
	if got.Cost.ReclaimableMonthly != 0 {
		t.Errorf("reclaimable = %d, want 0: idle capacity is not reclaimable", got.Cost.ReclaimableMonthly)
	}
	if got.Totals.IdlePVCs != 1 {
		t.Errorf("idlePVCs = %d, want 1", got.Totals.IdlePVCs)
	}
	var f *Finding
	for i := range got.Findings {
		if got.Findings[i].Kind == "idle-pvc" {
			f = &got.Findings[i]
		}
	}
	if f == nil {
		t.Fatal("no idle-pvc finding")
	}
	if f.Severity != SevInfo {
		t.Errorf("idle-pvc severity = %q, want info (a review queue, not an alarm)", f.Severity)
	}
	if !strings.Contains(f.Detail, "REVIEW ONLY") {
		t.Errorf("idle-pvc detail must say it is review-only, got %q", f.Detail)
	}
}

// TestLegacyFlexVolumePVProtects: a pre-CSI PV still shields its volume.
func TestLegacyFlexVolumePVProtects(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{ID: "v-flex", Name: "legacy", SizeGiB: 10}}
	scans := scansOK()
	scans[0].PVs = []PVRef{{Name: "pv-flex", Phase: "Bound", VolumeHandle: "v-flex", ClaimNS: "old", ClaimName: "data"}}
	if volByID(t, analyze(inv, scans), "v-flex").Deletable {
		t.Fatal("a flexVolume-referenced volume must not be deletable")
	}
}

// TestCostMathAndLocalDiskSeparation: block storage is billed per GiB; droplet local
// disk is included in the droplet price and must never be added to storage cost.
func TestCostMathAndLocalDiskSeparation(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{
		{ID: "a", Name: "a", SizeGiB: 200},
		{ID: "b", Name: "b", SizeGiB: 300},
	}
	inv.LoadBalancers = []digitalocean.LoadBalancer{
		{ID: "lb1", Name: "ingress", SizeUnit: 1, MonthlyCents: 1200, DropletIDs: []int{101}},
	}
	got := analyze(inv, scansOK())

	if got.Cost.VolumesMonthly != 5000 {
		t.Errorf("volumes = %d cents, want 5000 (500 GiB × $0.10)", got.Cost.VolumesMonthly)
	}
	if got.Cost.DropletsMonthly != 11200 {
		t.Errorf("droplets = %d cents, want 11200", got.Cost.DropletsMonthly)
	}
	if got.Cost.LoadBalancersMonthly != 1200 {
		t.Errorf("load balancers = %d cents, want 1200", got.Cost.LoadBalancersMonthly)
	}
	if got.Cost.TotalMonthly != 5000+11200+1200 {
		t.Errorf("total = %d cents, want %d", got.Cost.TotalMonthly, 5000+11200+1200)
	}
	// The 320 GiB of local disk is reported, but is NOT in any cost line.
	if got.Totals.LocalDiskGiB != 320 {
		t.Errorf("localDiskGiB = %d, want 320", got.Totals.LocalDiskGiB)
	}
	if got.Totals.VolumeGiB != 500 {
		t.Errorf("volumeGiB = %d, want 500 — local disk must never be folded into block storage", got.Totals.VolumeGiB)
	}
	if got.LoadBalancers[0].Cluster != "hanzo-k8s" {
		t.Errorf("lb cluster = %q, want hanzo-k8s (attributed via its member droplet)", got.LoadBalancers[0].Cluster)
	}
}

// TestNodeJoinAndClusterRollup: droplets join their Kubernetes node by name and roll
// up into the owning cluster.
func TestNodeJoinAndClusterRollup(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{ID: "v1", Name: "v1", SizeGiB: 10, DropletIDs: []int{101}}}
	scans := scansOK()
	scans[0].Nodes = []NodeState{{Name: "node-a1", Ready: true, Schedulable: false}}
	scans[0].Pods = []PodRef{
		{Namespace: "hanzo", Name: "p1", Node: "node-a1"},
		{Namespace: "hanzo", Name: "p2", Node: "node-a1"},
	}
	got := analyze(inv, scans)

	n := got.Nodes[0]
	if n.Cluster != "hanzo-k8s" || n.ClusterID != cidA {
		t.Errorf("node cluster = %q/%q, want hanzo-k8s", n.Cluster, n.ClusterID)
	}
	if !n.Ready || n.Schedulable {
		t.Errorf("node ready=%v schedulable=%v, want ready + cordoned", n.Ready, n.Schedulable)
	}
	if n.Pods != 2 || n.Volumes != 1 {
		t.Errorf("node pods=%d volumes=%d, want 2/1", n.Pods, n.Volumes)
	}
	var ca Cluster
	for _, c := range got.Clusters {
		if c.ID == cidA {
			ca = c
		}
	}
	if ca.Nodes != 1 || ca.Pods != 2 || !ca.Scanned {
		t.Errorf("cluster A rollup = %+v, want 1 node / 2 pods / scanned", ca)
	}
	// Node $112.00 + its 10 GiB volume $1.00.
	if ca.MonthlyCents != 11200+100 {
		t.Errorf("cluster monthly = %d, want 11300", ca.MonthlyCents)
	}
}

// TestUnknownImageDetection: our registries and the reviewed vendor set stay quiet;
// anything else is reported once per repository.
func TestUnknownImageDetection(t *testing.T) {
	inv := baseInventory()
	scans := scansOK()
	scans[0].Pods = []PodRef{{Namespace: "hanzo", Name: "p1", Images: []string{
		"ghcr.io/hanzoai/cloud:v1.801.218",
		"ghcr.io/luxfi/node:v1.2.3",
		"registry.k8s.io/pause:3.9",
		"grafana/grafana:11.0.0",
		"acmglobaltech/thing:1",
		"redis:7",                       // bare official library image
		"evil.example.com/miner:latest", // ← the only one that should be reported
	}}}
	got := analyze(inv, scans)

	var unknown []string
	for _, f := range got.Findings {
		if f.Kind == "unknown-image" {
			unknown = append(unknown, f.Resource)
		}
	}
	if len(unknown) != 1 || unknown[0] != "evil.example.com/miner" {
		t.Fatalf("unknown images = %v, want exactly [evil.example.com/miner]", unknown)
	}
}

// TestUnhealthyPodFindings covers the pod states the audit surfaces.
func TestUnhealthyPodFindings(t *testing.T) {
	inv := baseInventory()
	scans := scansOK()
	scans[0].Pods = []PodRef{
		{Namespace: "a", Name: "ok", Phase: "Running"},
		{Namespace: "a", Name: "gone", Phase: "Failed", Reason: "Evicted"},
		{Namespace: "a", Name: "loop", Phase: "Pending", Reason: "CrashLoopBackOff"},
		{Namespace: "a", Name: "pull", Phase: "Pending", Reason: "ImagePullBackOff"},
	}
	got := analyze(inv, scans)
	seen := map[string]bool{}
	for _, f := range got.Findings {
		if f.Kind == "pod-unhealthy" {
			seen[f.Resource] = true
		}
	}
	if len(seen) != 3 || !seen["a/gone"] || !seen["a/loop"] || !seen["a/pull"] {
		t.Fatalf("unhealthy pods = %v, want gone/loop/pull and not ok", seen)
	}
}

// TestClusterTagParsing: only a UUID-shaped k8s: tag is a cluster id; role tags are not.
func TestClusterTagParsing(t *testing.T) {
	for _, tc := range []struct {
		tags []string
		want string
	}{
		{[]string{"k8s", "k8s:" + cidA, "k8s:worker"}, cidA},
		{[]string{"k8s", "k8s:worker"}, ""},
		{[]string{"unrelated"}, ""},
		{nil, ""},
	} {
		if got := clusterIDFromTags(tc.tags); got != tc.want {
			t.Errorf("clusterIDFromTags(%v) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

// TestJSONArraysNeverNull: the console renders these directly; a null array is a crash.
func TestJSONArraysNeverNull(t *testing.T) {
	got := analyze(Inventory{}, nil)
	if got.Volumes == nil || got.Nodes == nil || got.Clusters == nil ||
		got.LoadBalancers == nil || got.Findings == nil || got.Sources == nil {
		t.Fatalf("empty snapshot has nil slices: %+v", got)
	}
}
