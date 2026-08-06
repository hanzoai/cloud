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
	webtools "github.com/hanzoai/ai/agent/builtin_tool/web"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
	airouters "github.com/hanzoai/ai/routers"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/websearch"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
)

// The MODEL API IS THE DOOR'S REGISTRY, and it is asked rather than described.
//
// aimod.Mount registers one greedy `app.All("/v1/*")` — the whole model API,
// ~200 routes, reaching the wire through a single wildcard. Read the router alone
// and the published document says `/v1/{wildcard1}` and seven operations, which is
// why no generated SDK and no MCP tool list carried chat completions: the fleet's
// largest product was, in the contract, one path.
//
// This package used to close the PROSE half of that hole — five hand-written
// openapi.Describe blocks saying what was behind each verb of the wildcard. That
// is now deleted, and its deletion is the point. Prose about a door is a
// description of a thing standing in for the API; hanzoai/ai can hand over the API
// itself, so nothing here has to say what is behind the door and nothing here can
// be wrong about it.
//
// routers.App is the ai runtime's ONE router — every /v1 route registers on it
// (routers/router.go) — and Patterns is its own accessor for the table, exported
// there precisely so a caller can assert things about it from outside. Prose is
// the second half: the sentence each of those routes owes a reader, lifted by
// hanzoai/ai's own cmd/routerdoc from the doc comment on the handler the
// registration names, because Go drops comments at compile time and a sentence
// written anywhere else is a second source for one fact. Its own tests hold the
// bijection — every route has a sentence, every sentence has a route — so a hole
// there is red in the repo that can fix it.
//
// Both are read out of the PINNED MODULE at describe time: no network, no vendored
// copy, no second list, reproducible from a checkout and a go.mod. What that buys
// is EXISTENCE, PLACEMENT, VERB, OPERATIONID and PROSE for every route ai serves.
// What it does not buy is schemas: App.Router names a controller method and the
// body types are read off the beego context inside the handler, never declared, so
// there is nothing to reflect. That is per-operation typing work in hanzoai/ai, and
// it is a different job from this one.
func init() {
	door := openapi.Table("github.com/hanzoai/ai", "/v1", airouters.App.Patterns, aiProse)
	// /v1 is a REMAINDER, not a namespace: this row is last in manifest.Apps, so
	// what it answers is everything under /v1 no earlier app claimed. Fifteen of
	// ai's own registrations are delivered to a sibling instead, and four of those
	// 404 on api.hanzo.ai because the sibling does not serve them. The door reads
	// which ones from the fleet's routing table rather than carrying a list beside
	// it — manifest/router_test.go asks the real router the same question, so a
	// wrong answer here is a red gate and not a shipped phantom.
	door.Yields = manifest.Elsewhere("ai")
	openapi.Front(door)
}

// aiProse restates hanzoai/ai's own prose in this package's vocabulary. It is a
// rename and nothing else: two repositories cannot share a struct without one
// depending on the other's document type, and the door is the wrong place for that
// coupling — hanzoai/ai must stay able to say what its routes do without importing
// a fleet document format.
func aiProse() map[string]openapi.Said {
	said := airouters.Prose()
	out := make(map[string]openapi.Said, len(said))
	for key, d := range said {
		out[key] = openapi.Said{Summary: d.Summary, Description: d.Description}
	}
	return out
}

// installWebSearch closes the web-search seam over a meta-search function.
//
// It takes the searcher as a PARAMETER rather than calling websearch.Search
// directly so the adapter — the part with the truncation and the field mapping —
// can be exercised without a network round trip. The production call site passes
// the real one; a test passes its own and asserts on what the tool actually
// receives.
//
// The mapping is the whole of it: websearch.Result carries Content, the tool
// contract calls that field Snippet, and a rename that goes unnoticed hands every
// agent results with empty snippets — which reads as "the web had nothing to say
// about this" rather than as a bug.
func installWebSearch(search func(ctx context.Context, query, lang string) []websearch.Result) {
	webtools.SetSearch(func(ctx context.Context, query string, limit int) ([]webtools.SearchResult, error) {
		hits := search(ctx, query, "")
		// Truncate to what the caller asked for. The tool clamps its own limit to a
		// sane maximum before it ever reaches here; this only honours it.
		if limit > 0 && len(hits) > limit {
			hits = hits[:limit]
		}
		out := make([]webtools.SearchResult, 0, len(hits))
		for _, h := range hits {
			out = append(out, webtools.SearchResult{Title: h.Title, URL: h.URL, Snippet: h.Content})
		}
		return out, nil
	})
}

// debitOverPlane charges one ai completion to the process that owns the ledger.
//
// A NAMED function rather than the closure it came out of, because it is the only line of
// this file that decides what a customer is charged and by what key, and a closure inside
// Mount can be read but not exercised: mounting ai to reach one field means standing the
// whole model API up. This can be handed a crafted event and asked what actually crosses.
//
// IT NAMES NO REF, and that is the point. The event's RequestID is the ai module's message
// row id — `Owner + "/" + Name` — and both halves are fields of the JSON body the client
// posts, so sending it as the debit's Ref handed the ledger's idempotency key to the payer:
// pin one owner/name pair and every completion after the first deduped into the first one's
// entry. An absent Ref is minted at the far end, per debit, by the server (Usage.Seal), so
// two answers are two acts however identical the request that asked for them.
//
// Nothing is lost by not naming one. This debit is made once per streamed answer and never
// re-driven, and the sibling debit on the OpenAI surface already keys on a fresh uuid per
// call — the two surfaces now mint the same way.
//
// The currency default lives here for the same reason the amount does: the peer records
// what it is sent, so the value has to be complete at the point it is built.
func debitOverPlane(ctx context.Context, u aiobject.UsageEvent) error {
	cur := u.Currency
	if cur == "" {
		cur = "usd"
	}
	_, err := cloud.Ask[plane.RecordIn, plane.Recorded](
		cloud.For(ctx, u.Namespace), "commerce", plane.FinanceRecord,
		&plane.RecordIn{
			Subject: u.Subject,
			Amount:  plane.Money{Decimal: u.USD, Currency: cur},
			Usage:   plane.Usage{Model: u.Model, Provider: u.Provider},
		})
	if err != nil {
		return fmt.Errorf("plane usage debit: %w", err)
	}
	return nil
}

// Mount installs the money, ingest and telemetry wiring, then mounts ai. A nil
// callback is left alone — cloud leaves one nil exactly when that subsystem
// isn't co-resident, and the module's own fallback applies.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// The typed MCP op and hanzoai/ai's own mount both register on the concrete
	// App, which cloud.ZipApp is the named hole for. ai's app-wide reach is
	// DECLARED as Plugin.Global at its composition root — it is a policy fact, not
	// something a parameter type should be able to grant on its own.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("ai.Mount: router is not a zip app — the typed op registry is unreachable")
	}
	// One provider, one wire. cloud.Listen installed the process-global tracer
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
	// THE WEB, FOR EVERY RESPONSES-API AGENT.
	//
	// ai's builtin registry declares web_search / fetch_url / deep_research but holds
	// no backend for the two this host serves — agent/builtin_tool/web must stay a
	// leaf package (object imports agent, agent imports the registry), and websearch
	// lives here in any case. This is where that seam is closed, beside the balance
	// and tier readers, for the same reason they are here: this package links both
	// sides and the host does not.
	//
	// In-process, never over api.hanzo.ai. The edge validates a CUSTOMER credential
	// and answers 401 to a service; routing our own calls back through it is what
	// once fail-closed every completion at 503 on a healthy pod.
	//
	// deep_research is deliberately NOT installed, and the reason is MONEY rather
	// than plumbing.
	//
	// Research carries an explicit per-answer FEE — 25 cents, apps/answer/mode.go —
	// charged through Bill.Gate on the request path, where a payer has been
	// resolved and can be refused. A tool call has no payer. Installing this seam
	// with a direct call to the engine would therefore be an unbilled 25-cent
	// operation an agent may invoke in a loop: free inference, arrived at by the
	// exact route this codebase keeps closing.
	//
	// That apps/answer makes it awkward is not an accident to route around:
	// Params is built from request-scoped billing context and Sink's methods are
	// unexported, so the money gate is structurally hard to bypass. Wiring this
	// properly means giving the package an entry that takes a payer and charges
	// it — a billing decision, not an adapter.
	//
	// The two tools above are different in kind, not merely cheaper: their HTTP
	// routes gate on AUTHENTICATION (a validated principal or the service key),
	// and the agent request that reaches this tool was already authenticated and
	// metered at /v1/responses. Using them in-process is consistent with how they
	// are reached over HTTP; deep_research is not.
	//
	// Until then the tool reports that it is unavailable in this deployment — the
	// honest answer, and specifically NOT an empty result: an agent told "no
	// results" concludes the web holds nothing on the subject and answers from
	// memory in a confident voice.
	installWebSearch(websearch.Search)

	// THE PREPAID GATE'S COMPLETION CEILING, PER MODEL, FROM THE CATALOG.
	//
	// cloud's meter must bound a completion BEFORE it runs, and that bound is a
	// property of the model — 1M-context models exist, and any constant caps them
	// at whatever number was typed. It cannot read models.yaml itself:
	// hanzoai/ai/controllers imports hanzoai/cloud, so the catalog is a CYCLE from
	// cloud's root, not merely weight. This package already links both, which is
	// why the seam is installed here beside the other cross-module hooks.
	//
	// max_output_tokens is the answer when the catalog declares one; otherwise the
	// model's context window is still a true architectural bound (prompt +
	// completion can never exceed it). 0 from both leaves cloud on its own floor.
	cloud.SetCompletionCeiling(func(model string) int {
		mc := aictl.GetModelConfig()
		if mc == nil {
			return 0
		}
		if n := mc.MaxOutput(model); n > 0 {
			return n
		}
		return mc.ContextWindow(model)
	})
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
			cents, err := bal.Amount.FloorMinor()
			if err != nil {
				return 0, fmt.Errorf("plane balance read: %w", err)
			}
			return cents, nil
		})
	}
	// The DEBIT crosses the same way, for the same reason — and it must key on the SAME
	// wallet the gate read, or spend can outrun the balance that admitted it.
	//
	// Neither branch names the act. cloud.UsageEvent has no Ref to carry one and
	// debitOverPlane sends none, so on both paths the ledger's key is minted by whoever
	// writes the entry — never by the request that asked for the work.
	if f := cloud.UsageRecorder(); f != nil {
		aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
			return f(ctx, cloud.UsageEvent{
				Subject: u.Subject, Namespace: u.Namespace, USD: u.USD,
				Currency: u.Currency, Model: u.Model, Provider: u.Provider,
			})
		})
	} else {
		aiobject.SetUsageRecorder(debitOverPlane)
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
	// The MCP door's inventory, registered BEFORE the wildcard below so the
	// reading order is the routing order (see mcp.go — the router would pick the
	// static path over All("/v1/*") either way).
	mountMCP(zapp)
	// The door: ONE `app.All("/v1/*")` (hanzoai/ai mount.go) adapting the legacy
	// beego ControllerRegister through zip.AdaptNetHTTP, so ai's ~200 real routes —
	// /v1/chat/completions, /v1/models, /v1/messages and the rest — reach the wire
	// through a single greedy wildcard.
	//
	// That is a routing fact, not a documentation one, and it stays: All has no typed
	// registrar, a `{wildcard1}` segment cannot be a bound In field, and the adapter
	// relays the beego handler's own status and Content-Type verbatim. What the door
	// no longer costs is the DOCUMENT — the relay declared in this package's init
	// projects routers.App's own table through it, so the published surface is ai's
	// 192 paths rather than one wildcard. Typed request and response schemas for them
	// are still work in github.com/hanzoai/ai, where those handlers live.
	return aimod.Mount(zapp, deps)
}
