package link

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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
	"github.com/hanzoai/cloud/apps/principal"
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

	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its caller through the request it parks.
	app.Group("/v1/links").Use(cloud.Bridge())

	// Collection root stays flat: Group("/v1/links").Post("") would register
	// "/v1/links/", not the bare collection path.
	zip.Post(z, "/v1/links", o.upsertLink, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/links", o.listLinks)

	g := app.Group("/v1/links")
	// Static literals before the :id param — Fiber matches in registration order,
	// so "route"/"devices"/"usage" must win over :id.
	zip.Get(z, "/v1/links/route", o.routePlan)
	// reportUsage stays an untyped handler: its body is one-or-many, built on an
	// EMBEDDED sampleReq that Go inlines but the schema generator does not, so any
	// single declared schema would state a nested object the wire never carries.
	// An undeclared route is honest; a wrongly-declared one poisons every consumer.
	g.Post("/usage", cloud.Handle(s, reportUsage))
	zip.Get(z, "/v1/links/usage/summary", o.usageSummary)
	// The per-account SERVER-ROUTED breakdown (usage_accounts.go). Static, so it must
	// register before the "/usage" catch and the ":id" param.
	zip.Get(z, "/v1/links/usage/accounts", o.usageAccounts)
	zip.Get(z, "/v1/links/usage", o.usageDash)
	zip.Get(z, "/v1/links/devices/:machine", o.deviceDetail)
	zip.Post(z, "/v1/links/devices/:machine/revoke", o.revokeDevice)
	zip.Get(z, "/v1/links/:id", o.getLink)
	zip.Delete(z, "/v1/links/:id", o.revokeLink)

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

type enrollReq struct {
	Machine  string          `json:"machine"`
	Host     string          `json:"host"`
	OS       string          `json:"os"`
	Provider string          `json:"provider"`
	Account  string          `json:"account"`
	Plan     string          `json:"plan"`
	Kind     string          `json:"kind"`
	Usage    json.RawMessage `json:"usage"`
}

// ops binds the link state to the typed handlers. A zip TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so the
// service arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[state] }

// begin resolves the caller's org and subject in ONE place. Neither ever comes
// from an In field — an In field is caller-supplied, so an identity read from one
// is a cross-tenant read the caller asserted for itself. Both come from the
// request cloud.Bridge parked; off the HTTP path there is none, so the op refuses.
func (o ops) begin(ctx context.Context) (org, user string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	org, user, ok = caller(c)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, user, nil
}

// upsertLink registers or refreshes one machine's provider account for the caller.
// Machine and provider identify the link, so reporting the same pair again updates
// the existing row rather than adding a second one.
//
// Example: {"machine": "m_7f31", "host": "spark", "os": "linux", "provider": "anthropic", "account": "z@hanzo.ai", "plan": "max", "kind": "subscription"}
func (o ops) upsertLink(ctx context.Context, in *enrollReq) (*linkView, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	body := *in
	machine := trim(body.Machine)
	if machine == "" {
		return nil, zip.ErrBadRequest("machine is required")
	}
	provider := trim(body.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	if len(machine) > maxMachine || len(provider) > maxProvider ||
		len(trim(body.Host)) > maxHost || len(trim(body.OS)) > maxOS ||
		len(trim(body.Account)) > maxAccount || len(trim(body.Plan)) > maxPlan {
		return nil, zip.ErrBadRequest("field too long")
	}
	kind := trim(body.Kind)
	if kind == "" {
		kind = KindSubscription
	}
	if !validKind(kind) {
		return nil, zip.ErrBadRequest("kind must be subscription or apikey")
	}
	usageJSON, err := normalizeUsage(body.Usage)
	if err != nil {
		return nil, err
	}
	id, err := genID("link")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	l := Link{
		ID: id, Org: org, User: user, Machine: machine, Host: trim(body.Host), OS: trim(body.OS),
		Provider: provider, Account: trim(body.Account), Plan: trim(body.Plan),
		Kind: kind, Status: StatusLinked, LastSeen: now, Usage: usageJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	stored, err := s.State.store.Upsert(ctx, l)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	out := toLinkView(stored)
	return &out, nil
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

// linkList is the caller's links, both flat and grouped by the machine they run on.
type linkList struct {
	// Links are every account link the caller has, newest first.
	Links []linkView `json:"links"`
	// Devices are the same links grouped by machine, one entry per device.
	Devices []deviceView `json:"devices"`
}

// listLinks lists the caller's own account links, flat and grouped by device.
// It answers only for the calling subject in the calling org — another user's
// links are never included.
//
// Response: {"links": [{"id": "link_4c1e", "user": "z@hanzo.ai", "machine": "m_7f31", "host": "spark", "provider": "anthropic", "kind": "subscription", "billing": "plan", "status": "linked", "createdAt": "2026-07-29T11:00:00Z", "updatedAt": "2026-07-29T11:00:00Z"}], "devices": []}
func (o ops) listLinks(ctx context.Context, _ *struct{}) (*linkList, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	links, err := o.s.State.store.List(ctx, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	views := make([]linkView, 0, len(links))
	for _, l := range links {
		views = append(views, toLinkView(l))
	}
	return &linkList{Links: views, Devices: devicesOf(links)}, nil
}

// linkRef addresses one link by id.
type linkRef struct {
	// ID is the link id from the path.
	ID string `json:"id"`
}

// getLink reads one of the caller's own account links. A link belonging to another
// user or org is reported not-found, so the id space leaks no existence.
//
// Example: {"id": "link_4c1e9b7a2d6f0538"}
func (o ops) getLink(ctx context.Context, in *linkRef) (*linkView, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	l, err := o.s.State.store.Get(ctx, org, user, trim(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("link not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	out := toLinkView(l)
	return &out, nil
}

// deviceRef addresses one device by its machine id.
type deviceRef struct {
	// Machine is the machine id from the path.
	Machine string `json:"machine"`
}

// deviceDetail reads one of the caller's devices: every provider account linked on
// that machine, plus how many sessions the caller currently runs there.
//
// Example: {"machine": "m_7f31"}
func (o ops) deviceDetail(ctx context.Context, in *deviceRef) (*deviceView, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	machine := trim(in.Machine)
	accounts, err := o.s.State.store.ListDevice(ctx, org, user, machine)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "device: %v", err)
	}
	if len(accounts) == 0 {
		return nil, zip.ErrNotFound("device not found")
	}
	d := deviceView{Machine: machine, Host: accounts[0].Host, OS: accounts[0].OS, LastSeen: rfc3339(accounts[0].LastSeen)}
	for _, a := range accounts {
		d.Accounts = append(d.Accounts, toLinkView(a))
	}
	d.ActiveSessions = countActive(o.s, ctx, org, SessionMatch{Subject: user, Host: d.Host})
	return &d, nil
}

// routePlan ranks the caller's linked accounts into a redundancy plan. It says which
// one to send the next request to and what to fall back to: subscription accounts
// come before metered API keys, and within a group the most headroom wins.
//
// Response: {"candidates": [{"provider": "anthropic", "kind": "subscription", "billing": "plan", "available": true, "headroomPct": 53, "linkId": "link_4c1e"}], "generatedAt": "2026-07-29T11:00:00Z"}
func (o ops) routePlan(ctx context.Context, _ *struct{}) (*RoutePlan, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	linked, err := o.s.State.store.ListLinked(ctx, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "route: %v", err)
	}
	plan := Plan(linked, time.Now())
	return &plan, nil
}

type revokeResp struct {
	Revoked         int        `json:"revoked"`
	SessionsStopped int        `json:"sessionsStopped"`
	Links           []linkView `json:"links,omitempty"`
}

// revokeLink revokes one account link and stops the sessions running on it. The row
// is kept and marked revoked so the history stays inspectable, and a link of another
// user or org is reported not-found.
//
// Example: {"id": "link_4c1e9b7a2d6f0538"}
func (o ops) revokeLink(ctx context.Context, in *linkRef) (*revokeResp, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	l, found, err := s.State.store.Revoke(ctx, org, user, trim(in.ID), time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "revoke: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("link not found")
	}
	stopped := stopSessions(s, ctx, org, SessionMatch{Subject: user, Host: l.Host, Provider: l.Provider, Account: l.Account})
	return &revokeResp{Revoked: 1, SessionsStopped: stopped, Links: []linkView{toLinkView(l)}}, nil
}

// revokeDevice revokes every account the caller linked on one machine. It stops the
// sessions they were running there and is the "I lost this laptop" verb, and other
// users' links on the same machine are untouched.
//
// Example: {"machine": "m_7f31"}
func (o ops) revokeDevice(ctx context.Context, in *deviceRef) (*revokeResp, error) {
	org, user, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	revoked, err := s.State.store.RevokeDevice(ctx, org, user, trim(in.Machine), time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "revoke device: %v", err)
	}
	if len(revoked) == 0 {
		return nil, zip.ErrNotFound("device not found or already revoked")
	}
	// Stop every session THIS USER ran on the device (all their accounts).
	stopped := stopSessions(s, ctx, org, SessionMatch{Subject: user, Host: revoked[0].Host})
	views := make([]linkView, 0, len(revoked))
	for _, l := range revoked {
		views = append(views, toLinkView(l))
	}
	return &revokeResp{Revoked: len(revoked), SessionsStopped: stopped, Links: views}, nil
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
