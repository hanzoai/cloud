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
	"github.com/zap-proto/zip"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	synccommon "github.com/hanzoai/deploy/gitops-engine/pkg/sync/common"
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
func engineRepo() string      { return envOr("DEPLOY_ENGINE_REPO", "https://github.com/hanzoai/universe") }
func engineRef() string       { return envOr("DEPLOY_ENGINE_REF", "main") }
func enginePath() string      { return envOr("DEPLOY_ENGINE_PATH", "infra/k8s/operator/crs") }
func engineInstance() string  { return envOr("DEPLOY_ENGINE_INSTANCE", "universe") }
func engineDefaultNS() string { return envOr("DEPLOY_ENGINE_NAMESPACE", "hanzo") }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// registerEngineRoutes adds the engine (write) routes alongside the existing
// read/visualize routes. Called from routes() in deploy.go.
func registerEngineRoutes(app cloud.Router, s *cloud.Service[state]) {
	z := cloud.ZipApp(app)
	zip.Post(z.With(admitted), "/v1/deploy/reconcile", ops{s: s}.reconcile)
}

// reconcileResult is one resource's outcome in a sync.
type reconcileResult struct {
	// Resource is the object key (group/kind/namespace/name) that was applied.
	Resource string `json:"resource"`
	// Status is the engine's outcome: Synced, Pruned, SyncFailed or Unknown.
	Status string `json:"status"`
	// Message is the engine's detail for that outcome; empty when it succeeded cleanly.
	Message string `json:"message"`
}

// reconcileSource names the git source the sync rendered.
type reconcileSource struct {
	// Repo is the git repository the manifests were rendered from.
	Repo string `json:"repo"`
	// Ref is the branch or tag that was rendered.
	Ref string `json:"ref"`
	// Path is the directory within the repo that was rendered.
	Path string `json:"path"`
}

// reconcileView is one engine sync's report.
type reconcileView struct {
	// Revision is the commit that was rendered and applied.
	Revision string `json:"revision"`
	// Source is the git source the revision came from.
	Source reconcileSource `json:"source"`
	// Instance is the tracking instance the applied objects are labelled with.
	Instance string `json:"instance"`
	// Prune reports whether objects absent from the source were deleted.
	Prune bool `json:"prune"`
	// Declared is how many objects the source rendered.
	Declared int `json:"declared"`
	// Synced is how many objects were applied.
	Synced int `json:"synced"`
	// Pruned is how many objects were deleted.
	Pruned int `json:"pruned"`
	// Failed is how many objects the apply rejected.
	Failed int `json:"failed"`
	// Results is the per-resource outcome.
	Results []reconcileResult `json:"results"`
}

// reconcile renders the configured git source and applies it to the cluster once.
// It is the SuperAdmin-gated write half that replaces universe-crs: a three-way
// server-side apply through the embedded gitops-engine, with a scoped prune and
// per-resource health. Disabled unless DEPLOY_ENGINE_ENABLED is set.
func (o ops) reconcile(ctx context.Context, _ *struct{}) (*reconcileView, error) {
	s := o.s
	if !engineEnabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "deploy engine disabled (set DEPLOY_ENGINE_ENABLED=true)")
	}
	// The source is read AS THE CALLER, so the request itself has to be in hand: a
	// native repo scopes its own answer rather than trusting this plane to have.
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("not authorized for this deploy console")
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

	// The principal is delegated to whichever source reads: a native repo is
	// read as the caller, so the git plane scopes the answer itself rather than
	// trusting this plane to have scoped it.
	objs, revision, err := newSource(engineRepo(), engineRef(), enginePath(), c).render(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine: render source: %v", err)
	}

	results, err := rec.reconcile(ctx, objs, revision, engineDefaultNS(), enginePrune(), pruneFuse())
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine: sync: %v", err)
	}

	synced, pruned, failed := 0, 0, 0
	items := make([]reconcileResult, 0, len(results))
	for _, rr := range results {
		switch rr.Status {
		case synccommon.ResultCodeSynced:
			synced++
		case synccommon.ResultCodePruned:
			pruned++
		case synccommon.ResultCodeSyncFailed:
			failed++
		}
		items = append(items, reconcileResult{
			Resource: rr.ResourceKey.String(),
			Status:   string(rr.Status),
			Message:  rr.Message,
		})
	}
	s.Log.Info("deploy engine reconcile", "revision", revision, "objects", len(objs),
		"synced", synced, "pruned", pruned, "failed", failed, "prune", enginePrune())

	return &reconcileView{
		Revision: revision,
		Source:   reconcileSource{Repo: engineRepo(), Ref: engineRef(), Path: enginePath()},
		Instance: engineInstance(),
		Prune:    enginePrune(),
		Declared: len(objs),
		Synced:   synced,
		Pruned:   pruned,
		Failed:   failed,
		Results:  items,
	}, nil
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
