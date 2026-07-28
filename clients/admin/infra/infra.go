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
	g.Delete("/infra/droplets/:id", core.Guard(s, b.deleteDroplet))
	g.Post("/infra/droplets/:id/resize", core.Guard(s, b.resizeDroplet))
	g.Delete("/infra/loadbalancers/:id", core.Guard(s, b.deleteLoadBalancer))
	g.Post("/infra/clusters/:id/nodepools/:pool/scale", core.Guard(s, b.scaleNodePool))
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

// mutation is one change to the fleet: what it touches, the verdict the ANALYZER
// already derived for it, and what to do once that verdict says yes. A handler supplies
// only WHAT to change — it never decides WHETHER.
type mutation[T any] struct {
	action  string // audit action, e.g. "infra.volume.delete"
	resType string // audit resource type, e.g. "do_volume"
	resID   string
	find    func(Snapshot) (T, bool)
	verdict func(T) (bool, string) // read off the row; never recomputed here
	apply   func(context.Context, *digitalocean.Client, T) (map[string]any, error)
}

// run is THE mutation discipline, written once and shared by every destructive route.
//
// The client's opinion is never trusted. The board is re-scanned from scratch
// (force=true, NEVER the cache), the verdict is taken from that fresh scan, and every
// outcome — success, failure and refusal — is audited. A resource that became live
// between the operator loading the page and pressing the button is refused, and if any
// cluster is unreachable the scan is incomplete and NOTHING may be mutated.
func run[T any](b *board, s *cloud.Service[core.State], c *zip.Ctx, m mutation[T]) error {
	snap, err := b.load(c.Context(), s.State.DO, true)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	subject, found := m.find(snap)
	if !found {
		return core.Fail(c, strings.ReplaceAll(strings.TrimPrefix(m.resType, "do_"), "_", " ")+" not found")
	}
	if ok, reason := m.verdict(subject); !ok {
		core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, nil,
			audit.Outcome{Result: "denied", Status: 200, Reason: reason})
		return core.Fail(c, "refusing: "+reason)
	}
	out, err := m.apply(c.Context(), s.State.DO, subject)
	if err != nil {
		core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, out,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return core.Fail(c, err.Error())
	}
	b.invalidate()
	core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, out,
		audit.Outcome{Result: "success", Status: 200})
	return core.OK(c, out)
}

// deleteVolume destroys a volume the board has just proven no PersistentVolume in any
// cluster references. Irreversible, so it snapshots first unless explicitly waived —
// the snapshot IS the undo.
func (b *board) deleteVolume(s *cloud.Service[core.State], c *zip.Ctx) error {
	id := strings.TrimSpace(c.Param("id"))
	snapshotFirst := c.Query("snapshot") != "false"
	return run(b, s, c, mutation[Volume]{
		action: "infra.volume.delete", resType: "do_volume", resID: id,
		find:    func(snap Snapshot) (Volume, bool) { return findVolume(snap, id) },
		verdict: func(v Volume) (bool, string) { return v.Deletable, v.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, v Volume) (map[string]any, error) {
			out := map[string]any{"deleted": false, "name": v.Name, "sizeGiB": v.SizeGiB,
				"freedMonthlyCents": v.MonthlyCents}
			if snapshotFirst {
				shot, err := takeSnapshot(ctx, do, v, "")
				if err != nil {
					return out, fmt.Errorf("snapshot failed, volume NOT deleted: %w", err)
				}
				out["snapshotId"] = shot.ID
			}
			if err := do.DeleteVolume(ctx, v.ID); err != nil {
				return out, err
			}
			out["deleted"] = true
			return out, nil
		},
	})
}

// deleteDroplet destroys a droplet the board has just proven is NOT a DOKS node. There
// is no snapshot-first undo for a droplet the way there is for a volume: the local disk
// goes with it.
func (b *board) deleteDroplet(s *cloud.Service[core.State], c *zip.Ctx) error {
	id, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil {
		return core.Fail(c, "droplet id must be numeric")
	}
	return run(b, s, c, mutation[Node]{
		action: "infra.droplet.delete", resType: "do_droplet", resID: c.Param("id"),
		find:    func(snap Snapshot) (Node, bool) { return findNode(snap, id) },
		verdict: func(n Node) (bool, string) { return n.Mutable, n.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, n Node) (map[string]any, error) {
			if err := do.DeleteDroplet(ctx, n.ID); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true, "name": n.Name,
				"freedMonthlyCents": n.MonthlyCents}, nil
		},
	})
}

// resizeDroplet changes a droplet's plan. Same refusal as delete and for the same
// reason: a DOKS node's size is the node pool's to declare.
//
// disk=true is a PERMANENT resize — the disk grows and DO can never resize the droplet
// DOWN again. disk=false (the default) changes CPU/RAM only and is reversible. DO
// requires the droplet to be powered off and applies the change asynchronously, so the
// response carries the action to poll, not a completed change.
func (b *board) resizeDroplet(s *cloud.Service[core.State], c *zip.Ctx) error {
	id, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil {
		return core.Fail(c, "droplet id must be numeric")
	}
	var body struct {
		Size string `json:"size"`
		Disk bool   `json:"disk"`
	}
	if err := c.Bind(&body); err != nil {
		return core.Fail(c, "invalid body")
	}
	if strings.TrimSpace(body.Size) == "" {
		return core.Fail(c, "size is required (a DigitalOcean size slug, e.g. s-4vcpu-8gb)")
	}
	return run(b, s, c, mutation[Node]{
		action: "infra.droplet.resize", resType: "do_droplet", resID: c.Param("id"),
		find:    func(snap Snapshot) (Node, bool) { return findNode(snap, id) },
		verdict: func(n Node) (bool, string) { return n.Mutable, n.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, n Node) (map[string]any, error) {
			act, err := do.ResizeDroplet(ctx, n.ID, body.Size, body.Disk)
			if err != nil {
				return nil, err
			}
			return map[string]any{"name": n.Name, "from": n.SizeSlug, "to": body.Size,
				"permanent": body.Disk, "actionId": act.ID, "actionStatus": act.Status}, nil
		},
	})
}

// deleteLoadBalancer destroys a load balancer the board has just proven no live
// type=LoadBalancer Service in any cluster targets.
func (b *board) deleteLoadBalancer(s *cloud.Service[core.State], c *zip.Ctx) error {
	id := strings.TrimSpace(c.Param("id"))
	return run(b, s, c, mutation[LoadBalancer]{
		action: "infra.loadbalancer.delete", resType: "do_load_balancer", resID: id,
		find:    func(snap Snapshot) (LoadBalancer, bool) { return findLoadBalancer(snap, id) },
		verdict: func(l LoadBalancer) (bool, string) { return l.Deletable, l.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, l LoadBalancer) (map[string]any, error) {
			if err := do.DeleteLoadBalancer(ctx, l.ID); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true, "name": l.Name, "ip": l.IP,
				"freedMonthlyCents": l.MonthlyCents}, nil
		},
	})
}

// scaleNodePool sets a node pool's node count — the ONE correct way to change how many
// nodes a DOKS cluster has.
//
// The response states what the board could NOT prove: DOKS picks which nodes a shrink
// removes, so no particular pod is shown to survive one. See NodePool.ScaleTo.
func (b *board) scaleNodePool(s *cloud.Service[core.State], c *zip.Ctx) error {
	clusterID, pool := strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Param("pool"))
	var body struct {
		Count int `json:"count"`
	}
	if err := c.Bind(&body); err != nil {
		return core.Fail(c, "invalid body")
	}
	return run(b, s, c, mutation[NodePool]{
		action: "infra.nodepool.scale", resType: "do_node_pool", resID: clusterID + "/" + pool,
		find:    func(snap Snapshot) (NodePool, bool) { return findNodePool(snap, clusterID, pool) },
		verdict: func(p NodePool) (bool, string) { return p.ScaleTo(body.Count) },
		apply: func(ctx context.Context, do *digitalocean.Client, p NodePool) (map[string]any, error) {
			if err := do.ScaleNodePool(ctx, p.ClusterID, p.ID, p.Name, body.Count); err != nil {
				return nil, err
			}
			out := map[string]any{"pool": p.Name, "cluster": p.Cluster, "from": p.Count, "to": body.Count}
			if body.Count < p.Count {
				out["note"] = "DOKS chooses which nodes to remove and drains them itself. " +
					"This board proved only that the cluster keeps a schedulable node; " +
					"PodDisruptionBudgets, taints, affinity and resource requests are enforced " +
					"by the cluster, so some pods may stay Pending."
			}
			return out, nil
		},
	})
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
	node, ok := findNode(snap, id)
	if !ok {
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

func findNode(s Snapshot, id int) (Node, bool) {
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func findLoadBalancer(s Snapshot, id string) (LoadBalancer, bool) {
	for _, l := range s.LoadBalancers {
		if l.ID == id {
			return l, true
		}
	}
	return LoadBalancer{}, false
}

// findNodePool resolves a pool by its DO id or by its name — both are unique within a
// cluster, and an operator reads the name off the board while the API speaks ids.
func findNodePool(s Snapshot, clusterID, pool string) (NodePool, bool) {
	for _, c := range s.Clusters {
		if c.ID != clusterID {
			continue
		}
		for _, p := range c.Pools {
			if p.ID == pool || p.Name == pool {
				return p, true
			}
		}
	}
	return NodePool{}, false
}

// sortSources keeps the freshness list stable across reads (map/goroutine order is not).
func sortSources(rows []core.SourceStatus) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Name < rows[j-1].Name; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
