// Package session mounts the Hanzo Cloud /v1/code/sessions/* surface: the
// registry of live coding-agent runs launched by `hanzo code <agent>`. It is
// what console.hanzo.ai lists and drives — every claude/codex/dev process, on
// every machine, shown as a session you can watch (over its ttyd terminal
// tunnel) and steer.
//
// Org isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value minted from the VALIDATED bearer owner claim —
// and NEVER a client header. One SQLite file per org ({DataDir}/orgs/{slug}/
// session.db), every query filtered WHERE org=?, so one org can never see or
// mutate another's sessions. Project is the LINK to the deployable
// projects.Project a run belongs to (a column, filterable), not a second
// partition: the console default is the org-wide "what's running" view.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/code/sessions                register/announce a run     -> Session (201)
//	GET    /v1/code/sessions[?live&status=&project=&host=&agent=]  list -> [Session]
//	GET    /v1/code/sessions/:id            session detail              -> Session
//	PATCH  /v1/code/sessions/:id            heartbeat/status/terminalURL -> Session
//	DELETE /v1/code/sessions/:id            forget a session
//	POST   /v1/code/sessions/:id/events     record a hook event         -> Event (201)
//	GET    /v1/code/sessions/:id/events     list events                 -> [Event]
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	maxField = 256   // host, model, user, project, agent
	maxCwd   = 4096  // a working-directory path
	maxURL   = 2048  // the ttyd terminal endpoint
	maxMsg   = 16384 // an event message
)

// idRE constrains a client-minted session id to a URL/identifier-safe token, so
// the launcher can mint a uuid locally and reference the run before the register
// round-trip returns. It is the boundary guard on the :id path segment.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// agents is the closed set of runnable coding agents (mirrors `hanzo code`).
var agents = map[string]bool{"claude": true, "codex": true, "dev": true}

// statuses is the lifecycle a session moves through; the launcher and the hook
// bridge set them, the console reads them.
var statuses = map[string]bool{
	"starting": true, "running": true, "waiting": true, "ended": true, "error": true,
}

type state struct {
	stores *cloud.OrgStore[*Store] // one session.db per org, opened once each
}

var mounted *cloud.Service[state]

// Mount wires /v1/code/sessions/* onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return errors.New("session.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return errors.New("session.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return errors.New("session.Mount: empty DataDir")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "session"), State: state{
		stores: cloud.NewOrgStore(deps.DataDir, "session", openStore),
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("session registry mounted", "brand", s.Brand)
	return nil
}

// Shutdown closes every open per-org store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}

// routes registers the surface. Collection endpoints register before their
// :id siblings so the first-match scan resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/code")
	g.Post("/sessions", cloud.Handle(s, createSession))
	g.Get("/sessions", cloud.Handle(s, listSessions))
	g.Get("/sessions/:id", cloud.Handle(s, getSession))
	g.Patch("/sessions/:id", cloud.Handle(s, updateSession))
	g.Delete("/sessions/:id", cloud.Handle(s, deleteSession))
	g.Post("/sessions/:id/events", cloud.Handle(s, createEvent))
	g.Get("/sessions/:id/events", cloud.Handle(s, listEvents))
}

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	return s.State.stores.For(org, "")
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

func genID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "ses_" + hex.EncodeToString(b[:])
}

// ---- HTTP shapes (the published contract) ----

type createSessionReq struct {
	ID          string `json:"id"`          // client-minted; generated if empty
	Agent       string `json:"agent"`       // claude | codex | dev
	Model       string `json:"model"`       // zen5-pro, ...
	Host        string `json:"host"`        // machine name
	Cwd         string `json:"cwd"`         // working directory
	Project     string `json:"project"`     // deployable-project link ("" = none)
	User        string `json:"user"`        // display label of who launched
	TerminalURL string `json:"terminalUrl"` // set now if the tunnel is already up
}

type updateSessionReq struct {
	Status      *string `json:"status"`
	TerminalURL *string `json:"terminalUrl"`
	Model       *string `json:"model"`
	Ended       *bool   `json:"ended"` // true → stamp ended_at + status=ended
}

type createEventReq struct {
	Kind    string `json:"kind"` // notification | stop | error | log
	Message string `json:"message"`
}

type sessionView struct {
	ID          string `json:"id"`
	Org         string `json:"org"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	Agent       string `json:"agent"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	TerminalURL string `json:"terminalUrl,omitempty"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	EndedAt     int64  `json:"endedAt,omitempty"`
}

type eventView struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

func toSessionView(x Session) sessionView {
	return sessionView{
		ID: x.ID, Org: x.Org, Project: x.Project, User: x.User, Agent: x.Agent,
		Model: x.Model, Host: x.Host, Cwd: x.Cwd, TerminalURL: x.TerminalURL,
		Status: x.Status, StartedAt: x.StartedAt, UpdatedAt: x.UpdatedAt, EndedAt: x.EndedAt,
	}
}

func toEventView(e Event) eventView {
	return eventView{ID: e.ID, SessionID: e.SessionID, Kind: e.Kind, Message: e.Message, CreatedAt: e.CreatedAt}
}

// ---- handlers ----

func createSession(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var body createSessionReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	agent := strings.ToLower(strings.TrimSpace(body.Agent))
	if !agents[agent] {
		return zip.ErrBadRequest("agent must be one of claude, codex, dev")
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = genID()
	} else if !idRE.MatchString(id) {
		return zip.ErrBadRequest("id must match ^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$")
	}
	if len(body.Cwd) > maxCwd || len(body.TerminalURL) > maxURL ||
		len(body.Model) > maxField || len(body.Host) > maxField ||
		len(body.Project) > maxField || len(body.User) > maxField {
		return zip.ErrBadRequest("a field exceeds its length bound")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().Unix()
	x := Session{
		ID: id, Org: org, Project: strings.TrimSpace(body.Project),
		User: strings.TrimSpace(body.User), Agent: agent,
		Model: strings.TrimSpace(body.Model), Host: strings.TrimSpace(body.Host),
		Cwd: body.Cwd, TerminalURL: strings.TrimSpace(body.TerminalURL),
		Status: "starting", StartedAt: now, UpdatedAt: now,
	}
	if err := store.Create(c.Context(), x); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toSessionView(x))
}

func listSessions(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	f := Filter{
		Status:  strings.TrimSpace(c.Query("status")),
		Project: strings.TrimSpace(c.Query("project")),
		Host:    strings.TrimSpace(c.Query("host")),
		Agent:   strings.TrimSpace(c.Query("agent")),
		Live:    c.Query("live") != "",
	}
	rows, err := store.List(c.Context(), org, f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]sessionView, 0, len(rows))
	for _, x := range rows {
		out = append(out, toSessionView(x))
	}
	return c.JSON(http.StatusOK, out)
}

func getSession(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	x, err := store.Get(c.Context(), org, idParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("session not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return c.JSON(http.StatusOK, toSessionView(x))
}

func updateSession(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var body updateSessionReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	p := Patch{Model: body.Model, TerminalURL: body.TerminalURL}
	if body.TerminalURL != nil && len(*body.TerminalURL) > maxURL {
		return zip.ErrBadRequest("terminalUrl too long")
	}
	if body.Status != nil {
		st := strings.ToLower(strings.TrimSpace(*body.Status))
		if !statuses[st] {
			return zip.ErrBadRequest("status must be one of starting, running, waiting, ended, error")
		}
		p.Status = &st
	}
	if body.Ended != nil && *body.Ended {
		now := time.Now().Unix()
		ended := "ended"
		p.EndedAt = &now
		if p.Status == nil {
			p.Status = &ended
		}
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	x, err := store.Update(c.Context(), org, idParam(c), time.Now().Unix(), p)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("session not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toSessionView(x))
}

func deleteSession(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	if err := store.Delete(c.Context(), org, idParam(c)); errors.Is(err, errNotFound) {
		return zip.ErrNotFound("session not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func createEvent(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var body createEventReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind == "" || len(kind) > maxField {
		return zip.ErrBadRequest("kind is required")
	}
	if len(body.Message) > maxMsg {
		return zip.ErrBadRequest("message too long")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	e := Event{ID: genID(), SessionID: idParam(c), Org: org, Kind: kind, Message: body.Message, CreatedAt: time.Now().Unix()}
	if err := store.AddEvent(c.Context(), e); errors.Is(err, errNotFound) {
		return zip.ErrNotFound("session not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist event: %v", err)
	}
	return c.JSON(http.StatusCreated, toEventView(e))
}

func listEvents(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	// Confirm the session is in this org before listing its events, so a
	// non-owned id 404s like every other :id route rather than leaking a 200.
	if _, err := store.Get(c.Context(), org, idParam(c)); errors.Is(err, errNotFound) {
		return zip.ErrNotFound("session not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	rows, err := store.ListEvents(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list events: %v", err)
	}
	out := make([]eventView, 0, len(rows))
	for _, e := range rows {
		out = append(out, toEventView(e))
	}
	return c.JSON(http.StatusOK, out)
}
