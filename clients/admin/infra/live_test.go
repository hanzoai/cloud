package infra

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/admin/money"
)

// TestLiveCollect runs the real fan-out against the real DigitalOcean account and the
// real clusters. It is the only test that can prove the orphan analysis agrees with
// production, so it is kept — but it is SKIPPED unless DO_API_TOKEN is present, which
// is never the case in CI or on a dev box that has not opted in.
//
//	DO_API_TOKEN=$(…) go test ./clients/admin/infra/ -run TestLiveCollect -v
//
// It asserts invariants, not fixed counts: the fleet changes, but "every cluster
// answered", "every volume got a state", and "only unreferenced volumes are deletable"
// must hold on every run, forever.
func TestLiveCollect(t *testing.T) {
	token := os.Getenv("DO_API_TOKEN")
	if token == "" {
		t.Skip("DO_API_TOKEN not set — live fleet test skipped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	snap, err := collect(ctx, digitalocean.New(token))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	byState := map[string]int{}
	gibByState := map[string]int{}
	for _, v := range snap.Volumes {
		byState[v.State]++
		gibByState[v.State] += v.SizeGiB
	}
	t.Logf("clusters=%d nodes=%d volumes=%d loadBalancers=%d complete=%v",
		snap.Totals.Clusters, snap.Totals.Nodes, snap.Totals.Volumes,
		snap.Totals.LoadBalancers, snap.Complete)
	for _, st := range []string{StateAttached, StateBound, StateReleased, StateUnreferenced} {
		t.Logf("  %-14s %4d volumes  %7.2f TiB", st, byState[st], float64(gibByState[st])/1024)
	}
	t.Logf("local disk (INCLUDED in droplet price, not separately billed): %.2f TiB",
		float64(snap.Totals.LocalDiskGiB)/1024)
	t.Logf("cost/mo: droplets $%.2f  volumes $%.2f  lbs $%.2f  TOTAL $%.2f  reclaimable $%.2f",
		float64(snap.Cost.DropletsMonthly)/100, float64(snap.Cost.VolumesMonthly)/100,
		float64(snap.Cost.LoadBalancersMonthly)/100, float64(snap.Cost.TotalMonthly)/100,
		float64(snap.Cost.ReclaimableMonthly)/100)
	for _, c := range snap.Clusters {
		t.Logf("  %-16s nodes=%-3d pods=%-4d pvs=%-4d pvcs=%-4d idle=%-3d scanned=%v %s",
			c.Name, c.Nodes, c.Pods, c.PVs, c.PVCs, c.IdlePVCs, c.Scanned, c.ScanError)
	}
	t.Logf("fill: %d of %d volumes measured (%d GiB); %d NOT measured (%d GiB — unknown, not empty)",
		snap.Totals.MeasuredVolumes, snap.Totals.Volumes, snap.Totals.MeasuredGiB,
		snap.Totals.UnmeasuredVolumes, snap.Totals.UnmeasuredGiB)
	t.Logf("WASTE: %d GiB provisioned-but-empty on the measured set = $%.2f/mo (a LOWER BOUND)",
		snap.Totals.WastedGiB, float64(snap.Cost.WastedMonthly)/100)
	// The ACHIEVABLE figure, which is smaller and the one worth acting on: what
	// right-sizing every flagged volume would really save once headroom is kept. Waste is
	// what is being paid for emptiness; this is what a migration would actually recover.
	var achievable money.Cents
	flagged := 0
	for _, f := range snap.Findings {
		if f.Kind == "oversized-volume" {
			flagged++
			achievable += f.MonthlyCents
		}
	}
	t.Logf("ACHIEVABLE: right-sizing the %d flagged volumes saves $%.2f/mo (2x measured usage kept as headroom)",
		flagged, float64(achievable)/100)
	worst := append([]Volume(nil), snap.Volumes...)
	sort.Slice(worst, func(i, j int) bool { return worst[i].WastedGiB > worst[j].WastedGiB })
	for i, v := range worst {
		if i == 10 || !v.HasUsage {
			break
		}
		t.Logf("  %5d GiB waste  $%6.2f/mo  %4d GiB prov  %9s used  %s/%s  %s",
			v.WastedGiB, float64(v.WastedMonthlyCents)/100, v.SizeGiB, gibLabel(v.UsedBytes),
			v.PVCNamespace, v.PVCName, v.Controller)
	}

	var deletable []Volume
	for _, v := range snap.Volumes {
		if v.Deletable {
			deletable = append(deletable, v)
		}
	}
	t.Logf("DELETABLE: %d volumes", len(deletable))
	for _, v := range deletable {
		t.Logf("  %s  %-40s %4d GiB  $%.2f/mo", v.ID, v.Name, v.SizeGiB, float64(v.MonthlyCents)/100)
	}

	// ---- invariants --------------------------------------------------------------
	if !snap.Complete {
		t.Fatalf("scan incomplete, so no verdict is trustworthy: %s", snap.IncompleteReason)
	}
	if snap.Totals.Clusters == 0 || snap.Totals.Nodes == 0 || snap.Totals.Volumes == 0 {
		t.Fatal("empty inventory from a live account")
	}
	for _, v := range snap.Volumes {
		if v.State == "" {
			t.Fatalf("volume %s has no state", v.ID)
		}
		if v.Deletable != (v.State == StateUnreferenced) {
			t.Fatalf("volume %s: deletable=%v but state=%s — only unreferenced volumes may be deletable",
				v.ID, v.Deletable, v.State)
		}
		if v.Deletable && len(v.DropletIDs) > 0 {
			t.Fatalf("volume %s is deletable while attached to %v", v.ID, v.DropletIDs)
		}
		if v.Deletable && v.PV != "" {
			t.Fatalf("volume %s is deletable while PV %s references it", v.ID, v.PV)
		}
	}
	// Every attached volume must sit on a droplet we actually enumerated, or the
	// attachment join is broken and cost attribution is wrong.
	nodes := map[int]bool{}
	for _, n := range snap.Nodes {
		nodes[n.ID] = true
	}
	for _, v := range snap.Volumes {
		for _, id := range v.DropletIDs {
			if !nodes[id] {
				t.Errorf("volume %s attached to unknown droplet %d", v.ID, id)
			}
		}
	}
	if int64(snap.Cost.ReclaimableMonthly) != sumCents(deletable) {
		t.Errorf("reclaimable %d != sum of deletable volumes %d", snap.Cost.ReclaimableMonthly, sumCents(deletable))
	}

	// ---- fill invariants ---------------------------------------------------------
	// The one that matters: an unmeasured volume must contribute NOTHING. On a live fleet
	// this is the difference between a real $600 and a fabricated $760.
	var wastedGiBSum int
	var wastedCents, measured, unmeasured int64
	for _, v := range snap.Volumes {
		if !v.HasUsage {
			unmeasured++
			if v.UsedBytes != 0 || v.WastedGiB != 0 || v.WastedMonthlyCents != 0 {
				t.Errorf("volume %s (%s) was never measured yet reports used=%d waste=%d GiB/%d cents",
					v.ID, v.Name, v.UsedBytes, v.WastedGiB, v.WastedMonthlyCents)
			}
			continue
		}
		measured++
		if v.WastedGiB > v.SizeGiB {
			t.Errorf("volume %s wastes %d GiB of a %d GiB volume", v.ID, v.WastedGiB, v.SizeGiB)
		}
		if v.WastedMonthlyCents > v.MonthlyCents {
			t.Errorf("volume %s wastes $%.2f of a $%.2f bill", v.ID,
				float64(v.WastedMonthlyCents)/100, float64(v.MonthlyCents)/100)
		}
		wastedGiBSum += v.WastedGiB
		wastedCents += int64(v.WastedMonthlyCents)
	}
	if int64(snap.Totals.MeasuredVolumes) != measured || int64(snap.Totals.UnmeasuredVolumes) != unmeasured {
		t.Errorf("measured/unmeasured tallies %d/%d disagree with the rows %d/%d",
			snap.Totals.MeasuredVolumes, snap.Totals.UnmeasuredVolumes, measured, unmeasured)
	}
	if snap.Totals.MeasuredVolumes+snap.Totals.UnmeasuredVolumes != snap.Totals.Volumes {
		t.Errorf("measured %d + unmeasured %d != %d volumes — every volume must be in exactly one bucket",
			snap.Totals.MeasuredVolumes, snap.Totals.UnmeasuredVolumes, snap.Totals.Volumes)
	}
	if snap.Totals.WastedGiB != wastedGiBSum || int64(snap.Cost.WastedMonthly) != wastedCents {
		t.Errorf("fleet waste %d GiB/%d cents != sum of rows %d GiB/%d cents",
			snap.Totals.WastedGiB, snap.Cost.WastedMonthly, wastedGiBSum, wastedCents)
	}
	// Waste is money already inside VolumesMonthly, never on top of it.
	if snap.Cost.WastedMonthly > snap.Cost.VolumesMonthly {
		t.Errorf("waste $%.2f exceeds the whole block-storage bill $%.2f",
			float64(snap.Cost.WastedMonthly)/100, float64(snap.Cost.VolumesMonthly)/100)
	}
}

func sumCents(vs []Volume) (t int64) {
	for _, v := range vs {
		t += int64(v.MonthlyCents)
	}
	return
}
