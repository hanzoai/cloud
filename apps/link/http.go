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
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
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

// linkScope is the sentence every operation on this surface needs and none of
// them can state alone: one gate, one tenancy rule, applied by the same caller()
// in all eleven handlers. Written once so eleven descriptions cannot become
// eleven accounts of one predicate.
const linkScope = " Scoped to the caller: a validated principal and a non-empty " +
	"org, else 403. Both org and subject are bound predicates on every statement, " +
	"so a caller reads and writes only their OWN accounts within their own org — " +
	"the org is never a parameter, and there is no path to another user's links."

// The prose for this surface, all eleven operations of it. Every route here is an
// untyped handler, so there is no typed op for zipdoc to lift a doc comment from
// and the whole subsystem published eleven operationIds and nothing else: eleven
// SDK methods and eleven CLI commands that could not say what a link is, whose
// accounts they touch, or that a usage row is a plan percentage rather than
// money. Declared through the registry openapi.Register uses, so a description
// renders only while the router actually serves the route and this can never
// invent one.
//
// Keyed by the WHOLE fiber pattern the group and leaf compose — "/v1/links/route",
// not the "/route" the leaf is written as — because that is the path the router
// carries and the document renders.
func init() {
	openapi.Describe("/v1/links", http.MethodPost,
		"Register a signed-in AI provider account on a machine",
		"Records that a developer has signed into one provider account on one "+
			"machine — a Claude Max or ChatGPT Plus subscription, a Hanzo key, a raw "+
			"provider key — and answers with the stored link. Re-reporting the same "+
			"(machine, provider, account) UPDATES that link rather than creating a "+
			"second, so a collector may call this on every heartbeat.\n\n"+
			"NO SECRET IS SENT OR STORED. The provider's OAuth token or API key stays on "+
			"the device; this registry holds link metadata and usage snapshots only, and "+
			"the caller authenticates with their own Hanzo bearer. `kind` decides how the "+
			"account's inference BILLS and defaults to `subscription`: a subscription "+
			"account bills the user's own monthly plan and is metered here for visibility "+
			"only, while an `apikey` account bills through commerce on the gateway path. "+
			"An optional `usage` snapshot is clamped and re-serialized to known fields; "+
			"omitting it keeps the last good one.\n\n"+
			"`machine` and `provider` are required (400 otherwise), as is a valid kind, "+
			"and every field is length-bounded."+linkScope)

	openapi.Describe("/v1/links", http.MethodGet,
		"List your linked accounts and the devices they sit on",
		"Answers the caller's own links plus a `devices` projection of the same rows "+
			"folded per machine — the cross-machine \"AI Providers / Accounts\" view. A "+
			"device is a projection, not a stored entity: its labels come from its "+
			"most-recently-seen account, so there is no device to create and none to "+
			"garbage-collect. Revoked links are INCLUDED rather than dropped, because a "+
			"logged-out account keeps its usage history and audit trail."+linkScope)

	openapi.Describe("/v1/links/route", http.MethodGet,
		"Get the failover order across your linked accounts",
		"Answers an ordered redundancy plan over the caller's LINKED (not revoked) "+
			"accounts: each candidate with its remaining rate-limit headroom, whether it "+
			"is routable right now, how it BILLS (`plan` or `commerce`), and a reason "+
			"when it is not — plus the primary to try first. It is what lets a router "+
			"fail over from one subscription to another and fall back to the metered API "+
			"as the always-available backstop, knowing the cost consequence before it "+
			"dials.\n\n"+
			"It is POLICY, not execution: the plan is computed purely from the usage "+
			"snapshots already in the registry, never by probing a provider, so it is a "+
			"total function of the links and costs nothing to ask for. Actually dialing, "+
			"detecting a live 429 and advancing to the next candidate belongs to the "+
			"caller. A link with no snapshot counts as full headroom."+linkScope)

	openapi.Describe("/v1/links/usage", http.MethodPost,
		"Report usage samples from the device collector",
		"Ingests a batch of usage samples from the on-device collector and answers "+
			"with how many were accepted, whether history was durably stored, and the "+
			"links they refreshed. A report also REFRESHES one link per distinct "+
			"(machine, provider, account) it names, so a running collector keeps the "+
			"accounts overview current without a separate registration call.\n\n"+
			"A caller can only ever report for THEMSELVES: org and subject come from the "+
			"validated bearer, never from the body, so no sample can be attributed to "+
			"another user or tenant. History is FAIL-SOFT and `stored` says which "+
			"happened — a warehouse outage still accepts the report and refreshes the "+
			"links rather than failing the device, and answers 202 either way.\n\n"+
			"Send either one sample inline or up to 256 in `samples`; an empty batch or "+
			"an over-long one is 400, as is a provider, window class or kind outside the "+
			"closed vocabulary — an unrecognized window is refused rather than rewritten, "+
			"because a silently reclassified sample would fill a dashboard with a class "+
			"nobody reported."+linkScope)

	openapi.Describe("/v1/links/usage/summary", http.MethodGet,
		"See plan consumption and Hanzo spend side by side",
		"Answers the global usage board over one window: the caller's own linked "+
			"accounts, metered from each provider's own login, alongside their org's "+
			"Hanzo-routed inference. These come from different ledgers and mean different "+
			"things, so every row is LABELLED by source, by scope and by availability, "+
			"and THE TWO ARE NEVER SUMMED — a plan's percentage is not money, and a "+
			"provider's own spend is not a Hanzo charge. The rows sit side by side and "+
			"say what they are.\n\n"+
			"One resolver fixes the window for both halves, so the two sets always cover "+
			"the same period. `range` is one of 1h, 24h, 7d or 30d and defaults to 24h; "+
			"anything else is 400 rather than a silent substitution. A ledger that cannot "+
			"answer reports `available: false` instead of a zero that would read as \"no "+
			"usage\"."+linkScope)

	openapi.Describe("/v1/links/usage/accounts", http.MethodGet,
		"Break down what the gateway routed through each of your accounts",
		"Answers one row per linked account the GATEWAY actually routed through, plus "+
			"their total — requests, prompt and completion tokens, and cost. This is the "+
			"routed ledger, the read twin of the counter the router writes, and it is "+
			"distinct from both of its neighbours: not the device collector's plan "+
			"snapshots, and not the org money ledger. The `source` and `scope` fields on "+
			"the response say so on every payload.\n\n"+
			"The same shape answers in the billing namespace, from one shaping function, "+
			"so the two mounts cannot drift."+linkScope)

	openapi.Describe("/v1/links/usage", http.MethodGet,
		"See one provider account's own usage dashboard",
		"Answers the time series for a SINGLE provider account — the windows in range "+
			"plus the currently-open ones — as that provider's own meter reported it. "+
			"`provider` is required; `account` narrows to one account when a user has "+
			"several with the same provider; `window` selects a window class (6h, day, "+
			"week or month) and `range` the period (1h, 24h, 7d or 30d, default 24h). An "+
			"unknown window class or range is 400, never a quiet fallback to a different "+
			"one.\n\n"+
			"When no series is available the response is a 200 with `available: false` "+
			"and empty lists — an honest \"we have no data\", which is a different claim "+
			"from zero usage."+linkScope)

	openapi.Describe("/v1/links/devices/:machine", http.MethodGet,
		"See one machine: its accounts, usage and live sessions",
		"Answers one device — its host and OS labels, every account the caller has "+
			"signed in on that machine with its latest usage, and how many agent sessions "+
			"the caller currently has running on it. The device labels come from the "+
			"most-recently-seen account, since a device is a projection of its links "+
			"rather than a row of its own. A machine with none of the caller's accounts "+
			"is 404, which is also the answer when the machine belongs to someone else — "+
			"the scope makes the two indistinguishable, deliberately. The session count "+
			"reports 0 where the agent plane is not mounted rather than failing the "+
			"read."+linkScope)

	openapi.Describe("/v1/links/devices/:machine/revoke", http.MethodPost,
		"Log out every account on one machine and stop its sessions",
		"Revokes every one of the caller's accounts on one machine and stops the agent "+
			"sessions they were running, answering with how many of each. This is the "+
			"\"I lost that laptop\" button.\n\n"+
			"Revoked links are RETAINED, not deleted, so usage history and the audit "+
			"trail survive a log-out — the rows come back in the response with their new "+
			"status. The session stop reaches only the REVOKING user's own sessions, so a "+
			"shared machine name can never be used to stop a co-tenant's work, and a stop "+
			"that fails does not fail the revoke: the revoked row is the durable truth and "+
			"the count then honestly reports fewer. A machine with nothing left to revoke "+
			"is 404."+linkScope)

	openapi.Describe("/v1/links/:id", http.MethodGet,
		"Read one linked account",
		"Answers a single link — its device, provider, account, plan, how it bills, its "+
			"status and its latest usage snapshot. An id that does not exist, or belongs "+
			"to another user or org, is the same 404: the scope is a bound predicate on "+
			"the read, so a wrong id and a foreign id are indistinguishable and neither "+
			"confirms the other's existence.\n\n"+
			"The static paths on this collection — route, usage, devices — are registered "+
			"BEFORE this one and win first-match, so a link whose id collided with one of "+
			"those words could not be addressed here."+linkScope)

	openapi.Describe("/v1/links/:id", http.MethodDelete,
		"Log out one account and stop the sessions it was running",
		"Revokes a single linked account and stops the agent sessions that ran under "+
			"it, answering with the revoked row and how many sessions stopped. The link is "+
			"RETAINED with a revoked status rather than deleted, so its usage history and "+
			"the audit trail survive the log-out — which also means a revoked account "+
			"still appears in the list, and is excluded from the route plan rather than "+
			"absent from it.\n\n"+
			"The session stop is narrowed to the revoking user's own sessions on that "+
			"device, provider and account, and a stop that fails does not fail the revoke: "+
			"the revoked row is the durable truth. An id that does not exist, or belongs "+
			"to another user or org, is the same 404."+linkScope)
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
