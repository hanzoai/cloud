package link

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// This file mounts the login-manager registry under /v1/links — the cross-machine
// account view console renders and the collector reports into.
//
//	POST   /v1/links                          register/upsert a link (device+account+usage) -> Link
//	GET    /v1/links                          the caller's links + device projection -> {links,devices}
//	GET    /v1/links/route                    the redundancy route plan across the caller's accounts -> RoutePlan
//	GET    /v1/links/devices/:machine         one device: its accounts + usage + active sessions -> Device
//	POST   /v1/links/devices/:machine/revoke  revoke every account on a device + stop its sessions
//	GET    /v1/links/:id                       one link -> Link
//	DELETE /v1/links/:id                       revoke a link (log out) + stop its sessions
//
// Every route is org+user scoped through principal.Org (a validated principal AND
// a non-empty org) plus c.User() (the owning subject), so a caller sees and
// mutates only their OWN accounts — cross-tenant and cross-user access is refused
// fail-closed.

// SessionMatch selects the live sessions a revoke stops. Fields are ANDed with the
// org; an empty field is "any". A link revoke matches {Host,Provider,Account} (the
// device+account the sessions ran under); a device revoke matches {Host}. Subject is
// the REVOKING user; the adapter turns it into the session Actor so a stop only ever
// reaches that user's OWN sessions — Host/Provider/Account (attacker-set at link
// upsert) can then only narrow WITHIN them, never widen to a co-tenant's.
type SessionMatch struct {
	Subject  string
	Host     string
	Provider string
	Account  string
}

// Sessions is the seam to the agent-session control plane (clients/agents,
// in-process). Revoke stops the sessions that ran under a revoked account/device;
// the device view counts a machine's active sessions. A nil seam (unit test / no
// agents mounted) makes revoke skip the stop and the count report 0 — the registry
// truth (the revoked row) is unaffected.
type Sessions interface {
	Stop(ctx context.Context, org string, m SessionMatch) (int, error)
	CountActive(ctx context.Context, org string, m SessionMatch) (int, error)
}

type state struct {
	store    *Store
	sessions Sessions
}

var mounted *cloud.Service[state]

// Mount wires the /v1/links surface. The sessions seam is set from the agents
// in-process adapter (adapters.go) so a revoke can stop the affected sessions.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("link.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("link.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "link")
	if deps.DataDir == "" {
		return fmt.Errorf("link.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("link.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "link.db"))
	if err != nil {
		return fmt.Errorf("link.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "link"),
		State: state{store: store, sessions: sessionAdapter{}},
	}
	mounted = s

	// Collection root stays flat: Group("/v1/links").Post("") would register
	// "/v1/links/", not the bare collection path.
	app.Post("/v1/links", cloud.Handle(s, upsertLink))
	app.Get("/v1/links", cloud.Handle(s, listLinks))

	g := app.Group("/v1/links")
	// Static literals before the :id param — Fiber matches in registration order,
	// so "route"/"devices"/"usage" must win over :id.
	g.Get("/route", cloud.Handle(s, routePlan))
	// The account-usage plane (usage.go): report samples, one account's own dash,
	// and the global view across every account + Hanzo-routed usage.
	g.Post("/usage", cloud.Handle(s, reportUsage))
	g.Get("/usage/summary", cloud.Handle(s, usageSummary))
	// The per-account SERVER-ROUTED breakdown (usage_accounts.go). Static, so it must
	// register before the "/usage" catch and the ":id" param.
	g.Get("/usage/accounts", cloud.Handle(s, usageAccounts))
	g.Get("/usage", cloud.Handle(s, usageDash))
	g.Get("/devices/:machine", cloud.Handle(s, deviceDetail))
	g.Post("/devices/:machine/revoke", cloud.Handle(s, revokeDevice))
	g.Get("/:id", cloud.Handle(s, getLink))
	g.Delete("/:id", cloud.Handle(s, revokeLink))

	log.Info("link mounted", "brand", deps.Brand)
	return nil
}

// Shutdown closes the store. Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// caller resolves the (org, subject) scope for a request: a validated principal
// with a non-empty org. Every handler gates on it — an off-gateway forge with no
// validated user is refused. c.User() is guaranteed non-empty once principal.Org
// returns ok (Org composes Validated, which is c.User() != "").
func caller(c *zip.Ctx) (org, user string, ok bool) {
	org, ok = principal.Org(c)
	if !ok {
		return "", "", false
	}
	return org, trim(c.User()), true
}

// ---- views ----

type linkView struct {
	ID        string          `json:"id"`
	User      string          `json:"user"`
	Machine   string          `json:"machine"`
	Host      string          `json:"host,omitempty"`
	OS        string          `json:"os,omitempty"`
	Provider  string          `json:"provider"`
	Account   string          `json:"account,omitempty"`
	Plan      string          `json:"plan,omitempty"`
	Kind      string          `json:"kind"`
	Billing   string          `json:"billing"` // BillingMode(Kind) — how this account's usage bills
	Status    string          `json:"status"`
	LastSeen  string          `json:"lastSeen,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

type deviceView struct {
	Machine        string     `json:"machine"`
	Host           string     `json:"host,omitempty"`
	OS             string     `json:"os,omitempty"`
	LastSeen       string     `json:"lastSeen,omitempty"`
	Accounts       []linkView `json:"accounts"`
	ActiveSessions int        `json:"activeSessions"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func toLinkView(l Link) linkView {
	var u json.RawMessage
	if l.Usage != "" {
		u = json.RawMessage(l.Usage)
	}
	return linkView{
		ID: l.ID, User: l.User, Machine: l.Machine, Host: l.Host, OS: l.OS,
		Provider: l.Provider, Account: l.Account, Plan: l.Plan, Kind: l.Kind,
		Billing: BillingMode(l.Kind), Status: l.Status, LastSeen: rfc3339(l.LastSeen),
		Usage: u, CreatedAt: rfc3339(l.CreatedAt), UpdatedAt: rfc3339(l.UpdatedAt),
	}
}

// devicesOf folds a user's links into the per-machine device projection, newest
// device first. A device's labels come from its most-recently-seen account.
func devicesOf(links []Link) []deviceView {
	order := make([]string, 0)
	byMachine := map[string]*deviceView{}
	for _, l := range links {
		d, ok := byMachine[l.Machine]
		if !ok {
			d = &deviceView{Machine: l.Machine, Host: l.Host, OS: l.OS, LastSeen: rfc3339(l.LastSeen)}
			byMachine[l.Machine] = d
			order = append(order, l.Machine)
		}
		// links arrive newest-first, so the first account seen carries the freshest
		// device labels; keep them.
		d.Accounts = append(d.Accounts, toLinkView(l))
	}
	out := make([]deviceView, 0, len(order))
	for _, m := range order {
		out = append(out, *byMachine[m])
	}
	return out
}

// ---- handlers ----

type registerReq struct {
	Machine  string          `json:"machine"`
	Host     string          `json:"host"`
	OS       string          `json:"os"`
	Provider string          `json:"provider"`
	Account  string          `json:"account"`
	Plan     string          `json:"plan"`
	Kind     string          `json:"kind"`
	Usage    json.RawMessage `json:"usage"`
}

func upsertLink(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body registerReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	machine := trim(body.Machine)
	if machine == "" {
		return zip.ErrBadRequest("machine is required")
	}
	provider := trim(body.Provider)
	if provider == "" {
		return zip.ErrBadRequest("provider is required")
	}
	if len(machine) > maxMachine || len(provider) > maxProvider ||
		len(trim(body.Host)) > maxHost || len(trim(body.OS)) > maxOS ||
		len(trim(body.Account)) > maxAccount || len(trim(body.Plan)) > maxPlan {
		return zip.ErrBadRequest("field too long")
	}
	kind := trim(body.Kind)
	if kind == "" {
		kind = KindSubscription
	}
	if !validKind(kind) {
		return zip.ErrBadRequest("kind must be subscription or apikey")
	}
	usageJSON, err := normalizeUsage(body.Usage)
	if err != nil {
		return err
	}
	id, err := genID("link")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	l := Link{
		ID: id, Org: org, User: user, Machine: machine, Host: trim(body.Host), OS: trim(body.OS),
		Provider: provider, Account: trim(body.Account), Plan: trim(body.Plan),
		Kind: kind, Status: StatusLinked, LastSeen: now, Usage: usageJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	stored, err := s.State.store.Upsert(c.Context(), l)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toLinkView(stored))
}

// normalizeUsage parses, clamps, and re-marshals a usage snapshot so the stored
// blob is bounded, well-formed, and carries only known fields. An absent usage is
// "" (a heartbeat that keeps the last good snapshot). A malformed or oversized
// usage is a 400 — the collector owns a valid projection.
func normalizeUsage(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if len(raw) > maxUsage {
		return "", zip.ErrBadRequest("usage too large")
	}
	var u Usage
	if err := json.Unmarshal(raw, &u); err != nil {
		return "", zip.ErrBadRequest("usage must be a valid usage snapshot")
	}
	u.SessionPct = clampPct(u.SessionPct)
	u.WeeklyPct = clampPct(u.WeeklyPct)
	b, err := json.Marshal(u)
	if err != nil {
		return "", zip.ErrBadRequest("usage must be a valid usage snapshot")
	}
	return string(b), nil
}

func listLinks(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	links, err := s.State.store.List(c.Context(), org, user)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	views := make([]linkView, 0, len(links))
	for _, l := range links {
		views = append(views, toLinkView(l))
	}
	return c.JSON(http.StatusOK, map[string]any{"links": views, "devices": devicesOf(links)})
}

func getLink(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := trim(c.Param("id"))
	l, err := s.State.store.Get(c.Context(), org, user, id)
	if err == errNotFound {
		return zip.ErrNotFound("link not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return c.JSON(http.StatusOK, toLinkView(l))
}

func deviceDetail(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	machine := trim(c.Param("machine"))
	accounts, err := s.State.store.ListDevice(c.Context(), org, user, machine)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "device: %v", err)
	}
	if len(accounts) == 0 {
		return zip.ErrNotFound("device not found")
	}
	d := deviceView{Machine: machine, Host: accounts[0].Host, OS: accounts[0].OS, LastSeen: rfc3339(accounts[0].LastSeen)}
	for _, a := range accounts {
		d.Accounts = append(d.Accounts, toLinkView(a))
	}
	d.ActiveSessions = countActive(s, c.Context(), org, SessionMatch{Subject: user, Host: d.Host})
	return c.JSON(http.StatusOK, d)
}

func routePlan(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	linked, err := s.State.store.ListLinked(c.Context(), org, user)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "route: %v", err)
	}
	return c.JSON(http.StatusOK, Plan(linked, time.Now()))
}

type revokeResp struct {
	Revoked         int        `json:"revoked"`
	SessionsStopped int        `json:"sessionsStopped"`
	Links           []linkView `json:"links,omitempty"`
}

func revokeLink(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := trim(c.Param("id"))
	l, found, err := s.State.store.Revoke(c.Context(), org, user, id, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "revoke: %v", err)
	}
	if !found {
		return zip.ErrNotFound("link not found")
	}
	stopped := stopSessions(s, c.Context(), org, SessionMatch{Subject: user, Host: l.Host, Provider: l.Provider, Account: l.Account})
	return c.JSON(http.StatusOK, revokeResp{Revoked: 1, SessionsStopped: stopped, Links: []linkView{toLinkView(l)}})
}

func revokeDevice(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	machine := trim(c.Param("machine"))
	revoked, err := s.State.store.RevokeDevice(c.Context(), org, user, machine, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "revoke device: %v", err)
	}
	if len(revoked) == 0 {
		return zip.ErrNotFound("device not found or already revoked")
	}
	// Stop every session THIS USER ran on the device (all their accounts).
	stopped := stopSessions(s, c.Context(), org, SessionMatch{Subject: user, Host: revoked[0].Host})
	views := make([]linkView, 0, len(revoked))
	for _, l := range revoked {
		views = append(views, toLinkView(l))
	}
	return c.JSON(http.StatusOK, revokeResp{Revoked: len(revoked), SessionsStopped: stopped, Links: views})
}

// stopSessions forwards to the sessions seam, tolerating a nil seam (unit test /
// no agents) and a seam error (a stop failure must not fail the revoke — the row
// is already revoked, which is the durable truth). Returns how many stopped.
func stopSessions(s *cloud.Service[state], ctx context.Context, org string, m SessionMatch) int {
	if s.State.sessions == nil {
		return 0
	}
	n, err := s.State.sessions.Stop(ctx, org, m)
	if err != nil {
		s.Log.Warn("link: stop sessions failed", "org", org, "err", err)
		return 0
	}
	return n
}

func countActive(s *cloud.Service[state], ctx context.Context, org string, m SessionMatch) int {
	if s.State.sessions == nil {
		return 0
	}
	n, err := s.State.sessions.CountActive(ctx, org, m)
	if err != nil {
		return 0
	}
	return n
}

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
