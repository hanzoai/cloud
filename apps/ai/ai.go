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
	"net/http"

	aimod "github.com/hanzoai/ai"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// fallback is the sentence every operation below shares, because each is read
// alone: the document publishes five operations at one address, and a consumer
// meeting `PATCH /v1/{wildcard1}` has no sibling to infer the rule from.
const fallback = "\n\nThis address is a FALLBACK, not a front door. It is registered last, so every " +
	"subsystem that claims a specific path under /v1 — billing, plans, pricing, the per-app " +
	"health routes — still serves its own, and this catches the rest. The wildcard is the " +
	"remainder of the path, and the answer is relayed with the model API's own status, " +
	"headers and content type, so a streamed response streams and an upstream refusal " +
	"arrives as itself."

// The prose for the five operations this package publishes. It is declared here
// rather than lifted from a doc comment because there is no handler in this
// repository to lift it from: aimod.Mount registers one greedy `app.All("/v1/*")`
// and the real routes live in github.com/hanzoai/ai. Describe is keyed on (method,
// path) and renders only while the router serves that route, so it states what is
// BEHIND the wildcard without pretending the operations have been typed — the
// document stops publishing five addresses and nothing else, and the typing work
// named above is unaffected.
func init() {
	openapi.Describe("/v1/*", http.MethodPost,
		"The Hanzo AI model API",
		"Carries every generating call in the model API: chat completions and responses "+
			"(streamed when the body asks for it), embeddings, reranking, image, video and "+
			"speech generation, speech-to-text, RAG ingest, and fine-tune jobs — plus the "+
			"creates on the managed collections behind /v1.\n\n"+
			"This is the billed half of the surface. A call is metered per org against the "+
			"balance the commerce subsystem holds, and the balance gate is FAIL-CLOSED: when "+
			"no native balance reader is wired into the process the call is refused rather "+
			"than guessed at, and an insufficient balance answers 402 with its own message "+
			"intact."+fallback)
	openapi.Describe("/v1/*", http.MethodGet,
		"Read the model catalogue and the AI subsystem's own resources",
		"Covers the reads: the model catalogue and a model's access status, connected "+
			"providers and their usage, fine-tune jobs and presets, Hugging Face model, "+
			"dataset and repository search, the subsystem's health and metrics, and the "+
			"collection and member reads on the managed resources behind /v1.\n\n"+
			"Reads are scoped to the calling org by the identity the gateway validated; the "+
			"catalogue a caller sees is the one its own org has access to, not the whole "+
			"provider list."+fallback)
	openapi.Describe("/v1/*", http.MethodPut,
		"Replace one AI resource in full",
		"Replaces a member of one of the AI subsystem's managed collections with the body "+
			"given. PUT and PATCH reach the SAME update — a full replacement and a partial "+
			"one are accepted at one address so a client need not know which verb a given "+
			"collection prefers — so the difference is in what the caller sends, not in what "+
			"the server does with it."+fallback)
	openapi.Describe("/v1/*", http.MethodPatch,
		"Update one AI resource in place",
		"Updates a member of one of the AI subsystem's managed collections with the fields "+
			"given, leaving the rest as they were. It is the same update PUT reaches, so "+
			"either verb is accepted on these resources.\n\n"+
			"Nothing under this method reaches the generating model API; that is the POST "+
			"surface."+fallback)
	openapi.Describe("/v1/*", http.MethodDelete,
		"Remove an AI resource, connection or indexed document",
		"Removes a member of one of the AI subsystem's managed collections, a connected "+
			"provider (which revokes the stored connection for the caller's org), or "+
			"documents from the RAG index.\n\n"+
			"Deleting a provider connection is the one a reader most easily "+
			"underestimates: it does not merely hide the provider, it drops the org's stored "+
			"authorization for it, and every later call routed to that provider fails until "+
			"it is connected again."+fallback)
	// The methods left over. This address is bound with All(), so it publishes every
	// method this generator knows and the ones above are only the ones that DO
	// something. DescribeRest covers the remainder from the generator's own set, so a
	// method added there is covered the day it appears rather than published bare —
	// which is what a hand-copied list here had already produced for OPTIONS and TRACE.
	openapi.DescribeRest("/v1/*",
		"Not served by the inference surface",
		"Published because this catch-all accepts every method, but the inference surface "+
			"answers none of them here — the request falls through to whatever owns the path, "+
			"and reaches no model.")

}

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
	// INSTALL ONLY WHAT THIS PROCESS ACTUALLY HAS. `ai` runs as its OWN process
	// (ps in a prod pod: /cloud, /kms, /tasks, /ai, …), and these hooks are
	// package-level vars — so a reader wireFinance sets in the CLOUD process is
	// invisible here, permanently. The ai module's contract is that a NIL hook means
	// "use my own path" (the HTTP call to /v1/billing/balance, which cloud now accepts
	// with the S2S token), so installing a hook that merely reports the host has none
	// SHADOWS the only path that works in this process and fail-closes every
	// completion with 503 balance_unavailable. Do not install what we cannot answer.
	//
	// In the cloud process the snapshot is safe by construction: wireFinance runs in
	// BuildDeps, which completes before MountAll — the ordering its own doc comment
	// guarantees. (RollingCapReader below is a genuine trampoline because clients/
	// rollingcap installs it from Mount, after this package, in some binaries.)
	if f := cloud.TierReader(); f != nil {
		aiobject.SetTierReader(aiobject.TierReaderFunc(f))
	}
	// THE BALANCE READ CROSSES THE PROCESS BOUNDARY OVER THE PLANE — ZAP on the
	// canonical unix socket — never HTTP back through our own edge.
	//
	// `ai` is its OWN process, so cloud.BalanceReader() (a package-level var wireFinance
	// sets in the CLOUD process) is ALWAYS nil here and the ai module fell back to an HTTP
	// self-call to /v1/billing/balance. That request carries COMMERCE_SERVICE_TOKEN and
	// no user, so it is not a validated principal at the edge and answers 401 — and the
	// balance gate is fail-CLOSED, so EVERY completion 503'd (chat, copilot, documents)
	// on a pod whose ledger was perfectly healthy. Routing money reads back through the
	// public edge was the mistake; the edge is for customers, the plane is for us.
	//
	// commerce already publishes the read as a plane op (apps/commerce/balance_rpc.go
	// exposeBalance → plane.FinanceBalance) precisely because the ledger has ONE writer
	// and must be asked, not opened. Ask it the way every other in-tree caller does
	// (apps/admin/finance, apps/marketplace): typed, over the socket, org-scoped by the
	// caller — no HTTP hop, no token to mint, no edge to satisfy.
	if f := cloud.BalanceReader(); f != nil {
		// Co-resident (the cloud process): the direct in-process read is strictly better.
		aiobject.SetBalanceReader(aiobject.BalanceReaderFunc(f))
	} else {
		aiobject.SetBalanceReader(func(ctx context.Context, subject, namespace, currency string) (int64, error) {
			if currency == "" {
				currency = "usd"
			}
			bal, err := cloud.Ask[plane.BalanceIn, plane.Balance](
				cloud.For(ctx, namespace), "commerce", plane.FinanceBalance,
				&plane.BalanceIn{Subject: subject, Currency: currency})
			if err != nil {
				return 0, fmt.Errorf("plane balance read: %w", err)
			}
			if bal == nil {
				return 0, fmt.Errorf("plane balance read: commerce answered nothing")
			}
			// The gate wants a COARSE cents figure (it compares > 0), but the ledger
			// keeps eighteen decimals — per-token charges are routinely finer than a
			// cent — so Money.Minor() refuses to answer rather than round behind the
			// caller's back. Its doc says to round explicitly, where the choice is
			// visible. This is that choice:
			//
			// ROUND DOWN. A gate that rounded up would admit a request the balance
			// cannot actually cover, and the debit that follows is exact — so the
			// error would land as a negative balance nobody authorized. Rounding down
			// can only ever refuse slightly early, which is the safe direction for a
			// fail-closed gate. Nothing is billed from this number; it decides
			// admission only.
			a, err := bal.Amount.Parse()
			if err != nil {
				return 0, fmt.Errorf("plane balance read: %w", err)
			}
			minor := a.Minor() // big.Int of cents, truncated toward zero by Rescale
			if !minor.IsInt64() {
				return 0, fmt.Errorf("plane balance read: %s %s exceeds int64 cents",
					bal.Amount.Decimal, bal.Amount.Currency)
			}
			return minor.Int64(), nil
		})
	}
	// The DEBIT crosses the same way, for the same reason — and it must key on the SAME
	// wallet the gate read, or spend can outrun the balance that admitted it.
	if f := cloud.UsageRecorder(); f != nil {
		aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
			return f(ctx, cloud.UsageEvent{
				Subject: u.Subject, Namespace: u.Namespace, USD: u.USD,
				Currency: u.Currency, Model: u.Model, Provider: u.Provider,
				RequestID: u.RequestID,
			})
		})
	} else {
		aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
			cur := u.Currency
			if cur == "" {
				cur = "usd"
			}
			_, err := cloud.Ask[plane.RecordIn, plane.Recorded](
				cloud.For(ctx, u.Namespace), "commerce", plane.FinanceRecord,
				&plane.RecordIn{
					Subject: u.Subject,
					Amount:  plane.Money{Decimal: u.USD, Currency: cur},
					Usage: plane.Usage{
						Model: u.Model, Provider: u.Provider, RequestID: u.RequestID,
					},
				})
			if err != nil {
				return fmt.Errorf("plane usage debit: %w", err)
			}
			return nil
		})
	}
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
	//
	// The prose half of that hole is closed — see the Describe block above, which
	// says what each method behind the wildcard actually reaches — but prose is all
	// it closes. A typed op is still what an SDK method and an MCP tool come from.
	return aimod.Mount(app, deps)
}
