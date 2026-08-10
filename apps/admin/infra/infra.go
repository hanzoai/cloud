package infra

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/admin/money"
	"github.com/hanzoai/cloud/audit"
)

// cacheTTL bounds how stale a READ may be. It exists because one board is a fan-out
// over the DO API plus every cluster's full pod/PV listing — not because staleness is
// acceptable when it matters: every MUTATION re-scans from scratch, ignoring this.
const cacheTTL = 60 * time.Second

// board is the receiver every infra op is a method value on: the kernel (a TypedHandler
// has no parameter for it) plus the one cached snapshot behind /v1/admin/infra.
type board struct {
	s *cloud.Service[core.State]

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
func Routes(z *zip.App, s *cloud.Service[core.State]) {
	b := &board{s: s}
	zip.Get(z, "/v1/admin/infra", b.read, zip.WithOperationID("adminInfra"))
	zip.Post(z, "/v1/admin/infra/volumes/:id/snapshot", b.snapshotVolume, zip.WithOperationID("adminSnapshotVolume"))
	zip.Post(z, "/v1/admin/infra/volumes/:id/resize", b.expandVolume, zip.WithOperationID("adminResizeVolume"))
	zip.Delete(z, "/v1/admin/infra/volumes/:id", b.deleteVolume, zip.WithOperationID("adminDeleteVolume"))
	zip.Post(z, "/v1/admin/infra/nodes/:id/cordon", b.cordonNode, zip.WithOperationID("adminCordonNode"))
	zip.Delete(z, "/v1/admin/infra/droplets/:id", b.deleteDroplet, zip.WithOperationID("adminDeleteDroplet"))
	zip.Post(z, "/v1/admin/infra/droplets/:id/resize", b.resizeDroplet, zip.WithOperationID("adminResizeDroplet"))
	zip.Delete(z, "/v1/admin/infra/loadbalancers/:id", b.deleteLoadBalancer, zip.WithOperationID("adminDeleteLoadBalancer"))
	zip.Post(z, "/v1/admin/infra/clusters/:id/nodepools/:pool/scale", b.scaleNodePool, zip.WithOperationID("adminScaleNodePool"))
}

// ReadIn is the GET /v1/admin/infra query.
type ReadIn struct {
	// Refresh, when present, forces a full re-scan instead of serving the cached
	// snapshot. Every MUTATION re-scans regardless — this is only for the reader.
	Refresh string `json:"refresh"`
}

// ReadOut is the GET /v1/admin/infra envelope.
type ReadOut struct {
	Status string    `json:"status"`
	Msg    string    `json:"msg"`
	Data   *Snapshot `json:"data"`
}

// MutationOut is the envelope EVERY infra change answers with. One type, because there is
// one mutation discipline (run) behind all of them: re-scan, check the fresh verdict,
// apply, audit.
//
// `data` is the per-action result and is declared opaque — its keys differ by action and
// each handler's doc comment names them. On a refusal or a failure it is null and msg
// says why; a refusal reads "refusing: <reason>" and means the board proved the change
// unsafe, which is different from the change being attempted and failing.
type MutationOut struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Data   any    `json:"data"`
}

// VolumeIn addresses one DigitalOcean volume.
type VolumeIn struct {
	// ID is the DO volume id, from the path.
	ID string `json:"id"`
	// Snapshot is the snapshot-first switch on DELETE. Anything other than the literal
	// "false" snapshots before destroying — the snapshot IS the undo, so waiving it is
	// deliberate and explicit.
	Snapshot string `json:"snapshot"`
	// Name is the snapshot name on the snapshot action. Blank gets a deterministic
	// "<volume>-predelete-<unix>" so the undo is findable in the DO console.
	Name string `json:"name"`
	// SizeGiB is the target size on the resize action. A volume only ever grows —
	// ExpandTo is the verdict that refuses a shrink, so this is not validated here.
	SizeGiB int `json:"sizeGiB"`
}

// DropletIn addresses one droplet, optionally with a resize.
type DropletIn struct {
	// ID is the DO droplet id, from the path. Numeric.
	ID string `json:"id"`
	// Size is the target DigitalOcean size slug on resize, e.g. "s-4vcpu-8gb".
	Size string `json:"size"`
	// Disk requests a PERMANENT resize that grows the disk. DO can never resize such a
	// droplet down again, so it defaults false — a CPU/RAM-only change, reversible.
	Disk bool `json:"disk"`
}

// CordonIn addresses one cluster node by its droplet id.
type CordonIn struct {
	// ID is the node's droplet id, from the path.
	ID string `json:"id"`
	// Cordon true marks the node unschedulable; false restores it.
	Cordon bool `json:"cordon"`
	// Drain additionally evicts the pods already running there.
	Drain bool `json:"drain"`
}

// LoadBalancerIn addresses one load balancer.
type LoadBalancerIn struct {
	// ID is the DO load balancer id, from the path.
	ID string `json:"id"`
}

// ScaleIn addresses one node pool and the count to set.
type ScaleIn struct {
	// ID is the DOKS cluster id, from the path.
	ID string `json:"id"`
	// Pool is the node pool, from the path. Its DO id or its name — both are unique
	// within a cluster, and an operator reads the name off the board.
	Pool string `json:"pool"`
	// Count is the node count to set.
	Count int `json:"count"`
}

// read serves the whole DigitalOcean infrastructure board: droplets, volumes, DOKS
// clusters and load balancers, each cross-referenced against every cluster's live
// Kubernetes state so the board can say what is safe to destroy and what is not.
//
// It is cached for up to a minute because one read is a fan-out over the DO API plus a
// full pod/PV listing per cluster. Staleness is never load-bearing: every MUTATION
// re-scans from scratch and ignores this cache.
//
// Only an unusable DO account is a hard failure. A partial read still produces a board,
// with the failing source named in sources[] — except for clusters and volumes, which
// the safety verdict depends on; without those the analysis degrades rather than
// classifying anything it cannot prove.
//
// Example: {"refresh":"1"}
// Response: {"status":"ok","msg":"","data":{"volumes":[],"nodes":[],"clusters":[],
// "loadBalancers":[],"sources":[{"name":"do.volumes","ok":true,"rows":2,
// "lastSync":"2026-07-27T00:00:00Z"}]}}
func (b *board) read(ctx context.Context, in *ReadIn) (*ReadOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	snap, err := b.load(ctx, b.s.State.DO, in.Refresh != "")
	if err != nil {
		return &ReadOut{Status: core.Err, Msg: err.Error()}, nil
	}
	return &ReadOut{Status: core.OK, Data: &snap}, nil
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
		wg.Go(func() {
			n, err := fn()
			mu.Lock()
			sources = append(sources, core.SrcOf(name, err, n, stamp))
			mu.Unlock()
		})
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

// VolumeSnapshotOut is the POST /v1/admin/infra/volumes/:id/snapshot envelope. It is the
// one infra change with a typed result, because DO returns a real snapshot object.
type VolumeSnapshotOut struct {
	Status string                 `json:"status"`
	Msg    string                 `json:"msg"`
	Data   *digitalocean.Snapshot `json:"data"`
}

// snapshotVolume takes a point-in-time snapshot of one volume — the undo a delete relies
// on, available on its own so an operator can take one before any risky change.
//
// It re-scans the board first (never the cache) so the volume it snapshots is one that
// exists right now, and audits the outcome either way.
//
// Example: {"name":"acme-data-before-migration"}
// Response: {"status":"ok","msg":"","data":{"id":"snap-01J","name":"acme-data-before-migration",
// "sizeGiB":200,"created":"2026-07-27T00:00:00Z"}}
func (b *board) snapshotVolume(ctx context.Context, in *VolumeIn) (*VolumeSnapshotOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := b.s
	id := strings.TrimSpace(in.ID)
	snap, err := b.load(ctx, s.State.DO, true)
	if err != nil {
		return &VolumeSnapshotOut{Status: core.Err, Msg: err.Error()}, nil
	}
	v, ok := findVolume(snap, id)
	if !ok {
		return &VolumeSnapshotOut{Status: core.Err, Msg: "volume not found"}, nil
	}
	out, err := takeSnapshot(ctx, s.State.DO, v, in.Name)
	if err != nil {
		core.EmitAudit(s, c, "infra.volume.snapshot", "do_volume", id, v, nil,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return &VolumeSnapshotOut{Status: core.Err, Msg: err.Error()}, nil
	}
	core.EmitAudit(s, c, "infra.volume.snapshot", "do_volume", id, v, out,
		audit.Outcome{Result: "success", Status: 200})
	return &VolumeSnapshotOut{Status: core.OK, Data: &out}, nil
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
func run[T any](ctx context.Context, b *board, m mutation[T]) (*MutationOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := b.s
	snap, err := b.load(ctx, s.State.DO, true)
	if err != nil {
		return &MutationOut{Status: core.Err, Msg: err.Error()}, nil
	}
	subject, found := m.find(snap)
	if !found {
		return &MutationOut{Status: core.Err, Msg: strings.ReplaceAll(strings.TrimPrefix(m.resType, "do_"), "_", " ") + " not found"}, nil
	}
	if ok, reason := m.verdict(subject); !ok {
		core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, nil,
			audit.Outcome{Result: "denied", Status: 200, Reason: reason})
		return &MutationOut{Status: core.Err, Msg: "refusing: " + reason}, nil
	}
	out, err := m.apply(ctx, s.State.DO, subject)
	if err != nil {
		core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, out,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return &MutationOut{Status: core.Err, Msg: err.Error()}, nil
	}
	b.invalidate()
	core.EmitAudit(s, c, m.action, m.resType, m.resID, subject, out,
		audit.Outcome{Result: "success", Status: 200})
	return &MutationOut{Status: core.OK, Data: out}, nil
}

// deleteVolume destroys a volume the board has just proven no PersistentVolume in any
// cluster references. Irreversible, so it snapshots first unless explicitly waived —
// the snapshot IS the undo.
// Response: {"status":"ok","msg":"","data":{"deleted":true,"name":"acme-data","sizeGiB":200,
// "freedMonthlyCents":2000,"snapshotId":"snap-01J"}}
func (b *board) deleteVolume(ctx context.Context, in *VolumeIn) (*MutationOut, error) {
	id := strings.TrimSpace(in.ID)
	snapshotFirst := in.Snapshot != "false"
	return run(ctx, b, mutation[Volume]{
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

// expandVolume grows a volume. GROW ONLY — see Volume.ExpandTo for why the other
// direction is a data migration this board deliberately refuses to run.
//
// The MECHANISM follows the volume's owner, because there is exactly one way to grow each
// kind completely. A volume a PVC claims is grown by patching the claim: the CSI driver
// then resizes the DigitalOcean device AND grows the filesystem on it, leaving claim, PV,
// device and filesystem all agreeing. Calling DigitalOcean directly for that volume would
// grow the device while the PV kept declaring the old capacity and the filesystem never
// grew at all. One operation, one correct mechanism per owner — not two ways to do it.
func (b *board) expandVolume(ctx context.Context, in *VolumeIn) (*MutationOut, error) {
	id := strings.TrimSpace(in.ID)
	return run(ctx, b, mutation[Volume]{
		action: "infra.volume.resize", resType: "do_volume", resID: id,
		find:    func(snap Snapshot) (Volume, bool) { return findVolume(snap, id) },
		verdict: func(v Volume) (bool, string) { return v.ExpandTo(in.SizeGiB) },
		apply: func(ctx context.Context, do *digitalocean.Client, v Volume) (map[string]any, error) {
			out := map[string]any{"name": v.Name, "from": v.SizeGiB, "to": in.SizeGiB,
				"addedMonthlyCents": money.Cents(in.SizeGiB-v.SizeGiB) * volumeGiBCents}
			if v.PVCName != "" {
				if err := ExpandPVC(ctx, do, v.ClusterID, v.PVCNamespace, v.PVCName, in.SizeGiB); err != nil {
					return out, err
				}
				out["via"] = fmt.Sprintf("pvc %s/%s", v.PVCNamespace, v.PVCName)
				out["note"] = "The CSI driver resizes the volume and then grows the filesystem. " +
					"Both are asynchronous: watch the PVC's conditions until FileSystemResizePending clears."
				return out, nil
			}
			act, err := do.ResizeVolume(ctx, v.ID, v.Region, in.SizeGiB)
			if err != nil {
				return out, err
			}
			out["via"] = "digitalocean volume action"
			out["actionId"], out["actionStatus"] = act.ID, act.Status
			out["note"] = "No PersistentVolumeClaim owns this volume, so only the DEVICE was grown. " +
				"Any filesystem on it still reports the old size until it is grown in place."
			return out, nil
		},
	})
}

// deleteDroplet destroys a droplet the board has just proven is NOT a DOKS node. There
// is no snapshot-first undo for a droplet the way there is for a volume: the local disk
// goes with it.
// Response: {"status":"ok","msg":"","data":{"deleted":true,"name":"worker-3","freedMonthlyCents":4800}}
func (b *board) deleteDroplet(ctx context.Context, in *DropletIn) (*MutationOut, error) {
	id, err := strconv.Atoi(strings.TrimSpace(in.ID))
	if err != nil {
		return &MutationOut{Status: core.Err, Msg: "droplet id must be numeric"}, nil
	}
	return run(ctx, b, mutation[Machine]{
		action: "infra.droplet.delete", resType: "do_droplet", resID: in.ID,
		find:    func(snap Snapshot) (Machine, bool) { return findNode(snap, id) },
		verdict: func(n Machine) (bool, string) { return n.Mutable, n.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, n Machine) (map[string]any, error) {
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
// Example: {"size":"s-4vcpu-8gb","disk":false}
// Response: {"status":"ok","msg":"","data":{"name":"worker-3","from":"s-2vcpu-4gb",
// "to":"s-4vcpu-8gb","permanent":false,"actionId":1234567,"actionStatus":"in-progress"}}
func (b *board) resizeDroplet(ctx context.Context, in *DropletIn) (*MutationOut, error) {
	id, err := strconv.Atoi(strings.TrimSpace(in.ID))
	if err != nil {
		return &MutationOut{Status: core.Err, Msg: "droplet id must be numeric"}, nil
	}
	if strings.TrimSpace(in.Size) == "" {
		return &MutationOut{Status: core.Err, Msg: "size is required (a DigitalOcean size slug, e.g. s-4vcpu-8gb)"}, nil
	}
	return run(ctx, b, mutation[Machine]{
		action: "infra.droplet.resize", resType: "do_droplet", resID: in.ID,
		find:    func(snap Snapshot) (Machine, bool) { return findNode(snap, id) },
		verdict: func(n Machine) (bool, string) { return n.Mutable, n.BlockedReason },
		apply: func(ctx context.Context, do *digitalocean.Client, n Machine) (map[string]any, error) {
			act, err := do.ResizeDroplet(ctx, n.ID, in.Size, in.Disk)
			if err != nil {
				return nil, err
			}
			return map[string]any{"name": n.Name, "from": n.SizeSlug, "to": in.Size,
				"permanent": in.Disk, "actionId": act.ID, "actionStatus": act.Status}, nil
		},
	})
}

// deleteLoadBalancer destroys a load balancer the board has just proven no live
// type=LoadBalancer Service in any cluster targets.
// Response: {"status":"ok","msg":"","data":{"deleted":true,"name":"ingress-lb","ip":"1.2.3.4",
// "freedMonthlyCents":1200}}
func (b *board) deleteLoadBalancer(ctx context.Context, in *LoadBalancerIn) (*MutationOut, error) {
	id := strings.TrimSpace(in.ID)
	return run(ctx, b, mutation[LoadBalancer]{
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
// Example: {"count":5}
// Response: {"status":"ok","msg":"","data":{"pool":"workers","cluster":"hanzo-k8s","from":3,"to":5}}
func (b *board) scaleNodePool(ctx context.Context, in *ScaleIn) (*MutationOut, error) {
	clusterID, pool := strings.TrimSpace(in.ID), strings.TrimSpace(in.Pool)
	return run(ctx, b, mutation[NodePool]{
		action: "infra.nodepool.scale", resType: "do_node_pool", resID: clusterID + "/" + pool,
		find:    func(snap Snapshot) (NodePool, bool) { return findNodePool(snap, clusterID, pool) },
		verdict: func(p NodePool) (bool, string) { return p.ScaleTo(in.Count) },
		apply: func(ctx context.Context, do *digitalocean.Client, p NodePool) (map[string]any, error) {
			if err := do.ScaleNodePool(ctx, p.ClusterID, p.ID, p.Name, in.Count); err != nil {
				return nil, err
			}
			out := map[string]any{"pool": p.Name, "cluster": p.Cluster, "from": p.Count, "to": in.Count}
			if in.Count < p.Count {
				out["note"] = "DOKS chooses which nodes to remove and drains them itself. " +
					"This board proved only that the cluster keeps a schedulable node; " +
					"PodDisruptionBudgets, taints, affinity and resource requests are enforced " +
					"by the cluster, so some pods may stay Pending."
			}
			return out, nil
		},
	})
}

// cordonNode marks one cluster node unschedulable — or schedulable again — and can drain
// the pods already on it.
//
// It is the ONE infra change that does not go through the run discipline, because there
// is no destructive verdict to check: cordoning is reversible and evicting respects the
// cluster's own PodDisruptionBudgets. It reads the cached board for the same reason.
// The outcome is audited either way, and the result reports how many pods were evicted.
//
// Example: {"cordon":true,"drain":true}
// Response: {"status":"ok","msg":"","data":{"name":"worker-3","schedulable":false,"evicted":7}}
func (b *board) cordonNode(ctx context.Context, in *CordonIn) (*MutationOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := b.s
	id, err := strconv.Atoi(strings.TrimSpace(in.ID))
	if err != nil {
		return &MutationOut{Status: core.Err, Msg: "node id must be a droplet id"}, nil
	}
	snap, err := b.load(ctx, s.State.DO, false)
	if err != nil {
		return &MutationOut{Status: core.Err, Msg: err.Error()}, nil
	}
	node, ok := findNode(snap, id)
	if !ok {
		return &MutationOut{Status: core.Err, Msg: "node not found"}, nil
	}
	if node.ClusterID == "" {
		return &MutationOut{Status: core.Err, Msg: "node is not a member of a known cluster"}, nil
	}
	evicted, err := SetSchedulable(ctx, s.State.DO, node.ClusterID, node.Name, !in.Cordon, in.Drain)
	out := map[string]any{"name": node.Name, "schedulable": !in.Cordon, "evicted": evicted}
	if err != nil {
		core.EmitAudit(s, c, "infra.node.cordon", "do_droplet", node.Name, node, out,
			audit.Outcome{Result: "failure", Status: 200, Reason: err.Error()})
		return &MutationOut{Status: core.Err, Msg: err.Error()}, nil
	}
	b.invalidate()
	core.EmitAudit(s, c, "infra.node.cordon", "do_droplet", node.Name, node, out,
		audit.Outcome{Result: "success", Status: 200})
	return &MutationOut{Status: core.OK, Data: out}, nil
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

func findNode(s Snapshot, id int) (Machine, bool) {
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Machine{}, false
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
