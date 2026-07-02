// Package agents mounts the Hanzo Cloud /v1/agents surface: per-org autonomous
// agent definitions and their runs. An agent is a model + a system prompt
// (instructions) + a set of tool names; running one executes a real chat
// completion through the in-process AI client (the SAME gateway path the rest
// of the console uses) and records the run. Tenant isolation is the
// gateway-minted X-Org-Id (HIP-0026) enforced as the org column on every
// query, so one tenant can never read, run, or delete another's agents.
//
// Surface (all org-scoped; console2's AgentsModule reads {agents:[...]}):
//
//	GET    /v1/agents                list agents for the org      -> {agents:[...]}
//	POST   /v1/agents                create an agent              -> Agent
//	GET    /v1/agents/:name          agent detail + recent runs   -> AgentDetail
//	PATCH  /v1/agents/:name          update an agent              -> Agent
//	DELETE /v1/agents/:name          delete an agent (+ its runs)
//	POST   /v1/agents/:name/run      run the agent {input}        -> RunResult
//	GET    /v1/agents/:name/runs     run history                  -> {runs:[...]}
//
// The store is SQLite in deps.DataDir (Base/SQLite-only). It holds definitions
// and run I/O only — never a secret; tool credentials live in KMS by reference.
package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// nameRE is the org-unique handle AND the URL path segment — the traversal
// guard at the boundary.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	maxInstructions = 32 * 1024 // system prompt cap
	maxInput        = 128 * 1024
)

type svc struct {
	store *Store
	ai    types.AIClient
	log   luxlog.Logger
}

var mounted *svc

// ---- HTTP response shapes (the published contract) ----

type agentView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools"`
	Status      string   `json:"status"`
	Runs        int      `json:"runs"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type agentDetail struct {
	agentView
	Instructions string    `json:"instructions"`
	RecentRuns   []runView `json:"recentRuns"`
}

type runView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	Input      string `json:"input"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  string `json:"createdAt"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func toView(a Agent, runs int) agentView {
	return agentView{
		ID: a.ID, Name: a.Name, Model: a.Model, Description: a.Description,
		Tools: nonNil(a.Tools), Status: a.Status, Runs: runs,
		CreatedAt: rfc3339(a.CreatedAt), UpdatedAt: rfc3339(a.UpdatedAt),
	}
}

func toRunView(r Run) runView {
	return runView{
		ID: r.ID, Status: r.Status, Model: r.Model, Input: r.Input, Output: r.Output,
		Error: r.Error, DurationMs: r.DurationMs, CreatedAt: rfc3339(r.CreatedAt),
	}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Mount wires the agents surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agents.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("agents.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "agents")
	if deps.DataDir == "" {
		return fmt.Errorf("agents.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("agents.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "agents.db"))
	if err != nil {
		return fmt.Errorf("agents.Mount: open store: %w", err)
	}
	// deps.AI may be nil when no gateway is configured; run() degrades honestly.
	s := &svc{store: store, ai: deps.AI, log: log}
	mounted = s

	app.Get("/v1/agents", s.list)
	app.Post("/v1/agents", s.create)
	app.Get("/v1/agents/:name", s.get)
	app.Patch("/v1/agents/:name", s.update)
	app.Delete("/v1/agents/:name", s.del)
	app.Post("/v1/agents/:name/run", s.run)
	app.Get("/v1/agents/:name/runs", s.runs)

	log.Info("agents mounted", "ai", s.ai != nil, "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("agents", 127, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("agents.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ---- handlers ----

type createReq struct {
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Instructions string   `json:"instructions"`
	Description  string   `json:"description"`
	Tools        []string `json:"tools"`
}

func (s *svc) create(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		return zip.ErrBadRequest("model is required")
	}
	if len(body.Instructions) > maxInstructions {
		return zip.ErrBadRequest("instructions too large")
	}
	id, err := genID("agent")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a := Agent{
		ID: id, Org: org, Name: name, Model: model, Instructions: body.Instructions,
		Description: strings.TrimSpace(body.Description), Tools: cleanList(body.Tools),
		Status: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Create(c.Context(), a); err != nil {
		if err == errConflict {
			return zip.ErrConflict("agent already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toView(a, 0))
}

func (s *svc) list(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]agentView, 0, len(rows))
	for _, a := range rows {
		n, err := s.store.CountRuns(c.Context(), org, a.Name)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
		}
		out = append(out, toView(a, n))
	}
	return c.JSON(http.StatusOK, map[string]any{"agents": out})
}

func (s *svc) get(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	runs, err := s.store.ListRuns(c.Context(), org, name, 20)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	rv := make([]runView, 0, len(runs))
	for _, r := range runs {
		rv = append(rv, toRunView(r))
	}
	return c.JSON(http.StatusOK, agentDetail{
		agentView: toView(a, len(runs)), Instructions: a.Instructions, RecentRuns: rv,
	})
}

type updateReq struct {
	Model        *string   `json:"model"`
	Instructions *string   `json:"instructions"`
	Description  *string   `json:"description"`
	Tools        *[]string `json:"tools"`
}

func (s *svc) update(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body updateReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Model != nil {
		m := strings.TrimSpace(*body.Model)
		if m == "" {
			return zip.ErrBadRequest("model cannot be empty")
		}
		a.Model = m
	}
	if body.Instructions != nil {
		if len(*body.Instructions) > maxInstructions {
			return zip.ErrBadRequest("instructions too large")
		}
		a.Instructions = *body.Instructions
	}
	if body.Description != nil {
		a.Description = strings.TrimSpace(*body.Description)
	}
	if body.Tools != nil {
		a.Tools = cleanList(*body.Tools)
	}
	a.UpdatedAt = time.Now().Unix()
	if err := s.store.Update(c.Context(), a); err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("agent not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	n, _ := s.store.CountRuns(c.Context(), org, name)
	return c.JSON(http.StatusOK, toView(a, n))
}

func (s *svc) del(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.Delete(c.Context(), org, nameParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("agent not found")
	}
	return c.NoContent(http.StatusNoContent)
}

type runReq struct {
	Input string `json:"input"`
}

// run executes the agent: it composes the agent's instructions with the caller
// input and runs a real chat completion via the in-process AI client, then
// records the run. Every returned run reflects an execution that actually
// happened — an inference failure is recorded and returned as an error run, not
// hidden and not fabricated.
func (s *svc) run(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	a, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body runReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Input) > maxInput {
		return zip.ErrBadRequest("input too large")
	}
	if s.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}

	r := executeRun(c.Context(), s.ai, org, a, body.Input)
	// Record the run regardless of inference outcome — the history is real.
	if err := s.store.InsertRun(c.Context(), r); err != nil {
		s.log.Warn("record run failed", "org", org, "agent", name, "err", err)
	}
	if r.Status != "ok" {
		// The run is recorded; surface the upstream failure honestly.
		return c.JSON(http.StatusBadGateway, toRunView(r))
	}
	return c.JSON(http.StatusOK, toRunView(r))
}

// executeRun composes the agent's instructions with the caller input, runs one
// real chat completion through the AI client, and returns the resulting Run —
// status "ok" with output, or "error" with the upstream failure. Pure of HTTP
// and persistence so it is directly testable; the caller records + responds.
func executeRun(ctx context.Context, ai types.AIClient, org string, a Agent, input string) Run {
	prompt := a.Instructions
	if in := strings.TrimSpace(input); in != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += in
	}
	start := time.Now()
	resp, aiErr := ai.ChatCompletion(ctx, &types.ChatRequest{Model: a.Model, Prompt: prompt})
	dur := time.Since(start).Milliseconds()
	id, _ := genID("run")
	r := Run{
		ID: id, Org: org, AgentName: a.Name, Model: a.Model, Input: input,
		DurationMs: dur, CreatedAt: time.Now().Unix(),
	}
	if aiErr != nil {
		r.Status = "error"
		r.Error = aiErr.Error()
	} else {
		r.Status = "ok"
		if resp != nil {
			r.Output = resp.Content
		}
	}
	return r
}

func (s *svc) runs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	limit := 50
	if q := strings.TrimSpace(c.Query("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	runs, err := s.store.ListRuns(c.Context(), org, name, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "runs: %v", err)
	}
	out := make([]runView, 0, len(runs))
	for _, r := range runs {
		out = append(out, toRunView(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"runs": out})
}

// ---- helpers ----

func nameParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("name")) }

// tenant resolves the org — the tenant isolation KEY. It uses c.Org() EXACTLY
// as SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026):
// never lowercased/stripped/truncated. Normalizing would collapse distinct
// owners into one bucket (Red HIGH-1). Reject only empty or pathologically
// long. No magic "admin" bucket — a global admin operating on per-org data
// carries an explicit org, so an empty org is a true 403.
func tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

func cleanList(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || len(x) > 128 || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		if len(out) >= 64 {
			break
		}
	}
	return out
}

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown closes the agents store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
