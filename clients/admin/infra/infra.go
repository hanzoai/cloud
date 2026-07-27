package infra

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
)

// cacheTTL bounds how stale a READ may be. It exists because one board is a fan-out
// over the DO API plus every cluster's full pod/PV listing — not because staleness is
// acceptable when it matters: every MUTATION re-scans from scratch, ignoring this.
const cacheTTL = 60 * time.Second

// board holds the one cached snapshot behind /v1/admin/infra.
type board struct {
	mu   sync.Mutex
	snap Snapshot
	at   time.Time
}

// Routes registers the DigitalOcean infrastructure board. SuperAdmin only: this is
// the whole account's physical inventory and the controls that destroy parts of it.
//
// NOTE ON THE NOUN: this is INFRASTRUCTURE — droplets, volumes, DOKS clusters, load
// balancers. The pre-existing /v1/fleet surface is compute workers and jobs. Different
// nouns, deliberately not merged.
func Routes(app cloud.Router, s *cloud.Service[core.State]) {
	b := &board{}
	g := app.Group("/v1/admin")
	g.Get("/infra", core.Guard(s, b.read))
	g.Post("/infra/volumes/:id/snapshot", core.Guard(s, b.snapshotVolume))
	g.Delete("/infra/volumes/:id", core.Guard(s, b.deleteVolume))
	g.Post("/infra/nodes/:id/cordon", core.Guard(s, b.cordonNode))
}

// read serves the board, from cache unless ?refresh=1.
func (b *board) read(s *cloud.Service[core.State], c *zip.Ctx) error {
	snap, err := b.load(c.Context(), s.State.DO, c.Query("refresh") != "")
	if err != nil {
		return core.Fail(c, err.Error())
	}
	return core.OK(c, snap)
}

// load returns the snapshot, recomputing when forced or stale. A forced load is the
// authority every mutation checks itself against.
func (b *board) load(ctx context.Context, do *digitalocean.Client, force bool) (Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !force && !b.at.IsZero() && time.Since(b.at) < cacheTTL {
		return b.snap, nil
	}
	snap, err := collect(ctx, do)
	if err != nil {
		return Snapshot{}, err
	}
	b.snap, b.at = snap, time.Now()
	return snap, nil
}

// collect performs the whole fan-out: the DO account inventory, then every cluster's
// Kubernetes state, then the pure fold.
//
// Only an unusable DO account is a hard error. A partial DO read (say load balancers
// fail) still produces a board, with the failure named in Sources — EXCEPT for the
// two reads the safety verdict depends on. Clusters and Volumes are load-bearing: if
// either is missing, the analysis cannot honestly classify anything, so it degrades
// via the completeness gate rather than pretending.
func collect(ctx context.Context, do *digitalocean.Client) (Snapshot, error) {
	if do == nil || !do.Ready() {
		return Snapshot{}, fmt.Errorf("DO_API_TOKEN not configured — DigitalOcean inventory unavailable")
	}
	at := time.Now().UTC()
	stamp := at.Format(time.RFC3339)

	var (
		inv     Inventory
		sources []core.SourceStatus
		mu      sync.Mutex
		wg      sync.WaitGroup
	)
	run := func(name string, fn func() (int, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := fn()
			mu.Lock()
			sources = append(sources, core.SrcOf(name, err, n, stamp))
			mu.Unlock()
		}()
	}
	run("do.clusters", func() (int, error) {
		v, err := do.Clusters(ctx)
		inv.Clusters = v
		return len(v), err
	})
	run("do.droplets", func() (int, error) {
		v, err := do.Droplets(ctx)
		inv.Droplets = v
		return len(v), err
	})
	run("do.volumes", func() (int, error) {
		v, err := do.Volumes(ctx)
		inv.Volumes = v
		return len(v), err
	})
	run("do.loadBalancers", func() (int, error) {
		v, err := do.LoadBalancers(ctx)
		inv.LoadBalancers = v
		return len(v), err
	})
	wg.Wait()

	scans := Scan(ctx, do, inv.Clusters)
	for i, sc := range scans {
		name := "k8s." + inv.Clusters[i].Name
		rows := len(sc.PVs) + len(sc.PVCs) + len(sc.Pods) + len(sc.Nodes)
		sources = append(sources, core.SrcOf(name, sc.Err, rows, stamp))
	}
	sortSources(sources)
	return Analyze(inv, scans, sources, at), nil
}

// snapshotVolume takes a point-in-time snapshot of one volume.
func (b *board) snapshotVolume(s *cloud.Service[core.State], c *zip.Ctx) error {
	id := strings.TrimSpace(c.Param("id"))
	snap, err := b.load(c.Context(), s.State.DO, true)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	v, ok := findVolume(snap, id)
	if !ok {
		return core.Fail(c, "volume not found")
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = c.Bind(&body)
	out, err := takeSnapshot(c.Context(), s.State.DO, v, body.Name)
	if err != nil {
		core.EmitAudit(s, c, "infra.volume.snapshot", "do_volume", id, v, nil,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return core.Fail(c, err.Error())
	}
	core.EmitAudit(s, c, "infra.volume.snapshot", "do_volume", id, v, out,
		audit.Outcome{Result: "success", Status: 200})
	return core.OK(c, out)
}

// deleteVolume destroys a volume — but ONLY one the server itself has just proven to
// be referenced by no PersistentVolume in any cluster.
//
// The client's opinion is never trusted: deletability is recomputed here from a FRESH
// complete cross-cluster scan (force=true, never the cache), so a volume that became
// live between the operator loading the page and pressing the button is refused. If
// any cluster is unreachable the scan is incomplete and NOTHING is deletable.
func (b *board) deleteVolume(s *cloud.Service[core.State], c *zip.Ctx) error {
	id := strings.TrimSpace(c.Param("id"))
	snap, err := b.load(c.Context(), s.State.DO, true)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	v, ok := findVolume(snap, id)
	if !ok {
		return core.Fail(c, "volume not found")
	}
	if !v.Deletable {
		core.EmitAudit(s, c, "infra.volume.delete", "do_volume", id, v, nil,
			audit.Outcome{Result: "denied", Status: 200, Reason: v.BlockedReason})
		return core.Fail(c, "refusing to delete: "+v.BlockedReason)
	}

	out := map[string]any{"deleted": false, "name": v.Name, "sizeGiB": v.SizeGiB,
		"freedMonthlyCents": v.MonthlyCents}
	// Snapshot first unless explicitly waived — the delete is irreversible, the
	// snapshot is the undo.
	if c.Query("snapshot") != "false" {
		shot, serr := takeSnapshot(c.Context(), s.State.DO, v, "")
		if serr != nil {
			core.EmitAudit(s, c, "infra.volume.delete", "do_volume", id, v, nil,
				audit.Outcome{Result: "failure", Status: 200, Reason: "snapshot failed: " + serr.Error()})
			return core.Fail(c, "snapshot failed, volume NOT deleted: "+serr.Error())
		}
		out["snapshotId"] = shot.ID
	}
	if err := s.State.DO.DeleteVolume(c.Context(), id); err != nil {
		core.EmitAudit(s, c, "infra.volume.delete", "do_volume", id, v, nil,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return core.Fail(c, err.Error())
	}
	out["deleted"] = true
	b.invalidate()
	core.EmitAudit(s, c, "infra.volume.delete", "do_volume", id, v, out,
		audit.Outcome{Result: "success", Status: 200})
	return core.OK(c, out)
}

// cordonNode cordons/uncordons a node, optionally draining it.
func (b *board) cordonNode(s *cloud.Service[core.State], c *zip.Ctx) error {
	id, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil {
		return core.Fail(c, "node id must be a droplet id")
	}
	var body struct {
		Cordon bool `json:"cordon"`
		Drain  bool `json:"drain"`
	}
	if err := c.Bind(&body); err != nil {
		return core.Fail(c, "invalid body")
	}
	snap, err := b.load(c.Context(), s.State.DO, false)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	var node *Node
	for i := range snap.Nodes {
		if snap.Nodes[i].ID == id {
			node = &snap.Nodes[i]
			break
		}
	}
	if node == nil {
		return core.Fail(c, "node not found")
	}
	if node.ClusterID == "" {
		return core.Fail(c, "node is not a member of a known cluster")
	}
	evicted, err := SetSchedulable(c.Context(), s.State.DO, node.ClusterID, node.Name, !body.Cordon, body.Drain)
	out := map[string]any{"name": node.Name, "schedulable": !body.Cordon, "evicted": evicted}
	if err != nil {
		core.EmitAudit(s, c, "infra.node.cordon", "do_droplet", node.Name, node, out,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return core.Fail(c, err.Error())
	}
	b.invalidate()
	core.EmitAudit(s, c, "infra.node.cordon", "do_droplet", node.Name, node, out,
		audit.Outcome{Result: "success", Status: 200})
	return core.OK(c, out)
}

// takeSnapshot names and takes a volume snapshot. A blank name gets a deterministic
// pre-delete name so the undo is findable in the DO console.
func takeSnapshot(ctx context.Context, do *digitalocean.Client, v Volume, name string) (digitalocean.Snapshot, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("%s-predelete-%d", v.Name, time.Now().Unix())
	}
	return do.SnapshotVolume(ctx, v.ID, name)
}

// invalidate drops the cache so the next read reflects a mutation immediately.
func (b *board) invalidate() {
	b.mu.Lock()
	b.at = time.Time{}
	b.mu.Unlock()
}

func findVolume(s Snapshot, id string) (Volume, bool) {
	for _, v := range s.Volumes {
		if v.ID == id {
			return v, true
		}
	}
	return Volume{}, false
}

// sortSources keeps the freshness list stable across reads (map/goroutine order is not).
func sortSources(rows []core.SourceStatus) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Name < rows[j-1].Name; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
