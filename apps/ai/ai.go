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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	aimod "github.com/hanzoai/ai"
	webtools "github.com/hanzoai/ai/agent/builtin_tool/web"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/hanzoai/cloud/apps/websearch"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
)

// The MODEL API IS THE RELAY'S REGISTRY, and it is asked rather than described.
//
// aimod.Mount registers one greedy `app.All("/v1/*")` — the whole model API,
// ~200 routes, reaching the wire through a single wildcard. Read the router alone
// and the published document says `/v1/{wildcard1}` and seven operations, which is
// why no generated SDK and no MCP tool list carried chat completions: the fleet's
// largest product was, in the contract, one path.
//
// This package used to close the PROSE half of that hole — five hand-written
// openapi.Describe blocks saying what was behind each verb of the wildcard. That
// is now deleted, and its deletion is the point. Prose about a relay is a
// description of a thing standing in for the API; hanzoai/ai can hand over the API
// itself, so nothing here has to say what is behind the relay and nothing here can
// be wrong about it.
//
// routers.Document is what it hands over: ai's WHOLE surface as one OpenAPI
// document, built there from its own live router and its own tables. Membership
// is routers.App, the ai runtime's ONE router — every /v1 route registers on it —
// so the addresses are the ones that answer. Prose is the doc comment on the
// handler the registration names, lifted by hanzoai/ai's own cmd/routerdoc,
// because Go drops comments at compile time and a sentence written anywhere else
// is a second source for one fact. Bodies are the resource table's own
// declaration, from the same rows that register the routes.
//
// It is read out of the PINNED MODULE at describe time: no network, no vendored
// copy, no second list, reproducible from a checkout and a go.mod.
//
// THE LIMIT, stated because it is easy to overstate what this buys. Two hundred
// and thirty-eight of the operations now carry a real 2xx contract, and it is the
// envelope — {status,msg,data,data2} — whose `data` is deliberately untyped: a
// resource answers through that shape and what is inside depends on the operation.
// Per-resource bodies, and bodies for the OpenAI-compatible half at all, are
// per-operation typing work in hanzoai/ai where those handlers live. Declaring
// them a second time here would be a second implementation of somebody else's
// wire format, wrong the moment it disagreed.
func init() {
	openapi.Front(openapi.Relay{
		Source: "github.com/hanzoai/ai",
		Prefix: "/v1",
		// /v1 is a REMAINDER, not a namespace: this row is last in manifest.Apps,
		// so what it answers is everything under /v1 no earlier app claimed.
		// Fifteen of ai's own registrations are delivered to a sibling instead, and
		// four of those 404 on api.hanzo.ai because the sibling does not serve
		// them. The relay reads which ones from the fleet's routing table rather
		// than carrying a list beside it — manifest/router_test.go asks the real
		// router the same question, so a wrong answer here is a red gate and not a
		// shipped phantom.
		Yields: manifest.Elsewhere("ai"),
		Behind: document,
	})
}

// document is hanzoai/ai's own document, in this repo's type.
//
// It crosses as JSON because that is what an OpenAPI document is. Two
// repositories cannot share a document type without one depending on the other's,
// and the relay is the wrong place for that coupling — hanzoai/ai must stay able to
// describe itself without importing a fleet document format. openapi.Typed reads
// zip's spec the same way for the same reason.
// describeAI states each of ai's operations for the document generator, from ai's own
// document.
//
// The spelling is converted because the two layers name a parameter differently: a
// document says {id}, a route says :id, and the generator keys on the route. One
// place, with both spellings visible.
//
// A silent operation is skipped rather than given an empty sentence: Describe refuses
// an empty declaration, and correctly — a sentence nobody wrote is not a sentence.
func describeAI(doc map[string]any) {
	paths, _ := doc["paths"].(map[string]any)
	for path, raw := range paths {
		item, _ := raw.(map[string]any)
		for verb, rawOp := range item {
			op, _ := rawOp.(map[string]any)
			summary, _ := op["summary"].(string)
			description, _ := op["description"].(string)
			if summary == "" && description == "" {
				continue
			}
			route := routeSpelling(path)
			openapi.Describe(route, strings.ToUpper(verb), summary, description)
			// ai's own responses, verbatim. Carrying only the prose published 293
			// of its 294 operations with no `responses` at all, so every generated
			// client handed the caller an untyped result -- GET /v1/models, the
			// most-called address on the surface, returned `undefined` in
			// typescript-fetch and a bare *http.Response in Go. ai declares the
			// envelope; there is no reason for it to stop at this boundary.
			openapi.Answers(route, strings.ToUpper(verb), op["responses"])
		}
	}
}

// routeSpelling turns a document path into the route it was registered under: {id} to
// :id, segment by segment.
func routeSpelling(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if len(s) > 2 && s[0] == '{' && s[len(s)-1] == '}' {
			segs[i] = ":" + s[1:len(s)-1]
		}
	}
	return strings.Join(segs, "/")
}

func document() (*openapi.Document, error) {
	raw, err := json.Marshal(aimod.Document())
	if err != nil {
		return nil, fmt.Errorf("encode the model API's document: %w", err)
	}
	doc := &openapi.Document{}
	if err := json.Unmarshal(raw, doc); err != nil {
		return nil, fmt.Errorf("read the model API's document: %w", err)
	}
	return doc, nil
}

// installWebSearch closes the web-search client over a meta-search function.
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

// record settles what ONE SERVED CALL consumed: money for a priced call, a count for a
// free one. They are the same act — this call happened — so both are reached through
// the one hook the ai module calls after an answer, and neither can be reached any
// other way.
//
// THE POSITION IS STRUCTURAL RATHER THAN CONVENTIONAL. A count is a FIELD on the
// record of a served call (aiobject.UsageEvent.Allowance), this is the only hook that
// carries one, and the module produces one only for a success — so counting a free
// call REQUIRES having recorded that a call was served. A ceiling on spend is reached
// where spend is incurred, and no separate verb survives that anything could call
// earlier: not the gate, which reads, and not a controller, which has nothing to call.
//
// The two halves are independent facts, not two writes of one, so there is no
// half-applied state anywhere to recover. A count that does not land costs us one free
// call and leaves the debit exactly as it was; both errors travel back through the one
// line that already watches this client.
func record(money aiobject.UsageRecorderFunc) aiobject.UsageRecorderFunc {
	return func(ctx context.Context, u aiobject.UsageEvent) error {
		// BOTH HALVES ALWAYS RUN. A count that cannot land must not hold back a debit,
		// and a debit that fails must not quietly drop a count.
		count, paid := countFree(ctx, u), money(ctx, u)
		if count == nil {
			return paid // a call that counted nothing answers with the debit's own error
		}
		return errors.Join(count, paid)
	}
}

// countFree counts one served free call against its subject's plan allowance.
//
// A priced call names no subject here and counts nothing: money already bounds those,
// and a second bound over them would refuse work a subscriber has paid for. The
// subject is the caller's own except on the public lane, where it is the visitor the
// lane served — one shared subject would spend every stranger's day at once.
func countFree(ctx context.Context, u aiobject.UsageEvent) error {
	if u.Allowance == "" {
		return nil
	}
	if _, err := cloud.Ask[plane.AllowanceIn, plane.Allowance](
		cloud.For(ctx, u.Namespace), "allowance", plane.AllowanceTake,
		&plane.AllowanceIn{Subject: u.Allowance}); err != nil {
		return fmt.Errorf("plane allowance count: %w", err)
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
	// lives here in any case. This is where that client is closed, beside the balance
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
	// resolved and can be refused. A tool call has no payer. Installing this client
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
	// why the client is installed here beside the other cross-module hooks.
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
	// package-level vars — so a reader installFinance sets in the CLOUD process is
	// invisible here, permanently. The ai module's contract is that a NIL hook means
	// "use my own path" (the HTTP call to /v1/billing/balance, which cloud now accepts
	// with the S2S token), so installing a hook that merely reports the host has none
	// SHADOWS the only path that works in this process and fail-closes every
	// completion with 503 balance_unavailable. Do not install what we cannot answer.
	//
	// In the cloud process the snapshot is safe by construction: installFinance runs in
	// BuildDeps, which completes before MountAll — the ordering its own doc comment
	// guarantees. The tier read below is the exception that is NOT a snapshot: cap.go
	// resolves it per call, because a ceiling that cannot read the plan is uncapped
	// and benign, so there is nothing to shadow by asking late.
	if f := cloud.TierReader(); f != nil {
		aiobject.SetTierReader(aiobject.TierReaderFunc(f))
	}
	// THE BALANCE READ CROSSES THE PROCESS BOUNDARY OVER THE PLANE — ZAP on the
	// canonical unix socket — never HTTP back through our own edge.
	//
	// `ai` is its OWN process, so cloud.BalanceReader() (a package-level var installFinance
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
	money := debitOverPlane
	if f := cloud.UsageRecorder(); f != nil {
		money = func(ctx context.Context, u aiobject.UsageEvent) error {
			return f(ctx, cloud.UsageEvent{
				Subject: u.Subject, Namespace: u.Namespace, USD: u.USD,
				Currency: u.Currency, Model: u.Model, Provider: u.Provider,
			})
		}
	}
	aiobject.SetUsageRecorder(record(money))
	if d := cloud.IngestDialer(); d != nil {
		aiobject.SetIngestDialer(d)
	}
	// The paid lane's ceiling, and it is composed HERE rather than handed over.
	//
	// It used to be a trampoline through cloud.RollingCapReader, so a separate
	// rollingcap app could install the verdict from its own Mount. That hook was
	// a package global and every app is its own process, so a verdict written in
	// rollingcap's child was never readable in this one and the cap never applied
	// to a completion. cap.go composes it from the tier reader above and
	// commerce's windowed sum, in the process that asks the question.
	aiobject.SetRollingCapReader(overCap)
	// The free lane's ceiling. It crosses the plane for the reason the balance does
	// — the counter has ONE writer and it is another process — and it is the only
	// bound on a route priced at zero, where the wallet has nothing to refuse.
	//
	// IT READS, AND READING IS ALL IT DOES. The count rises where the call was SERVED
	// (see record, above), so this asks only whether the caller is already out. A
	// caller refused at the ceiling keeps their count, and so does one whose request
	// dies before any model — an unresolvable route, a vendor that never answered, a
	// pod being rolled. A caller pays for answers.
	//
	// WHO FAILS OPEN IS DECIDED HERE, because this is the layer that knows the
	// vocabulary. The gate treats an error as "allow", which is right for a tenant we
	// can name: a plane blip must not take the free models away from a customer whose
	// priced routes still work. It is wrong for the public lane. A route STATED at
	// zero is not always served by our own compute — a vendor can be behind it and
	// bills us either way — so "we could not ask" must never become "a stranger may
	// have as much as they want". An unanswerable ask in that lane is refused.
	aiobject.SetSpent(func(ctx context.Context, subject, namespace string) (bool, error) {
		out, err := cloud.Ask[plane.AllowanceIn, plane.Allowance](
			cloud.For(ctx, namespace), "allowance", plane.AllowanceRead,
			&plane.AllowanceIn{Subject: subject})
		switch {
		case err != nil && namespace == tenant.Public:
			return true, nil // spent: an unnamed caller gets no benefit of the doubt
		case err != nil:
			return false, fmt.Errorf("plane allowance read: %w", err)
		case out == nil && namespace == tenant.Public:
			return true, nil
		case out == nil:
			return false, fmt.Errorf("plane allowance read: the counter answered nothing")
		}
		return out.Spent, nil
	})
	// The MCP server's inventory, registered BEFORE the wildcard below so the
	// reading order is the routing order (see mcp.go — the router would pick the
	// static path over All("/v1/*") either way).
	mountMCP(zapp)
	// The relay: ONE `app.All("/v1/*")` (hanzoai/ai mount.go) adapting the legacy
	// router through zip.AdaptNetHTTP, so ai's ~200 real routes —
	// /v1/chat/completions, /v1/models, /v1/messages and the rest — reach the wire
	// through a single greedy wildcard.
	//
	// That is a routing fact, not a documentation one, and it stays: All has no typed
	// registrar, a `{wildcard1}` segment cannot be a bound In field, and the adapter
	// relays the handler's own status and Content-Type verbatim. What the wildcard
	// no longer costs is the DOCUMENT — the relay declared in this package's init
	// projects routers.App's own table through it, so the published surface is ai's
	// 192 paths rather than one wildcard. Typed request and response schemas for them
	// are still work in github.com/hanzoai/ai, where those handlers live.
	// ai is an APP now, and composing one is Use — the same verb every component
	// takes. It used to be handed THIS host's router to write into, which is what
	// let a subsystem depend on its host: hanzoai/cloud has two editions declaring
	// one module path, so cloud.Deps meant a different type in each and only one
	// of them could ever mount ai. It builds its own app and this host adds it.
	//
	// The secret store crosses as AI'S OWN interface (GetSecret/PutSecret), never
	// as this host's Deps — a subsystem states what it needs and the host supplies
	// it. deps.KMS satisfies it structurally, so nothing is adapted here.
	sub, err := aimod.App(deps.KMS)
	if err != nil {
		return err
	}
	// WHAT AI'S OPERATIONS DO, CARRIED ACROSS RATHER THAN RESTATED HERE.
	//
	// ai registers its real routes on its own app — around 190 of them — so this host's
	// document generator sees every one and asks what it does. The answer has one home
	// and it is not this file: the Go doc comment on ai's own handler, which ai lifts
	// into its own document. This reads that document and says the same sentence at the
	// key the generator looks it up by.
	//
	// None of it was needed while ai arrived as ONE wildcard behind a net/http adapter:
	// the generator saw a single route and took the whole document from the relay
	// declared in this package's init. Real routes are the better shape — they are what
	// the router, the MCP tool list and the CLI all read — and this is what the shape
	// costs.
	describeAI(aimod.Document())
	// The web fetch ai relays is THIS host's crawl. ai declares the shape and the
	// host binds the implementation, so the dependency points from host into
	// subsystem — the direction that lets a second host compose the same ai.
	aiobject.SetFetcher(func(ctx context.Context, url string) (*aiobject.Page, error) {
		page, err := crawl.Fetch(ctx, url)
		if err != nil {
			return nil, err
		}
		return &aiobject.Page{Title: page.Title, Markdown: page.Markdown, Metadata: page.Metadata}, nil
	})
	zapp.Use(sub)
	return nil
}
