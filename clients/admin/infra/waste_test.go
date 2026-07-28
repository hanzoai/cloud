package infra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/hanzoai/cloud/clients/admin/digitalocean"
)

const halfGiB = int64(gib) / 2

// oversizedFixture is the fleet's real worst offender, reduced: a 200 GiB volume attached
// to a node, claimed by a StatefulSet's PVC, holding half a gigabyte.
func oversizedFixture() (Inventory, []ClusterScan) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{
		ID: "vol-luxd", Name: "pvc-luxd-0", Region: "sfo3", SizeGiB: 200,
		DropletIDs: []int{101}, Tags: []string{"k8s:" + cidA},
	}}
	scans := scansOK()
	scans[0].PVs = []PVRef{{
		Name: "pv-luxd", Phase: "Bound", VolumeHandle: "vol-luxd",
		ClaimNS: "lux-testnet", ClaimName: "data-luxd-0",
	}}
	scans[0].Pods = []PodRef{{
		Namespace: "lux-testnet", Name: "luxd-0", Phase: "Running", Node: "node-a1",
		Claims: []string{"data-luxd-0"}, Controller: "StatefulSet/luxd",
	}}
	scans[0].Usage = []VolumeUsage{{Namespace: "lux-testnet", Name: "data-luxd-0", UsedBytes: halfGiB}}
	return inv, scans
}

// TestUnmeasuredVolumeIsUnknownNotEmpty is THE honesty regression test for this feature.
//
// A volume nothing has measured must report HasUsage=false and contribute NOTHING to the
// fleet's waste. The bug this forbids is treating "no reading" as "0 bytes used", which
// would render a live 200 GiB database as 100% wasted and put $20/mo of fictional savings
// on the board next to a delete button.
func TestUnmeasuredVolumeIsUnknownNotEmpty(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{
		ID: "vol-detached", Name: "pvc-db", Region: "sfo3", SizeGiB: 500,
	}}
	scans := scansOK()
	// Live data: a Bound PV, but detached, so no kubelet has it mounted and no reading exists.
	scans[0].PVs = []PVRef{{
		Name: "pv-db", Phase: "Bound", VolumeHandle: "vol-detached",
		ClaimNS: "hanzo", ClaimName: "db-data",
	}}

	got := analyze(inv, scans)
	v := volByID(t, got, "vol-detached")

	if v.HasUsage {
		t.Fatal("HasUsage true for a volume no kubelet reported — nothing measured it")
	}
	if v.WastedGiB != 0 || v.WastedMonthlyCents != 0 {
		t.Fatalf("unmeasured volume claims %d GiB / %d cents of waste; unknown must claim none",
			v.WastedGiB, v.WastedMonthlyCents)
	}
	if got.Cost.WastedMonthly != 0 {
		t.Fatalf("fleet waste = %d cents from a volume that was never measured", got.Cost.WastedMonthly)
	}
	if got.Totals.UnmeasuredVolumes != 1 || got.Totals.UnmeasuredGiB != 500 {
		t.Fatalf("unmeasured tally = %d volumes / %d GiB, want 1 / 500 — the board must be able to say "+
			"how much of the fleet the waste figure was NOT computed from",
			got.Totals.UnmeasuredVolumes, got.Totals.UnmeasuredGiB)
	}
	if got.Totals.MeasuredVolumes != 0 {
		t.Fatalf("measured tally = %d, want 0", got.Totals.MeasuredVolumes)
	}
	// And it must not be flagged: there is no evidence to flag it on.
	for _, f := range got.Findings {
		if f.Kind == "oversized-volume" {
			t.Fatalf("unmeasured volume produced an oversized finding: %s", f.Title)
		}
	}
}

// TestSubGiBUsageDoesNotRoundToZero guards the precision that motivates the whole feature.
// The worst offenders hold a fraction of a GiB in 200; an integer-GiB `used` field would
// print 0 and be indistinguishable from unmeasured.
func TestSubGiBUsageDoesNotRoundToZero(t *testing.T) {
	inv, scans := oversizedFixture()
	got := analyze(inv, scans)
	v := volByID(t, got, "vol-luxd")

	if !v.HasUsage {
		t.Fatal("HasUsage false for a volume the kubelet reported")
	}
	if v.UsedBytes != halfGiB {
		t.Fatalf("UsedBytes = %d, want %d — usage is carried in BYTES precisely so half a "+
			"gigabyte does not become zero", v.UsedBytes, halfGiB)
	}
	if want := "0.5 GiB"; gibLabel(v.UsedBytes) != want {
		t.Fatalf("gibLabel = %q, want %q", gibLabel(v.UsedBytes), want)
	}
	// 200 provisioned - ceil(0.5) = 199.
	if v.WastedGiB != 199 || v.WastedMonthlyCents != 1990 {
		t.Fatalf("waste = %d GiB / %d cents, want 199 / 1990", v.WastedGiB, v.WastedMonthlyCents)
	}
}

// TestWasteIsBilledSizeNotFilesystemCapacity pins the unit. A 200 GiB DigitalOcean volume
// carries a ~196 GiB filesystem after format overhead, and the invoice says 200. Waste is
// computed against what is BILLED, so the money on the board is money actually spent.
func TestWasteIsBilledSizeNotFilesystemCapacity(t *testing.T) {
	inv, scans := oversizedFixture()
	got := analyze(inv, scans)
	v := volByID(t, got, "vol-luxd")

	if v.SizeGiB != 200 {
		t.Fatalf("SizeGiB = %d, want DigitalOcean's billed 200", v.SizeGiB)
	}
	if v.MonthlyCents != 2000 {
		t.Fatalf("MonthlyCents = %d, want 2000 ($0.10 x 200 billed GiB)", v.MonthlyCents)
	}
	// Waste + used-rounded-up must reconstruct the BILLED size exactly, never 196.
	if v.WastedGiB+1 != v.SizeGiB {
		t.Fatalf("waste %d + used 1 = %d, want the billed %d", v.WastedGiB, v.WastedGiB+1, v.SizeGiB)
	}
}

// TestWasteIsNeverOverstated: usage rounds UP before subtracting, and a filesystem
// reporting more used than DigitalOcean provisioned is not negative waste.
func TestWasteIsNeverOverstated(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		used  int64
		waste int
	}{
		{"exactly empty", 100, 0, 100},
		{"one byte used rounds the byte up to a whole billed GiB", 100, 1, 99},
		{"a hair under a GiB still costs a GiB", 100, int64(gib) - 1, 99},
		{"exactly one GiB", 100, int64(gib), 99},
		{"a hair over a GiB costs two", 100, int64(gib) + 1, 98},
		{"full", 100, 100 * int64(gib), 0},
		{"over-full is zero waste, never negative", 100, 120 * int64(gib), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wastedGiB(c.size, c.used); got != c.waste {
				t.Fatalf("wastedGiB(%d, %d) = %d, want %d", c.size, c.used, got, c.waste)
			}
		})
	}
}

// TestFleetWasteCountsOnlyMeasuredVolumes proves the fleet total is a sum over the measured
// set and an honest LOWER BOUND — an unmeasured terabyte moves neither the waste nor the
// used figure.
func TestFleetWasteCountsOnlyMeasuredVolumes(t *testing.T) {
	inv, scans := oversizedFixture()
	inv.Volumes = append(inv.Volumes, digitalocean.Volume{
		ID: "vol-dark", Name: "pvc-unmeasured", Region: "sfo3", SizeGiB: 1024,
	})
	got := analyze(inv, scans)

	if got.Totals.MeasuredVolumes != 1 || got.Totals.UnmeasuredVolumes != 1 {
		t.Fatalf("measured/unmeasured = %d/%d, want 1/1",
			got.Totals.MeasuredVolumes, got.Totals.UnmeasuredVolumes)
	}
	if got.Totals.MeasuredGiB != 200 || got.Totals.UnmeasuredGiB != 1024 {
		t.Fatalf("measured/unmeasured GiB = %d/%d, want 200/1024",
			got.Totals.MeasuredGiB, got.Totals.UnmeasuredGiB)
	}
	if got.Totals.WastedGiB != 199 {
		t.Fatalf("fleet waste = %d GiB, want 199 — the unmeasured 1024 GiB must contribute nothing",
			got.Totals.WastedGiB)
	}
	if got.Cost.WastedMonthly != 1990 {
		t.Fatalf("fleet waste = %d cents, want 1990", got.Cost.WastedMonthly)
	}
	// UsedGiB truncates the measured bytes; 0.5 GiB of real data is under one whole GiB.
	if got.Totals.UsedGiB != 0 {
		t.Fatalf("UsedGiB = %d, want 0 (0.5 GiB truncates); per-volume UsedBytes carries the precision",
			got.Totals.UsedGiB)
	}
}

// TestWastedIsNotReclaimable keeps the two money figures orthogonal. Reclaimable is money a
// button on this board collects by deleting volumes nothing references. Wasted is money
// locked inside volumes that are IN USE holding live data. Adding them would double-count
// and imply the whole sum is one click away.
func TestWastedIsNotReclaimable(t *testing.T) {
	inv, scans := oversizedFixture()
	inv.Volumes = append(inv.Volumes, digitalocean.Volume{
		ID: "vol-orphan", Name: "pvc-orphan", Region: "sfo3", SizeGiB: 10,
	})
	got := analyze(inv, scans)

	if !got.Complete {
		t.Fatalf("scan should be complete: %s", got.IncompleteReason)
	}
	if got.Cost.ReclaimableMonthly != 100 {
		t.Fatalf("reclaimable = %d cents, want 100 (the one 10 GiB unreferenced volume)",
			got.Cost.ReclaimableMonthly)
	}
	if got.Cost.WastedMonthly != 1990 {
		t.Fatalf("wasted = %d cents, want 1990 (the oversized, IN-USE volume)", got.Cost.WastedMonthly)
	}
	// The oversized volume contributes to waste and is NOT deletable; the orphan is
	// deletable and contributes no waste. Neither figure may include the other's volume.
	if v := volByID(t, got, "vol-luxd"); v.Deletable {
		t.Fatal("an oversized but attached volume must never be deletable")
	}
	if v := volByID(t, got, "vol-orphan"); v.WastedMonthlyCents != 0 {
		t.Fatalf("an unreferenced volume claims %d cents of waste; nothing measured it",
			v.WastedMonthlyCents)
	}
}

// TestUsageDoesNotBleedAcrossClusters: two clusters can hold identically named PVCs. Usage
// is keyed by cluster, so cluster B's reading must never be attributed to cluster A's volume.
func TestUsageDoesNotBleedAcrossClusters(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{
		ID: "vol-a", Name: "pvc-a", Region: "sfo3", SizeGiB: 100, DropletIDs: []int{101},
	}}
	scans := scansOK()
	// The volume's PV lives in cluster A and NOBODY measured it there.
	scans[0].PVs = []PVRef{{Name: "pv-a", Phase: "Bound", VolumeHandle: "vol-a",
		ClaimNS: "app", ClaimName: "data"}}
	// Cluster B has a same-named claim that IS measured. It is a different volume.
	scans[1].Usage = []VolumeUsage{{Namespace: "app", Name: "data", UsedBytes: 4 * int64(gib)}}

	got := analyze(inv, scans)
	v := volByID(t, got, "vol-a")

	if v.HasUsage {
		t.Fatalf("cluster B's reading for app/data was attributed to cluster A's volume "+
			"(UsedBytes=%d) — the usage key must include the cluster", v.UsedBytes)
	}
}

// TestOversizedFindingCarriesTheStatefulSetRecipe proves the board states the exact
// migration and the immutability that makes it a migration, rather than implying a button.
func TestOversizedFindingCarriesTheStatefulSetRecipe(t *testing.T) {
	inv, scans := oversizedFixture()
	got := analyze(inv, scans)

	var f Finding
	for _, x := range got.Findings {
		if x.Kind == "oversized-volume" {
			f = x
		}
	}
	if f.ID == "" {
		t.Fatalf("no oversized-volume finding for a 200 GiB volume holding 0.5 GiB; findings: %v", got.Findings)
	}
	// Suggested target = 2 x ceil(0.5 GiB) = 2, floored to minTargetGiB (32).
	// Saving = 200 - 32 = 168 GiB = $16.80/mo, which is LESS than the 199 GiB / $19.90 of
	// raw waste — headroom is not reclaimable and the finding must not claim it is.
	if f.MonthlyCents != 1680 {
		t.Fatalf("finding money = %d cents, want 1680 — the finding must carry what right-sizing "+
			"would actually SAVE, not the raw waste (1990)", f.MonthlyCents)
	}
	if !strings.Contains(f.Detail, "SIZE IT YOURSELF") {
		t.Fatalf("recipe does not warn that the suggestion is one sample with no growth rate\n%s", f.Detail)
	}
	for _, want := range []string{
		"--cascade=orphan",             // the StatefulSet-specific step
		"volumeClaimTemplates are IMM", // …and why it exists
		"kubectl -n lux-testnet scale statefulset/luxd --replicas=0",
		"data-luxd-0-rightsize",  // the temp claim
		"rsync -aHAX",            // the copy
		"claimRef",               // the swap
		"cannot shrink a volume", // the limitation, stated plainly
		"will not run it for you",
	} {
		if !strings.Contains(f.Detail, want) {
			t.Fatalf("recipe missing %q\n--- recipe ---\n%s", want, f.Detail)
		}
	}
}

// TestNonStatefulSetRecipeOmitsTheOrphanStep: a standalone PVC needs no workload surgery,
// and telling an operator to delete a Deployment as a StatefulSet would be wrong.
func TestNonStatefulSetRecipeOmitsTheOrphanStep(t *testing.T) {
	inv, scans := oversizedFixture()
	scans[0].Pods[0].Controller = "ReplicaSet/api-7f9"
	got := analyze(inv, scans)

	var f Finding
	for _, x := range got.Findings {
		if x.Kind == "oversized-volume" {
			f = x
		}
	}
	if f.ID == "" {
		t.Fatal("no oversized-volume finding")
	}
	if strings.Contains(f.Detail, "--cascade=orphan") {
		t.Fatalf("orphan step offered for a ReplicaSet-owned claim\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, "scale replicaset/api-7f9 --replicas=0") {
		t.Fatalf("recipe does not name the controlling workload\n%s", f.Detail)
	}
}

// TestHalfEmptyButNotWorthMigrating: a volume can be genuinely half empty and still not
// be worth a data migration once headroom is kept. Saying so is the honest answer.
func TestHalfEmptyButNotWorthMigrating(t *testing.T) {
	inv, scans := oversizedFixture()
	inv.Volumes[0].SizeGiB = 60
	scans[0].Usage[0].UsedBytes = 30 * int64(gib) // exactly half full

	got := analyze(inv, scans)
	v := volByID(t, got, "vol-luxd")
	if v.WastedGiB != 30 {
		t.Fatalf("waste = %d GiB, want 30", v.WastedGiB)
	}
	// Right-sizing to 2 x 30 = 60 GiB saves nothing at all.
	if target, worth := rightSize(v); worth {
		t.Fatalf("flagged a volume whose suggested size is %d GiB against a current %d GiB — "+
			"there is nothing to reclaim", target, v.SizeGiB)
	}
	for _, f := range got.Findings {
		if f.Kind == "oversized-volume" {
			t.Fatalf("finding raised with no achievable saving: %s", f.Title)
		}
	}
}

// TestExpandVerdicts covers the grow rule end to end. Growing is the only direction that
// exists, so every refusal here is a refusal to pretend otherwise.
func TestExpandVerdicts(t *testing.T) {
	inv, scans := oversizedFixture()
	got := analyze(inv, scans)
	v := volByID(t, got, "vol-luxd")

	if !v.Expandable {
		t.Fatalf("a PVC-owned volume must be expandable: %s", v.ExpandBlockedReason)
	}
	if ok, why := v.ExpandTo(400); !ok {
		t.Fatalf("ExpandTo(400) refused a genuine growth: %s", why)
	}
	if ok, why := v.ExpandTo(200); ok {
		t.Fatal("ExpandTo accepted the CURRENT size as growth")
	} else if !strings.Contains(why, "can only grow") {
		t.Fatalf("refusal does not explain the one-way limit: %s", why)
	}
	if ok, why := v.ExpandTo(50); ok {
		t.Fatal("ExpandTo accepted a SHRINK — DigitalOcean cannot shrink a volume")
	} else if !strings.Contains(why, "copying the data to a smaller volume") {
		t.Fatalf("shrink refusal must say what shrinking really costs: %s", why)
	}
	if ok, _ := v.ExpandTo(maxVolumeGiB + 1); ok {
		t.Fatal("ExpandTo accepted a size beyond DigitalOcean's 16 TiB maximum")
	}
}

// TestReleasedPVBlocksExpand: with no PVC to patch, growing the device would leave the PV
// declaring a capacity that is now wrong. Refuse rather than desynchronise.
func TestReleasedPVBlocksExpand(t *testing.T) {
	inv := baseInventory()
	inv.Volumes = []digitalocean.Volume{{ID: "vol-rel", Name: "pvc-rel", Region: "sfo3", SizeGiB: 100}}
	scans := scansOK()
	scans[0].PVs = []PVRef{{Name: "pv-rel", Phase: "Released", VolumeHandle: "vol-rel"}}

	v := volByID(t, analyze(inv, scans), "vol-rel")
	if v.Expandable {
		t.Fatal("expand allowed on a volume whose PV has no PVC — nothing can be patched")
	}
	if !strings.Contains(v.ExpandBlockedReason, "no PVC does") {
		t.Fatalf("reason does not name the problem: %q", v.ExpandBlockedReason)
	}
}

// TestIncompleteScanBlocksExpand: the completeness gate governs EVERY mutation, including
// the safe-direction one. A fleet we cannot fully see stays untouched.
func TestIncompleteScanBlocksExpand(t *testing.T) {
	inv, scans := oversizedFixture()
	scans[1].Err = context.DeadlineExceeded

	got := analyze(inv, scans)
	v := volByID(t, got, "vol-luxd")

	if v.Expandable {
		t.Fatal("expand allowed while a cluster was unreachable — the gate must cover every mutation")
	}
	if v.ExpandBlockedReason != got.IncompleteReason {
		t.Fatalf("expand reason %q is not the shared gate reason %q — every verdict comes from "+
			"Snapshot.verdict", v.ExpandBlockedReason, got.IncompleteReason)
	}
	if ok, why := v.ExpandTo(400); ok || why != got.IncompleteReason {
		t.Fatalf("ExpandTo bypassed the gate: ok=%v why=%q", ok, why)
	}
	// And the waste analysis still reports what it measured, so an operator is not blinded
	// by a partial scan — it just cannot act on it.
	if got.Cost.WastedMonthly != 1990 {
		t.Fatalf("waste = %d cents; measurement is independent of the mutation gate",
			got.Cost.WastedMonthly)
	}
}

// TestVolumeUsageReadsKubeletsAndToleratesFailure exercises the real stats/summary shape
// against a served endpoint: the pvcRef join, the skip of non-PVC volumes, the max-wins
// dedup across two kubelets, and — most importantly — that a kubelet returning 500 costs a
// metric and NOT the scan. A failed usage read must never block a mutation.
func TestVolumeUsageReadsKubeletsAndToleratesFailure(t *testing.T) {
	const nodeA = `{"pods":[
	  {"volume":[
	    {"name":"config","usedBytes":128},
	    {"pvcRef":{"namespace":"lux","name":"data-luxd-0"},"capacityBytes":210301943808,"usedBytes":536870912},
	    {"pvcRef":{"namespace":"lux","name":"shared"},"usedBytes":100}
	  ]}]}`
	const nodeB = `{"pods":[
	  {"volume":[{"pvcRef":{"namespace":"lux","name":"shared"},"usedBytes":900}]},
	  {"volume":[{"pvcRef":{"namespace":"hanzo","name":"s3-data"},"usedBytes":196000000000}]}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes/node-a/proxy/stats/summary":
			_, _ = w.Write([]byte(nodeA))
		case "/api/v1/nodes/node-b/proxy/stats/summary":
			_, _ = w.Write([]byte(nodeB))
		default:
			w.WriteHeader(http.StatusInternalServerError) // the kubelet that will not answer
		}
	}))
	defer srv.Close()

	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	got := volumeUsage(context.Background(), cs,
		[]NodeState{{Name: "node-a"}, {Name: "node-b"}, {Name: "node-broken"}})

	want := []VolumeUsage{
		{Namespace: "hanzo", Name: "s3-data", UsedBytes: 196000000000},
		{Namespace: "lux", Name: "data-luxd-0", UsedBytes: 536870912},
		{Namespace: "lux", Name: "shared", UsedBytes: 900}, // max of 100 and 900
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v (order must be stable; a volume with no pvcRef "+
				"must be skipped; duplicate claims keep the LARGEST reading)", i, got[i], want[i])
		}
	}
}
