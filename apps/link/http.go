package link

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	luxlog "github.com/luxfi/log"

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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each typed op can be a method value — the
// only bound form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// scope is the (org, subject) pair every op on this surface acts in, resolved
// from the request cloud.Bridge parked on the context — the SAME caller() gate
// the raw handlers used, so both planes key one boundary. The subject (c.User())
// is why the request is reached at all: principal parks the org, not the user.
//
// Off the HTTP path (a CLI invoke with no request) there is no attested caller,
// so it refuses with exactly the 403 an anonymous REST call gets. Fail closed
// with one gate, not two.
func scope(ctx context.Context) (org, user string, err error) {
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
	log := luxlog.Default().New("subsystem", "link")
	if deps.DataDir == "" {
		return fmt.Errorf("link.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("link.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "link"),
		State: state{store: store, sessions: sessionAdapter{}},
	}
	mounted = s

	// Every route is a TYPED op — the one registration REST, OpenAPI, the MCP tool
	// list, the CLI and the by-name call plane all project from. Declared on the
	// registry with FULL paths (a group leaf of "" would publish "/v1/links/", a
	// path this API has never served). cloud.Bridge — what carries the validated
	// request to a typed op — is the composer's install, once at the root of every
	// program, so this package does not install its own.
	r := cloud.ZipApp(app)
	if r == nil {
		return fmt.Errorf("link.Mount: router carries no typed-op registry")
	}
	o := ops{s: s}
	zip.Post(r, "/v1/links", o.upsertLink, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/links", o.listLinks)
	// Static literals before the :id param — Fiber matches in registration order,
	// so "route"/"devices"/"usage" must win over :id.
	zip.Get(r, "/v1/links/route", o.routePlan)
	// The account-usage plane (usage.go): report samples, one account's own dash,
	// and the global view across every account + Hanzo-routed usage.
	zip.Post(r, "/v1/links/usage", o.reportUsage, zip.WithStatus(http.StatusAccepted))
	zip.Get(r, "/v1/links/usage/summary", o.usageSummary)
	// The per-account SERVER-ROUTED breakdown (usage_accounts.go). Static, so it must
	// register before the "/usage" catch and the ":id" param.
	zip.Get(r, "/v1/links/usage/accounts", o.usageAccounts)
	zip.Get(r, "/v1/links/usage", o.usageDash)
	zip.Get(r, "/v1/links/devices/:machine", o.deviceDetail)
	zip.Post(r, "/v1/links/devices/:machine/revoke", o.revokeDevice)
	zip.Get(r, "/v1/links/:id", o.getLink)
	zip.Delete(r, "/v1/links/:id", o.revokeLink)

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

// linkView is one linked account as the console renders it: metadata and the
// latest usage snapshot, never a secret — the provider's OAuth token or API key
// stays on the device.
type linkView struct {
	// ID is the link's opaque handle ("link_" + 32 hex chars).
	ID string `json:"id"`
	// User is the owning subject — the validated caller who registered the link.
	User string `json:"user"`
	// Machine is the stable machine identifier the collector reports.
	Machine string `json:"machine"`
	// Host is the machine's human hostname label, from its most recent report.
	Host string `json:"host,omitempty"`
	// OS is the machine's operating system label.
	OS string `json:"os,omitempty"`
	// Provider is the AI provider this account belongs to (claude, openai, hanzo…).
	Provider string `json:"provider"`
	// Account is the provider-side account identifier, when the collector knows it.
	Account string `json:"account,omitempty"`
	// Plan is the provider plan label (e.g. "Claude Max").
	Plan string `json:"plan,omitempty"`
	// Kind is how the account authenticates: subscription or apikey.
	Kind string `json:"kind"`
	// Billing is how this account's inference bills — plan (the user's own
	// subscription, metered here for visibility only) or commerce (the gateway
	// path). Derived from Kind, never stored.
	Billing string `json:"billing"`
	// Status is linked or revoked. Revoked rows are retained for history.
	Status string `json:"status"`
	// LastSeen is when the account last reported, RFC 3339 UTC.
	LastSeen string `json:"lastSeen,omitempty"`
	// Usage is the last good usage snapshot, clamped and re-serialized to known
	// fields at ingest.
	Usage json.RawMessage `json:"usage,omitempty"`
	// CreatedAt is when the link was first registered, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the link was last refreshed, RFC 3339 UTC.
	UpdatedAt string `json:"updatedAt"`
}

// deviceView is one machine as a PROJECTION of its links — there is no device
// row to create and none to garbage-collect; its labels come from its
// most-recently-seen account.
type deviceView struct {
	// Machine is the stable machine identifier.
	Machine string `json:"machine"`
	// Host is the machine's hostname label, from its most-recently-seen account.
	Host string `json:"host,omitempty"`
	// OS is the machine's operating system label.
	OS string `json:"os,omitempty"`
	// LastSeen is when any account on this machine last reported, RFC 3339 UTC.
	LastSeen string `json:"lastSeen,omitempty"`
	// Accounts is every account the caller has signed in on this machine.
	Accounts []linkView `json:"accounts"`
	// ActiveSessions is how many agent sessions the caller currently has running
	// on this machine; 0 where the agent plane is not mounted.
	ActiveSessions int `json:"activeSessions"`
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

// enrollReq records one signed-in provider account on one machine. NO SECRET
// IS SENT OR STORED — the provider's OAuth token or API key stays on the device,
// and the caller authenticates with their own Hanzo bearer.
type enrollReq struct {
	// Machine is the stable machine identifier. Required, length-bounded.
	Machine string `json:"machine"`
	// Host is the machine's human hostname label.
	Host string `json:"host"`
	// OS is the machine's operating system label.
	OS string `json:"os"`
	// Provider is the AI provider the account belongs to. Required, length-bounded.
	Provider string `json:"provider"`
	// Account is the provider-side account identifier.
	Account string `json:"account"`
	// Plan is the provider plan label (e.g. "Claude Max").
	Plan string `json:"plan"`
	// Kind decides how the account's inference BILLS and defaults to
	// subscription: a subscription account bills the user's own monthly plan and
	// is metered here for visibility only, while an apikey account bills through
	// commerce on the gateway path.
	Kind string `json:"kind"`
	// Usage is an optional usage snapshot, clamped and re-serialized to known
	// fields; omitting it keeps the last good one.
	Usage json.RawMessage `json:"usage"`
}

// UpsertLink registers a signed-in AI provider account on a machine.
//
// It records that a developer has signed into one provider account on one
// machine — a Claude Max or ChatGPT Plus subscription, a Hanzo key, a raw
// provider key — and answers 201 with the stored link. Re-reporting the same
// (machine, provider, account) UPDATES that link rather than creating a second,
// so a collector may call this on every heartbeat. machine and provider are
// required (400 otherwise), as is a valid kind, and every field is
// length-bounded. Scoped to the caller: a validated principal and a non-empty
// org, else 403, so a caller writes only their OWN accounts within their own org.
func (o ops) upsertLink(ctx context.Context, in *enrollReq) (*linkView, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	machine := trim(in.Machine)
	if machine == "" {
		return nil, zip.ErrBadRequest("machine is required")
	}
	provider := trim(in.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	if len(machine) > maxMachine || len(provider) > maxProvider ||
		len(trim(in.Host)) > maxHost || len(trim(in.OS)) > maxOS ||
		len(trim(in.Account)) > maxAccount || len(trim(in.Plan)) > maxPlan {
		return nil, zip.ErrBadRequest("field too long")
	}
	kind := trim(in.Kind)
	if kind == "" {
		kind = KindSubscription
	}
	if !validKind(kind) {
		return nil, zip.ErrBadRequest("kind must be subscription or apikey")
	}
	usageJSON, err := normalizeUsage(in.Usage)
	if err != nil {
		return nil, err
	}
	id, err := genID("link")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	l := Link{
		ID: id, Org: org, User: user, Machine: machine, Host: trim(in.Host), OS: trim(in.OS),
		Provider: provider, Account: trim(in.Account), Plan: trim(in.Plan),
		Kind: kind, Status: StatusLinked, LastSeen: now, Usage: usageJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	stored, err := o.s.State.store.Upsert(ctx, l)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	v := toLinkView(stored)
	return &v, nil
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

// linkList is the caller's links plus their per-machine device projection.
type linkList struct {
	// Devices is the same rows folded per machine — the cross-machine "AI
	// Providers / Accounts" view.
	Devices []deviceView `json:"devices"`
	// Links is every link the caller registered, newest first. Revoked links are
	// INCLUDED rather than dropped, because a logged-out account keeps its usage
	// history and audit trail.
	Links []linkView `json:"links"`
}

// ListLinks lists your linked accounts and the devices they sit on.
//
// It answers the caller's own links plus a devices projection of the same rows
// folded per machine — the cross-machine "AI Providers / Accounts" view. A
// device is a projection, not a stored entity: its labels come from its
// most-recently-seen account, so there is no device to create and none to
// garbage-collect. Revoked links are INCLUDED rather than dropped, because a
// logged-out account keeps its usage history and audit trail. Scoped to the
// caller: a validated principal and a non-empty org, else 403.
func (o ops) listLinks(ctx context.Context, _ *noIn) (*linkList, error) {
	org, user, err := scope(ctx)
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

// noIn is the input of an op that takes nothing off the request: the whole read is
// scoped by the validated caller.
type noIn struct{}

// linkRef addresses ONE link by its opaque id.
type linkRef struct {
	// ID is the link to act on, from the path. It is scoped to the caller, so
	// another user's or org's id is a 404.
	ID string `json:"id"`
}

// machineRef addresses ONE machine by its stable identifier.
type machineRef struct {
	// Machine is the machine to act on, from the path. It is scoped to the
	// caller, so a machine with none of the caller's accounts is a 404.
	Machine string `json:"machine"`
}

// GetLink reads one linked account.
//
// It answers a single link — its device, provider, account, plan, how it bills,
// its status and its latest usage snapshot. An id that does not exist, or
// belongs to another user or org, is the same 404: the scope is a bound
// predicate on the read, so a wrong id and a foreign id are indistinguishable
// and neither confirms the other's existence. The static paths on this
// collection — route, usage, devices — register before this one and win
// first-match, so a link whose id collided with one of those words could not be
// addressed here.
func (o ops) getLink(ctx context.Context, in *linkRef) (*linkView, error) {
	org, user, err := scope(ctx)
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
	v := toLinkView(l)
	return &v, nil
}

// DeviceDetail shows one machine: its accounts, usage and live sessions.
//
// It answers one device — its host and OS labels, every account the caller has
// signed in on that machine with its latest usage, and how many agent sessions
// the caller currently has running on it. The device labels come from the
// most-recently-seen account, since a device is a projection of its links rather
// than a row of its own. A machine with none of the caller's accounts is 404,
// which is also the answer when the machine belongs to someone else — the scope
// makes the two indistinguishable, deliberately. The session count reports 0
// where the agent plane is not mounted rather than failing the read.
func (o ops) deviceDetail(ctx context.Context, in *machineRef) (*deviceView, error) {
	org, user, err := scope(ctx)
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

// RoutePlan gets the failover order across your linked accounts.
//
// It answers an ordered redundancy plan over the caller's LINKED (not revoked)
// accounts: each candidate with its remaining rate-limit headroom, whether it is
// routable right now, how it BILLS (plan or commerce), and a reason when it is
// not — plus the primary to try first. It is what lets a router fail over from
// one subscription to another and fall back to the metered API as the
// always-available backstop, knowing the cost consequence before it dials.
//
// It is POLICY, not execution: the plan is computed purely from the usage
// snapshots already in the registry, never by probing a provider, so it is a
// total function of the links and costs nothing to ask for. Actually dialing,
// detecting a live 429 and advancing to the next candidate belongs to the
// caller. A link with no snapshot counts as full headroom.
func (o ops) routePlan(ctx context.Context, _ *noIn) (*RoutePlan, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	linked, err := o.s.State.store.ListLinked(ctx, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "route: %v", err)
	}
	p := Plan(linked, time.Now())
	return &p, nil
}

// revokeResp reports one revoke: how many links were logged out and how many of
// the caller's sessions stopped with them.
type revokeResp struct {
	// Revoked is how many links this call revoked.
	Revoked int `json:"revoked"`
	// SessionsStopped is how many of the caller's own agent sessions stopped. A
	// stop that fails does not fail the revoke, so this may honestly report fewer.
	SessionsStopped int `json:"sessionsStopped"`
	// Links is each revoked row with its new status — retained, not deleted, so
	// usage history and the audit trail survive the log-out.
	Links []linkView `json:"links,omitempty"`
}

// RevokeLink logs out one account and stops the sessions it was running.
//
// It revokes a single linked account and stops the agent sessions that ran under
// it, answering with the revoked row and how many sessions stopped. The link is
// RETAINED with a revoked status rather than deleted, so its usage history and
// the audit trail survive the log-out — which also means a revoked account still
// appears in the list, and is excluded from the route plan rather than absent
// from it. The session stop is narrowed to the revoking user's own sessions on
// that device, provider and account, and a stop that fails does not fail the
// revoke: the revoked row is the durable truth. An id that does not exist, or
// belongs to another user or org, is the same 404.
func (o ops) revokeLink(ctx context.Context, in *linkRef) (*revokeResp, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	l, found, err := o.s.State.store.Revoke(ctx, org, user, trim(in.ID), time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "revoke: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("link not found")
	}
	stopped := stopSessions(o.s, ctx, org, SessionMatch{Subject: user, Host: l.Host, Provider: l.Provider, Account: l.Account})
	return &revokeResp{Revoked: 1, SessionsStopped: stopped, Links: []linkView{toLinkView(l)}}, nil
}

// RevokeDevice logs out every account on one machine and stops its sessions.
//
// It revokes every one of the caller's accounts on one machine and stops the
// agent sessions they were running, answering with how many of each. This is the
// "I lost that laptop" button. Revoked links are RETAINED, not deleted, so usage
// history and the audit trail survive a log-out — the rows come back in the
// response with their new status. The session stop reaches only the REVOKING
// user's own sessions, so a shared machine name can never be used to stop a
// co-tenant's work, and a stop that fails does not fail the revoke: the revoked
// row is the durable truth and the count then honestly reports fewer. A machine
// with nothing left to revoke is 404.
func (o ops) revokeDevice(ctx context.Context, in *machineRef) (*revokeResp, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	revoked, err := o.s.State.store.RevokeDevice(ctx, org, user, trim(in.Machine), time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "revoke device: %v", err)
	}
	if len(revoked) == 0 {
		return nil, zip.ErrNotFound("device not found or already revoked")
	}
	// Stop every session THIS USER ran on the device (all their accounts).
	stopped := stopSessions(o.s, ctx, org, SessionMatch{Subject: user, Host: revoked[0].Host})
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
