package guide

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/principal"
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

func routes(app cloud.Router, s *cloud.Service[state]) {
	// Overview stays flat: Group("/v1/guide").Get("") would register "/v1/guide/",
	// not the bare surface path.
	app.Get("/v1/guide", cloud.Handle(s, overview))

	g := app.Group("/v1/guide")
	g.Get("/analytics", cloud.Handle(s, gtmAnalytics))
	// The OBSERVE surface: the org's real-time growth profile — observed signals, the
	// classified growth stage, and the org's own key metrics. READ-ONLY (see profile).
	g.Get("/profile", cloud.Handle(s, profile))
	// The STRATEGIES corpus: the tactics library, org-scoped and filtered by
	// category/workload + the caller's observed stage/signals. READ-ONLY — the corpus
	// the recommendation-surface engine consumes. Content is SHARED (no org records).
	g.Get("/strategies", cloud.Handle(s, strategies))
	// The Business AI's dynamic "what to do next": the ranked next-best quests + a
	// grounded narrative (suggest), and a grounded founder chat (chat). Both are
	// READ-ONLY — they advise, never run an action (the only executing path is
	// /steps/:id/do). See suggest.go.
	g.Get("/suggest", cloud.Handle(s, suggest))
	g.Post("/chat", cloud.Handle(s, chat))
	// The per-org OVERRIDE tier (tier 1): a customer sets/clears its OWN curriculum.
	// Org-scoped, per-customer — NOT the shared brand blueprint.
	g.Get("/curriculum", cloud.Handle(s, getCurriculum))
	g.Put("/curriculum", cloud.Handle(s, putCurriculum))
	g.Delete("/curriculum", cloud.Handle(s, deleteCurriculum))
	g.Get("/actions", cloud.Handle(s, listActions))
	g.Post("/steps/:id/start", cloud.Handle(s, markStart))
	g.Post("/steps/:id/done", cloud.Handle(s, markDone))
	g.Post("/steps/:id/skip", cloud.Handle(s, markSkip))
	g.Post("/steps/:id/reset", cloud.Handle(s, markReset))
	g.Post("/steps/:id/do", cloud.Handle(s, doStep))

	// The SuperAdmin BLUEPRINT plane (tier 2 authoring): the platform/brand blueprint,
	// authored LIVE on admin.hanzo.ai. Gated on IsSuperAdmin (owner=="admin") — a normal
	// org member/admin gets 403; the brand blueprint is SHARED platform content, not a
	// per-customer surface. See admin.go.
	b := app.Group("/v1/guide/blueprint")
	b.Get("", superAdmin(s, getBlueprint))
	b.Put("", superAdmin(s, putBlueprint))
	b.Get("/versions", superAdmin(s, listBlueprintVersions))
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
	Step
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
			Step:        s,
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

func overview(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, cur, custom, rows, err := snapshotFor(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	ov := buildOverview(cur, custom, rows)
	// Fold the analytics lens in so the overview shows the real funnel alongside the
	// checklist — the AI-GTM read. Best-effort: a degraded warehouse yields an
	// unavailable Funnel, never a failed overview.
	f := analyticsFunnel(c.Context(), org)
	ov.Funnel = &f
	return c.JSON(http.StatusOK, ov)
}

// gtmAnalytics answers GET /v1/guide/analytics: the org's funnel from the analytics
// lens plus the derived GTM recommendations. It is the Business AI's data-grounded
// read — what the funnel is doing and the next-best action to move the weakest stage.
func gtmAnalytics(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	f := analyticsFunnel(c.Context(), org)
	return c.JSON(http.StatusOK, map[string]any{
		"funnel":          f,
		"recommendations": f.recommend(),
	})
}

// profileResponse is the GET /v1/guide/profile body: the observed growth signals,
// the classified stage, and the org's own key metrics. Its shape is the contract the
// Guide and the later recommendation-corpus filter consume.
type profileResponse struct {
	Stage      Stage          `json:"stage"`
	Signals    SignalSet      `json:"signals"`
	KeyMetrics profileMetrics `json:"keyMetrics"`
}

// profile answers GET /v1/guide/profile: the org's OBSERVED growth profile — the
// signal set, the classified growth stage, and the org's own key metrics. It is a
// pure READ, recomputed from the org's CURRENT state each request (real-time by
// pull): it reuses the reconcile path (snapshotFor runs the detectors) for launch
// progress and runs the growth probes (observe) for the signals — it never caches,
// never runs a billable effect, never targets another org. Org-scoped on the
// validated principal; fail-closed without one. It PRODUCES the profile and
// classifies the stage; it decides NO recommendation (that is a later surface).
func profile(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	ctx := c.Context()
	_, cur, _, rows, err := snapshotFor(s, ctx, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	funnel := analyticsFunnel(ctx, org)
	set, metrics := observe(ctx, org, s.State.signals, funnel)
	states := stateMap(rows)
	done, total, percent := cur.Counts(states)
	metrics.LaunchProgress = progressView{Done: done, Total: total, Percent: percent, Next: cur.Next(states)}
	return c.JSON(http.StatusOK, profileResponse{
		Stage:      classifyStage(set),
		Signals:    set,
		KeyMetrics: metrics,
	})
}

func getCurriculum(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	cur, custom := s.State.activeCurriculum(c.Context(), store)
	return c.JSON(http.StatusOK, map[string]any{"custom": custom, "curriculum": cur})
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

func deleteCurriculum(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if err := store.ClearCurriculum(c.Context()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	// Re-resolve: with the override cleared the caller falls through to the brand
	// blueprint (or the fixture) — the effective default they now see.
	cur, custom := s.State.activeCurriculum(c.Context(), store)
	return c.JSON(http.StatusOK, map[string]any{"custom": custom, "curriculum": cur})
}

func listActions(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	acts, err := store.ListActions(c.Context(), listActionsLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": acts})
}

// transition is the shared body of the state-write handlers. gate reports whether
// dependency gating applies (start/done gate on availability; skip/reset never do).
func transition(s *cloud.Service[state], c *zip.Ctx, target State, gate bool) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	store, cur, custom, rows, err := snapshotFor(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if _, exists := cur.stepByID(id); !exists {
		return zip.ErrNotFound("unknown step: " + id)
	}
	if gate {
		if blocked := cur.BlockedBy(stateMap(rows), id); len(blocked) > 0 {
			return c.JSON(http.StatusConflict, map[string]any{
				"error":     "step is blocked by unfinished dependencies",
				"step":      id,
				"blockedBy": blocked,
			})
		}
	}
	if target == StateTodo {
		if err := store.ResetState(c.Context(), id); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
		}
		rows[id] = StateRow{State: StateTodo}
	} else {
		if err := store.SetState(c.Context(), id, target, "manual", "", time.Now().Unix()); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
		}
		rows[id] = StateRow{State: target, Source: "manual", UpdatedAt: time.Now().Unix()}
	}
	return c.JSON(http.StatusOK, buildOverview(cur, custom, rows))
}

func markStart(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateInProgress, true)
}
func markDone(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateDone, true)
}
func markSkip(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateSkipped, false)
}
func markReset(s *cloud.Service[state], c *zip.Ctx) error {
	return transition(s, c, StateTodo, false)
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
