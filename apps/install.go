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

// mountAI installs the money, ingest and telemetry wiring, then mounts ai. A nil
// callback is left alone — cloud leaves one nil exactly when that subsystem
// isn't co-resident, and the module's own fallback applies.
func mountAI(app *zip.App, deps cloud.Deps) error {
	// One provider, one wire. cloud.Serve installed the process-global tracer
	// provider before MountAll; DECLARE it to ai here so ai emits every gen_ai span
	// through THAT provider instead of forking its own. Without this ai's
	// object.InitTelemetry (run inside ai.Mount, just below) finds no exporter
	// endpoint — in-process mode sets none and Serve clears the OTLP env — and
	// DISABLES its emit, which is exactly why the gen_ai plane was dark while
	// cloud's own /v1/* request spans reached o11y_traces.
	//
	// Gated on the provider actually being installed: adopting the global NO-OP
	// provider would latch ai "telemetry ready" against something that discards
	// every span AND suppress ai's own standalone fallback. This lives here, beside
	// the other aiobject.Set* calls, because apps/ already links ai — the host must
	// not (github.com/hanzoai/ai/object is 1270 packages, and cloud's root is 644).
	if cloud.TracerProviderInstalled() {
		aiobject.AdoptHostTracerProvider()
	}
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
