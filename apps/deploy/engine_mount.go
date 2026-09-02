// engine_mount.go wires the embedded gitops-engine (engine.go) into the
// /v1/deploy surface: a SuperAdmin-gated, one-shot reconcile endpoint that
// renders the configured git source and syncs it → cluster. This is the write
// half of /v1/deploy that replaces the retired universe-crs Application — the
// operator still renders each App CR into workloads (the domain half).
//
// Fail-safe: the whole path is gated by DEPLOY_ENGINE_ENABLED (default off), so
// the first deploy of this binary is inert and the engine is turned on
// deliberately after the shadow proof.

package deploy

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	synccommon "github.com/hanzoai/cd/gitops-engine/pkg/sync/common"
)

// Engine config — all optional; defaults target the live universe manifest repo
// (the exact source universe-crs syncs). Configure only what must vary.
func engineEnabled() bool { return os.Getenv("DEPLOY_ENGINE_ENABLED") == "true" }
func enginePrune() bool   { return os.Getenv("DEPLOY_ENGINE_PRUNE") == "true" }

// pruneFuse bounds a single reconcile's deletions (RED HIGH-1). Conservative
// defaults: at most 10 objects OR 20% of the managed set, whichever is smaller,
// unless explicitly raised. A silent empty/partial render trips the fuse instead
// of sweeping the fleet.
func pruneFuse() PruneFuse {
	return PruneFuse{
		MaxDeletions: envInt("DEPLOY_ENGINE_PRUNE_MAX", 10),
		MaxRatio:     envFloat("DEPLOY_ENGINE_PRUNE_MAX_RATIO", 0.20),
	}
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

func envFloat(k string, d float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return d
}
func engineRepo() string {
	return environ.Or("DEPLOY_ENGINE_REPO", "https://github.com/hanzoai/universe")
}
func engineRef() string       { return environ.Or("DEPLOY_ENGINE_REF", "main") }
func enginePath() string      { return environ.Or("DEPLOY_ENGINE_PATH", "infra/k8s/operator/crs") }
func engineInstance() string  { return environ.Or("DEPLOY_ENGINE_INSTANCE", "universe") }
func engineDefaultNS() string { return environ.Or("DEPLOY_ENGINE_NAMESPACE", "hanzo") }

// registerEngineRoutes adds the engine (write) op alongside the existing
// read/visualize routes. Called from routes() in deploy.go.
//
// It is declared ABSOLUTELY on the concrete *zip.App rather than on a group: this
// file builds no group of its own, and cmd/zipdoc resolves a prefix it can READ
// in the file it is generating from — the same form the health probe beside it
// takes (routes(), deploy.go). Declared on the `cloud.Router` interface instead,
// zipdoc refuses outright and the op ships with no prose.
func registerEngineRoutes(app cloud.Router, s *cloud.Service[state]) {
	zip.Post(cloud.ZipApp(app), dashPrefix+"/reconcile", ops{s: s}.reconcile)
}

// RunDeployReconcile renders the configured git source and applies it to the
// cluster, once.
//
// It runs one full GitOps sync through the embedded engine — render the
// configured repo, ref and path, then three-way server-side apply with scoped
// prune — and answers the revision it applied, the source it came from, the
// declared/synced/pruned/failed counts and a per-resource result. This is the
// WRITE half of the plane: it mutates live cluster objects and, with prune
// enabled, deletes objects the source no longer declares.
//
// SuperAdmin-only and fail-closed, with the gate INSIDE the op because a typed op
// is also reached by POST /mcp and by the by-name call plane, where no route
// middleware runs. The git source is read AS THE PLATFORM, not as the caller: the
// coordinate is this deployment's own configuration and never a parameter, which
// is why the op reads no request body at all. A deployment with the engine
// switched off, or with no usable cluster config, answers 503; a failure to
// start, render or sync is a 502.
func (o ops) reconcile(ctx context.Context, _ *cloud.Unit) (*reconcileReport, error) {
	if _, err := superAdminOf(ctx); err != nil {
		return nil, err
	}
	if !engineEnabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "deploy engine disabled (set DEPLOY_ENGINE_ENABLED=true)")
	}
	cfg, err := engineRestConfig()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "engine: kube config: %v", err)
	}

	rec := newReconciler(cfg, []string{engineDefaultNS()}, engineInstance(), logr.Discard())
	stop, err := rec.run()
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine start: %v", err)
	}
	defer stop()
	// Let the informer cache warm before the first sync so live state is known.
	time.Sleep(2 * time.Second)

	// The source coordinate is this deployment's own configuration, and the gate
	// above admits SuperAdmins only — see forgeTree (source_tree.go) for why the
	// fleet's desired state is read as the platform rather than as whoever asked
	// for the reconcile.
	objs, revision, err := newSource(engineRepo(), engineRef(), enginePath()).render(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine: render source: %v", err)
	}

	results, err := rec.reconcile(ctx, objs, revision, engineDefaultNS(), enginePrune(), pruneFuse())
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine: sync: %v", err)
	}

	report := reconcileReport{
		Declared: len(objs),
		Instance: engineInstance(),
		Prune:    enginePrune(),
		Results:  make([]appliedResource, 0, len(results)),
		Revision: revision,
		Source:   reconcileSource{Path: enginePath(), Ref: engineRef(), Repo: engineRepo()},
	}
	for _, rr := range results {
		switch rr.Status {
		case synccommon.ResultCodeSynced:
			report.Synced++
		case synccommon.ResultCodePruned:
			report.Pruned++
		case synccommon.ResultCodeSyncFailed:
			report.Failed++
		}
		report.Results = append(report.Results, appliedResource{
			Message:  rr.Message,
			Resource: rr.ResourceKey.String(),
			Status:   string(rr.Status),
		})
	}
	o.s.Log.Info("deploy engine reconcile", "revision", revision, "objects", len(objs),
		"synced", report.Synced, "pruned", report.Pruned, "failed", report.Failed, "prune", enginePrune())
	return &report, nil
}

// reconcileReport is one engine run, as the answer carries it.
//
// Its fields are declared in ALPHABETICAL json-tag order, and that is load
// bearing rather than tidy: this shape replaced a map[string]any, encoding/json
// writes a map's keys sorted and a struct's fields in declaration order, so any
// other order would move the bytes on a route whose job is to report a live
// cluster mutation. TestReconcileReportIsByteIdenticalToTheMapItReplaced asserts
// the marshalled BYTES rather than an equal value, which is what proves it.
type reconcileReport struct {
	// Declared is how many objects the rendered source declares — the denominator
	// the three outcome counts below are read against. Zero means the render
	// produced nothing, which trips the prune fuse rather than sweeping the fleet.
	Declared int `json:"declared"`
	// Failed is how many objects the apply could not reconcile. Non-zero is a
	// PARTIAL run reported at 200: the engine applied what it could and each
	// failure names itself in Results, so a caller reads this number rather than
	// the status code to learn whether the fleet matches the source.
	Failed int `json:"failed"`
	// Instance is the tracking id this run stamps on everything it manages, so a
	// later run can tell the objects it owns from objects another instance
	// declares. DEPLOY_ENGINE_INSTANCE names it; the default is `universe`.
	Instance string `json:"instance"`
	// Prune reports whether DELETION was enabled for this run. False means an
	// object the source no longer declares was left alone rather than removed, so
	// a zero Pruned below means "nothing to delete" only when this is true.
	Prune bool `json:"prune"`
	// Pruned is how many live objects this run DELETED because the source no
	// longer declares them. Always 0 when Prune is false.
	Pruned int `json:"pruned"`
	// Results is one entry per object the run acted on, in the order the engine
	// applied them. Empty (never null) when the run reconciled nothing.
	Results []appliedResource `json:"results"`
	// Revision is the source commit this run applied, as the source resolved it —
	// a git commit SHA, not an image tag. It is what an operator cites when asking
	// what the cluster was last made to match.
	Revision string `json:"revision"`
	// Source is the git coordinate the run rendered. It is this deployment's own
	// configuration echoed back, never a request parameter, and it is reported so
	// a reader of the answer knows WHICH tree the revision names.
	Source reconcileSource `json:"source"`
	// Synced is how many objects the run applied successfully.
	Synced int `json:"synced"`
}

// reconcileSource is the git coordinate one engine run rendered. Alphabetical for
// the reason reconcileReport is: it replaced a nested map.
type reconcileSource struct {
	// Path is the directory WITHIN the repository that is rendered — everything
	// outside it is not this plane's desired state and is never applied.
	Path string `json:"path"`
	// Ref is the branch or tag the revision was resolved from.
	Ref string `json:"ref"`
	// Repo is the clone URL of the repository holding the desired state.
	Repo string `json:"repo"`
}

// appliedResource is what one engine run did to ONE cluster object. Alphabetical,
// for the reason reconcileReport is.
type appliedResource struct {
	// Message is the engine's own sentence about this object — the apiserver's
	// refusal on a failure, and typically empty on success. It is for a human
	// reading a failed run, not a value to branch on.
	Message string `json:"message"`
	// Resource identifies the object as group/version/kind/namespace/name, the
	// engine's own key. It is stable across runs, so two reports can be diffed on
	// it.
	Resource string `json:"resource"`
	// Status is what happened to this object, from the engine's closed
	// vocabulary: `Synced` (applied), `Pruned` (deleted because the source no
	// longer declares it), `SyncFailed` (refused — read Message) and
	// `PruneSkipped` (deletion was declined). It is the per-object detail behind
	// the report's counts.
	Status string `json:"status"`
}

// engineRestConfig builds a rest.Config from the in-cluster service account,
// falling back to KUBECONFIG for local/dev — the SAME construction as
// newClients() (deploy.go), so the engine talks to the same cluster.
func engineRestConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	cfg.UserAgent = userAgent
	return cfg, nil
}
