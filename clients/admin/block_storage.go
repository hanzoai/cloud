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

// blockStorage — GET /v1/admin/block-storage, the realtime DO block-storage fleet the
// operator's Block Storage board (admin.hanzo.ai) watches to scale DO before it runs out.
// (Named `block-storage`, not `storage`, so the operator's separate S3 object-buckets
// view keeps /v1/admin/storage — two distinct storage concerns, two endpoints.)
// Two REAL sources, honest by construction:
//   - The FLEET inventory (count · total capacity · monthly cost · per-volume region +
//     attachment) from the DigitalOcean API (the same DO_API_TOKEN client the finance
//     dashboard already uses). DO exposes capacity + attachment but NOT fill %, so a
//     volume's used/pct stay ABSENT (the console renders an honest "—", never a
//     fabricated number).
//   - The analytics DATASTORE's own fill from Datastore `system.disks` (the 200Gi PVC
//     the datastore fork mounts) — total/free space over the SAME shared client
//     (aiobject.DatastoreQuery) the analytics + compute lenses read, no second
//     connection. This is THE number the operator scales on.
//
// SUPERADMIN ONLY (the s.guard wrap in admin.go): a cross-tenant infra read, all-orgs.
// admin holds NO storage state — it only reads DO + the datastore. DO unconfigured →
// empty fleet; datastore not connected → no datastore card. Never a fabricated fleet.

import (
	"context"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/zap-proto/zip"
)

// doBlockUsdPerGiB is DO block storage's list price ($0.10/GiB/mo) — the fleet cost
// line for the ops budget when DO doesn't itemize per-volume spend.
const doBlockUsdPerGiB = 0.10

// bytesPerGiB converts system.disks bytes (UInt64) → GiB.
const bytesPerGiB = 1024 * 1024 * 1024

// storageVolume mirrors the console StorageVolume. UsedGiB/Pct are POINTERS so an
// absent fill serializes as JSON null (→ the console's honest "—"), distinct from a
// real 0%.
type storageVolume struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Region   string   `json:"region"`
	SizeGiB  int      `json:"sizeGiB"`
	UsedGiB  *float64 `json:"usedGiB"`
	Pct      *float64 `json:"pct"`
	Attached bool     `json:"attached"`
	Service  string   `json:"service"`
}

// storageFleet is the roll-up: real count/capacity/cost; fleet fill absent (DO gives
// no per-volume fill, so there is no honest fleet-wide used total to report).
type storageFleet struct {
	Count      int      `json:"count"`
	TotalGiB   int      `json:"totalGiB"`
	UsedGiB    *float64 `json:"usedGiB"`
	Pct        *float64 `json:"pct"`
	MonthlyUsd int      `json:"monthlyUsd"`
}

// datastoreVolume is the analytics backend's own volume, fill REAL from system.disks.
type datastoreVolume struct {
	Name    string  `json:"name"`
	Mount   string  `json:"mount"`
	SizeGiB int     `json:"sizeGiB"`
	UsedGiB float64 `json:"usedGiB"`
	Pct     float64 `json:"pct"`
}

// storageAlert flags a near-full volume (only the datastore carries a real fill today,
// so alerts are datastore-derived until a per-volume filesystem source is wired).
type storageAlert struct {
	Volume string  `json:"volume"`
	Pct    float64 `json:"pct"`
	Level  string  `json:"level"`
}

// storageSnapshot is the whole board payload the console normalizes.
type storageSnapshot struct {
	Fleet     storageFleet     `json:"fleet"`
	Datastore *datastoreVolume `json:"datastore"`
	Volumes   []storageVolume  `json:"volumes"`
	Alerts    []storageAlert   `json:"alerts"`
}

// blockStorage answers GET /v1/admin/block-storage. SuperAdmin only. Each source
// degrades independently — a DO outage still returns the real datastore fill, and v.v.
func blockStorage(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	vols, _ := s.State.DO.Volumes(ctx) // honest empty on not-configured / unreachable
	fill := datastoreFill(ctx)         // nil unless system.disks answered
	return core.OK(c, buildStorageSnapshot(vols, fill))
}

// buildStorageSnapshot assembles the board payload (PURE — unit-tested). It folds the
// DO inventory into the fleet totals + per-volume rows (fill absent), attaches the real
// datastore card, and derives a near-full alert from the datastore fill. No fabrication:
// a volume's fill is left nil (DO gives none), and the datastore card is present only
// when system.disks actually answered.
func buildStorageSnapshot(vols []digitalocean.Volume, fill *datastoreVolume) storageSnapshot {
	out := make([]storageVolume, 0, len(vols))
	totalGiB := 0
	for _, v := range vols {
		out = append(out, storageVolume{
			ID:       v.ID,
			Name:     v.Name,
			Region:   v.Region,
			SizeGiB:  v.SizeGiB,
			Attached: len(v.DropletIDs) > 0,
		})
		totalGiB += v.SizeGiB
	}
	alerts := make([]storageAlert, 0, 1)
	if fill != nil {
		if lvl := alertLevel(fill.Pct); lvl != "" {
			alerts = append(alerts, storageAlert{Volume: fill.Name, Pct: fill.Pct, Level: lvl})
		}
	}
	return storageSnapshot{
		Fleet: storageFleet{
			Count:      len(vols),
			TotalGiB:   totalGiB,
			MonthlyUsd: int(float64(totalGiB)*doBlockUsdPerGiB + 0.5),
		},
		Datastore: fill,
		Volumes:   out,
		Alerts:    alerts,
	}
}

// alertLevel thresholds a fill %: critical ≥ 90, warn ≥ 80, else none (PURE, tested).
func alertLevel(pct float64) string {
	switch {
	case pct >= 90:
		return "critical"
	case pct >= 80:
		return "warn"
	default:
		return ""
	}
}

// datastoreFill reads the analytics datastore's own volume usage from Datastore
// `system.disks` (the largest disk by capacity is the data volume — the 200Gi PVC).
// Returns nil when the datastore isn't connected or the query fails (honest — the
// console shows no datastore card, never a fabricated fill).
func datastoreFill(ctx context.Context) *datastoreVolume {
	if !aiobject.DatastoreEnabled() {
		return nil
	}
	rows, err := aiobject.DatastoreQuery(ctx,
		"SELECT name, path, total_space, free_space FROM system.disks ORDER BY total_space DESC LIMIT 1")
	if err != nil || len(rows) == 0 {
		return nil
	}
	return datastoreFillFromRow(rows[0])
}

// datastoreFillFromRow maps a system.disks row → the datastore card (PURE, tested).
// nil when the disk reports no capacity (an unusable read, not a fabricated 0%).
func datastoreFillFromRow(r map[string]any) *datastoreVolume {
	total := float64(chInt64(r["total_space"]))
	if total <= 0 {
		return nil
	}
	free := float64(chInt64(r["free_space"]))
	used := total - free
	if used < 0 {
		used = 0
	}
	return &datastoreVolume{
		Name:    chStr(r["name"]),
		Mount:   chStr(r["path"]),
		SizeGiB: int(total / bytesPerGiB),
		UsedGiB: round1(used / bytesPerGiB),
		Pct:     round1(used / total * 100),
	}
}

// round1 rounds to one decimal (used/pct read cleanly; not a scientific quantity).
func round1(x float64) float64 { return float64(int(x*10+0.5)) / 10 }
