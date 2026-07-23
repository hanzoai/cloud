// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package admin

import (
	"testing"

	"github.com/hanzoai/cloud/clients/admin/digitalocean"
)

// The DO inventory folds into fleet totals + rows, and per-volume fill stays ABSENT
// (DO exposes no fill) so the console renders an honest "—", never a fabricated 0%.
func TestBuildStorageSnapshotFleet(t *testing.T) {
	vols := []digitalocean.Volume{
		{ID: "a", Name: "pvc-a", Region: "sfo3", SizeGiB: 200, DropletIDs: []int{1}},
		{ID: "b", Name: "pvc-b", Region: "nyc1", SizeGiB: 100, DropletIDs: nil},
		{ID: "c", Name: "pvc-c", Region: "sfo3", SizeGiB: 50, DropletIDs: []int{2, 3}},
	}
	snap := buildStorageSnapshot(vols, nil)

	if snap.Fleet.Count != 3 {
		t.Fatalf("count = %d, want 3", snap.Fleet.Count)
	}
	if snap.Fleet.TotalGiB != 350 {
		t.Fatalf("totalGiB = %d, want 350", snap.Fleet.TotalGiB)
	}
	// 350 GiB * $0.10 = $35.
	if snap.Fleet.MonthlyUsd != 35 {
		t.Fatalf("monthlyUsd = %d, want 35", snap.Fleet.MonthlyUsd)
	}
	if snap.Fleet.UsedGiB != nil || snap.Fleet.Pct != nil {
		t.Fatalf("fleet fill must be absent (DO gives none); got used=%v pct=%v", snap.Fleet.UsedGiB, snap.Fleet.Pct)
	}
	if len(snap.Volumes) != 3 {
		t.Fatalf("volumes = %d, want 3", len(snap.Volumes))
	}
	// Attachment derives from droplet_ids; fill is absent per row.
	if !snap.Volumes[0].Attached || snap.Volumes[1].Attached || !snap.Volumes[2].Attached {
		t.Fatalf("attachment wrong: %+v", snap.Volumes)
	}
	for _, v := range snap.Volumes {
		if v.UsedGiB != nil || v.Pct != nil {
			t.Fatalf("volume %s fill must be absent, got used=%v pct=%v", v.ID, v.UsedGiB, v.Pct)
		}
	}
	if snap.Datastore != nil {
		t.Fatalf("datastore must be nil when no fill was read; got %+v", snap.Datastore)
	}
	if len(snap.Alerts) != 0 {
		t.Fatalf("no alerts without a datastore fill; got %v", snap.Alerts)
	}
}

// A near-full datastore raises exactly one alert; a healthy one raises none.
func TestBuildStorageSnapshotDatastoreAlert(t *testing.T) {
	full := &datastoreVolume{Name: "default", Mount: "/var/lib/hanzo-datastore", SizeGiB: 196, UsedGiB: 178, Pct: 91}
	snap := buildStorageSnapshot(nil, full)
	if snap.Datastore == nil || snap.Datastore.Pct != 91 {
		t.Fatalf("datastore card missing/wrong: %+v", snap.Datastore)
	}
	if len(snap.Alerts) != 1 || snap.Alerts[0].Level != "critical" || snap.Alerts[0].Volume != "default" {
		t.Fatalf("expected one critical alert, got %+v", snap.Alerts)
	}

	healthy := &datastoreVolume{Name: "default", SizeGiB: 196, UsedGiB: 13, Pct: 7}
	if a := buildStorageSnapshot(nil, healthy).Alerts; len(a) != 0 {
		t.Fatalf("a 7%%-full datastore must raise no alert, got %+v", a)
	}
}

func TestAlertLevel(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{0, ""}, {7, ""}, {79.9, ""}, {80, "warn"}, {89.9, "warn"}, {90, "critical"}, {99, "critical"},
	}
	for _, c := range cases {
		if got := alertLevel(c.pct); got != c.want {
			t.Fatalf("alertLevel(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

// system.disks bytes → GiB + pct, matching the live datastore (13.5G used of ~196G ≈ 7%).
func TestDatastoreFillFromRow(t *testing.T) {
	// total ≈ 196.6 GiB, free ≈ 183.1 GiB → used ≈ 13.5 GiB → ~6.9%.
	gib := float64(bytesPerGiB) // a variable → runtime math (a float→int const conversion won't compile)
	total := int64(196.6 * gib)
	used := int64(13.5 * gib)
	row := map[string]any{
		"name":        "default",
		"path":        "/var/lib/hanzo-datastore",
		"total_space": uint64(total),
		"free_space":  uint64(total - used),
	}
	d := datastoreFillFromRow(row)
	if d == nil {
		t.Fatal("expected a datastore fill, got nil")
	}
	if d.Name != "default" || d.Mount != "/var/lib/hanzo-datastore" {
		t.Fatalf("name/mount wrong: %+v", d)
	}
	if d.SizeGiB != 196 {
		t.Fatalf("sizeGiB = %d, want 196", d.SizeGiB)
	}
	if d.UsedGiB < 13.4 || d.UsedGiB > 13.6 {
		t.Fatalf("usedGiB = %v, want ~13.5", d.UsedGiB)
	}
	if d.Pct < 6.5 || d.Pct > 7.5 {
		t.Fatalf("pct = %v, want ~7", d.Pct)
	}

	// A disk that reports no capacity is an unusable read → nil (never a fake 0%).
	if datastoreFillFromRow(map[string]any{"total_space": uint64(0)}) != nil {
		t.Fatal("zero-capacity disk must yield nil, not a fabricated 0%")
	}
}
