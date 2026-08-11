package guide

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/automations"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// listActionsLimit bounds the action-ledger read.
const listActionsLimit = 200

// state is the guide subsystem's own data. Shared deps (logger, meter, brand) live
// in the embedded cloud.Base. invoke/toolOK are the per-principal MCP plane seam,
// defaulted to automations and overridable in tests; detectors are the auto-detect
// registry.
type state struct {
	stores       *cloud.OrgStore[*Store] // per-org checklist progress + org-override tier
	blueprints   *BlueprintStore         // shared, versioned brand blueprint (seeded, SuperAdmin-authored)
	brand        string                  // deployment brand — the resolution + admin authoring key
	defBlueprint Blueprint               // embedded fail-safe blueprint for this brand (ultimate fallback)
	detectors    map[string]Detector
	signals      Signals // cross-subsystem growth-observe seam (bound at the composition root)
	ai           cloud.AIClient
	model        string
	audit        *audit.Recorder // tamper-evident trail for SuperAdmin blueprint edits
	invoke       func(ctx context.Context, org, tool string, args map[string]any) (any, error)
	toolOK       func(tool string) bool
}

// mounted is the active service so Shutdown can close the per-org stores.
var mounted *cloud.Service[state]

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single door a validated org walks through —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process seam.
func storeFor(stores *cloud.OrgStore[*Store], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return stores.For(ns)
}

// Mount wires /v1/guide/* onto app. Complex flavour (a package global for Shutdown
// + a per-org OrgStore), so it constructs the Service value directly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("guide.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("guide.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("guide.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "guide")
	stores := cloud.NewOrgStore(b, "guide", openStore)

	// Open the SHARED brand-blueprint store (one file for the deployment) and SEED it
	// idempotently: the embedded fixtures (base + each brand) are seeded-if-absent, so
	// a redeploy never clobbers a SuperAdmin's live edits. After seeding the DB is
	// authoritative; the embedded fixture is only the seed source + fail-safe fallback.
	blueprints, err := openBlueprintStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("guide.Mount: open blueprint store: %w", err)
	}
	seeded, err := seedBlueprints(context.Background(), blueprints)
	if err != nil {
		_ = blueprints.Close()
		return fmt.Errorf("guide.Mount: seed blueprints: %w", err)
	}

	s := &cloud.Service[state]{Base: b, State: state{
		stores:       stores,
		blueprints:   blueprints,
		brand:        deps.Brand,
		defBlueprint: fixtureBlueprint(deps.Brand),
		signals:      boundSignals, // installed by the composition root before Mount; zero value honest-degrades
		ai:           deps.AI,
		model:        cloud.DefaultModel,
		audit:        deps.Audit,
		invoke:       automations.InvokeTool,
		toolOK:       automations.ToolExists,
	}}
	s.State.detectors = newDetectors(func(_ context.Context, org string) (*Store, error) {
		return storeFor(stores, org)
	}, s.State.signals)
	mounted = s
	routes(app, s)
	s.Log.Info("guide mounted", "brand", deps.Brand,
		"principles", len(s.State.defBlueprint.Principles),
		"steps", len(s.State.defBlueprint.Steps), "strategies", len(s.State.defBlueprint.Strategies),
		"version", s.State.defBlueprint.Version, "seedVersion", seedVersion, "seededOrUpgraded", seeded)
	return nil
}

// seedBlueprints seeds the embedded fixtures into the shared store, VERSION-AWARE: the base
// blueprint under brand "" plus each embedded brand blueprint under its key. The canonical
// JSON of the PARSED blueprint is stored (so the persisted seed is exactly what the engine
// runs, independent of input syntax). Per brand (SeedOrUpgrade): seed when absent; upgrade
// an UNEDITED seed when the embedded seedVersion is newer; NEVER touch an admin-edited
// brand. Returns how many brands were seeded or upgraded (a no-op redeploy returns 0).
func seedBlueprints(ctx context.Context, store *BlueprintStore) (int, error) {
	now := time.Now().Unix()
	seed := func(brand string, bp Blueprint) (SeedAction, error) {
		doc, err := json.Marshal(bp)
		if err != nil {
			return SeedNone, fmt.Errorf("marshal %q seed: %w", brand, err)
		}
		return store.SeedOrUpgrade(ctx, brand, doc, seedVersion, now)
	}
	count := 0
	if act, err := seed("", defaultBlueprint); err != nil {
		return count, err
	} else if act != SeedNone {
		count++
	}
	for _, brand := range BrandCurriculums() {
		bp, _ := brandBlueprint(brand)
		if act, err := seed(brand, bp); err != nil {
			return count, err
		} else if act != SeedNone {
			count++
		}
	}
	return count, nil
}

// ops binds the service to the typed guide ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — there is no parameter for the
// service — so it arrives as a RECEIVER and every op is a method value
// (o.overview), which is also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *cloud.Service[state]) {
	// A TYPED op receives only a context, so the validated org reaches it from the
	// request parked on that context; it is never read off an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. Whoever composes the app parks it — the fused host, and the plugin
	// program when guide runs alone — because only a composer knows the identity
	// boundary has already run and that nothing serves ahead of it. A subsystem
	// asserting that for itself is repeating a claim it cannot check, which is how
	// one copy per subsystem accumulated and how the copy on a route-less node took
	// a surface down.

	// TYPED ops are declared on the GROUPS this surface already has, so an op's
	// path is the prefix composed with its leaf — the identity every projection
	// (the document, the MCP tool, the CLI command, the SDK method) keys on, and
	// the one cmd/zipdoc resolves the same way.
	v1 := app.Group("/v1")
	g := v1.Group("/guide")
	o := ops{s: s}

	// Overview stays flat: the surface path IS /v1/guide, so it is declared on
	// /v1 — declaring it on g would name /v1/guide/, which this API never served.
	zip.Get(v1, "/guide", o.overview)

	zip.Get(g, "/analytics", o.analytics)
	// The OBSERVE surface: the org's real-time growth profile — observed signals, the
	// classified growth stage, and the org's own key metrics. READ-ONLY (see profile).
	zip.Get(g, "/profile", o.profile)
	// The STRATEGIES corpus: the tactics library, org-scoped and filtered by
	// category/workload + the caller's observed stage/signals. READ-ONLY — the corpus
	// the recommendation-surface engine consumes. Content is SHARED (no org records).
	zip.Get(g, "/strategies", o.strategies)
	// The Business AI's dynamic "what to do next": the ranked next-best quests + a
	// grounded narrative (suggest), and a grounded founder chat (chat). Both are
	// READ-ONLY — they advise, never run an action (the only executing path is
	// /steps/:id/do). See suggest.go.
	zip.Get(g, "/suggest", o.suggest)
	zip.Post(g, "/chat", o.chat)
	// The per-org OVERRIDE tier (tier 1): a customer sets/clears its OWN curriculum.
	// Org-scoped, per-customer — NOT the shared brand blueprint.
	zip.Get(g, "/curriculum", o.getCurriculum)
	// PUT stays UNTYPED: the body is a raw curriculum DOCUMENT that Parse accepts as
	// YAML or JSON (sigs.k8s.io/yaml). A typed In is decoded as JSON before the
	// handler sees it, so typing this route would answer 400 to every YAML PUT the
	// route accepts today. It converts when the input is a declared document type,
	// not before.
	g.Put("/curriculum", cloud.Handle(s, putCurriculum))
	zip.Delete(g, "/curriculum", o.deleteCurriculum)
	zip.Get(g, "/actions", o.listActions)
	// The two ids these ops carry MOVE — post_v1_guide_steps_by_id_skip becomes
	// post_v1_guide_steps_id_skip — because the untyped projection derives the id
	// from the route pattern and a typed op from zip's defaultOpID. That is the
	// house rule, not an accident: take the rename, never pin it back with
	// WithOperationID, which would make one app's ids a special case (LLM.md,
	// failure mode 6). It is not the wire — no status, body or field name moves.
	//
	// The step transitions split on ONE fact: whether the route is dependency-GATED.
	// start and done are, and a blocked step answers 409 with a STRUCTURED body
	// ({error, step, blockedBy}) written in-band — a shape a typed op cannot
	// produce, because its only non-2xx is the error it returns and that error's
	// envelope is {status, code, error}. So those two stay UNTYPED until zip lands
	// multi-status responses (LLM.md). skip and reset are NOT gated — the 409 branch
	// is unreachable for them — so their whole answer set is expressible and they
	// are ops.
	g.Post("/steps/:id/start", cloud.Handle(s, markStart))
	g.Post("/steps/:id/done", cloud.Handle(s, markDone))
	zip.Post(g, "/steps/:id/skip", o.skipStep)
	zip.Post(g, "/steps/:id/reset", o.resetStep)
	// /do stays UNTYPED for a second reason on top of the 409: it STREAMS the
	// agent's actions as SSE when the caller asks for them, and a typed op answers
	// exactly one JSON value. TestDoStreamsSSE pins that wire fact — both triggers,
	// the Accept header and the ?stream=1 alias — so the refusal cannot rot into a
	// stale claim, the same discipline TestDocumentPutsAcceptYAML gives the two
	// document PUTs.
	g.Post("/steps/:id/do", cloud.Handle(s, doStep))

	// The SuperAdmin BLUEPRINT plane (tier 2 authoring): the platform/brand blueprint,
	// authored LIVE on admin.hanzo.ai. Gated on IsSuperAdmin (owner=="admin") — a normal
	// org member/admin gets 403; the brand blueprint is SHARED platform content, not a
	// per-customer surface. See admin.go.
	//
	// The plane's ROOT is declared on g with a /blueprint leaf, for the same reason
	// overview is declared on v1: an EMPTY leaf composes to the group's prefix plus
	// "/", so declaring the root on a /blueprint group names /v1/guide/blueprint/ —
	// a path this API has never served, and one that reaches every projection
	// (operationId get_v1_guide_blueprint_, the MCP tool of that name, the path a
	// generated SDK calls). Only the sub-paths hang off the group, where the leaf is
	// non-empty and the composition is exact.
	zip.Get(g, "/blueprint", o.getBlueprint)
	// PUT and PATCH stay UNTYPED, for the same reason as PUT /curriculum: the PUT
	// body is a YAML-or-JSON blueprint document, and the PATCH body is an opaque
	// JSON merge-patch whose keys are the item's own (and whose explicit nulls DELETE
	// a key, which a pointer field cannot distinguish from absent) — neither is a
	// declarable In.
	g.Put("/blueprint", superAdmin(s, putBlueprint))
	b := g.Group("/blueprint")
	zip.Get(b, "/versions", o.listBlueprintVersions)
	b.Patch("/:collection/:id", superAdmin(s, patchBlueprintItem))
}

// The prose for the six operations above that stay untyped. Every op carries its
// own in a doc comment zipdoc lifts; these six have no op to lift from, so
// without this each publishes an operationId and NOTHING else — an SDK method
// that cannot explain itself and a CLI command with no help text. The wire facts
// that keep them raw are stated in routes, next to the registration; this states
// what a CALLER gets. Declared through the same registry Register uses, so a
// description renders only while the router actually serves the route and this
// list can never invent a path.
func init() {
	openapi.Describe("/v1/guide/curriculum", http.MethodPut,
		"Replace your org's journey with a curriculum you author",
		"Sets the caller org's OWN curriculum — the per-customer override — and answers the "+
			"journey now in force with `custom: true`. The body is a curriculum document, and it is "+
			"accepted as YAML **or** JSON: that is the caller-visible reason this takes a raw body "+
			"rather than a declared shape. Whatever the syntax, the CANONICAL parsed form is what "+
			"is stored, so the document the engine runs never depends on how it was written.\n\n"+
			"Fail-closed: a body that does not parse, or parses but is not a valid journey (unique "+
			"step ids, no dangling or cyclic dependencies), is 422 and NEVER becomes active — the "+
			"org keeps the journey it had. Requires a validated org; 403 without one. An empty body "+
			"is 400 and one over 256 KiB is 413.\n\n"+
			"This is tier one only. It overrides nothing but this org's own journey; the shared "+
			"brand blueprint is a different surface with a different gate. DELETE the same path to "+
			"drop the override and fall back to it.")

	openapi.Describe("/v1/guide/steps/:id/start", http.MethodPost,
		"Mark a step of your org's journey started",
		"Moves one step of the caller org's journey to in-progress and answers the whole refreshed "+
			"journey, so a console needs no second read.\n\n"+
			"The transition is dependency-GATED, and that is why the answer set is wider than a "+
			"success: a step whose prerequisites are unfinished is 409 carrying `{error, step, "+
			"blockedBy}`, where `blockedBy` names the exact steps in the way — enough to render the "+
			"blockage rather than merely report it. A step id the org's active journey does not "+
			"contain is 404.\n\n"+
			"Requires a validated org; 403 without one, and the journey read and written is that "+
			"org's alone. The mark is recorded as `manual`, and the journey is reconciled against "+
			"the auto-detectors on every read, so a step the org has demonstrably completed "+
			"elsewhere can still be moved to done underneath it.")

	openapi.Describe("/v1/guide/steps/:id/done", http.MethodPost,
		"Mark a step of your org's journey finished",
		"Moves one step of the caller org's journey to done and answers the whole refreshed "+
			"journey, which is what unblocks everything downstream of it.\n\n"+
			"Dependency-GATED like start: finishing a step whose prerequisites are themselves "+
			"unfinished is 409 carrying `{error, step, blockedBy}` naming what is in the way, not a "+
			"silent success. A step id the org's active journey does not contain is 404. Skipping "+
			"is the ungated alternative — a founder declaring a step does not apply — and it lives "+
			"at /skip.\n\n"+
			"Requires a validated org; 403 without one. The mark is recorded as `manual`, and "+
			"/reset returns the step to todo.")

	openapi.Describe("/v1/guide/steps/:id/do", http.MethodPost,
		"Have the Business AI actually do the step for you",
		"Executes one step of the caller org's journey through that principal's OWN tool plane and "+
			"answers the action log — `{step, events, state}` — so the caller sees every tool call "+
			"the agent made and where the step ended up. This is the ONE executing path in guide: "+
			"suggest and chat advise, this acts, and the work is charged to the calling "+
			"principal's ledger.\n\n"+
			"Ask for it live and the same actions arrive as Server-Sent Events instead, on either "+
			"of two triggers — `Accept: text/event-stream` or `?stream=1`. The stream opens with a "+
			"comment, emits one frame per action as it happens, and closes with an `end` frame "+
			"carrying `ok` and the final state. The streamed run is detached and bounded at 120 "+
			"seconds, so it finishes on its own clock once the response has begun.\n\n"+
			"An agent that FAILS is not a failed request: the JSON answer still comes back 200 with "+
			"`error` beside the events it did manage, and the stream still ends with `ok:false`. "+
			"The refusals are the ones before the agent runs — 409 with `{error, step, blockedBy}` "+
			"for a step whose dependencies are unfinished, 404 for an id the journey does not "+
			"contain, 403 without a validated org.")

	openapi.Describe("/v1/guide/blueprint", http.MethodPut,
		"Publish a new version of the brand blueprint",
		"Replaces the deployment's brand blueprint — the shared journey, sections, strategies and "+
			"templates every org starts from — as a NEW VERSION, and answers the stored document "+
			"with its key and version number. The previous versions are kept, so /blueprint/versions "+
			"is a real recovery trail.\n\n"+
			"SuperAdmin ONLY. A per-org admin is 403: this is platform content, not a per-customer "+
			"surface — the per-customer surface is /v1/guide/curriculum. The write is audited.\n\n"+
			"The body is a blueprint document accepted as YAML **or** JSON, which is the "+
			"caller-visible reason it takes a raw body. It must parse AND validate — unique ids "+
			"throughout, an acyclic step graph with no dangling dependencies, every step's section "+
			"and every strategy's principle resolving to a real one — or it is 422 and never "+
			"becomes active, leaving the version already serving authoritative. An empty body is "+
			"400 and one over 16 MiB is 413.\n\n"+
			"Edits are live: the next resolve reads the newest version. A stored document that is "+
			"itself corrupt or schema-drifted does not block this write — the target is resolved "+
			"without parsing what is there — so a bad version can always be published over.")

	openapi.Describe("/v1/guide/blueprint/:collection/:id", http.MethodPatch,
		"Edit — or retire — one item of the brand blueprint",
		"Edits a single item of the brand blueprint by id and saves it as a NEW VERSION, answering "+
			"the whole blueprint after the edit. `collection` is one of `sections`, `steps`, "+
			"`strategies` or `templates`; anything else is 400, and an id that collection does not "+
			"hold is 404. This is also the retire lever: `{\"enabled\": false}` takes an item out of "+
			"every org's journey without deleting it or its history.\n\n"+
			"SuperAdmin ONLY, like the rest of the authoring plane; a per-org admin is 403. The "+
			"write is audited.\n\n"+
			"The patch is a SHALLOW merge over the item's own top-level keys — a key you send "+
			"replaces that key whole, a key you omit is left alone — and `id` is dropped from the "+
			"patch before it is applied, so an edit can never rekey an item. That is why the body "+
			"has no declarable shape: its keys are the patched item's, not this route's.\n\n"+
			"Fail-closed on the WHOLE document, not just the item: the blueprint is re-validated "+
			"after the merge, so a patch that would dangle a dependency, break the step DAG or "+
			"empty the journey is 422 and nothing is saved. An empty patch is 400 and one over "+
			"16 MiB is 413.")
}

// Shutdown closes every cached per-org store and the shared blueprint store.
// Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	if mounted.State.blueprints != nil {
		if berr := mounted.State.blueprints.Close(); err == nil {
			err = berr
		}
	}
	mounted = nil
	return err
}

// ── shared helpers ──────────────────────────────────────────────────────────

// tenant resolves the caller's org — the isolation KEY — through principal.Org, the
// ONE org accessor (validated IAM owner, never a raw header). A missing validated
// principal is refused 403.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// ledgerOf is principal.Ledger for a typed op — the payer ONE grounded AI
// completion is billed to. It needs the REQUEST rather than the tenant because
// the payer is a validated identity beyond the org (the billing org / wallet
// claim) that principal.OrgFrom does not carry. Empty off the HTTP path, which
// no op reaches: every caller has already refused through tenantOf.
func ledgerOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return principal.Ledger(c)
	}
	return ""
}

// superAdminOK is the SuperAdmin gate for a typed op — the same predicate the
// superAdmin wrapper applies to the untyped blueprint writes. It needs the
// REQUEST because platform admin-ness lives in a validated header
// (X-User-IsAdmin) that principal.OrgFrom does not carry. False off the HTTP
// path: no request, no attested caller, no platform rights.
func superAdminOK(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && principal.IsSuperAdmin(c)
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

// newID returns a collision-resistant action id.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "act_" + hex.EncodeToString(b[:])
}

// resolveBlueprint resolves the ACTIVE blueprint for the caller — three decomplected
// tiers (the legal-template override-or-builtin pattern):
//
//  1. org override   — the customer's OWN curriculum (per-org guide_curriculum row).
//  2. brand blueprint — the SuperAdmin-authored, seeded platform blueprint (shared DB),
//     resolved for this deployment's brand (brand → base "").
//  3. fixture         — the embedded fail-safe (this brand's blueprint or the base).
//
// tier is "org" | "brand" | "fixture". A tier whose stored doc fails to parse, or whose
// root is disabled, is SKIPPED (fail-closed) so the guide never breaks or serves a
// disabled blueprint. A stored doc was validated at write time; a re-parse failure
// (e.g. a schema the binary predates) simply falls through to the next tier.
func (st state) resolveBlueprint(ctx context.Context, store *Store) (Blueprint, string) {
	if doc, ok, err := store.GetCurriculum(ctx); err == nil && ok {
		if bp, err := Parse(doc); err == nil && on(bp.Enabled) {
			return bp, "org"
		}
	}
	if st.blueprints != nil {
		if doc, _, _, ok, err := st.blueprints.LatestResolved(ctx, st.brand); err == nil && ok {
			if bp, err := Parse(doc); err == nil && on(bp.Enabled) {
				return bp, "brand"
			}
		}
	}
	return st.defBlueprint, "fixture"
}

// activeCurriculum returns the caller's effective (enabled-projected) engine curriculum
// and whether an ORG OVERRIDE is active (the `custom` flag the views surface). The
// brand blueprint and the fixture both read as custom=false — they are the platform
// default, however it was authored.
func (st state) activeCurriculum(ctx context.Context, store *Store) (Curriculum, bool) {
	bp, tier := st.resolveBlueprint(ctx, store)
	return bp.Curriculum(), tier == "org"
}

// stateMap projects the persisted rows onto a bare state map for the engine logic.
func stateMap(rows map[string]StateRow) map[string]State {
	m := make(map[string]State, len(rows))
	for id, r := range rows {
		m[id] = r.State
	}
	return m
}

// snapshotFor loads the caller's store, active curriculum, and progress rows, then
// runs auto-detect (reconcile) so every read reflects the org's real state. It
// returns the reconciled rows (auto-detected steps already persisted + reflected).
// A free function (not a method) — Go forbids methods on the external cloud.Service.
func snapshotFor(s *cloud.Service[state], ctx context.Context, org string) (store *Store, cur Curriculum, custom bool, rows map[string]StateRow, err error) {
	store, err = storeFor(s.State.stores, org)
	if err != nil {
		return nil, Curriculum{}, false, nil, err
	}
	cur, custom = s.State.activeCurriculum(ctx, store)
	rows, err = store.States(ctx)
	if err != nil {
		return nil, Curriculum{}, false, nil, err
	}
	states := stateMap(rows)
	now := time.Now().Unix()
	mark := func(id string) error {
		if err := store.SetState(ctx, id, StateDone, "auto", "auto-detected", now); err != nil {
			return err
		}
		rows[id] = StateRow{State: StateDone, Source: "auto", Note: "auto-detected", UpdatedAt: now}
		return nil
	}
	if err := reconcile(ctx, org, cur, states, s.State.detectors, mark); err != nil {
		return nil, Curriculum{}, false, nil, err
	}
	return store, cur, custom, rows, nil
}

// ── views ─────────────────────────────────────────────────────────────────────

// stepView is one step of the journey as the org-facing reads answer it: the
// step's own fields with its per-org state folded in beside them.
//
// The JourneyStep fields are SPELLED OUT rather than embedded because the two
// projections disagree about embedding: encoding/json PROMOTES an embedded
// struct's fields to the top level, while zip's structSchema publishes the
// embedded type as ONE NESTED property named after it — so the wire carried
// {id, title, …, state} while the document (and every SDK and MCP tool read
// from it) claimed {JourneyStep: {…}, state} for every step object. Same class
// as agents' patchTargetIn and visor's botView (LLM.md, recipe rule 7).
// TestStepViewCarriesJourneyStep pins the copy, so a field JourneyStep gains
// later cannot silently drop out of this view.
type stepView struct {
	// ID is the step's id, as it appears in the journey (e.g. "gsuite").
	ID string `json:"id"`
	// Section is the phase (section id) this step groups under.
	Section string `json:"section,omitempty"`
	Title   string `json:"title"`
	// Detail is the prose/juncture — what the Guide asks or explains here.
	Detail string `json:"detail,omitempty"`
	// Dependencies are step ids that must be done/skipped before this step is
	// available. The wire key is `deps` (the blueprint contract).
	Dependencies []string `json:"deps,omitempty"`
	// Enabled is the admin on/off lever; absent reads as enabled.
	Enabled *bool `json:"enabled,omitempty"`
	// Signal names the machine detector that auto-marks this step done.
	Signal string `json:"signal,omitempty"`
	// Tool is the MCP tool the Business AI runs for "do it for me"; Args are its
	// default arguments, Draft an optional AI prompt whose output fills the
	// DraftInto arg (default "brief").
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Draft     string         `json:"draft,omitempty"`
	DraftInto string         `json:"draftInto,omitempty"`

	// State is the step's per-org lifecycle state: todo|in_progress|done|skipped.
	State State `json:"state"`
	// Source records what marked the state: manual, auto (detected) or agent.
	Source string `json:"source,omitempty"`
	// Available is true when every dependency is done or skipped.
	Available bool `json:"available"`
	// Automatable is true when the Business AI can run this step (it names a tool).
	Automatable bool `json:"automatable"`
	// BlockedBy lists the unfinished dependencies keeping the step unavailable.
	BlockedBy []string `json:"blockedBy,omitempty"`
}

type progressView struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Percent int    `json:"percent"`
	Next    string `json:"next"`
}

type overviewView struct {
	Version  string       `json:"version"`
	Title    string       `json:"title,omitempty"`
	Custom   bool         `json:"custom"`
	Progress progressView `json:"progress"`
	Steps    []stepView   `json:"steps"`
	Funnel   *Funnel      `json:"funnel,omitempty"`
}

func buildOverview(cur Curriculum, custom bool, rows map[string]StateRow) overviewView {
	states := stateMap(rows)
	done, total, percent := cur.Counts(states)
	steps := make([]stepView, 0, len(cur.Steps))
	for _, s := range cur.Steps {
		row := rows[s.ID]
		steps = append(steps, stepView{
			ID: s.ID, Section: s.Section, Title: s.Title, Detail: s.Detail,
			Dependencies: s.Dependencies, Enabled: s.Enabled, Signal: s.Signal,
			Tool: s.Tool, Args: s.Args, Draft: s.Draft, DraftInto: s.DraftInto,
			State:       stateOf(states, s.ID),
			Source:      row.Source,
			Available:   cur.Available(states, s.ID),
			Automatable: strings.TrimSpace(s.Tool) != "",
			BlockedBy:   cur.BlockedBy(states, s.ID),
		})
	}
	return overviewView{
		Version: cur.Version, Title: cur.Title, Custom: custom,
		Progress: progressView{Done: done, Total: total, Percent: percent, Next: cur.Next(states)},
		Steps:    steps,
	}
}

// ── handlers ────────────────────────────────────────────────────────────────

// Overview returns the caller org's launch journey: the active curriculum's
// version and title, every step with its state, whether it is available, what
// blocks it and whether the Business AI can run it, the done/total/percent
// progress with the next step to take, and the org's analytics funnel folded in.
// Auto-detect runs first, so a step the org has already completed elsewhere reads
// done without anyone marking it.
func (o ops) overview(ctx context.Context, _ *noInput) (*overviewView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	_, cur, custom, rows, err := snapshotFor(o.s, ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	ov := buildOverview(cur, custom, rows)
	// Fold the analytics lens in so the overview shows the real funnel alongside the
	// checklist — the AI-GTM read. Best-effort: a degraded warehouse yields an
	// unavailable Funnel, never a failed overview.
	f := analyticsFunnel(ctx, org)
	ov.Funnel = &f
	return &ov, nil
}

// analyticsView is the GET /v1/guide/analytics body.
type analyticsView struct {
	// Funnel is the org's trailing-30-day traffic → signups → orders from the
	// shared analytics warehouse; available is false when it has emitted nothing.
	Funnel Funnel `json:"funnel"`
	// Recommendations are the next-best GTM actions derived from that funnel.
	Recommendations []string `json:"recommendations"`
}

// Analytics returns the caller org's funnel from the analytics lens plus the GTM
// recommendations derived from it. It is the Business AI's data-grounded read —
// what the funnel is doing, and the next-best action to move its weakest stage. An
// unreachable or silent warehouse answers available=false, never a fabricated
// number.
func (o ops) analytics(ctx context.Context, _ *noInput) (*analyticsView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f := analyticsFunnel(ctx, org)
	return &analyticsView{Funnel: f, Recommendations: f.recommend()}, nil
}

// profileResponse is the GET /v1/guide/profile body: the observed growth signals,
// the classified stage, and the org's own key metrics. Its shape is the contract the
// Guide and the later recommendation-corpus filter consume.
type profileResponse struct {
	Stage      Stage          `json:"stage"`
	Signals    SignalSet      `json:"signals"`
	KeyMetrics profileMetrics `json:"keyMetrics"`
}

// Profile returns the caller org's OBSERVED growth profile — the signal set, the
// classified growth stage, and the org's own key metrics. It is a pure READ,
// recomputed from the org's CURRENT state each request (real-time by pull): it
// reuses the reconcile path (snapshotFor runs the detectors) for launch progress
// and runs the growth probes (observe) for the signals — it never caches, never
// runs a billable effect, never targets another org. Org-scoped on the validated
// principal; fail-closed without one. It PRODUCES the profile and classifies the
// stage; it decides NO recommendation (that is a later surface).
func (o ops) profile(ctx context.Context, _ *noInput) (*profileResponse, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	_, cur, _, rows, err := snapshotFor(o.s, ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	funnel := analyticsFunnel(ctx, org)
	set, metrics := observe(ctx, org, o.s.State.signals, funnel)
	states := stateMap(rows)
	done, total, percent := cur.Counts(states)
	metrics.LaunchProgress = progressView{Done: done, Total: total, Percent: percent, Next: cur.Next(states)}
	return &profileResponse{
		Stage:      classifyStage(set),
		Signals:    set,
		KeyMetrics: metrics,
	}, nil
}

// curriculumView is the org's EFFECTIVE curriculum — the enabled journey the
// engine runs — plus whether an org override is what produced it.
type curriculumView struct {
	// Custom is true when the org's OWN curriculum override is active; false when
	// the journey comes from the brand blueprint or the embedded fixture.
	Custom bool `json:"custom"`
	// Curriculum is the enabled journey: its version, title and ordered steps.
	Curriculum Curriculum `json:"curriculum"`
}

// GetCurriculum returns the journey the caller's org is actually running, and
// whether it comes from the org's OWN override (custom) or from the platform
// default — the brand blueprint, else the embedded fixture.
func (o ops) getCurriculum(ctx context.Context, _ *noInput) (*curriculumView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s.State.stores, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	cur, custom := o.s.State.activeCurriculum(ctx, store)
	return &curriculumView{Custom: custom, Curriculum: cur}, nil
}

func putCurriculum(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty curriculum body")
	}
	if len(body) > maxCurriculum {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "curriculum exceeds the %d-byte limit", maxCurriculum)
	}
	bp, err := Parse(body)
	if err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	store, err := storeFor(s.State.stores, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	// Store the CANONICAL parsed form (JSON) so the persisted doc is exactly what the
	// engine runs, independent of the input syntax (YAML or JSON).
	canon, err := json.Marshal(bp)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if err := store.SetCurriculum(c.Context(), canon, time.Now().Unix()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"custom": true, "curriculum": bp.Curriculum()})
}

// DeleteCurriculum clears the caller org's curriculum override and returns the
// journey it falls back to — the brand blueprint, else the embedded fixture.
// Clearing an org that never set one is a no-op that answers the same default.
func (o ops) deleteCurriculum(ctx context.Context, _ *noInput) (*curriculumView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s.State.stores, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if err := store.ClearCurriculum(ctx); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	// Re-resolve: with the override cleared the caller falls through to the brand
	// blueprint (or the fixture) — the effective default they now see.
	cur, custom := o.s.State.activeCurriculum(ctx, store)
	return &curriculumView{Custom: custom, Curriculum: cur}, nil
}

// actionsView is a page of the caller org's Business AI action ledger.
type actionsView struct {
	// Data is the most-recent actions first, capped at listActionsLimit.
	Data []ActionRecord `json:"data"`
}

// ListActions returns the caller org's Business AI action ledger, most recent
// first: every "do it for me" tool call, the arguments it ran with, its result and
// whether it succeeded. It is the audit-visible record of what the agent did on
// the org's behalf, and the backing state for the "acted" auto-detect signal.
func (o ops) listActions(ctx context.Context, _ *noInput) (*actionsView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s.State.stores, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	acts, err := store.ListActions(ctx, listActionsLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return &actionsView{Data: acts}, nil
}

// stepRef names ONE step of the caller's journey. It binds from the URL — the
// route matched on it — so a body can never redirect the write to another step.
type stepRef struct {
	// ID is the step's id, as it appears in the journey (e.g. "gsuite").
	ID string `json:"id"`
}

// blockedErr reports that a GATED transition was refused because the step still
// has unfinished dependencies. It is a value, not a zip error, because the two
// gated routes answer it as a structured 409 body ({error, step, blockedBy}) that
// a zip error envelope ({status, code, error}) cannot express — the one reason
// start and done are still untyped handlers. The ungated routes pass gate=false
// and can never receive it.
type blockedErr struct {
	step      string
	blockedBy []string
}

func (e blockedErr) Error() string { return "step is blocked by unfinished dependencies" }

// applyStep is the ONE body of every step transition: reconcile the caller's
// journey, refuse an id it does not contain, write the new state, and answer the
// refreshed journey. gate reports whether dependency gating applies (start/done
// gate on availability; skip/reset never do) — a gated, blocked step comes back as
// blockedErr for the caller to render.
func applyStep(s *cloud.Service[state], ctx context.Context, org, id string, target State, gate bool) (*overviewView, error) {
	store, cur, custom, rows, err := snapshotFor(s, ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if _, exists := cur.stepByID(id); !exists {
		return nil, zip.ErrNotFound("unknown step: " + id)
	}
	if gate {
		if blocked := cur.BlockedBy(stateMap(rows), id); len(blocked) > 0 {
			return nil, blockedErr{step: id, blockedBy: blocked}
		}
	}
	if target == StateTodo {
		if err := store.ResetState(ctx, id); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
		}
		rows[id] = StateRow{State: StateTodo}
	} else {
		if err := store.SetState(ctx, id, target, "manual", "", time.Now().Unix()); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
		}
		rows[id] = StateRow{State: target, Source: "manual", UpdatedAt: time.Now().Unix()}
	}
	ov := buildOverview(cur, custom, rows)
	return &ov, nil
}

// transition is the untyped half of the split: the GATED pair, whose blocked
// answer is a structured 409 written in-band.
func transition(s *cloud.Service[state], c *zip.Ctx, target State) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	ov, err := applyStep(s, c.Context(), org, idParam(c), target, true)
	var blocked blockedErr
	if errors.As(err, &blocked) {
		return c.JSON(http.StatusConflict, map[string]any{
			"error":     blocked.Error(),
			"step":      blocked.step,
			"blockedBy": blocked.blockedBy,
		})
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, ov)
}

func markStart(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateInProgress)
}
func markDone(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateDone)
}

// SkipStep marks one step of the caller org's journey skipped and returns the
// refreshed journey. Skipping is never dependency-gated — the founder is
// declaring the step does not apply to them — so a step whose dependencies are
// unfinished can still be skipped, and a skipped step counts as terminal for
// everything downstream of it.
func (o ops) skipStep(ctx context.Context, in *stepRef) (*overviewView, error) {
	return o.setStep(ctx, in, StateSkipped)
}

// ResetStep returns one step of the caller org's journey to todo — clearing a
// manual mark or a skip — and returns the refreshed journey. Reset is never
// dependency-gated. Auto-detect runs on the next read, so a step the org has in
// fact completed elsewhere goes straight back to done.
func (o ops) resetStep(ctx context.Context, in *stepRef) (*overviewView, error) {
	return o.setStep(ctx, in, StateTodo)
}

// setStep is the ungated transition an op performs: resolve the tenant, then the
// shared body. Never gated, so applyStep can never hand it a blockedErr.
func (o ops) setStep(ctx context.Context, in *stepRef, target State) (*overviewView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	return applyStep(o.s, ctx, org, strings.TrimSpace(in.ID), target, false)
}

// doStep is "do it for me": the Business AI executes the step through the
// per-principal MCP plane. Dependency-gated (a blocked step is 409). Streams the
// agent's actions as SSE when the caller asks (Accept: text/event-stream or
// ?stream=1); otherwise returns the full action log as JSON.
func doStep(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	payer := principal.Ledger(c)
	id := idParam(c)
	store, cur, _, rows, err := snapshotFor(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	step, exists := cur.stepByID(id)
	if !exists {
		return zip.ErrNotFound("unknown step: " + id)
	}
	if blocked := cur.BlockedBy(stateMap(rows), id); len(blocked) > 0 {
		return c.JSON(http.StatusConflict, map[string]any{
			"error":     "step is blocked by unfinished dependencies",
			"step":      id,
			"blockedBy": blocked,
		})
	}
	d := agentDeps{ai: s.State.ai, model: s.State.model, store: store, invoke: s.State.invoke, toolOK: s.State.toolOK}

	if wantsSSE(c) {
		// The stream loop OUTLIVES this handler (runs under SendStreamWriter after the
		// Ctx is recycled), so clone the retained values and use a detached, bounded
		// context for the agent's AI + MCP calls.
		orgC, payerC := strings.Clone(org), strings.Clone(payer)
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		c.SetHeader("X-Accel-Buffering", "no")
		return c.SendStreamWriter(func(w *bufio.Writer) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			_, _ = w.WriteString(": stream open\n\n")
			_ = w.Flush()
			emit := func(e event) { writeSSE(w, e.Type, e) }
			final, aerr := runAgent(ctx, d, orgC, payerC, step, emit)
			end := map[string]any{"ok": aerr == nil, "state": final}
			if aerr != nil {
				end["error"] = aerr.Error()
			}
			writeSSE(w, "end", end)
		})
	}

	events := make([]event, 0, 6)
	final, aerr := runAgent(c.Context(), d, org, payer, step, func(e event) { events = append(events, e) })
	resp := map[string]any{"step": id, "events": events, "state": final}
	if aerr != nil {
		resp["error"] = aerr.Error()
	}
	return c.JSON(http.StatusOK, resp)
}

// wantsSSE reports whether the caller wants a Server-Sent-Events stream.
func wantsSSE(c *zip.Ctx) bool {
	return strings.Contains(c.Header("Accept"), "text/event-stream") ||
		strings.TrimSpace(c.Query("stream")) == "1"
}

// writeSSE writes one SSE frame (event: <type>\ndata: <json>\n\n) and flushes.
func writeSSE(w *bufio.Writer, evt string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt, b); err != nil {
		return
	}
	_ = w.Flush()
}
