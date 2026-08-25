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

// volumes — GET /v1/admin/volumes, the realtime DO block-storage fleet the operator's
// Block Storage board (admin.hanzo.ai) watches to scale DO before it runs out.
// (`volumes`, because a volume is the thing this returns — one per row of the answer.
// The operator's separate S3 object-buckets view keeps /v1/admin/storage, so the two
// storage concerns are told apart by naming what each one holds, not by qualifying
// the word "storage" twice.)
// Two REAL sources, honest by construction:
//   - The FLEET inventory (count · total capacity · monthly cost · per-volume region +
//     attachment) from the DigitalOcean API (the same DO_API_TOKEN client the finance
//     dashboard already uses). DO exposes capacity + attachment but NOT fill %, so a
//     volume's used/pct stay ABSENT (the console renders an honest "—", never a
//     fabricated number).
//   - The analytics DATASTORE's own fill from Datastore `system.disks` (the 200Gi PVC
//     the datastore fork mounts) — total/free space over the SAME shared client
//     (datastore.Query) the analytics + compute lenses read, no second
//     connection. This is THE number the operator scales on.
//
// SUPERADMIN ONLY (core.Admit, the op's first line): a cross-tenant infra read, all-orgs.
// admin holds NO storage state — it only reads DO + the datastore. DO unread → the fleet
// numbers are marked incomplete and the reason names which read failed; datastore not
// connected → no datastore card. Never a fabricated fleet, and never a silent one.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/datastore"
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
	// ID is DigitalOcean's own volume id.
	ID string `json:"id"`
	// Name is the volume's name at DigitalOcean.
	Name string `json:"name"`
	// Region is the DO region slug it lives in (nyc3, sfo3). A volume can only
	// attach to a droplet in its own region.
	Region string `json:"region"`
	// SizeGiB is the PROVISIONED capacity in GiB. It is what is billed, attached
	// or not, used or not.
	SizeGiB int `json:"sizeGiB"`
	// UsedGiB is null on every row. DigitalOcean exposes capacity and attachment
	// but not fill, so nothing here has measured it — null means unmeasured, and
	// is not 0.
	UsedGiB *float64 `json:"usedGiB"`
	// Pct is null on every row, for the same reason as UsedGiB. The console
	// renders an em dash rather than a number nobody took.
	Pct *float64 `json:"pct"`
	// Attached is whether at least one droplet has the volume mounted. A detached
	// volume still bills its full size, which is what makes this the reclaim
	// signal.
	Attached bool `json:"attached"`
	// Service names the workload this volume backs. Empty on every row today:
	// nothing tags a DO volume with its consumer, and guessing one from the name
	// would be a claim rather than a reading.
	Service string `json:"service"`
}

// storageFleet is the roll-up: real count/capacity/cost; fleet fill absent (DO gives
// no per-volume fill, so there is no honest fleet-wide used total to report).
type storageFleet struct {
	// Count is how many volumes the DigitalOcean account holds. A measurement only
	// when the snapshot is complete — otherwise it is zero because nothing was
	// read.
	Count int `json:"count"`
	// TotalGiB is their summed provisioned capacity in GiB.
	TotalGiB int `json:"totalGiB"`
	// UsedGiB is null: with no per-volume fill there is no honest fleet total to
	// sum.
	UsedGiB *float64 `json:"usedGiB"`
	// Pct is null, for the same reason as UsedGiB.
	Pct *float64 `json:"pct"`
	// MonthlyUsd is the fleet's block-storage cost in whole US DOLLARS per month —
	// capacity times DO's $0.10/GiB list price, rounded. A list-price estimate for
	// the ops budget, not an invoice line, and dollars rather than the cents the
	// money boards use.
	MonthlyUsd int `json:"monthlyUsd"`
}

// datastoreVolume is the analytics backend's own volume, fill REAL from system.disks.
type datastoreVolume struct {
	// Name is the disk as the datastore names it.
	Name string `json:"name"`
	// Mount is its filesystem path on the datastore node.
	Mount string `json:"mount"`
	// SizeGiB is its total capacity in GiB.
	SizeGiB int `json:"sizeGiB"`
	// UsedGiB is the space in use, in GiB to one decimal. This is the ONE real
	// fill on the board and the number to scale on.
	UsedGiB float64 `json:"usedGiB"`
	// Pct is UsedGiB as a percentage of SizeGiB, 0..100 to one decimal. The alert
	// thresholds read this.
	Pct float64 `json:"pct"`
}

// storageAlert flags a near-full volume (only the datastore carries a real fill today,
// so alerts are datastore-derived until a per-volume filesystem source is wired).
type storageAlert struct {
	// Volume is the volume the alert is about. Only the datastore carries a
	// measured fill today, so it is the only name that appears here.
	Volume string `json:"volume"`
	// Pct is the fill that raised the alert, 0..100.
	Pct float64 `json:"pct"`
	// Level is "warn" from 80% and "critical" from 90%. Below 80 no alert is
	// emitted at all — there is no third level meaning fine.
	Level string `json:"level"`
}

// storageSnapshot is the whole board payload the console normalizes.
//
// Complete/IncompleteReason/Sources carry the same meaning they carry on the infra
// board, which is the one vocabulary for this in admin: Sources names each upstream
// and why it did or did not answer, and Complete says whether the numbers beside it
// may be read as measurements. The fleet roll-up needs it because its fields cannot
// abstain — a fleet of 0 volumes at $0/mo is a perfectly ordinary answer for an
// account with no block storage, so it is also exactly what an unread account looks
// like, and nothing in the payload told the two apart.
type storageSnapshot struct {
	// Complete is whether the fleet roll-up may be read as a MEASUREMENT. False
	// means DigitalOcean was not read, so the count, capacity and cost below are
	// unknown rather than zero — and zero is exactly what an account with no
	// volumes looks like, which is why the distinction has to travel beside them.
	Complete bool `json:"complete"`
	// IncompleteReason says which read did not happen and what that makes the
	// numbers, in a sentence fit to show an operator. Empty when Complete is true.
	IncompleteReason string `json:"incompleteReason"`
	// Sources is one row per upstream — do.volumes — with whether it answered, how
	// many rows it gave, and why not.
	Sources []core.SourceStatus `json:"sources"`
	// Fleet is the DigitalOcean roll-up: count, capacity and monthly list cost.
	Fleet storageFleet `json:"fleet"`
	// Datastore is the analytics datastore's own volume, the one card carrying a
	// measured fill. NULL when the datastore is not connected or reported no
	// capacity — no card at all rather than a fabricated 0%.
	Datastore *datastoreVolume `json:"datastore"`
	// Volumes is one row per DigitalOcean volume: capacity, region and attachment,
	// with fill absent.
	Volumes []storageVolume `json:"volumes"`
	// Alerts is the near-full warnings, derived from the datastore fill. Empty
	// means nothing is near full AND also that there was no fill to judge —
	// Datastore being null is what tells those apart.
	Alerts []storageAlert `json:"alerts"`
}

// volumes returns the realtime block-storage board: the DigitalOcean volume fleet
// (count, capacity, monthly list cost, per-volume region and attachment) plus the
// analytics datastore's OWN fill, read from its system.disks.
//
// A volume's usedGiB and pct are null, always: DO exposes capacity and attachment but no
// fill, so the console renders "—" rather than a number nobody measured. The datastore
// card is the one real fill here, and it is the number to scale on.
//
// The two sources degrade independently — a DO outage still returns the datastore fill,
// and a disconnected datastore still returns the DO fleet. What a DO outage must NOT do
// is pass for an account with no volumes, so the fleet it could not read is marked
// incomplete rather than reported as a count of zero at a cost of zero.
//
// Response: {"status":"ok","msg":"","data":{"complete":true,"incompleteReason":"",
// "sources":[{"name":"do.volumes","ok":true,"rows":2,"error":"","at":"2026-08-19T00:00:00Z"}],
// "fleet":{"count":2,"totalGiB":300,"usedGiB":null,
// "pct":null,"monthlyUsd":30},"datastore":{"name":"default","mount":"/var/lib/datastore",
// "sizeGiB":200,"usedGiB":81.4,"pct":40.7},"volumes":[{"id":"v1","name":"datastore-data",
// "region":"nyc3","sizeGiB":200,"usedGiB":null,"pct":null,"attached":true,"service":""}],
// "alerts":[]}}
func (o ops) volumes(ctx context.Context, _ *core.None) (*volumesOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	at := time.Now().UTC().Format(time.RFC3339)
	vols, err := o.s.State.DO.Volumes(ctx) // the error is the board's, not a detail to drop
	fill := datastoreFill(ctx)             // nil unless system.disks answered
	snap := buildStorageSnapshot(vols, fill)
	snap.Sources = []core.SourceStatus{core.SrcOf("do.volumes", err, len(vols), at)}
	snap.Complete, snap.IncompleteReason = fleetRead(o.s.State.DO.Ready(), err)
	return &volumesOut{Status: core.OK, Data: &snap}, nil
}

// fleetRead says whether the fleet roll-up may be read as a measurement, and when it
// may not, why (PURE — unit-tested).
//
// The two negative cases are different facts and are told apart: an account we were
// never given a token for has no fleet to report, while a token that stopped working
// leaves a fleet we simply did not see. Both produce the same zeros, which is the
// whole reason the distinction has to be carried alongside them rather than inferred
// from them. This still answers 200 with the datastore card intact — an operator
// reads this board DURING an incident, and a named gap beats a blank page.
func fleetRead(configured bool, err error) (bool, string) {
	switch {
	case !configured:
		return false, "DO_API_TOKEN is not configured, so the block-storage fleet was never read — its count, capacity and cost are unknown, not zero."
	case err != nil:
		return false, fmt.Sprintf("DigitalOcean did not answer (%v), so the fleet count, capacity and cost below are not measurements.", err)
	}
	return true, ""
}

// volumesOut is the GET /v1/admin/volumes envelope.
type volumesOut struct {
	// Status is "ok" or "error", at HTTP 200 either way. A DigitalOcean outage
	// still answers "ok": an operator reads this board DURING an incident, and the
	// gap is named in data.incompleteReason rather than replacing the page.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the board. Null only when the caller was refused.
	Data *storageSnapshot `json:"data"`
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
	if !datastore.Ready() {
		return nil
	}
	rows, err := datastore.Query(ctx,
		"SELECT name, path, total_space, free_space FROM system.disks ORDER BY total_space DESC LIMIT 1")
	if err != nil || len(rows) == 0 {
		return nil
	}
	return datastoreFillFromRow(rows[0])
}

// datastoreFillFromRow maps a system.disks row → the datastore card (PURE, tested).
// nil when the disk reports no capacity (an unusable read, not a fabricated 0%).
func datastoreFillFromRow(r map[string]any) *datastoreVolume {
	total := float64(core.CHInt64(r["total_space"]))
	if total <= 0 {
		return nil
	}
	free := float64(core.CHInt64(r["free_space"]))
	used := total - free
	if used < 0 {
		used = 0
	}
	return &datastoreVolume{
		Name:    core.CHStr(r["name"]),
		Mount:   core.CHStr(r["path"]),
		SizeGiB: int(total / bytesPerGiB),
		UsedGiB: round1(used / bytesPerGiB),
		Pct:     round1(used / total * 100),
	}
}

// round1 rounds to one decimal (used/pct read cleanly; not a scientific quantity).
func round1(x float64) float64 { return float64(int(x*10+0.5)) / 10 }
