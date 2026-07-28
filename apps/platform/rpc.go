package platform

import (
	"context"

	"github.com/hanzoai/cloud"
)

// rpc.go — platform's methods on the internal plane.
//
// The fleet board is a READ of what the operator already reconciled, so it
// crosses as the projection admin renders, not as the k8s client that produced
// it. Reading it by import cost the caller client-go + apimachinery and still
// returned nothing: CurrentFleet resolves a package global that only exists in
// a binary which mounts platform.

// exposeFleet publishes the observer's view. An unmounted or unready observer
// is an EMPTY fleet with no error — the same honest-empty the in-process board
// rendered, because "the operator has not observed yet" is a real state and not
// a failure. Anything else (an RBAC denial listing apps.hanzo.ai) is an error,
// so a board never shows a denial as an empty estate.
func exposeFleet() {
	cloud.Expose("platform.fleet", func(ctx context.Context, who cloud.Ident, _ []byte) ([]byte, error) {
		if !who.Admin {
			return nil, cloud.Fault(403, "SuperAdmin required")
		}
		f := CurrentFleet()
		if f == nil {
			return cloud.PutApps(nil), nil
		}
		if ok, _ := f.Ready(); !ok {
			return cloud.PutApps(nil), nil
		}
		views, err := f.Observe(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]cloud.App, 0, len(views))
		for _, v := range views {
			out = append(out, cloud.App{
				Org: v.Org, Name: v.App, Env: v.Env, Repo: v.Repo, Role: v.Role,
				Cluster: v.Cluster, Namespace: v.Namespace,
				Phase: v.Phase, Health: v.Health,
				DeclaredTag: v.DeclaredTag, RunningTag: v.RunningTag, LatestTag: v.LatestTag,
				Registry:      v.Registry,
				DriftSeverity: string(v.Drift.Severity),
			})
		}
		return cloud.PutApps(out), nil
	})
}
