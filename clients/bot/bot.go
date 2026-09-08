// Package bot mounts the Hanzo Cloud /v1/bot surface: the gateway a Hanzo Bot
// announces itself to, and the registry a console reads to say which bots are
// live.
//
// A bot is a loop. It runs either on someone's own machine or in a sandbox
// this cloud placed it in, and either way it says hello here, keeps saying so,
// and is listed until it stops. The gateway holds no conversation and drives
// no model: it knows that a bot exists, where to reach it, what it is allowed
// to drive, and whether it is running, suspended or gone.
//
// Suspension is the reason this is a registry rather than a liveness ping. A
// bot that suspends hands back a resume token — opaque here — and the row
// survives so the same bot can come back, on the same host or another one.
// The gateway never reads the token; it holds it.
//
//	POST   /v1/bot                     announce a bot                -> Bot (201)
//	GET    /v1/bot[?live&status=&where=&edition=&host=&project=]  list -> [Bot]
//	GET    /v1/bot/:id                 one bot
//	PATCH  /v1/bot/:id                 heartbeat, status, url, model -> Bot
//	DELETE /v1/bot/:id                 forget a bot
//	POST   /v1/bot/:id/suspend         suspend, carrying a resume token -> Bot
//	POST   /v1/bot/:id/resume          resume a suspended bot         -> Bot
//	POST   /v1/bot/:id/events          record something it reported  -> Event (201)
//	GET    /v1/bot/:id/events          what it has reported          -> [Event]
package bot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	maxField  = 256   // name, host, model, user, project
	maxURL    = 2048  // where a bot publishes itself
	maxMsg    = 16384 // an event message
	maxResume = 65536 // a resume token, opaque and held not read
	maxEvents = 500   // the most events one list returns
)

// idRE constrains a client-minted bot id to a URL-safe token, so a launcher
// can mint one locally and refer to the bot before the announce round-trip
// returns. It is the boundary guard on the :id path segment.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// wheres is where a bot runs. Local means hardware this cloud does not own:
// it announces outward and only it can stop itself. Cloud means a sandbox this
// cloud placed it in, which is what makes suspend something the cloud may do
// rather than merely record.
var wheres = map[string]bool{"local": true, "cloud": true}

// editions is what the loop is allowed to drive, which is the boundary the
// sandbox is built to.
var editions = map[string]bool{"plain": true, "computer": true, "browser": true}

// statuses is the lifecycle. suspended is a resting state, not an end: a
// suspended bot holds a resume token and is expected back.
var statuses = map[string]bool{
	"starting": true, "running": true, "waiting": true,
	"suspended": true, "ended": true, "error": true,
}

// kinds is what a bot may report.
var kinds = map[string]bool{
	"notification": true, "log": true, "error": true,
	"suspend": true, "resume": true,
}

type state struct {
	stores *cloud.OrgStore[*Store] // one bot.db per org, opened once each
}

var mounted *cloud.Service[state]

// Mount wires /v1/bot onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return errors.New("bot.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return errors.New("bot.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return errors.New("bot.Mount: empty DataDir")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "bot"), State: state{
		stores: cloud.NewOrgStore(deps.DataDir, "bot", openStore),
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("bot gateway mounted", "brand", s.Brand)
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

// routes registers the surface. The collection registers before its :id
// siblings so the first-match scan resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1")
	g.Post("/bot", cloud.Handle(s, announce))
	g.Get("/bot", cloud.Handle(s, list))
	g.Get("/bot/:id", cloud.Handle(s, get))
	g.Patch("/bot/:id", cloud.Handle(s, update))
	g.Delete("/bot/:id", cloud.Handle(s, forget))
	g.Post("/bot/:id/suspend", cloud.Handle(s, suspend))
	g.Post("/bot/:id/resume", cloud.Handle(s, resume))
	g.Post("/bot/:id/events", cloud.Handle(s, addEvent))
	g.Get("/bot/:id/events", cloud.Handle(s, listEvents))
}

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	return s.State.stores.For(org, "")
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

func genID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "bot_" + hex.EncodeToString(b[:])
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- HTTP shapes (the published contract) ----

type announceReq struct {
	ID      string `json:"id"`      // client-minted; generated if empty
	Name    string `json:"name"`    // what to call it in a list
	Where   string `json:"where"`   // local | cloud
	Edition string `json:"edition"` // plain | computer | browser
	Model   string `json:"model"`
	Host    string `json:"host"`
	Project string `json:"project"`
	User    string `json:"user"`
	URL     string `json:"url"` // set now if the bot is already reachable
}

type updateReq struct {
	Status string `json:"status"`
	URL    string `json:"url"`
	Model  string `json:"model"`
}

type suspendReq struct {
	Resume  string `json:"resume"`  // opaque; held so the bot can come back
	Message string `json:"message"` // why, for the event log
}

type resumeReq struct {
	Host string `json:"host"` // where it came back, if it moved
	URL  string `json:"url"`
}

type eventReq struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// view is the wire shape of a Bot. Resume is deliberately absent: it is the
// bot's to hold and the gateway's to keep, and a list of bots is not the place
// to hand it out.
type view struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Where       string `json:"where"`
	Edition     string `json:"edition"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	URL         string `json:"url,omitempty"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	SuspendedAt int64  `json:"suspendedAt,omitempty"`
	EndedAt     int64  `json:"endedAt,omitempty"`
}

func viewOf(x Bot) view {
	return view{
		ID: x.ID, Name: x.Name, Where: x.Where, Edition: x.Edition,
		Model: x.Model, Host: x.Host, Project: x.Project, User: x.User,
		URL: x.URL, Status: x.Status,
		StartedAt: x.StartedAt, UpdatedAt: x.UpdatedAt,
		SuspendedAt: x.SuspendedAt, EndedAt: x.EndedAt,
	}
}

// resumeView is what a resume returns: the bot as everyone sees it, plus the
// token, which leaves through this one door and no other.
type resumeView struct {
	Bot    view   `json:"bot"`
	Resume string `json:"resume,omitempty"`
}

type eventView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// ---- handlers ----

func announce(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req announceReq
	if err := c.Bind(&req); err != nil {
		return err
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = genID()
	} else if !idRE.MatchString(id) {
		return zip.ErrBadRequest("bad id")
	}
	where := strings.TrimSpace(req.Where)
	if !wheres[where] {
		return zip.ErrBadRequest("where must be local or cloud")
	}
	edition := strings.TrimSpace(req.Edition)
	if edition == "" {
		edition = "plain"
	}
	if !editions[edition] {
		return zip.ErrBadRequest("edition must be plain, computer or browser")
	}

	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UnixMilli()
	x := Bot{
		ID: id, Org: o,
		Project: clip(req.Project, maxField),
		User:    clip(req.User, maxField),
		Name:    clip(req.Name, maxField),
		Where:   where, Edition: edition,
		Model:     clip(req.Model, maxField),
		Host:      clip(req.Host, maxField),
		URL:       clip(req.URL, maxURL),
		Status:    "starting",
		StartedAt: now, UpdatedAt: now,
	}
	if err := st.Create(c.Context(), x); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, viewOf(x))
}

func list(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	f := Filter{
		Status:  clip(c.Query("status"), maxField),
		Where:   clip(c.Query("where"), maxField),
		Edition: clip(c.Query("edition"), maxField),
		Host:    clip(c.Query("host"), maxField),
		Project: clip(c.Query("project"), maxField),
		Live:    c.Query("live") != "",
	}
	xs, err := st.List(c.Context(), o, f)
	if err != nil {
		return err
	}
	out := make([]view, 0, len(xs))
	for _, x := range xs {
		out = append(out, viewOf(x))
	}
	return c.JSON(http.StatusOK, out)
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	x, err := st.Get(c.Context(), o, idParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(x))
}

// update is the heartbeat. A bot that says nothing but its own name still
// moves updated_at, which is how "live" is answered without holding a
// connection open.
func update(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	status := strings.TrimSpace(req.Status)
	if status != "" && !statuses[status] {
		return zip.ErrBadRequest("unknown status")
	}
	// Suspension and resumption are their own doors, because each has to write
	// more than a status: a token in one direction, a clearing in the other.
	if status == "suspended" {
		return zip.ErrBadRequest("use POST /v1/bot/:id/suspend")
	}

	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UnixMilli()
	patch := Bot{
		Status: status, URL: clip(req.URL, maxURL), Model: clip(req.Model, maxField),
		UpdatedAt: now,
	}
	if status == "ended" || status == "error" {
		patch.EndedAt = now
	}
	x, err := st.Update(c.Context(), o, idParam(c), patch)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(x))
}

func forget(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	err = st.Delete(c.Context(), o, idParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	}
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// suspend parks a bot. The resume token is whatever the bot needs to come back
// as itself; the gateway holds it without reading it, so that what a bot
// checkpoints is the bot's business and not this package's.
func suspend(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req suspendReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := idParam(c)
	cur, err := st.Get(c.Context(), o, id)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	}
	if err != nil {
		return err
	}
	if cur.Status == "ended" || cur.Status == "error" {
		return zip.Errorf(http.StatusConflict, "bot has stopped")
	}

	now := time.Now().UnixMilli()
	x, err := st.Update(c.Context(), o, id, Bot{
		Status: "suspended", Resume: clip(req.Resume, maxResume),
		SuspendedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	_ = st.AddEvent(c.Context(), Event{
		ID: genID(), BotID: id, Org: o, Kind: "suspend",
		Message: clip(req.Message, maxMsg), CreatedAt: now,
	})
	return c.JSON(http.StatusOK, viewOf(x))
}

// resume hands a suspended bot back its token and marks it running. The token
// leaves through this one door and no other, which is why it is not on view.
func resume(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req resumeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := idParam(c)
	cur, err := st.Get(c.Context(), o, id)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	}
	if err != nil {
		return err
	}
	if cur.Status != "suspended" {
		return zip.Errorf(http.StatusConflict, "bot is not suspended")
	}

	now := time.Now().UnixMilli()
	x, err := st.Update(c.Context(), o, id, Bot{
		Status: "running", URL: clip(req.URL, maxURL), UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	_ = st.AddEvent(c.Context(), Event{
		ID: genID(), BotID: id, Org: o, Kind: "resume", CreatedAt: now,
	})
	return c.JSON(http.StatusOK, resumeView{Bot: viewOf(x), Resume: cur.Resume})
}

func addEvent(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req eventReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	kind := strings.TrimSpace(req.Kind)
	if !kinds[kind] {
		return zip.ErrBadRequest("unknown kind")
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := idParam(c)
	if _, err := st.Get(c.Context(), o, id); errors.Is(err, errNotFound) {
		return zip.ErrNotFound("bot not found")
	} else if err != nil {
		return err
	}
	e := Event{
		ID: genID(), BotID: id, Org: o, Kind: kind,
		Message: clip(req.Message, maxMsg), CreatedAt: time.Now().UnixMilli(),
	}
	if err := st.AddEvent(c.Context(), e); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, eventView{
		ID: e.ID, Kind: e.Kind, Message: e.Message, CreatedAt: e.CreatedAt,
	})
}

func listEvents(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	limit := maxEvents
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n < maxEvents {
		limit = n
	}
	es, err := st.Events(c.Context(), o, idParam(c), limit)
	if err != nil {
		return err
	}
	out := make([]eventView, 0, len(es))
	for _, e := range es {
		out = append(out, eventView{ID: e.ID, Kind: e.Kind, Message: e.Message, CreatedAt: e.CreatedAt})
	}
	return c.JSON(http.StatusOK, out)
}
