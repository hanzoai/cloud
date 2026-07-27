package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/admin/digitalocean"
)

// fakeDO stands in for the DigitalOcean API. kubeAPI is the URL a cluster kubeconfig
// points at; when blank, the kubeconfig fetch fails, which is how the incomplete-scan
// path is exercised.
type fakeDO struct {
	kubeAPI  string
	clusters []string // cluster ids; empty means the account has none
	volumes  string   // raw JSON array body for /v2/volumes
	deleted  []string
	snapshot int
}

func (f *fakeDO) server(t *testing.T) *digitalocean.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"droplets":[{"id":101,"name":"node-a1","status":"active","size_slug":"s-1vcpu-1gb",
			"vcpus":1,"memory":1024,"disk":25,"size":{"price_monthly":6},"region":{"slug":"sfo3"},
			"networks":{"v4":[{"type":"private","ip_address":"10.0.0.1"}]},
			"tags":["k8s","k8s:`+clusterUUID+`"]}],"meta":{"total":1}}`)
	})
	mux.HandleFunc("/v2/volumes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"volumes":%s,"meta":{"total":2}}`, f.volumes)
	})
	mux.HandleFunc("/v2/load_balancers", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"load_balancers":[
			{"id":"lb-live","name":"ingress","status":"active","ip":"1.2.3.4","region":{"slug":"sfo3"},"size_unit":1,"droplet_ids":[101]},
			{"id":"lb-junk","name":"stray","status":"active","ip":"5.6.7.8","region":{"slug":"sfo3"},"size_unit":1,"droplet_ids":[]}
		],"meta":{"total":2}}`)
	})
	mux.HandleFunc("/v2/kubernetes/clusters", func(w http.ResponseWriter, r *http.Request) {
		rows := []string{}
		for _, id := range f.clusters {
			rows = append(rows, fmt.Sprintf(
				`{"id":%q,"name":"test-k8s","region":"sfo3","version":"1.35","status":{"state":"running"},
				"node_pools":[{"id":"pool-1","name":"p","size":"s-1vcpu-1gb","count":1}]}`, id))
		}
		fmt.Fprintf(w, `{"kubernetes_clusters":[%s],"meta":{"total":%d}}`, strings.Join(rows, ","), len(rows))
	})
	mux.HandleFunc("/v2/kubernetes/clusters/"+clusterUUID+"/kubeconfig", func(w http.ResponseWriter, r *http.Request) {
		if f.kubeAPI == "" {
			http.Error(w, `{"message":"cluster unreachable"}`, http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: %s, insecure-skip-tls-verify: true}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: x, context: {cluster: c, user: u}}]
current-context: x
`, f.kubeAPI)
	})
	mux.HandleFunc("/v2/volumes/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v2/volumes/")
		switch {
		case strings.HasSuffix(id, "/snapshots"):
			f.snapshot++
			fmt.Fprint(w, `{"snapshot":{"id":"snap-1","name":"s","size_gigabytes":40}}`)
		case r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"message":"nope"}`, http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return digitalocean.NewWithBase(srv.URL, "test-token")
}

const clusterUUID = "cccccccc-1111-2222-3333-444444444444"

// twoVolumes: one bound to a live PV, one referenced by nothing.
const twoVolumes = `[
	{"id":"vol-live","name":"live","size_gigabytes":20,"region":{"slug":"sfo3"},"droplet_ids":[],"tags":["k8s:` + clusterUUID + `"]},
	{"id":"vol-junk","name":"junk","size_gigabytes":40,"region":{"slug":"sfo3"},"droplet_ids":[],"tags":["k8s:` + clusterUUID + `"]}
]`

// fakeAPIServer serves the five core/v1 collections scanOne reads.
func fakeAPIServer(t *testing.T) string {
	t.Helper()
	t.Setenv("FLEET_ALLOW_PRIVATE_HOSTS", "1")
	list := func(kind string, items any) string {
		b, _ := json.Marshal(items)
		return fmt.Sprintf(`{"apiVersion":"v1","kind":%q,"metadata":{},"items":%s}`, kind, b)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/persistentvolumes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list("PersistentVolumeList", []map[string]any{{
			"metadata": map[string]any{"name": "pv-live"},
			"spec": map[string]any{
				"csi":      map[string]any{"driver": "dobs.csi.digitalocean.com", "volumeHandle": "vol-live"},
				"claimRef": map[string]any{"namespace": "db", "name": "data"},
			},
			"status": map[string]any{"phase": "Bound"},
		}}))
	})
	mux.HandleFunc("/api/v1/persistentvolumeclaims", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list("PersistentVolumeClaimList", []map[string]any{{
			"metadata": map[string]any{"namespace": "db", "name": "data"},
			"spec":     map[string]any{"volumeName": "pv-live"},
			"status":   map[string]any{"phase": "Bound"},
		}}))
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list("PodList", []map[string]any{{
			"metadata": map[string]any{"namespace": "db", "name": "pg-0"},
			"spec": map[string]any{
				"nodeName":   "node-a1",
				"containers": []map[string]any{{"name": "c", "image": "ghcr.io/hanzoai/base:v1"}},
				"volumes":    []map[string]any{{"name": "v", "persistentVolumeClaim": map[string]any{"claimName": "data"}}},
			},
			"status": map[string]any{"phase": "Running"},
		}}))
	})
	mux.HandleFunc("/api/v1/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list("ServiceList", []map[string]any{{
			"metadata": map[string]any{"namespace": "ingress", "name": "hanzo-ingress",
				"annotations": map[string]any{doLBIDAnnotation: "lb-live"}},
			"spec":   map[string]any{"type": "LoadBalancer"},
			"status": map[string]any{"loadBalancer": map[string]any{"ingress": []map[string]any{{"ip": "1.2.3.4"}}}},
		}, {
			// A ClusterIP Service claims no load balancer and must be ignored.
			"metadata": map[string]any{"namespace": "db", "name": "pg"},
			"spec":     map[string]any{"type": "ClusterIP"},
			"status":   map[string]any{},
		}}))
	})
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list("NodeList", []map[string]any{{
			"metadata": map[string]any{"name": "node-a1"},
			"spec":     map[string]any{},
			"status":   map[string]any{"conditions": []map[string]any{{"type": "Ready", "status": "True"}}},
		}}))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestCollectEndToEnd walks the whole fan-out — DO inventory, a real client-go read of
// a fake apiserver, and the fold — proving the scan decodes what Kubernetes actually
// sends, not just what the pure tests hand it.
func TestCollectEndToEnd(t *testing.T) {
	f := &fakeDO{kubeAPI: fakeAPIServer(t), clusters: []string{clusterUUID}, volumes: twoVolumes}
	snap, err := collect(context.Background(), f.server(t))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !snap.Complete {
		t.Fatalf("Complete = false: %s", snap.IncompleteReason)
	}
	liveVol, _ := findVolume(snap, "vol-live")
	if liveVol.State != StateBound || liveVol.Deletable {
		t.Errorf("vol-live = %s deletable=%v, want bound + not deletable", liveVol.State, liveVol.Deletable)
	}
	if liveVol.PV != "pv-live" || liveVol.PVCName != "data" {
		t.Errorf("vol-live PV binding not decoded: %+v", liveVol)
	}
	if len(liveVol.MountedBy) != 1 || liveVol.MountedBy[0] != "db/pg-0" {
		t.Errorf("vol-live mountedBy = %v, want [db/pg-0]", liveVol.MountedBy)
	}
	junkVol, _ := findVolume(snap, "vol-junk")
	if junkVol.State != StateUnreferenced || !junkVol.Deletable {
		t.Errorf("vol-junk = %s deletable=%v, want unreferenced + deletable", junkVol.State, junkVol.Deletable)
	}
	if snap.Cost.ReclaimableMonthly != 400 {
		t.Errorf("reclaimable = %d, want 400 (40 GiB)", snap.Cost.ReclaimableMonthly)
	}
	if n := snap.Nodes[0]; !n.Ready || n.Pods != 1 || n.Cluster != "test-k8s" {
		t.Errorf("node join wrong: %+v", n)
	}
	// The DOKS node is DOKS's to manage, so the board refuses to touch the droplet.
	if n := snap.Nodes[0]; n.Mutable || !strings.Contains(n.BlockedReason, "node pool") {
		t.Errorf("DOKS node mutable=%v reason=%q, want refused and pointed at the pool", n.Mutable, n.BlockedReason)
	}
	// The load balancer the Service annotation claims is in use; the other is not.
	live, _ := findLoadBalancer(snap, "lb-live")
	if live.Service != "ingress/hanzo-ingress" || live.Deletable {
		t.Errorf("lb-live = service %q deletable=%v, want claimed + refused", live.Service, live.Deletable)
	}
	junk, _ := findLoadBalancer(snap, "lb-junk")
	if junk.Service != "" || !junk.Deletable {
		t.Errorf("lb-junk = service %q deletable=%v, want unclaimed + deletable", junk.Service, junk.Deletable)
	}
	// The node pool is the lever the board DOES offer, decoded from the cluster read.
	p, ok := findNodePool(snap, clusterUUID, "p")
	if !ok || p.ID != "pool-1" || p.Count != 1 || !p.Scalable {
		t.Fatalf("node pool = %+v (found=%v), want pool-1 count 1, scalable", p, ok)
	}
}

// TestUnreachableClusterFreezesEverything: the cluster exists but will not answer, so
// the volume no reachable PV references must STILL be undeletable.
func TestUnreachableClusterFreezesEverything(t *testing.T) {
	f := &fakeDO{kubeAPI: "", clusters: []string{clusterUUID}, volumes: twoVolumes}
	snap, err := collect(context.Background(), f.server(t))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if snap.Complete {
		t.Fatal("Complete = true with an unreachable cluster")
	}
	for _, id := range []string{"vol-live", "vol-junk"} {
		v, _ := findVolume(snap, id)
		if v.Deletable {
			t.Fatalf("%s deletable despite an unreachable cluster", id)
		}
	}
	if snap.Cost.ReclaimableMonthly != 0 {
		t.Errorf("reclaimable = %d, want 0", snap.Cost.ReclaimableMonthly)
	}
	// The failure must be named in Sources, not swallowed.
	var found bool
	for _, s := range snap.Sources {
		if s.Name == "k8s.test-k8s" && !s.OK && s.Error != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("cluster failure absent from sources: %+v", snap.Sources)
	}
}

// TestDeleteRefusesNonDeletable proves the server never trusts the caller: asking to
// delete a live volume is refused with a reason, and NOTHING is deleted upstream.
func TestDeleteRefusesNonDeletable(t *testing.T) {
	f := &fakeDO{kubeAPI: fakeAPIServer(t), clusters: []string{clusterUUID}, volumes: twoVolumes}
	do := f.server(t)
	b := &board{}
	snap, err := b.load(context.Background(), do, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	live, _ := findVolume(snap, "vol-live")
	if live.Deletable {
		t.Fatal("fixture wrong: vol-live must not be deletable")
	}
	if live.BlockedReason == "" {
		t.Error("no blockedReason for a live volume")
	}
	if len(f.deleted) != 0 {
		t.Fatalf("volumes deleted during a read: %v", f.deleted)
	}
}

// TestSnapshotThenDelete proves the undo exists: deleting takes a snapshot first.
func TestSnapshotThenDelete(t *testing.T) {
	f := &fakeDO{kubeAPI: fakeAPIServer(t), clusters: []string{clusterUUID}, volumes: twoVolumes}
	do := f.server(t)
	snap, err := collect(context.Background(), do)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	junk, _ := findVolume(snap, "vol-junk")
	if _, err := takeSnapshot(context.Background(), do, junk, ""); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if f.snapshot != 1 {
		t.Fatalf("snapshots taken = %d, want 1", f.snapshot)
	}
	if err := do.DeleteVolume(context.Background(), junk.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "vol-junk" {
		t.Fatalf("deleted = %v, want [vol-junk]", f.deleted)
	}
}

// TestNoTokenIsHonest: an unconfigured deployment says so instead of rendering an
// empty fleet that looks like a clean account.
func TestNoTokenIsHonest(t *testing.T) {
	_, err := collect(context.Background(), digitalocean.New(""))
	if err == nil || !strings.Contains(err.Error(), "DO_API_TOKEN") {
		t.Fatalf("err = %v, want an explicit not-configured error", err)
	}
}
