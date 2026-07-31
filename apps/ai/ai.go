// Package ai is Hanzo AI — the model API on /v1 (/v1/chat/completions,
// /v1/messages, /v1/models and the rest of hanzoai/ai's surface) — mounted into
// a cloud binary with the money, ingest and telemetry callbacks cloud BUILDS
// but cannot INSTALL.
//
// It cannot live in package cloud — github.com/hanzoai/ai imports
// github.com/hanzoai/cloud, so that direction is an import cycle. It lived in
// package apps for that reason, and the cost was that the composition root's ai
// entry named an apps-local helper: plugin/gen-app-cmds can only emit a
// package-qualified expression, so ai got the fat stub (every subsystem linked)
// and NO manifest row — which is why the light host 404'd /v1/chat/completions.
// A sibling package that imports both the module and cloud is legal and is all
// this needs to be.
//
// The module is aliased because this package is also called ai: unaliased,
// `return ai.Mount(...)` inside `func Mount` reads as recursion.
package ai

import (
	"context"
	"fmt"

	aimod "github.com/hanzoai/ai"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Mount installs the money, ingest and telemetry wiring, then mounts ai. A nil
// callback is left alone — cloud leaves one nil exactly when that subsystem
// isn't co-resident, and the module's own fallback applies.
func Mount(app *zip.App, deps cloud.Deps) error {
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
	// the other aiobject.Set* calls, because this package already links ai — the
	// host must not (github.com/hanzoai/ai/object is 1270 packages, and cloud's
	// root is 644).
	if cloud.TracerProviderInstalled() {
		aiobject.AdoptHostTracerProvider()
	}
	// TRAMPOLINES, not snapshots — for the same reason the rolling-cap hook below is
	// one, and it is not a hypothetical: `ai` is a LAZY plugin (it mounts on the first
	// /v1/chat/completions), so it can mount before the app that installs these hooks.
	// A nil snapshot then latches for the PROCESS LIFETIME, and the money hooks have no
	// benign nil: with no native balance reader the ai module falls back to an HTTP
	// self-call to /v1/billing/*, which the edge 401s (the toothless-gate bug named in
	// cloud/build.go wireTierReader) — and the balance gate is fail-CLOSED, so every
	// completion answered 503 balance_unavailable. Observed in prod on v1.801.320:
	// "balance_gate: balance unverifiable for cold subject=hanzo: commerce returned 401
	// (fail-CLOSED, retryable)" on a pod whose commerce plugin was fully mounted 10
	// minutes later. Resolving per call takes mount order out of the equation.
	aiobject.SetTierReader(func(ctx context.Context, subject, namespace string) (string, error) {
		f := cloud.TierReader()
		if f == nil {
			return "", nil // unknown tier → the gate ALLOWs, the documented fail-safe
		}
		return f(ctx, subject, namespace)
	})
	aiobject.SetBalanceReader(func(ctx context.Context, subject, namespace, currency string) (int64, error) {
		f := cloud.BalanceReader()
		if f == nil {
			// Say WHICH wiring is missing. Returning 0 would read as a real zero balance
			// and deny a paying caller as "insufficient"; the ai module's other branch —
			// an HTTP self-call to /v1/billing/* — is the path the edge 401s, so it is
			// deliberately unreachable from cloud. Fail closed, and legibly.
			return 0, fmt.Errorf("no native balance reader installed in this process (commerce/finance not wired); refusing to guess a balance")
		}
		return f(ctx, subject, namespace, currency)
	})
	aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
		f := cloud.UsageRecorder()
		if f == nil {
			return nil // nothing to debit through; ai's own path records it
		}
		return f(ctx, cloud.UsageEvent{
			Subject: u.Subject, Namespace: u.Namespace, USD: u.USD,
			Currency: u.Currency, Model: u.Model, Provider: u.Provider,
			RequestID: u.RequestID,
		})
	})
	if d := cloud.IngestDialer(); d != nil {
		aiobject.SetIngestDialer(d)
	}
	// A TRAMPOLINE, not a snapshot like the four above: clients/rollingcap installs
	// its reader from Mount, and apps.Wire() mounts rollingcap AFTER this package in
	// some binaries. Resolving cloud.RollingCapReader() per request instead of once
	// at wire time takes mount order out of the equation entirely.
	aiobject.SetRollingCapReader(func(ctx context.Context, subject, namespace string) (bool, error) {
		f := cloud.RollingCapReader()
		if f == nil {
			return false, nil // no cap installed → uncapped, the same semantics a nil hook had
		}
		return f(ctx, subject, namespace)
	})
	// UNTYPED BY DESIGN, and NOT this package's to fix. aimod.Mount registers a
	// single `app.All("/v1/*")` (hanzoai/ai mount.go) adapting the legacy beego
	// ControllerRegister through zip.AdaptNetHTTP, so ai's ~200 real routes —
	// /v1/chat/completions, /v1/models, /v1/messages and the rest — reach the wire
	// through ONE greedy wildcard. Typing it here is impossible on three counts
	// (All has no typed registrar; a `{wildcard1}` path segment cannot be a bound
	// In field; the adapter relays the beego handler's own status and Content-Type
	// verbatim), and typing it AT ALL means declaring ops inside github.com/hanzoai/ai
	// where those handlers live. The consequence is worth stating plainly because
	// it is the largest hole in the fleet document: plugin/ai/openapi.json publishes
	// seven operations at /v1/{wildcard1} and NONE of the AI API, so no generated
	// SDK and no MCP tool list carries chat completions today.
	return aimod.Mount(app, deps)
}
