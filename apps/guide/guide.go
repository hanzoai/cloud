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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/automations"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
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
	stores := cloud.NewOrgStore(deps.DataDir, "guide", openStore)

	// Open the SHARED brand-blueprint store (one file for the deployment) and SEED it
	// idempotently: the embedded fixtures (base + each brand) are seeded-if-absent, so
	// a redeploy never clobbers a SuperAdmin's live edits. After seeding the DB is
	// authoritative; the embedded fixture is only the seed source + fail-safe fallback.
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("guide.Mount: data dir: %w", err)
	}
	blueprints, err := openBlueprintStore(filepath.Join(deps.DataDir, "guide-blueprint.db"))
	if err != nil {
		return fmt.Errorf("guide.Mount: open blueprint store: %w", err)
	}
	seeded, err := seedBlueprints(context.Background(), blueprints)
	if err != nil {
		_ = blueprints.Close()
		return fmt.Errorf("guide.Mount: seed blueprints: %w", err)
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "guide"), State: state{
		stores:       stores,
		blueprints:   blueprints,
		brand:        deps.Brand,
		defBlueprint: fixtureBlueprint(deps.Brand),
		signals:      boundSignals, // installed by the composition root before Mount; zero value honest-degrades
		ai:           deps.AI,
		model:        strings.TrimSpace(deps.AIDefaultModel),
		audit:        deps.Audit,
		invoke:       automations.InvokeTool,
		toolOK:       automations.ToolExists,
	}}
	s.State.detectors = newDetectors(func(_ context.Context, org string) (*Store, error) {
		return stores.For(org, "")
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
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *cloud.Service[state]) {
	// The bridge FIRST, and on guide's OWN subtree — the shape apps/admin and
	// apps/marketing already use. A TYPED op receives only a context, so the
	// validated org has to be parked there; it is never taken from an In field,
	// which is caller-supplied and would be a cross-tenant read the caller
	// asserted for itself. fiber runs middleware in registration order, so this
	// must precede every leaf below. Serve installs one app-wide too; nesting is
	// harmless — the inner one is what the handler sees — and this one is what
	// makes guide's own tests (which mount only guide) carry an org at all.
	app.Group("/v1/guide").Use(cloud.Bridge())

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
	// exactly one JSON value.
	g.Post("/steps/:id/do", cloud.Handle(s, doStep))

	// The SuperAdmin BLUEPRINT plane (tier 2 authoring): the platform/brand blueprint,
	// authored LIVE on admin.hanzo.ai. Gated on IsSuperAdmin (owner=="admin") — a normal
	// org member/admin gets 403; the brand blueprint is SHARED platform content, not a
	// per-customer surface. See admin.go.
	b := g.Group("/blueprint")
	zip.Get(b, "", o.getBlueprint)
	// PUT and PATCH stay UNTYPED, for the same reason as PUT /curriculum: the PUT
	// body is a YAML-or-JSON blueprint document, and the PATCH body is an opaque
	// JSON merge-patch whose keys are the item's own — neither is a declarable In.
	b.Put("", superAdmin(s, putBlueprint))
	zip.Get(b, "/versions", o.listBlueprintVersions)
	b.Patch("/:collection/:id", superAdmin(s, patchBlueprintItem))
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

// tenantOf is tenant() for a TYPED op: the same validated org, resolved off the
// context cloud.Bridge parked it on because a typed handler receives only a
// context. It is never an In field — an In field is caller-supplied, so a tenant
// key read from one is a cross-tenant read the caller asserted for itself. Fails
// closed off the HTTP path, where nothing parked an org.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

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
	store, err = s.State.stores.For(org, "")
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

type stepView struct {
	JourneyStep
	State       State    `json:"state"`
	Source      string   `json:"source,omitempty"`
	Available   bool     `json:"available"`
	Automatable bool     `json:"automatable"`
	BlockedBy   []string `json:"blockedBy,omitempty"`
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
		st := stateOf(states, s.ID)
		steps = append(steps, stepView{
			JourneyStep: s,
			State:       st,
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
	org, err := tenantOf(ctx)
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
	org, err := tenantOf(ctx)
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
	org, err := tenantOf(ctx)
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := o.s.State.stores.For(org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	cur, custom := o.s.State.activeCurriculum(ctx, store)
	return &curriculumView{Custom: custom, Curriculum: cur}, nil
}

func putCurriculum(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
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
	store, err := s.State.stores.For(org, "")
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := o.s.State.stores.For(org, "")
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := o.s.State.stores.For(org, "")
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
		return zip.ErrForbidden("X-Org-Id required")
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
	org, err := tenantOf(ctx)
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
		return zip.ErrForbidden("X-Org-Id required")
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
