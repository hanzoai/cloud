// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

// The CLUSTER half of a lease: a sandbox may run on one of the org's ATTACHED
// clusters (apps/fleet) instead of the home one, and the seams that make that
// safe are the ones proved here — the registry is the only resolver, an
// absence is a 404 with nothing behind it, and the gvisor floor is asked of
// the attached cluster rather than assumed of it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/zap-proto/zip"
	"k8s.io/client-go/rest"
)

// fakeAttached is the fleet registry with one org's one cluster in it,
// recording what was asked of it.
type fakeAttached struct {
	org, name string
	cfg       *rest.Config
	asked     []string
}

func (f *fakeAttached) RESTForOrgCluster(_ context.Context, org, project, name string) (*rest.Config, error) {
	f.asked = append(f.asked, org+"/"+project+"/"+name)
	if org == f.org && name == f.name && f.cfg != nil {
		return f.cfg, nil
	}
	return nil, fleet.ErrNoCluster
}

// apiserver is the attached cluster: enough of one to answer the RuntimeClass
// probe with the classes it installs, and nothing else — so a lease proceeds
// exactly to the pod create and fails there, proving where it was headed.
func apiserver(t *testing.T, classes ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/apis/node.k8s.io/v1/runtimeclasses", func(w http.ResponseWriter, _ *http.Request) {
		items := make([]map[string]any, 0, len(classes))
		for _, c := range classes {
			items = append(items, map[string]any{"metadata": map[string]any{"name": c}, "handler": c})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "node.k8s.io/v1", "kind": "RuntimeClassList", "items": items,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A lease that names a cluster resolves it THROUGH THE REGISTRY — the
// validated org, the default project shard, the given name — and the row
// records where the sandbox ran.
func TestLeaseOnAnAttachedClusterResolvesTheRegistry(t *testing.T) {
	away(t)
	s, err := New(cloud.Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := apiserver(t, "gvisor")
	fa := &fakeAttached{org: "acme", name: "lab", cfg: &rest.Config{Host: srv.URL}}
	s.State.rt.attached = fa
	ctx := context.Background()

	// The fake cluster serves no pods, so the lease reaches its start and fails
	// THERE — past the resolve, past the derivation, with the row written.
	_, err = Lease(s, ctx, "acme", "acme", false, "", Spec{Class: "exec", Cluster: "lab"})
	if err == nil {
		t.Fatal("Lease succeeded against a cluster that serves no pods")
	}
	if !strings.Contains(err.Error(), "start sandbox") {
		t.Fatalf("Lease failed before the cluster was used: %v", err)
	}
	if len(fa.asked) != 1 || fa.asked[0] != "acme//lab" {
		t.Fatalf("registry asked %v, want exactly [acme//lab] (org-scoped, default project)", fa.asked)
	}
	out, err := List(s, ctx, "acme", "", "")
	if err != nil || len(out) != 1 {
		t.Fatalf("List = %+v, %v — want the one row the lease recorded", out, err)
	}
	if out[0].Cluster != "lab" {
		t.Fatalf("row cluster = %q, want lab", out[0].Cluster)
	}
	// The floor holds off home exactly as on it: the attached cluster installs
	// gvisor and nothing stronger, so that is what the sandbox got.
	if out[0].Runtime != shared {
		t.Fatalf("row runtime = %q, want %q", out[0].Runtime, shared)
	}

	// The resolve is paid once: a second lease reuses the built runtime.
	_, _ = Lease(s, ctx, "acme", "acme", false, "", Spec{Class: "exec", Cluster: "lab"})
	if len(fa.asked) != 1 {
		t.Fatalf("registry asked %v, want the first resolve reused", fa.asked)
	}
}

// A cluster the org has not attached is an absence, and an absence is 404 —
// with nothing built and nothing recorded behind it.
func TestLeaseOnAnUnknownClusterIs404(t *testing.T) {
	away(t)
	s, err := New(cloud.Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.State.rt.attached = &fakeAttached{}
	ctx := context.Background()

	_, err = Lease(s, ctx, "acme", "acme", false, "", Spec{Class: "exec", Cluster: "nope"})
	he, ok := err.(*zip.HTTPError)
	if !ok || he.Status != http.StatusNotFound {
		t.Fatalf("unknown cluster: err = %v, want 404", err)
	}
	if out, _ := List(s, ctx, "acme", "", ""); len(out) != 0 {
		t.Fatalf("a refused lease left a row behind: %+v", out)
	}

	// A deployment with no registry at all answers the same absence.
	s.State.rt.attached = nil
	he = nil
	if _, err := Lease(s, ctx, "acme", "acme", false, "", Spec{Class: "exec", Cluster: "lab"}); !errorsAs(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("no registry: err = %v, want 404", err)
	}
}

// errorsAs is the one assertion shape both refusal tests need.
func errorsAs(err error, out **zip.HTTPError) bool {
	he, ok := err.(*zip.HTTPError)
	if ok {
		*out = he
	}
	return ok
}

// The floor is asked of the ATTACHED cluster, not assumed of it: a cluster
// without the gvisor RuntimeClass cannot hold any tenant sandbox, so the lease
// refuses by name instead of writing a pod that waits Pending forever.
func TestLeaseRefusesAClusterWithoutTheFloor(t *testing.T) {
	away(t)
	s, err := New(cloud.Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := apiserver(t, "runc") // installed, and not a floor a tenant may stand on
	s.State.rt.attached = &fakeAttached{org: "acme", name: "lab", cfg: &rest.Config{Host: srv.URL}}
	ctx := context.Background()

	_, err = Lease(s, ctx, "acme", "acme", false, "", Spec{Class: "exec", Cluster: "lab"})
	he, ok := err.(*zip.HTTPError)
	if !ok || he.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "runtime class") {
		t.Fatalf("floorless cluster: err = %v, want a 400 naming the runtime class", err)
	}
	if out, _ := List(s, ctx, "acme", "", ""); len(out) != 0 {
		t.Fatalf("a refused lease left a row behind: %+v", out)
	}
}
