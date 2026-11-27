package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"log/slog"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/websearch"
	"github.com/hanzoai/cloud/client"
	allowancepeer "github.com/hanzoai/cloud/client/allowance"
	trainpeer "github.com/hanzoai/cloud/client/train"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/tenant"
	aimod "github.com/hanzoai/ai"
	webtools "github.com/hanzoai/ai/agent/builtin_tool/web"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// The MODEL API IS THE RELAY'S REGISTRY, and it is asked rather than described.
//
// aimod.Use registers one greedy `app.All("/v1/*")` — the whole model API,
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
	spec.Front(spec.Relay{
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
	// The shapes ai's requests and responses name. They have to cross with them or
	// every $ref that comes across resolves to nothing.
	if c, ok := doc["components"].(map[string]any); ok {
		if s, ok := c["schemas"].(map[string]any); ok {
			spec.Schemas(s)
		}
	}
	paths, _ := doc["paths"].(map[string]any)
	for path, raw := range paths {
		item, _ := raw.(map[string]any)
		for verb, rawOp := range item {
			op, _ := rawOp.(map[string]any)
			route := routeSpelling(path)
			summary, _ := op["summary"].(string)
			description, _ := op["description"].(string)
			if summary == "" && description == "" {
				continue
			}
			spec.Describe(route, strings.ToUpper(verb), summary, description)
			// ai's own responses, verbatim. Carrying only the prose published 293
			// of its 294 operations with no `responses` at all, so every generated
			// client handed the caller an untyped result -- GET /v1/models, the
			// most-called address on the surface, returned `undefined` in
			// typescript-fetch and a bare *http.Response in Go. ai declares the
			// envelope; there is no reason for it to stop at this boundary.
			spec.Answers(route, strings.ToUpper(verb), op["responses"])
			spec.Takes(route, strings.ToUpper(verb), op["requestBody"])
			spec.Params(route, strings.ToUpper(verb), op["parameters"])
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

func document() (*spec.Document, error) {
	raw, err := json.Marshal(aimod.Document())
	if err != nil {
		return nil, fmt.Errorf("encode the model API's document: %w", err)
	}
	doc := &spec.Document{}
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

// debit charges one ai completion through the metering client, which resolves where the
// ledger is: this process when it owns the file, otherwise the process that does, over
// the plane. The gate above reads the same wallet through the same client, so spend
// cannot outrun the balance that admitted it.
//
// A NAMED function rather than the closure it came out of, because it is the only line of
// this file that decides what a customer is charged and by what key, and a closure inside
// Mount can be read but not exercised: mounting ai to reach one field means standing the
// whole model API up. This can be handed a crafted event and asked what actually crosses.
//
// THE LEDGER'S KEY IS THE DEBIT'S OWN REF, and nothing a caller wrote. The ai module
// mints one Ref per usage record, the first time the record is filed, and keeps it
// across every try (aiobject.UsageEvent.Ref) — so a debit ai re-sends after the answer
// to it was lost is the same act and moves money once, while two records, however
// alike, are two acts. The event's RequestID stays correlation: it crosses as
// Usage.RequestID and keys nothing, so a caller pinning a header or a message id
// still pays for every answer.
//
// The amount is the module's own decimal, parsed exact: a per-token price is routinely
// finer than a cent, and a cents-only debit floors it to nothing. Post defaults an
// absent currency to usd, so an event that names none still bills rather than erroring.
//
// It POSTS rather than records: the debit is on the usage stream's disk when this
// returns, and the ledger's drain writes it, so an answer never waits for the books.
func debit(m *meter.Client) aiobject.UsageRecorderFunc {
	return func(ctx context.Context, u aiobject.UsageEvent) error {
		amount, err := money.ParseUSD(u.USD)
		if err != nil {
			return fmt.Errorf("ai usage amount: %w", err)
		}
		// WHO MADE THE CALL AND ON WHICH KEY cross with the charge, with the model's class,
		// the work priced and what serving it cost us: the usage log names the person
		// (Actor) and the key (Key) of every charge, not the wallet alone, so "what did
		// this key spend" and "who spent it" are answered from the books.
		use := meter.Usage{
			User: u.Subject, Org: u.Namespace, Amount: amount.Unwrap(), Currency: u.Currency,
			Model: u.Model, Provider: u.Provider, RequestID: u.RequestID, Ref: u.Ref, Cash: u.Cash,
			Actor: u.Actor, Key: u.Key, Class: u.Class, Units: u.Units, PaidBy: u.PaidBy,
		}
		if u.CostUSD != "" {
			if cost, err := money.ParseUSD(u.CostUSD); err == nil {
				use.Cost = cost.Unwrap()
			}
		}
		// Drawn cash first, then credit: the wallet the gate weighed, both halves of it —
		// or cash alone, when the usage policy refused granted credit for the model.
		return m.Post(ctx, use)
	}
}

// record settles what ONE SERVED CALL consumed, in money. A free call's count was
// taken when the gate admitted it (spent), before any model was reached, so the
// record of a call is money alone.
//
// A call made on an API key is also added to that key's own spend, whoever pays
// for it, so the key can be held to its budget (limits.Key). A failed count is
// logged and the call is still settled: the debit is the money, this is the cap.
func record(lim *limits.Limiter, money aiobject.UsageRecorderFunc) aiobject.UsageRecorderFunc {
	return func(ctx context.Context, u aiobject.UsageEvent) error {
		// The key names its own org (`<org>/<id>`, cloud.HeaderKey): the org a call is
		// billed to is not always the key's, and the key's budget is kept in its own.
		if org, key, ok := strings.Cut(u.Key, "/"); lim != nil && ok && org != "" && key != "" {
			if nanos, err := nanosUp(u.USD); err != nil {
				slog.Error("ai key spend: unreadable amount", "key", u.Key, "usd", u.USD, "err", err)
			} else if err := lim.Spend(ctx, org, key, nanos); err != nil {
				slog.Error("ai key spend: not counted", "key", u.Key, "err", err)
			}
		}
		// A call the plan, a free cap, or a settled x402 payment covered draws no
		// second wallet debit.
		if u.Plan || X402Settle(ctx) {
			return nil
		}
		return money(ctx, u)
	}
}

// give hands back the free call admit took at at for a call that was not served, to
// the hour and day it was taken in and the session that opened at start (the take's
// own answer).
func give(ctx context.Context, subject, namespace string, at time.Time, start int64) {
	org, in := allowanceIn(ctx, subject, namespace)
	in.At, in.Start = at.Unix(), start
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if _, err := allowancepeer.AllowanceGive(cloud.For(ctx, org), in); err != nil {
		slog.Warn("ai allowance: an unserved free call could not be given back", "org", org, "err", err)
	}
}

// admit takes one free call from the allowance as the gate lets it through, and
// answers the standing that follows. The count is the PERSON's — the caller's IAM home
// org and user id (client.Person), so the members of one org each hold their own —
// and the ceiling is the wallet's, subject, which pays. The public lane counts the
// visitor the lane served; one shared subject would spend every stranger's day at
// once.
func admit(ctx context.Context, subject, namespace string) (*client.Allowance, error) {
	org, in := allowanceIn(ctx, subject, namespace)
	return allowancepeer.AllowanceTake(cloud.For(ctx, org), in)
}

// capabilities reads an org's published capabilities from the train app.
func capabilities(ctx context.Context, org string) (string, error) {
	c, err := trainpeer.TrainCapabilities(cloud.For(ctx, org), &client.CapabilitiesIn{})
	if err != nil {
		return "", fmt.Errorf("plane capabilities read: %w", err)
	}
	return c.Set, nil
}

// allowanceIn names whose count a free call spends, which wallet sets its ceiling,
// and the org that wallet is in.
//
// A COMPLETION A SIBLING BOUGHT FOR A PERSON (chat_rpc.go) reaches the model API on
// the deployment's own token, so subject and namespace name the deployment's wallet;
// the person rides as "<org>/<name>". That wallet is the platform's, and reading it as
// the person's would leave every agent and Slack call on a non-Zen free model
// unbounded wherever the platform holds credit — so the person's own wallet in their
// org decides, as it does for a Zen completion (apps/zen free.go payer).
func allowanceIn(ctx context.Context, subject, namespace string) (string, *client.AllowanceIn) {
	in := &client.AllowanceIn{Subject: subject}
	if namespace == tenant.Public {
		return namespace, in
	}
	person := client.Person(zip.CallerOf(ctx))
	if person == "" {
		return namespace, in
	}
	in.Subject, in.Payer = person, subject
	if namespace == authz.AdminOrg {
		if org, name, ok := strings.Cut(strings.TrimSpace(zip.CallerOf(ctx).User), "/"); ok && principal.NamesOrg(org) && name != "" {
			if w := meter.PayerFor(org, name); !w.Zero() {
				return w.Org(), &client.AllowanceIn{Subject: person, Payer: w.Subject()}
			}
		}
	}
	return namespace, in
}

// spent turns the allowance counter's answer into the gate's standing. The counter
// TAKES the unit as the gate asks: admitting a free call is what counts it.
//
// WHO FAILS OPEN IS DECIDED HERE, because this is the layer that knows the
// vocabulary: an unanswerable ask about the public lane is spent, one about a
// tenant we can name is an error the gate lets through. A NAMED function rather
// than the closure it was, so that rule can be handed a failing read and asked.
//
// A call admitted against a ceiling took a unit, and its standing carries the give
// that hands the unit back when the call is not served.
func spent(read func(ctx context.Context, subject, namespace string) (*client.Allowance, error),
	back func(ctx context.Context, subject, namespace string, at time.Time, start int64)) aiobject.SpentFunc {
	return func(ctx context.Context, subject, namespace string) (aiobject.Standing, error) {
		at := time.Now()
		out, err := read(ctx, subject, namespace)
		switch {
		case err != nil && namespace == tenant.Public:
			return aiobject.Standing{Spent: true}, nil // an unnamed caller gets no benefit of the doubt
		case err != nil:
			return aiobject.Standing{}, fmt.Errorf("plane allowance read: %w", err)
		case out == nil && namespace == tenant.Public:
			return aiobject.Standing{Spent: true}, nil
		case out == nil:
			return aiobject.Standing{}, fmt.Errorf("plane allowance read: the counter answered nothing")
		}
		st := aiobject.Standing{Spent: out.Spent, Window: out.Window, Limit: out.Limit, Used: out.Used}
		if out.Resets > 0 {
			st.Resets = time.Unix(out.Resets, 0)
		}
		if !out.Spent && out.Limit > 0 && back != nil {
			st.Release = func() { back(ctx, subject, namespace, at, out.Start) }
		}
		return st, nil
	}
}

// Shutdown waits for the debits ai files after its replies to reach the ledger, so
// a decision answered just before the process stops is still billed. zip runs it
// once the listeners have stopped and in-flight requests have drained, and it gives
// the debits their whole budget — every try of each at its full timeout — whatever
// the drain before it left of the host's.
func Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aictl.SettleBudget())
	defer cancel()
	// The store closes last: the debits above may still write to it.
	return errors.Join(aictl.Settled(ctx), release())
}

// Mount installs the money, ingest and telemetry wiring, then mounts ai. A nil
// callback is left alone — cloud leaves one nil exactly when that subsystem
// isn't co-resident, and the module's own fallback applies.
func Use(app cloud.Router, deps cloud.Deps) error {
	// Gated on the provider actually being installed: adopting the global NO-OP
	// provider would latch ai "telemetry ready" against something that discards
	// every span AND suppress ai's own standalone fallback. This lives here, beside
	// the other aiobject.Set* calls, because this package already links ai — the
	// host must not (github.com/hanzoai/ai/object is 1270 packages, and cloud's
	// root is 644).
	if host.TracerProviderInstalled() {
		aiobject.AdoptHostTracerProvider()
	}
	confine()
	sudo()
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
	// charged through Bill.Authorize on the request path, where a payer has been
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
	// THE TRANSCRIPT A RATE CEILING COUNTS ONCE. The live sessions are in this
	// module's memory, in this process, which is also the process whose edge
	// chain runs ScopeRateLimit for them — so the ceiling asks the session table
	// directly whether a push or close continues a session the caller opened —
	// which is exactly when the module can name the model that session bills as.
	host.SetAdmitted(func(method, path, auth string) bool {
		_, live := aictl.TranscriptModel(method, path, auth)
		return live
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
	// BuildDeps, which completes before UseAll — the ordering its own doc comment
	// guarantees.
	//
	// The tier read is not a hook a process can lack. aiTierReader prefers the
	// catalog plan when it is Unlimited (which Commerce groups under tier.name "pro",
	// so forwarding cloud.Tier directly would subject an unlimited org to Pro's 8h
	// request-count quota in hanzoai/ai), and falls back to cloud.Tier for all other
	// plans ("pro-3x", "dedicated", etc.).
	aiobject.SetTierReader(aiTierReader)
	// What a plan covers of Enso and Zen in the consumer apps (apps/ai/limits): it
	// answers the gate's plan hook, and a covered call is the plan's, never the
	// wallet's (record). The co-resident zen asks the same limiter.
	lim, err := limits.Shared()
	if err != nil {
		return fmt.Errorf("ai.Use: open limits store: %w", err)
	}
	mountLimits(app, lim, deps.Audit)
	exposeSpend(lim)
	// A sibling's call meets the same requests a minute an HTTP one does (rate.go).
	limiter = lim
	// THE BALANCE READ CROSSES THE PROCESS BOUNDARY OVER THE PLANE — ZAP on the
	// canonical unix socket — never HTTP back through our own edge.
	//
	// `ai` is its OWN process, so cloud.BalanceReader() (a package-level var installFinance
	// sets in the CLOUD process) is ALWAYS nil here and the ai module fell back to an HTTP
	// self-call to /v1/billing/balance. That request carried a shared service token and
	// no user, so it was not a validated principal at the edge and answered 401 — and the
	// balance gate is fail-CLOSED, so EVERY completion 503'd (chat, copilot, documents)
	// on a pod whose ledger was perfectly healthy. Routing money reads back through the
	// public edge was the mistake; the edge is for customers, the plane is for us.
	//
	// commerce already publishes the read as a plane op (apps/commerce/balance_rpc.go
	// exposeBalance → client.FinanceBalance) precisely because the ledger has ONE writer
	// and must be asked, not opened. Ask it the way every other in-tree caller does
	// (apps/admin/finance, apps/marketplace): typed, over the socket, org-scoped by the
	// caller — no HTTP hop, no token to mint, no edge to satisfy.
	//
	// It reads the whole spendable balance, cash plus credit (balance): a grant pays
	// for priced calls exactly as a payment does, and the debit spends cash first.
	aiobject.SetBalanceReader(balance)
	// The DEBIT keys on the SAME wallet the gate read, or spend can outrun the balance
	// that admitted it. It goes through metering whether or not commerce runs in this
	// process, and carries the record's own Ref, so a retry after a lost answer lands once.
	aiobject.SetUsageRecorder(record(lim, debit(deps.Metering)))
	if d := cloud.IngestDialer(); d != nil {
		aiobject.SetIngestDialer(d)
	}
	// The plan: asked by the gate about every Enso and Zen request.
	aiobject.SetLimits(limitAsk(lim))
	// Whether a signed-in app's Free chat that asks a third party's model nothing pays
	// for is handed to Enso or refused with the top-up: the flag ai_handover_premium.
	aiobject.SetHandover(handover)
	// The free lane's ceiling: three calls a person every five hours on the Free plan,
	// the only bound on a route priced at zero, where the wallet has nothing to refuse.
	// It crosses the plane to the allowance app, which keeps the count on the KV every
	// pod shares.
	//
	// IT COUNTS AS IT ADMITS. The gate asks this before any model is reached and the
	// answer takes the unit, so a loop retrying a failing upstream spends its three
	// rather than running forever; a call refused at the ceiling counts nothing.
	//
	// WHO FAILS OPEN IS DECIDED HERE, because this is the layer that knows the
	// vocabulary. The gate treats an error as "allow", which is right for a tenant we
	// can name: a plane blip must not take the free models away from a customer whose
	// priced routes still work. It is wrong for the public lane. A route STATED at
	// zero is not always served by our own compute — a vendor can be behind it and
	// bills us either way — so "we could not ask" must never become "a stranger may
	// have as much as they want". An unanswerable ask in that lane is refused.
	aiobject.SetSpent(spent(admit, give))
	// An org's published capabilities ride each of its decisions to the decision
	// service. The train process holds them; ai asks it over the plane per call.
	aiobject.SetCapabilities(capabilities)
	// What a sync of a vendor's model list changed goes on the SuperAdmin's pulse
	// (listing.go).
	aiobject.SetListingPublisher(announceListing)
	// One of our vendor accounts that cannot pay goes on the same pulse, by its
	// name, as an incident (drained.go).
	aiobject.SetDrainedPublisher(drained(deps.Audit))
	// A video clip a vendor makes is kept in the object store (assets.go). Where the
	// store can sign no link a browser can follow, ai refuses video rather than make
	// a clip it cannot hand over.
	if store := newMediaStore(); object.Signs(context.Background(), store.store) {
		aiobject.SetAssets(store)
	}
	// A model resold from a vendor bills at the vendor's price plus the vendor's fee,
	// which a SuperAdmin sets in the usage policy (listing.go).
	aiobject.SetFees(fee)
	// The MCP server's inventory, registered BEFORE the wildcard below so the
	// reading order is the routing order (see mcp.go — the router would pick the
	// static path over All("/v1/*") either way).
	mountMCP(app)
	// The model API as an agent calls it, answered with its receipt (calls.go).
	mountCalls(app)
	// A decision's corrections, and the states Kai captured of an org's decisions,
	// both kept by train (corrections.go).
	mountCorrections(app)
	useCapture()
	// The completion a sibling buys, on the internal plane: the one hop that
	// carries the person a run acts for. See chat_rpc.go.
	exposeChat()
	exposeMedia()
	// The free pool's standing, for the Free plan's usage page. See pool_rpc.go.
	exposePool()
	// What each Enso model costs, for the price list. See rates_rpc.go.
	exposeRates()
	// A family's routing catalog and its one write path, for the Enso policy
	// registry. See routing_rpc.go.
	exposeRouting()
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
	//
	// Its store is handed over first (store.go): ai opens none from config.
	if err := hand(context.Background()); err != nil {
		return err
	}
	sub, err := aimod.App(scopedAIKMS(deps.KMS))
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
	app.Use(sub)
	return nil
}

// confine hands ai this process's verdict on a DELEGATED token (cloud.Confined).
//
// ai re-reads the bearer on every model call and verifies a JWT against IAM's
// public JWKS, which never carries the delegation key — so without this every
// coding run's model call, admitted by the identity boundary a moment earlier,
// was refused 401 here. ai now asks the boundary's verifier about the exact token
// bytes instead (object.SetConfinedVerifier): one verifier for a delegated token,
// and no header a client writes can stand in for its answer.
func confine() { aiobject.SetConfinedVerifier(cloud.Confined) }

// aiTierReader feeds hanzoai/ai's tier gates and request-count quota. Commerce
// groups the Unlimited catalog plan under tier.name "pro", so asking cloud.Tier
// alone would enforce Pro's 8h request quota on an unlimited org. Prefer "unlimited"
// when cloud.Plan reports it, and fall back to cloud.Tier for all other catalog slugs.
func aiTierReader(ctx context.Context, subject, org string) (string, error) {
	if plan, err := cloud.Plan(ctx, subject, org); err == nil && strings.EqualFold(strings.TrimSpace(plan), "unlimited") {
		return "unlimited", nil
	}
	return cloud.Tier(ctx, subject, org)
}

// sudo hands ai this boundary's SuperAdmin verdict (principal.Super: the
// X-User-IsAdmin the boundary strips from every request and writes from
// authz.Claims.Sudo alone). ai asks it at every seam where a request's caller
// becomes a user and decides nothing itself; with no seam installed nobody is a
// SuperAdmin there.
func sudo() { aiobject.SetSuper(principal.Super) }

// scopedAIKMS wraps deps.KMS for aimod.App so background tenant-scoped reads of
// `orgs/<org>/...` (per-org/per-user provider API key overrides and linked compute
// nodes in hanzoai/ai/object) carry zip.Caller{Org: org} when the caller context
// names no org, satisfying KMS tenant isolation (`admit`, apps/kms/attest.go).
type aiScopedKMS struct{ secret.Store }

func scopedAIKMS(s secret.Store) secret.Store {
	if s == nil {
		return nil
	}
	return aiScopedKMS{Store: s}
}

func scopeOrgRef(ctx context.Context, ref string) context.Context {
	if zip.CallerOf(ctx).Org != "" {
		return ctx
	}
	if rest, ok := strings.CutPrefix(ref, "orgs/"); ok {
		if org, _, hasSlash := strings.Cut(rest, "/"); hasSlash && org != "" {
			who := zip.CallerOf(ctx)
			who.Org = org
			return zip.WithCaller(ctx, who)
		}
	}
	return ctx
}

func (k aiScopedKMS) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	return k.Store.GetSecret(scopeOrgRef(ctx, ref), ref)
}
func (k aiScopedKMS) PutSecret(ctx context.Context, ref string, value []byte) error {
	return k.Store.PutSecret(scopeOrgRef(ctx, ref), ref, value)
}
func (k aiScopedKMS) DeleteSecret(ctx context.Context, ref string) error {
	return k.Store.DeleteSecret(scopeOrgRef(ctx, ref), ref)
}
