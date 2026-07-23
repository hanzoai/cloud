package guide

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
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
	stores    *cloud.OrgStore[*Store]
	def       Curriculum
	detectors map[string]Detector
	ai        cloud.AIClient
	model     string
	invoke    func(ctx context.Context, org, tool string, args map[string]any) (any, error)
	toolOK    func(tool string) bool
}

// mounted is the active service so Shutdown can close the per-org stores.
var mounted *cloud.Service[state]

// Mount wires /v1/guide/* onto app. Complex flavour (a package global for Shutdown
// + a per-org OrgStore), so it constructs the Service value directly.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("guide.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("guide.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("guide.Mount: empty DataDir")
	}
	stores := cloud.NewOrgStore(deps.DataDir, "guide", openStore)
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "guide"), State: state{
		stores: stores,
		def:    defaultCurriculum,
		ai:     deps.AI,
		model:  strings.TrimSpace(deps.AIDefaultModel),
		invoke: automations.InvokeTool,
		toolOK: automations.ToolExists,
	}}
	s.State.detectors = newDetectors(func(_ context.Context, org string) (*Store, error) {
		return stores.For(org, "")
	})
	mounted = s
	routes(app, s)
	s.Log.Info("guide mounted", "steps", len(defaultCurriculum.Steps), "brand", deps.Brand)
	return nil
}

func routes(app *zip.App, s *cloud.Service[state]) {
	// Overview stays flat: Group("/v1/guide").Get("") would register "/v1/guide/",
	// not the bare surface path.
	app.Get("/v1/guide", cloud.Handle(s, overview))

	g := app.Group("/v1/guide")
	g.Get("/analytics", cloud.Handle(s, gtmAnalytics))
	g.Get("/curriculum", cloud.Handle(s, getCurriculum))
	g.Put("/curriculum", cloud.Handle(s, putCurriculum))
	g.Delete("/curriculum", cloud.Handle(s, deleteCurriculum))
	g.Get("/actions", cloud.Handle(s, listActions))
	g.Post("/steps/:id/start", cloud.Handle(s, markStart))
	g.Post("/steps/:id/done", cloud.Handle(s, markDone))
	g.Post("/steps/:id/skip", cloud.Handle(s, markSkip))
	g.Post("/steps/:id/reset", cloud.Handle(s, markReset))
	g.Post("/steps/:id/do", cloud.Handle(s, doStep))
}

// Shutdown closes every cached per-org store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
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

// activeCurriculum returns the org's curriculum — its custom override if present,
// else the built-in default — and whether an override is active. A stored override
// was validated at PUT time; if it somehow fails to parse now (e.g. a schema the
// binary predates) the default is used so the guide never breaks.
func (st state) activeCurriculum(ctx context.Context, store *Store) (Curriculum, bool) {
	doc, ok, err := store.GetCurriculum(ctx)
	if err != nil || !ok {
		return st.def, false
	}
	cur, err := Parse(doc)
	if err != nil {
		return st.def, false
	}
	return cur, true
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
	cur, err := Parse(body)
	if err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	store, err := s.State.stores.For(org, "")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	// Store the CANONICAL parsed form (JSON) so the persisted doc is exactly what the
	// engine runs, independent of the input syntax (YAML or JSON).
	canon, err := json.Marshal(cur)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	if err := store.SetCurriculum(c.Context(), canon, time.Now().Unix()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "guide: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"custom": true, "curriculum": cur})
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
	return c.JSON(http.StatusOK, map[string]any{"custom": false, "curriculum": s.State.def})
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
	payer := principal.HomeOrg(c)
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
