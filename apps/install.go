package apps

import (
	"context"

	"github.com/hanzoai/ai"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/membership"
	"github.com/zap-proto/zip"
)

// cloud builds these callbacks but does not install them: importing the ai
// module or the Kubernetes client from cloud drags ~1700 packages into every
// subsystem, since they all import cloud for Deps. Registration lives here,
// where both are already linked.

// Outside a cluster K8s errors and cloud falls back to its static peer set.
func init() { cloud.Peers = membership.K8s }

// mountAI installs the money and ingest callbacks, then mounts ai. A nil
// callback is left alone — cloud leaves one nil exactly when that subsystem
// isn't co-resident, and the module's own fallback applies.
func mountAI(app *zip.App, deps cloud.Deps) error {
	if f := cloud.TierReader(); f != nil {
		aiobject.SetTierReader(aiobject.TierReaderFunc(f))
	}
	if f := cloud.BalanceReader(); f != nil {
		aiobject.SetBalanceReader(aiobject.BalanceReaderFunc(f))
	}
	if f := cloud.UsageRecorder(); f != nil {
		aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
			return f(ctx, cloud.UsageEvent{
				Subject: u.Subject, Namespace: u.Namespace, USD: u.USD,
				Currency: u.Currency, Model: u.Model, Provider: u.Provider,
				RequestID: u.RequestID,
			})
		})
	}
	if d := cloud.IngestDialer(); d != nil {
		aiobject.SetIngestDialer(d)
	}
	return ai.Mount(app, deps)
}
