// subsystem.go mounts /v1/patrol: the operational surface over the patrol
// DocType store.
//
// The generic role-gated surface at /v1/framework/patrol.* already gives every
// DocType full CRUD and the console renders it. This adds the four things it
// cannot: the reads shaped for one screen (/snapshot and the lists behind it),
// the incident state machine (incident.go), alarm intake (alarm.go), and the
// live feed (feed.go).
//
// It owns no store. Every read and write goes through the framework in-process
// API with an org this file resolved from the validated principal, so there is
// one storage engine and no second copy of CRUD.
//
// One route is anonymous — GET /tenant/:org serves a public face — so the group
// carries no blanket scope. admit is the single place both questions are asked:
// is there a principal, and does it hold one of these roles.

package patrol

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/singleflight"
)

// List bounds. The engine has no range query, so every list is capped and every
// caller-supplied limit is clamped to the cap.
const (
	maxSites     = 500
	maxUnits     = 200
	maxEvents    = 80
	maxFixes     = 200
	maxPoints    = 500
	maxIncidents = 50
	maxSteps     = 200
	maxNotes     = 100
	maxCameras   = 200
	trailFixes   = 60
	nearUnits    = 3
	maxNear      = 10
)

// stampLayout is how the engine stores a Datetime, and so how patrol writes and
// reads one.
const stampLayout = "2006-01-02 15:04:05"

// roleWait bounds the role read. A slower answer than this denies, which is the
// direction to fail.
const roleWait = 3 * time.Second

// state is empty: patrol holds no handle of its own. The framework owns the
// store, the bus owns the feed, and KMS owns the camera credential.
type state struct{}

// Use mounts /v1/patrol.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, Module, build, routes)
}

func build(b cloud.Base) (state, error) {
	b.Log.Info("patrol mounted", "module", Module)
	return state{}, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is how that prose reaches the published document and the
// MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/patrol generate`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each op is a method value, the one bound form
// cmd/zipdoc lifts prose from. It carries state and no logic.
type ops struct{ s *cloud.Service[state] }

// routes mounts the surface. Bridge goes on first: a typed op receives only a
// context, so the validated org reaches it by being parked there rather than as
// an In field a caller could write. fiber runs middleware in registration order,
// so it must precede every leaf below.
//
// The ops are declared on the GROUP, so each one's path is the prefix composed
// with its leaf — the identity every projection keys on, and the composition
// cmd/zipdoc resolves too.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/" + Module)
	g.Use(cloud.Bridge())
	o := ops{s: s}

	g.Get("/tenant/:org", o.tenant)
	g.Get("/snapshot", o.snapshot)

	g.Get("/site", o.sites)
	g.Get("/site/:name", o.site)
	g.Put("/site/:name", o.editSite)

	g.Get("/unit", o.units)
	g.Get("/unit/near", o.near)
	g.Post("/unit/:name/fix", o.fix)
	g.Post("/unit/:name/state", o.unitState)

	g.Get("/tour", o.tours)
	g.Post("/point/:name/confirm", o.confirm)

	g.Get("/key", o.keys)
	g.Get("/key/:name", o.keyRead)
	g.Post("/key/:name/move", o.moveKey)

	g.Get("/camera", o.cameras)
	g.Get("/camera/:name/stream", o.stream)
	g.Get("/camera/:name/stream/:ticket", o.ticket)

	g.Get("/event", o.events)
	g.Post("/event", o.raiseEvent, zip.WithStatus(http.StatusCreated))

	g.Get("/incident", o.incidents)
	g.Get("/incident/:name", o.incident)
	g.Post("/incident", o.openIncident, zip.WithStatus(http.StatusCreated))
	g.Post("/incident/:name/step", o.stepIncident)

	g.Get("/note", o.notes)
	g.Post("/note", o.writeNote, zip.WithStatus(http.StatusCreated))

	g.Post("/alarm", o.alarm, zip.WithStatus(http.StatusCreated, http.StatusAccepted))
	g.Post("/report", o.makeReport, zip.WithStatus(http.StatusCreated))

	// Two raw handlers, and neither can be otherwise. The feed is an open
	// Server-Sent Events response written by a loop that outlives the handler; a
	// report is bytes under a filename. A typed op returns one marshalled value
	// and describes neither, so each one states itself through openapi.Describe
	// below — the same sentence zipdoc would have lifted off a typed op.
	g.Raw(http.MethodGet, "/feed", cloud.Handle(s, feed))
	g.Raw(http.MethodGet, "/report/:name", cloud.Handle(s, download))
}

// The prose for the two routes that are not typed ops. A typed op's prose is
// lifted from its handler's doc comment by zipdoc, so an op described here as
// well would be the same sentence in two places; openapi.Describe panics on a
// duplicate, which is that rule enforced rather than remembered.
//
// It is keyed by the fiber pattern verbatim, through the same registry the
// projector reads, so prose renders only while the router serves the route.
func init() {
	spec.Describe("/v1/patrol/feed", http.MethodGet,
		"Watch the live feed",
		"Holds a Server-Sent Events connection open and writes every change in the picture as it "+
			"happens: a unit's position, an incident moving, a feed row, a checkpoint confirmed. "+
			"Each record is one `data:` line carrying a JSON frame whose `kind` says which of the "+
			"four it is, terminated by a blank line; a comment line arrives every 25 seconds so an "+
			"idle connection stays open.\n\n"+
			"It carries the caller's own org and no other. ?since=<YYYY-MM-DD HH:MM:SS> replays the "+
			"feed rows written at or after that moment before the stream goes live, so a reader that "+
			"reconnected misses nothing it can still see. The list routes are the truth and the "+
			"stream is a live hint: a dropped frame costs one refresh.")

	spec.Describe("/v1/patrol/report/:name", http.MethodGet,
		"Download a filed report",
		"Renders a filed report as a PDF and answers with the bytes under an attachment filename. "+
			"?format=text answers the stored text verbatim under text/plain, which is what a copy "+
			"button reads.\n\n"+
			"Everyone but the customer may read every report; a client reads only a report about a "+
			"site of their own, and anything else is 404 rather than a refusal, so the route is no "+
			"existence oracle. A report whose text cannot be set answers that text rather than "+
			"failing, because the account is the thing being asked for.")
}

// ---- admission ----

// admit resolves the caller's org and refuses unless they hold one of the named
// roles. Naming no role admits any validated principal. A caller holding System
// Manager — the org owner — is admitted to everything.
//
// It is the ONE place patrol decides which org a request acts in, so the two
// questions that decide it are asked together: the boundary's effective org, and
// the membership set it minted beside it. A principal carrying a set that does
// not name the org it is acting in is refused — the org-switch is IAM's to
// grant, and a request that reached here naming one it was not granted is
// reaching for another tenant's estate.
func (o ops) admit(ctx context.Context, roles ...string) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok || org == "" {
		return "", principal.RefusedFrom(ctx)
	}
	if held := principal.OrgsFrom(ctx); len(held) > 0 && !holdsAny(held, org) {
		return "", principal.RefusedFrom(ctx)
	}
	if len(roles) == 0 {
		return org, nil
	}
	held, err := o.roles(ctx, org)
	if err != nil {
		return "", err
	}
	if holdsAny(held, framework.RoleSystemManager) || holdsAny(held, roles...) {
		return org, nil
	}
	return "", principal.RefusedFrom(ctx)
}

// roles asks IAM for the caller's role names in the org they are acting in, once
// per request. The call, not the token claim, so a role granted a minute ago is
// in force on the next request; once, because admission, the narrowing and the
// machine check each ask, and one request that asked four times could be
// answered four ways. An unanswered call denies.
func (o ops) roles(ctx context.Context, org string) ([]string, error) {
	c, ok := requestOf(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	if held, asked := c.Context().Value(granted{}).([]string); asked {
		return held, nil
	}
	user := who(ctx)
	if user == "" {
		return nil, principal.RefusedFrom(ctx)
	}
	call, cancel := context.WithTimeout(cloud.For(zip.WithCaller(ctx, zip.Caller{User: user}), org), roleWait)
	defer cancel()
	got, err := iam.IAMRoles(call)
	if err != nil {
		o.s.Log.Warn("patrol: roles unresolved", "org", org, "err", err)
		return nil, principal.RefusedFrom(ctx)
	}
	c.SetContext(context.WithValue(c.Context(), granted{}, got.Roles))
	return got.Roles, nil
}

// granted names the request-local slot the answer is kept in. A package type, so
// nothing else can collide with it.
type granted struct{}

// who is the caller's subject — the one place patrol reads who is calling. Every
// question about the caller's identity resolves through it: whose roles IAM is
// asked for, whose unit a fix may move, whose name goes on a step. Empty off the
// HTTP path, where there is nobody to name.
func who(ctx context.Context) string {
	c, ok := requestOf(ctx)
	if !ok {
		return ""
	}
	return strings.Clone(strings.TrimSpace(c.User()))
}

// actor is who to write on a record, which is the subject qualified by the org
// it acted in — the attribution key, not the identity key.
func actor(ctx context.Context) string {
	c, ok := requestOf(ctx)
	if !ok {
		return ""
	}
	return principal.Actor(c)
}

// holdsAny reports whether held contains any of want.
func holdsAny(held []string, want ...string) bool {
	for _, h := range held {
		if slices.Contains(want, h) {
			return true
		}
	}
	return false
}

// requestOf recovers the request a typed op is serving. Absent off the HTTP
// path, where there is no caller to read.
func requestOf(ctx context.Context) (*zip.Ctx, bool) { return cloud.Request(ctx) }

// reach is how much of the estate one caller may read: the whole operation, or
// the sites of the one customer they are.
//
// A row about a site is a row about an estate — an incident names where a
// break-in happened, a feed row carries the trigger verbatim, a camera watches a
// yard — so every read of one passes through here and no route carries a filter
// of its own. A row about no site at all is the operation's own and a customer
// never sees it.
type reach struct {
	// client is the patrol.client document the caller is the contact for, empty
	// for a caller who reads the whole estate.
	client string
	// sites is that customer's estate by document name.
	sites map[string]bool
	// all is the operation reading everything.
	all bool
}

// reachOf resolves it from the caller's roles and their subject — never from
// anything they sent. A caller whose roles cannot be resolved, and a customer
// matching no client record, reach nothing.
func (o ops) reachOf(ctx context.Context, org string) reach {
	held, err := o.roles(ctx, org)
	if err != nil {
		return reach{}
	}
	if holdsAny(held, framework.RoleSystemManager, RoleOfficer) || holdsAny(held, control...) {
		return reach{all: true}
	}
	name := o.customer(ctx, org)
	if name == "" {
		return reach{}
	}
	// Names, not documents: the estate is a set of sites and every read a customer
	// makes resolves it first, so the whole of each site's record would be
	// unpacked to learn which names are in the set.
	names, err := framework.Names(ctx, org, DTSite, map[string]string{"client": name}, maxSites)
	if err != nil {
		return reach{}
	}
	sites := make(map[string]bool, len(names))
	for _, site := range names {
		sites[site] = true
	}
	return reach{client: name, sites: sites}
}

// narrow is the same answer asked of the store rather than of a row: a site
// query carrying the caller's own customer, so a list is bounded by their estate
// instead of trimmed after the fact.
func (r reach) narrow(filters map[string]string) map[string]string {
	if r.all {
		return filters
	}
	if filters == nil {
		filters = map[string]string{}
	}
	filters["client"] = r.client
	return filters
}

// sees reports whether a row about `site` is this caller's to read.
func (r reach) sees(site string) bool {
	if r.all {
		return true
	}
	return site != "" && r.sites[site]
}

// seen reads the documents of a DocType this caller may read, narrowed in the
// QUERY rather than after it.
//
// A window taken from the whole operation and then trimmed is not the caller's
// window: a customer whose own break-in sits behind fifty of somebody else's
// reads an empty list, and whether it is empty is the operation's activity
// rather than anything about them. A customer's estate is a SET of sites, which
// the store can be asked about in one question, so the window is cut from what
// is theirs to begin with.
//
// `among` carries any further narrowing, each field with the values that satisfy
// it. A row about no site at all belongs to the operation, so a customer is
// answered none of them, and a customer whose estate is empty is answered
// nothing at all.
func (o ops) seen(ctx context.Context, org string, id framework.ID, among map[string][]string, limit int, r reach) []framework.Document {
	if !r.all {
		among = with(among, "site", estate(r.sites))
	}
	docs, err := framework.Among(ctx, org, id, among, limit)
	if err != nil {
		return nil
	}
	return docs
}

// with is `among` plus one more set, leaving the caller's map alone.
func with(among map[string][]string, field string, vals []string) map[string][]string {
	out := make(map[string][]string, len(among)+1)
	maps.Copy(out, among)
	out[field] = vals
	return out
}

// estate names a customer's sites in order, so one estate is always one query.
func estate(sites map[string]bool) []string {
	out := make([]string, 0, len(sites))
	for site := range sites {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}

// oldest reverses a newest-first read. The store answers most-recently-updated
// first, which is the window a bounded list wants; a trail is read forward —
// an act, then the act after it.
func oldest[T any](rows []T) []T {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// customer is the client record the caller is the contact for. Exactly one, or
// none: two records sharing a contact make the estate a customer reads a matter
// of which row the store returned first, so an ambiguous contact reads nothing
// until the register is corrected.
func (o ops) customer(ctx context.Context, org string) string {
	user := who(ctx)
	if user == "" {
		return ""
	}
	docs, err := framework.Search(ctx, org, DTClient, map[string]string{"contact": user}, 2)
	if err != nil || len(docs) != 1 {
		if len(docs) > 1 {
			o.s.Log.Warn("patrol: one contact on two customers", "org", org, "contact", user)
		}
		return ""
	}
	return docs[0].Name
}

// fail maps a framework error to the status it means, so no route answers a
// different one for the same condition.
func (o ops) fail(err error, what string) error {
	switch {
	case errors.Is(err, framework.ErrNotFound):
		return zip.ErrNotFound(what + ": not found")
	case errors.Is(err, framework.ErrBadRef):
		return zip.ErrBadRequest(what + ": unknown reference")
	case framework.IsValidationError(err):
		return zip.ErrBadRequest(err.Error())
	}
	o.s.Log.Warn("patrol: "+what, "err", err)
	return zip.Errorf(http.StatusInternalServerError, "%s", what)
}

// ---- tenant ----

// patrolTenantRef names the org whose public face to read.
type patrolTenantRef struct {
	// Org is the IAM org the tenant belongs to, from the path.
	Org string `json:"org"`
}

// patrolTenant is a tenant's public configuration: enough to draw the product
// before anyone signs in, and nothing operational.
type patrolTenant struct {
	// Brand is the look: names, colours, logo references.
	Brand json.RawMessage `json:"brand"`
	// Company is the legal entity behind the operation — name, address, licence.
	Company json.RawMessage `json:"company"`
	// Operations is how the operation runs: coverage, timezone, SLA targets.
	Operations json.RawMessage `json:"operations"`
	// Features says which parts of the product this tenant runs.
	Features json.RawMessage `json:"features"`
	// Terminology is the words this tenant's screens use for site, unit and
	// officer.
	Terminology json.RawMessage `json:"terminology"`
}

// patrolTenantOut is the tenant envelope.
type patrolTenantOut struct {
	// Data is the tenant's public configuration.
	Data patrolTenant `json:"data"`
}

// tenant returns one org's public face — brand, company, operations, features
// and terminology. It is the one anonymous route here: the front end loads it
// before anyone signs in. It carries no contact list and nothing operational,
// and an org with no tenant document is 404.
//
// The org is a name a stranger supplied, so the read asks whether the deployment
// already holds that tenant before it opens anything. Resolving a store CREATES
// one, and a caller naming a fresh org on every request would leave a database
// per name they invented.
func (o ops) tenant(ctx context.Context, in *patrolTenantRef) (*patrolTenantOut, error) {
	org := strings.TrimSpace(in.Org)
	if !principal.NamesOrg(org) || !framework.Installed(ctx, org, DTTenant) {
		return nil, zip.ErrNotFound("tenant not found")
	}
	t, err := framework.Get(ctx, org, DTTenant, DTTenant.Name)
	if err != nil {
		return nil, zip.ErrNotFound("tenant not found")
	}
	return &patrolTenantOut{Data: patrolTenant{
		Brand:       jsonField(t, "brand"),
		Company:     jsonField(t, "company"),
		Operations:  jsonField(t, "operations"),
		Features:    jsonField(t, "features"),
		Terminology: jsonField(t, "terminology"),
	}}, nil
}

// ---- snapshot ----

// patrolSnapshotQuery bounds the one read that draws the whole screen.
type patrolSnapshotQuery struct {
	// Events caps how many feed rows ride along. Anything that is not a positive
	// integer uses 80, and higher values are clamped to 80.
	Events int `json:"events"`
}

// patrolEstate is everything one screen draws: the sites, the units and their trails,
// today's tours, the key register, the recent feed, and the incident currently
// running.
type patrolEstate struct {
	// Sites is the estate under contract.
	Sites []patrolSite `json:"sites"`
	// Units is the fleet, each with the tail of its position trail.
	Units []patrolUnit `json:"units"`
	// Tours is today's rounds with their checkpoints.
	Tours []patrolTour `json:"tours"`
	// Keys is the key register, empty for a caller whose roles do not reach it.
	Keys []patrolKey `json:"keys"`
	// Events is the recent feed, newest first.
	Events []patrolEvent `json:"events"`
	// Incident is the incident currently running, or null when none is.
	Incident *patrolIncident `json:"incident"`
}

// patrolSnapshotOut is the snapshot envelope.
type patrolSnapshotOut struct {
	// Data is the whole estate as one read.
	Data patrolEstate `json:"data"`
}

// snapshot returns the whole picture in one read: sites, units with their
// position trails, today's tours and checkpoints, the key register, the recent
// feed and the open incident. It replaces the five separate list calls the
// screen used to make. A client sees only their own sites, and the key register
// is empty for anyone whose roles do not reach it.
func (o ops) snapshot(ctx context.Context, in *patrolSnapshotQuery) (*patrolSnapshotOut, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	out := patrolEstate{
		Sites: []patrolSite{}, Units: []patrolUnit{}, Tours: []patrolTour{},
		Keys: []patrolKey{}, Events: []patrolEvent{},
	}
	r := o.reachOf(ctx, org)
	if r.all || r.client != "" {
		sites, err := framework.Search(ctx, org, DTSite, r.narrow(nil), maxSites)
		if err != nil {
			return nil, o.fail(err, "list sites")
		}
		for _, s := range sites {
			out.Sites = append(out.Sites, viewSite(s, false))
		}
	}
	// The fleet is the operation's: a unit names no site, its officer is a member
	// of the company's staff and its trail is where that person has been all
	// shift. A customer is answered none of it, here and at GET /unit both.
	if r.all {
		units, err := framework.Search(ctx, org, DTUnit, nil, maxUnits)
		if err != nil {
			return nil, o.fail(err, "list units")
		}
		for _, u := range units {
			out.Units = append(out.Units, o.viewUnit(ctx, org, u, true))
		}
	}
	out.Tours = append(out.Tours, o.readTours(ctx, org, "", r)...)
	// The key register rides along only for the roles GET /key admits. A read is
	// one rule wherever it is served, so the snapshot asks that rule rather than
	// carrying a looser one of its own.
	if _, err := o.admit(ctx, control...); err == nil {
		keys, _ := framework.Search(ctx, org, DTKey, nil, maxSites)
		for _, k := range keys {
			out.Keys = append(out.Keys, viewKey(k))
		}
	}
	for _, e := range o.seen(ctx, org, DTEvent, nil, clamp(in.Events, maxEvents), r) {
		out.Events = append(out.Events, viewEvent(e))
	}
	// The incident currently running: the most recently worked of the ones that
	// are still open.
	for _, d := range o.seen(ctx, org, DTIncident, map[string][]string{"state": openStates}, maxIncidents, r) {
		out.Incident = o.viewIncident(ctx, org, d, r)
		break
	}
	return &patrolSnapshotOut{Data: out}, nil
}

// ---- sites ----

// patrolZone is one detection zone on a site's panel.
type patrolZone struct {
	// Ref is the zone number the panel reports.
	Ref string `json:"ref"`
	// Location is where in the building the zone is.
	Location string `json:"location"`
	// Device is the detector type.
	Device string `json:"device"`
	// State is the zone's last known condition.
	State string `json:"state"`
}

// patrolSite is one site. Zones and hazards ride inside the document, so a single
// fetch answers the site view; a list leaves them out.
type patrolSite struct {
	// Name is the site's document name and the segment /v1/patrol/site/{name}
	// addresses it by.
	Name string `json:"name"`
	// Ref is the site's number in the register.
	Ref int64 `json:"ref"`
	// Account is the alarm-panel account number the receiving centre reports.
	Account string `json:"account"`
	// Category is what kind of premises this is.
	Category string `json:"category"`
	// Town is the town the site is in.
	Town string `json:"town"`
	// Eircode is the postal code.
	Eircode string `json:"eircode"`
	// Lat is the site's latitude in degrees.
	Lat float64 `json:"lat"`
	// Lon is the site's longitude in degrees.
	Lon float64 `json:"lon"`
	// Risk is the assessed risk score.
	Risk int64 `json:"risk"`
	// Tier is the contracted service tier.
	Tier string `json:"tier"`
	// SLA is the contracted response time in minutes.
	SLA int64 `json:"sla"`
	// Sector is the patrol sector the site sits in.
	Sector string `json:"sector"`
	// Unit is the call sign of the unit that covers the site, or empty.
	Unit string `json:"unit"`
	// Keys says whether the operation holds a key set for this site.
	Keys bool `json:"keys"`
	// Cameras is how many cameras are registered here.
	Cameras int64 `json:"cameras"`
	// Visits is how many patrol visits the contract calls for.
	Visits int64 `json:"visits"`
	// Contact is the keyholder to reach.
	Contact string `json:"contact"`
	// Status is ok, fault, alarm or offline.
	Status string `json:"status"`
	// Client is the customer this site belongs to.
	Client string `json:"client"`
	// Notes is the standing brief for the site.
	Notes string `json:"notes"`
	// Zones is the panel's detection zones, present on a single-site read.
	Zones []patrolZone `json:"zones,omitempty"`
	// Hazards is the standing warnings, present on a single-site read.
	Hazards []string `json:"hazards,omitempty"`
}

// patrolSiteQuery selects which sites to list.
type patrolSiteQuery struct {
	// Limit caps how many sites are returned. Anything that is not a positive
	// integer uses 500, and higher values are clamped to 500.
	Limit int `json:"limit"`
	// Status narrows the list to ok, fault, alarm or offline. Empty lists every
	// status.
	Status string `json:"status"`
}

// patrolSiteList is the estate as a list.
type patrolSiteList struct {
	// Data is the matching sites, without their zones and hazards.
	Data []patrolSite `json:"data"`
}

// sites lists the estate. A client sees only the sites their own customer
// record owns, filtered from their role rather than from anything they sent.
func (o ops) sites(ctx context.Context, in *patrolSiteQuery) (*patrolSiteList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	filters := map[string]string{}
	if s := strings.TrimSpace(in.Status); s != "" {
		filters["status"] = s
	}
	r := o.reachOf(ctx, org)
	if !r.all && r.client == "" {
		return &patrolSiteList{Data: []patrolSite{}}, nil
	}
	docs, err := framework.Search(ctx, org, DTSite, r.narrow(filters), clamp(in.Limit, maxSites))
	if err != nil {
		return nil, o.fail(err, "list sites")
	}
	out := make([]patrolSite, 0, len(docs))
	for _, d := range docs {
		out = append(out, viewSite(d, false))
	}
	return &patrolSiteList{Data: out}, nil
}

// patrolSiteRef addresses one site.
type patrolSiteRef struct {
	// Name is the site's document name, from the path.
	Name string `json:"name"`
}

// patrolSiteOut is one site.
type patrolSiteOut struct {
	// Data is the site with its zones and hazards.
	Data patrolSite `json:"data"`
}

// site returns one site whole: the register row plus the panel's zones and the
// standing hazards, which ride inside the document. A client asking for a site
// that is not theirs is 404.
func (o ops) site(ctx context.Context, in *patrolSiteRef) (*patrolSiteOut, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	d, err := framework.Get(ctx, org, DTSite, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.ErrNotFound("site not found")
	}
	if !o.reachOf(ctx, org).sees(d.Name) {
		return nil, zip.ErrNotFound("site not found")
	}
	return &patrolSiteOut{Data: viewSite(d, true)}, nil
}

// patrolSiteEdit is the fields a manager may change on a site. Every one is optional;
// what is absent is left as it stands.
type patrolSiteEdit struct {
	// Name is the site's document name, from the path.
	Name string `json:"name" url:"name"`
	// Category is what kind of premises this is.
	Category string `json:"category"`
	// Town is the town the site is in.
	Town string `json:"town"`
	// Eircode is the postal code.
	Eircode string `json:"eircode"`
	// Tier is the contracted service tier.
	Tier string `json:"tier"`
	// SLA is the contracted response time in minutes.
	SLA int64 `json:"sla"`
	// Sector is the patrol sector the site sits in.
	Sector string `json:"sector"`
	// Contact is the keyholder to reach.
	Contact string `json:"contact"`
	// Status is ok, fault, alarm or offline.
	Status string `json:"status"`
	// Notes is the standing brief for the site.
	Notes string `json:"notes"`
}

// editSite changes a site's contract and brief fields. It leaves absent fields
// as they stand, and it never moves the account number, the coordinates or the
// customer — those are register facts, changed through the framework surface.
func (o ops) editSite(ctx context.Context, in *patrolSiteEdit) (*patrolSiteOut, error) {
	org, err := o.admit(ctx, RoleManager, RoleOwner, RoleSupervisor)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	d, err := framework.Get(ctx, org, DTSite, name)
	if err != nil {
		return nil, zip.ErrNotFound("site not found")
	}
	change := map[string]any{}
	set(change, "category", in.Category)
	set(change, "town", in.Town)
	set(change, "eircode", in.Eircode)
	set(change, "tier", in.Tier)
	set(change, "sector", in.Sector)
	set(change, "contact", in.Contact)
	set(change, "status", in.Status)
	set(change, "notes", in.Notes)
	if in.SLA > 0 {
		change["sla"] = float64(in.SLA)
	}
	if err := framework.UpdateData(ctx, org, DTSite, name, merge(d.Data, change)); err != nil {
		return nil, o.fail(err, "edit site")
	}
	d, err = framework.Get(ctx, org, DTSite, name)
	if err != nil {
		return nil, o.fail(err, "read site")
	}
	return &patrolSiteOut{Data: viewSite(d, true)}, nil
}

// ---- units ----

// patrolFix is one recorded position on a unit's trail.
type patrolFix struct {
	// Lat is the latitude in degrees.
	Lat float64 `json:"lat"`
	// Lon is the longitude in degrees.
	Lon float64 `json:"lon"`
	// At is when the position was recorded.
	At string `json:"at"`
}

// patrolUnit is one patrol unit.
type patrolUnit struct {
	// Name is the unit's call sign, which is its document name and the segment
	// /v1/patrol/unit/{name}/fix addresses it by.
	Name string `json:"name"`
	// Officer is who is crewing it.
	Officer string `json:"officer"`
	// Sector is the patrol sector it works.
	Sector string `json:"sector"`
	// State is available, assigned, enroute, onsite, offline, break or emergency.
	State string `json:"state"`
	// Lat is the unit's last latitude in degrees.
	Lat float64 `json:"lat"`
	// Lon is the unit's last longitude in degrees.
	Lon float64 `json:"lon"`
	// Seen is when the unit last reported.
	Seen string `json:"seen"`
	// Due is the lone-worker deadline, or empty when none is set.
	Due string `json:"due"`
	// Trail is the tail of the unit's recorded positions, oldest first.
	Trail []patrolFix `json:"trail,omitempty"`
}

// patrolUnitQuery takes nothing: the fleet is small enough to answer whole.
type patrolUnitQuery struct{}

// patrolUnitList is the fleet.
type patrolUnitList struct {
	// Data is every unit in the org, each with its position trail.
	Data []patrolUnit `json:"data"`
}

// units lists the fleet with each unit's position trail, which is what the map
// draws. It is the operation's own: a unit names no site, so a customer reads
// none of it — the officer crewing one is the company's staff, and the trail is
// where that person has been all shift.
func (o ops) units(ctx context.Context, _ *patrolUnitQuery) (*patrolUnitList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	if !o.reachOf(ctx, org).all {
		return &patrolUnitList{Data: []patrolUnit{}}, nil
	}
	docs, err := framework.Search(ctx, org, DTUnit, nil, maxUnits)
	if err != nil {
		return nil, o.fail(err, "list units")
	}
	out := make([]patrolUnit, 0, len(docs))
	for _, d := range docs {
		out = append(out, o.viewUnit(ctx, org, d, true))
	}
	return &patrolUnitList{Data: out}, nil
}

// patrolFixIn is one position report from a unit.
type patrolFixIn struct {
	// Name is the unit's call sign, from the path.
	Name string `json:"name" url:"name"`
	// Lat is the latitude in degrees.
	Lat float64 `json:"lat"`
	// Lon is the longitude in degrees.
	Lon float64 `json:"lon"`
	// At is not a caller's to fill. A position report is stamped by the server,
	// and a body naming it is refused rather than silently restamped: the trail is
	// the proof of where a unit was, so a report that thought it was dating itself
	// must learn otherwise.
	At string `json:"at"`
}

// patrolFixed acknowledges a position report.
type patrolFixed struct {
	// At is the moment the position was recorded under.
	At string `json:"at"`
}

// patrolFixOut is the position envelope.
type patrolFixOut struct {
	// Data is the acknowledgement.
	Data patrolFixed `json:"data"`
}

// fix records where a unit is. It appends the position to the unit's trail,
// moves the unit's own coordinates and last-seen stamp, and publishes one frame
// to every screen watching. An officer may report only their own unit; the
// control room may report any.
func (o ops) fix(ctx context.Context, in *patrolFixIn) (*patrolFixOut, error) {
	org, err := o.admit(ctx, staff...)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.At) != "" {
		return nil, zip.ErrBadRequest("a position report is stamped by the server")
	}
	name := strings.TrimSpace(in.Name)
	if err := o.ownUnit(ctx, org, name); err != nil {
		return nil, err
	}
	u, err := framework.Get(ctx, org, DTUnit, name)
	if err != nil {
		return nil, zip.ErrNotFound("unit not found")
	}
	at := now()
	if _, err := framework.Ingest(ctx, org, DTFix, map[string]any{
		"unit": name, "lat": in.Lat, "lon": in.Lon, "at": at,
	}, ""); err != nil {
		return nil, o.fail(err, "record fix")
	}
	if err := framework.UpdateData(ctx, org, DTUnit, name, merge(u.Data, map[string]any{
		"lat": in.Lat, "lon": in.Lon, "seen": at,
	})); err != nil {
		return nil, o.fail(err, "move unit")
	}
	o.publish(ctx, org, frame{Kind: "fix", Unit: name, Lat: in.Lat, Lon: in.Lon, At: at})
	return &patrolFixOut{Data: patrolFixed{At: at}}, nil
}

// patrolNearQuery asks which units are closest to a site.
type patrolNearQuery struct {
	// Site is the site to measure from, by document name.
	Site string `json:"site"`
	// Limit caps how many units come back. Anything that is not a positive
	// integer uses 3, and higher values are clamped to 10.
	Limit int `json:"limit"`
}

// patrolNearest is one unit and how far it is from the site asked about.
type patrolNearest struct {
	// Unit is the unit's call sign.
	Unit string `json:"unit"`
	// Km is the great-circle distance to the site in kilometres.
	Km float64 `json:"km"`
	// Eta is the minutes the drive would take at the planning speed.
	Eta int `json:"eta"`
}

// patrolNearList is the answer to "who can get there".
type patrolNearList struct {
	// Data is the closest units, available ones first and then by distance.
	Data []patrolNearest `json:"data"`
}

// near answers which units can reach a site soonest. Distance is a great-circle
// haversine over the units' last reported positions, and an available unit sorts
// ahead of a busy one at any distance — a controller asking for the nearest unit
// is asking for the nearest free one.
func (o ops) near(ctx context.Context, in *patrolNearQuery) (*patrolNearList, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	st, err := framework.Get(ctx, org, DTSite, strings.TrimSpace(in.Site))
	if err != nil {
		return nil, zip.ErrNotFound("site not found")
	}
	docs, err := framework.Search(ctx, org, DTUnit, nil, maxUnits)
	if err != nil {
		return nil, o.fail(err, "list units")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = nearUnits
	}
	found := nearest(docs, floatField(st, "lat"), floatField(st, "lon"), clamp(limit, maxNear))
	out := make([]patrolNearest, 0, len(found))
	for _, f := range found {
		out = append(out, patrolNearest{Unit: f.unit, Km: f.km, Eta: f.eta})
	}
	return &patrolNearList{Data: out}, nil
}

// patrolUnitStateIn moves a unit between states.
type patrolUnitStateIn struct {
	// Name is the unit's call sign, from the path.
	Name string `json:"name" url:"name"`
	// State is available, assigned, enroute, onsite, offline, break or emergency.
	State string `json:"state"`
}

// patrolUnitOut is one unit.
type patrolUnitOut struct {
	// Data is the unit as it now stands.
	Data patrolUnit `json:"data"`
}

// unitState moves a unit between states — on shift, on a break, off, or in
// trouble. An officer may move only their own unit; the control room may move
// any.
func (o ops) unitState(ctx context.Context, in *patrolUnitStateIn) (*patrolUnitOut, error) {
	org, err := o.admit(ctx, staff...)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if err := o.ownUnit(ctx, org, name); err != nil {
		return nil, err
	}
	u, err := framework.Get(ctx, org, DTUnit, name)
	if err != nil {
		return nil, zip.ErrNotFound("unit not found")
	}
	if err := framework.UpdateData(ctx, org, DTUnit, name,
		merge(u.Data, map[string]any{"state": strings.TrimSpace(in.State)})); err != nil {
		return nil, o.fail(err, "move unit")
	}
	u, err = framework.Get(ctx, org, DTUnit, name)
	if err != nil {
		return nil, o.fail(err, "read unit")
	}
	return &patrolUnitOut{Data: o.viewUnit(ctx, org, u, false)}, nil
}

// ---- tours ----

// patrolCheckpoint is one checkpoint on a tour.
type patrolCheckpoint struct {
	// Name is the checkpoint's document name and the segment
	// /v1/patrol/point/{name}/confirm addresses it by.
	Name string `json:"name"`
	// Tour is the round this checkpoint belongs to.
	Tour string `json:"tour"`
	// Site is the site to attend.
	Site string `json:"site"`
	// Tag is the tag or marker to read there.
	Tag string `json:"tag"`
	// Method is how it was confirmed: tag, scan, fence, manual or photo.
	Method string `json:"method"`
	// Open is when the window to confirm it opens.
	Open string `json:"open"`
	// Close is when that window shuts.
	Close string `json:"close"`
	// At is when it was confirmed, or empty.
	At string `json:"at"`
	// Lat is the latitude the confirmation was taken at.
	Lat float64 `json:"lat"`
	// Lon is the longitude the confirmation was taken at.
	Lon float64 `json:"lon"`
	// Photo is the object key of the photo taken, or empty.
	Photo string `json:"photo"`
	// Actor is who confirmed it.
	Actor string `json:"actor"`
	// State is pending, done, late or skipped.
	State string `json:"state"`
}

// patrolTour is one unit's round with its checkpoints.
type patrolTour struct {
	// Name is the tour's document name.
	Name string `json:"name"`
	// Unit is the call sign running the round.
	Unit string `json:"unit"`
	// Date is the day the round belongs to.
	Date string `json:"date"`
	// Start is when the round began.
	Start string `json:"start"`
	// End is when it finished.
	End string `json:"end"`
	// Points is the round's checkpoints in store order.
	Points []patrolCheckpoint `json:"points"`
}

// patrolTourQuery selects which rounds to list.
type patrolTourQuery struct {
	// Date narrows the list to one day, as YYYY-MM-DD. "today" means today.
	// Empty lists every round held.
	Date string `json:"date"`
}

// patrolTourList is the rounds.
type patrolTourList struct {
	// Data is the matching rounds, each with its checkpoints.
	Data []patrolTour `json:"data"`
}

// tours lists patrol rounds with their checkpoints — the proof-of-presence
// record. ?date=today narrows it to the current day.
func (o ops) tours(ctx context.Context, in *patrolTourQuery) (*patrolTourList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	date := strings.TrimSpace(in.Date)
	if strings.EqualFold(date, "today") {
		date = time.Now().UTC().Format("2006-01-02")
	}
	return &patrolTourList{Data: o.readTours(ctx, org, date, o.reachOf(ctx, org))}, nil
}

// readTours is the tour read both /tour and /snapshot answer with.
//
// A checkpoint names the site to attend, so a round is a set of rows about
// sites: a customer reads the checkpoints at their own premises — which is the
// proof of presence they are paying for — and a round that holds none of theirs
// is not theirs to know about.
func (o ops) readTours(ctx context.Context, org, date string, r reach) []patrolTour {
	filters := map[string]string{}
	if date != "" {
		filters["date"] = date
	}
	docs, err := framework.Search(ctx, org, DTTour, filters, maxUnits)
	if err != nil {
		return []patrolTour{}
	}
	out := make([]patrolTour, 0, len(docs))
	for _, d := range docs {
		v := patrolTour{
			Name: d.Name, Unit: strField(d, "unit"), Date: strField(d, "date"),
			Start: strField(d, "start"), End: strField(d, "end"),
			Points: []patrolCheckpoint{},
		}
		points, err := framework.Search(ctx, org, DTPoint, map[string]string{"tour": d.Name}, maxPoints)
		if err == nil {
			for _, p := range oldest(points) {
				if r.sees(strField(p, "site")) {
					v.Points = append(v.Points, viewPoint(p))
				}
			}
		}
		if !r.all && len(v.Points) == 0 {
			continue
		}
		out = append(out, v)
	}
	return out
}

// patrolConfirmIn is an officer confirming presence at a checkpoint.
type patrolConfirmIn struct {
	// Name is the checkpoint's document name, from the path.
	Name string `json:"name" url:"name"`
	// Method is how presence was proved: tag, scan, fence, manual or photo.
	Method string `json:"method"`
	// Lat is the latitude the confirmation was taken at.
	Lat float64 `json:"lat"`
	// Lon is the longitude the confirmation was taken at.
	Lon float64 `json:"lon"`
	// Photo is the object key of a photo taken there, never the bytes.
	Photo string `json:"photo"`
}

// patrolCheckpointOut is one checkpoint.
type patrolCheckpointOut struct {
	// Data is the checkpoint as it now stands.
	Data patrolCheckpoint `json:"data"`
}

// confirm records presence at a checkpoint: who confirmed it, the method, where
// they stood, and the photo key if one was taken. A confirmation after the
// window shuts is recorded late rather than refused — the visit happened, and a
// report that hides it is worth nothing.
//
// An officer confirms their own round and no other, by the same rule a position
// report and every incident act are held to: the checkpoint belongs to a tour,
// the tour names a unit, and the unit names its officer. The record carries the
// actor, so it says who claimed the visit and not merely that somebody did.
func (o ops) confirm(ctx context.Context, in *patrolConfirmIn) (*patrolCheckpointOut, error) {
	org, err := o.admit(ctx, RoleOfficer, RoleController, RoleSupervisor)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	p, err := framework.Get(ctx, org, DTPoint, name)
	if err != nil {
		return nil, zip.ErrNotFound("checkpoint not found")
	}
	round, err := framework.Get(ctx, org, DTTour, strField(p, "tour"))
	if err != nil {
		return nil, zip.ErrNotFound("checkpoint not found")
	}
	if err := o.ownUnit(ctx, org, strField(round, "unit")); err != nil {
		return nil, err
	}
	stamped := now()
	state := "done"
	shut, bounded := at(strField(p, "close"))
	if taken, ok := at(stamped); bounded && ok && taken.After(shut) {
		state = "late"
	}
	change := map[string]any{"at": stamped, "state": state, "actor": actor(ctx)}
	set(change, "method", in.Method)
	set(change, "photo", in.Photo)
	if in.Lat != 0 || in.Lon != 0 {
		change["lat"], change["lon"] = in.Lat, in.Lon
	}
	if err := framework.UpdateData(ctx, org, DTPoint, name, merge(p.Data, change)); err != nil {
		return nil, o.fail(err, "confirm checkpoint")
	}
	p, err = framework.Get(ctx, org, DTPoint, name)
	if err != nil {
		return nil, o.fail(err, "read checkpoint")
	}
	v := viewPoint(p)
	o.publish(ctx, org, frame{Kind: "point", Point: &v})
	return &patrolCheckpointOut{Data: v}, nil
}

// ---- keys ----

// patrolKey is one client key set.
type patrolKey struct {
	// Name is the key set's reference, which is its document name.
	Name string `json:"name"`
	// Site is the site the keys open.
	Site string `json:"site"`
	// Where is where the set is kept.
	Where string `json:"where"`
	// Holder is who has it now.
	Holder string `json:"holder"`
	// Moved is when it last changed hands.
	Moved string `json:"moved"`
}

// patrolKeyQuery takes nothing: the key register is answered whole.
type patrolKeyQuery struct{}

// patrolKeyList is the key register.
type patrolKeyList struct {
	// Data is every key set held.
	Data []patrolKey `json:"data"`
}

// keys lists the key register. Neither the officer nor the client reaches it:
// patrol.key is its own DocType and its permissions name neither, so there is no
// key row for either to read.
func (o ops) keys(ctx context.Context, _ *patrolKeyQuery) (*patrolKeyList, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	docs, err := framework.Search(ctx, org, DTKey, nil, maxSites)
	if err != nil {
		return nil, o.fail(err, "list keys")
	}
	out := make([]patrolKey, 0, len(docs))
	for _, d := range docs {
		out = append(out, viewKey(d))
	}
	return &patrolKeyList{Data: out}, nil
}

// patrolKeyRef addresses one key set.
type patrolKeyRef struct {
	// Name is the key set's reference, from the path.
	Name string `json:"name"`
}

// patrolKeyOut is one key set.
type patrolKeyOut struct {
	// Data is the key set.
	Data patrolKey `json:"data"`
}

// keyRead returns one key set from the register.
func (o ops) keyRead(ctx context.Context, in *patrolKeyRef) (*patrolKeyOut, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	d, err := framework.Get(ctx, org, DTKey, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.ErrNotFound("key not found")
	}
	return &patrolKeyOut{Data: viewKey(d)}, nil
}

// patrolMoveIn signs a key set out, back in, or counts it.
type patrolMoveIn struct {
	// Name is the key set's reference, from the path.
	Name string `json:"name" url:"name"`
	// Act is out, in or audit.
	Act string `json:"act"`
	// Holder is who the keys went to. Who SIGNED for the move is the caller, and
	// is stamped from the request rather than read from here.
	Holder string `json:"holder"`
	// Job is the work the keys were drawn for.
	Job string `json:"job"`
}

// moveKey records a key set leaving, returning, or being counted, and moves the
// register row to match. The register says where a set is now and the movements
// say how it got there, so a movement is written once and never edited.
//
// Who signed for the move is the caller, stamped from the request. A body that
// named the signer would make the chain of custody a value the signer chose.
func (o ops) moveKey(ctx context.Context, in *patrolMoveIn) (*patrolKeyOut, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	k, err := framework.Get(ctx, org, DTKey, name)
	if err != nil {
		return nil, zip.ErrNotFound("key not found")
	}
	act := strings.ToLower(strings.TrimSpace(in.Act))
	switch act {
	case "out", "in", "audit":
	default:
		return nil, zip.ErrBadRequest("act is out, in or audit")
	}
	at := now()
	holder := text.Trim(in.Holder, maxRef)
	if _, err := framework.Ingest(ctx, org, DTMovement, map[string]any{
		"key": name, "act": act, "actor": actor(ctx), "holder": holder,
		"job": text.Trim(in.Job, maxRef), "at": at,
	}, ""); err != nil {
		return nil, o.fail(err, "record movement")
	}
	change := map[string]any{"moved": at}
	if act == "out" {
		change["holder"] = holder
	} else if act == "in" {
		change["holder"] = ""
	}
	if err := framework.UpdateData(ctx, org, DTKey, name, merge(k.Data, change)); err != nil {
		return nil, o.fail(err, "move key")
	}
	k, err = framework.Get(ctx, org, DTKey, name)
	if err != nil {
		return nil, o.fail(err, "read key")
	}
	return &patrolKeyOut{Data: viewKey(k)}, nil
}

// ---- cameras ----

// patrolCamera is one camera. The stream credential is never part of it.
type patrolCamera struct {
	// Name is the camera's document name and the segment
	// /v1/patrol/camera/{name}/stream addresses it by.
	Name string `json:"name"`
	// Site is the site the camera watches.
	Site string `json:"site"`
	// Label is what the camera is called on screen.
	Label string `json:"label"`
	// Kind is the camera type.
	Kind string `json:"kind"`
	// Online says whether the camera last reported healthy.
	Online bool `json:"online"`
}

// patrolCameraQuery selects which cameras to list.
type patrolCameraQuery struct {
	// Site narrows the list to one site by document name. Empty lists every
	// camera.
	Site string `json:"site"`
}

// patrolCameraList is the cameras.
type patrolCameraList struct {
	// Data is the matching cameras, never their credentials.
	Data []patrolCamera `json:"data"`
}

// cameras lists the cameras a caller may OPEN. A row somebody can never reach is
// a catalogue of a customer's premises and nothing else, so the admission the
// stream enforces is asked here too rather than left to differ from it.
//
// The stream credential is not part of a camera row — it is a KMS path the
// document store never holds — so what comes back says where a camera is and
// never how to reach it.
func (o ops) cameras(ctx context.Context, in *patrolCameraQuery) (*patrolCameraList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	if _, err := o.admit(ctx, staff...); err != nil {
		return &patrolCameraList{Data: []patrolCamera{}}, nil
	}
	filters := map[string]string{}
	if s := strings.TrimSpace(in.Site); s != "" {
		filters["site"] = s
	}
	r := o.reachOf(ctx, org)
	docs, err := framework.Search(ctx, org, DTCamera, filters, maxCameras)
	if err != nil {
		return nil, o.fail(err, "list cameras")
	}
	out := make([]patrolCamera, 0, len(docs))
	for _, d := range docs {
		if !r.sees(strField(d, "site")) {
			continue
		}
		out = append(out, patrolCamera{
			Name: d.Name, Site: strField(d, "site"), Label: strField(d, "label"),
			Kind: strField(d, "kind"), Online: intField(d, "online") == 1,
		})
	}
	return &patrolCameraList{Data: out}, nil
}

// patrolCameraRef addresses one camera.
type patrolCameraRef struct {
	// Name is the camera's document name, from the path.
	Name string `json:"name"`
}

// patrolStream is a short-lived way in to one camera.
type patrolStream struct {
	// URL is the address to open the stream at.
	URL string `json:"url"`
	// Expires is when that address stops working.
	Expires string `json:"expires"`
}

// patrolStreamOut is the stream envelope.
type patrolStreamOut struct {
	// Data is the stream address and its expiry.
	Data patrolStream `json:"data"`
}

// stream mints a short-lived way in to one camera.
//
// The credential is kept in KMS, at the one place a camera's credential is kept
// — the org's own patrol namespace, named for the camera — and what comes back
// is minted from it rather than being it: the camera's address with the user and
// password removed, bounded by an expiry, and signed so that whatever opens the
// camera can tell a live ticket from a made-up one. The caller receives
// something that opens one camera for five minutes and says nothing about how.
//
// A camera whose row points anywhere but its own place, and one with no
// credential registered, is 404.
func (o ops) stream(ctx context.Context, in *patrolCameraRef) (*patrolStreamOut, error) {
	org, err := o.admit(ctx, staff...)
	if err != nil {
		return nil, err
	}
	camera := strings.TrimSpace(in.Name)
	credential, err := o.credential(ctx, org, camera)
	if err != nil {
		// A camera with nothing registered is an absence and says so; anything
		// else is a fault reading the vault and is worth a line.
		if !errors.Is(err, errNoStream) {
			o.s.Log.Warn("patrol: camera credential unreadable", "org", org, "camera", camera, "err", err)
		}
		return nil, zip.ErrNotFound("camera has no stream")
	}
	key, err := o.signing(ctx, org)
	if err != nil {
		o.s.Log.Warn("patrol: stream key unavailable", "org", org, "err", err)
		return nil, zip.ErrNotFound("camera has no stream")
	}
	until := time.Now().UTC().Add(streamLife)
	address, ok := mint(credential, key, org, camera, until)
	if !ok {
		o.s.Log.Warn("patrol: camera credential is not an address", "org", org, "camera", camera)
		return nil, zip.ErrNotFound("camera has no stream")
	}
	return &patrolStreamOut{Data: patrolStream{
		URL:     address,
		Expires: until.Format(stampLayout),
	}}, nil
}

// errNoStream is there being nothing to open: no KMS in this deployment, no
// signing key kept for the org, or a camera with no credential registered. All
// three are 404 at the surface, so the routes say them with one value — and a
// value distinct from a fault reading the vault, which is not an absence.
var errNoStream = errors.New("patrol: no stream")

// credential is what opens one camera, read from the one place a camera's
// credential is kept.
func (o ops) credential(ctx context.Context, org, camera string) (string, error) {
	if o.s.KMS == nil {
		return "", errNoStream
	}
	d, err := framework.Get(ctx, org, DTCamera, camera)
	if err != nil || strField(d, "secret") != kept(org, camera) {
		return "", errNoStream
	}
	secret, err := o.s.KMS.GetSecret(ctx, kept(org, camera))
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(string(secret))
	if address == "" {
		return "", errNoStream
	}
	return address, nil
}

// kept is where one camera's credential is kept: the org's own patrol namespace,
// named for the camera. It is computed from the two facts that identify the
// camera, so no value anyone writes can point this route at another secret.
func kept(org, camera string) string { return org + "/patrol/camera/" + camera }

// mint turns a credential into an address a caller may hold: the camera's own
// location with everything but scheme, host and path dropped, and an expiry the
// signature covers.
//
// The signature is keyed by `key` and never by the credential. A tag keyed by the
// secret it withholds is an oracle on that secret: every other input rides in the
// response beside it, so the holder tests a guess by recomputing the tag, offline
// and as fast as they like — and a camera password is a short string from a small
// alphabet. The key is a value nobody can guess and nobody needs but this process
// and the ticket's reader.
func mint(credential string, key []byte, org, camera string, until time.Time) (string, bool) {
	u, err := url.Parse(credential)
	if err != nil || u.Host == "" {
		return "", false
	}
	stamp := strconv.FormatInt(until.Unix(), 10)
	bare := url.URL{Scheme: u.Scheme, Host: u.Hostname(), Path: u.Path}
	if p := u.Port(); p != "" {
		bare.Host = net.JoinHostPort(u.Hostname(), p)
	}
	bare.RawQuery = url.Values{
		"expires": {stamp},
		"ticket":  {ticketOf(key, org, camera, stamp)},
	}.Encode()
	return bare.String(), true
}

// ticketOf is the ticket a minted address carries: the tenant it was minted in,
// and the tag over that tenant, the camera and the moment.
//
// The tenant travels inside it because whatever redeems it holds the address and
// nothing else — no session, no org named on the call. The tag covers the tenant,
// so naming another one verifies against nothing.
func ticketOf(key []byte, org, camera, stamp string) string {
	return org + "." + tag(key, org, camera, stamp)
}

// opens splits a presented ticket into the tenant it names and its tag.
func opens(ticket string) (string, string, bool) {
	org, sig, cut := strings.Cut(strings.TrimSpace(ticket), ".")
	if !cut || sig == "" || !principal.NamesOrg(org) {
		return "", "", false
	}
	return org, sig, true
}

// tag binds a ticket to one camera, until one moment, under one deployment's key.
func tag(key []byte, org, camera, stamp string) string {
	sig := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(sig, "%s\n%s\n%s", org, camera, stamp)
	return hex.EncodeToString(sig.Sum(nil))
}

// keyRef is where an org's ticket-signing key is kept: its own patrol namespace,
// beside the camera credentials.
func keyRef(org string) string { return org + "/patrol/stream/key" }

// held is the signing key an org already has, and it never mints one. Redeeming
// a ticket is reachable by whoever holds it, so the org on that call is a name a
// stranger supplied — and a name supplied must not be how this deployment comes
// to keep a secret for it.
func (o ops) held(ctx context.Context, org string) ([]byte, error) {
	if o.s.KMS == nil {
		return nil, errNoStream
	}
	key, err := o.s.KMS.GetSecret(ctx, keyRef(org))
	if err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, errNoStream
	}
	return key, nil
}

// signing is that key, minted the first time an org mints a ticket.
//
// Minted ONCE. Two callers that both found none would both generate and both
// write, and each would answer with whatever its OWN read-back saw — so every
// ticket signed under the loser's key verifies nowhere, for the five minutes it
// is live. keying holds the second caller until the first is done and hands it
// the same answer, and the read-back after the write settles a second process
// racing this one.
func (o ops) signing(ctx context.Context, org string) ([]byte, error) {
	if key, err := o.held(ctx, org); err == nil {
		return key, nil
	}
	key, err, _ := keying.Do(keyRef(org), func() (any, error) {
		if key, err := o.held(ctx, org); err == nil {
			return key, nil
		}
		if o.s.KMS == nil {
			return nil, errNoStream
		}
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, err
		}
		if err := o.s.KMS.PutSecret(ctx, keyRef(org), fresh); err != nil {
			return nil, err
		}
		if key, err := o.held(ctx, org); err == nil {
			return key, nil
		}
		return fresh, nil
	})
	if err != nil {
		return nil, err
	}
	return key.([]byte), nil
}

// keying is an org's first mint, once per process.
var keying singleflight.Group

// patrolTicket is one minted ticket, presented back.
type patrolTicket struct {
	// Name is the camera's document name, from the path.
	Name string `json:"name"`
	// Ticket is the tag the minted address carries, from the path.
	Ticket string `json:"ticket"`
	// Expires is the moment the tag covers, as the seconds the minted address
	// carries in its `expires` parameter.
	Expires string `json:"expires"`
}

// patrolTicketOut is what a live ticket opens.
type patrolTicketOut struct {
	// Data is the address that opens the camera and when the ticket stops working.
	Data patrolStream `json:"data"`
}

// ticket redeems a minted ticket for what opens the camera.
//
// The mint is the operation's — staff only — and it hands back the camera's
// address with the credential stripped out of it. This is where the holder of
// that address gets what actually dials the camera, and it asks for no
// credential of its own: whatever opens a camera is a relay, not a member of the
// operation, and a check only the issuer could perform checks nothing.
//
// The ticket IS the authority. It names one camera, it covers one moment, and it
// is tagged with a key nobody but this deployment holds, so holding a live one is
// the right to open that camera until it runs out — which is the whole of what
// was issued, and the reason issuing one is staff-only and five minutes long.
//
// A tag that does not verify is 404 — the route is no oracle on which tenants or
// cameras exist, or which tickets were ever issued — and one that verifies but
// has passed its moment is 410, because that ticket was real and is over.
func (o ops) ticket(ctx context.Context, in *patrolTicket) (*patrolTicketOut, error) {
	org, sig, ok := opens(in.Ticket)
	if !ok {
		return nil, zip.ErrNotFound("ticket not found")
	}
	camera := strings.TrimSpace(in.Name)
	stamp := strings.TrimSpace(in.Expires)
	until, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return nil, zip.ErrNotFound("ticket not found")
	}
	key, err := o.held(ctx, org)
	if err != nil {
		return nil, zip.ErrNotFound("ticket not found")
	}
	if subtle.ConstantTimeCompare([]byte(tag(key, org, camera, stamp)), []byte(sig)) != 1 {
		return nil, zip.ErrNotFound("ticket not found")
	}
	expiry := time.Unix(until, 0).UTC()
	if !time.Now().UTC().Before(expiry) {
		return nil, zip.Errorf(http.StatusGone, "ticket expired at %s", expiry.Format(stampLayout))
	}
	address, err := o.credential(ctx, org, camera)
	if err != nil {
		return nil, zip.ErrNotFound("ticket not found")
	}
	return &patrolTicketOut{Data: patrolStream{URL: address, Expires: expiry.Format(stampLayout)}}, nil
}

// streamLife is how long a minted camera address is good for.
const streamLife = 5 * time.Minute

// ---- events ----

// patrolEvent is one row of the live feed.
type patrolEvent struct {
	// Name is the event's document name.
	Name string `json:"name"`
	// Level is red, amber, green or cyan.
	Level string `json:"level"`
	// Title is the one line the feed shows.
	Title string `json:"title"`
	// Detail is the longer account, or empty.
	Detail string `json:"detail"`
	// Site is the site the event is about, or empty.
	Site string `json:"site"`
	// Unit is the unit the event is about, or empty.
	Unit string `json:"unit"`
	// Source is what raised it.
	Source string `json:"source"`
	// At is when it was raised.
	At string `json:"at"`
}

// patrolEventQuery selects which feed rows to list.
type patrolEventQuery struct {
	// Level narrows the list to red, amber, green or cyan. Empty lists every
	// level.
	Level string `json:"level"`
	// Limit caps how many rows are returned. Anything that is not a positive
	// integer uses 80, and higher values are clamped to 80.
	Limit int `json:"limit"`
}

// patrolEventList is the feed as a list.
type patrolEventList struct {
	// Data is the matching rows, newest first.
	Data []patrolEvent `json:"data"`
}

// events lists the feed, newest first. It is the same rows the live stream
// carries, read back for a screen that has just opened — so the window is the
// newest rows, or a screen that reloads reads a feed that stopped on the day the
// tenant was installed.
func (o ops) events(ctx context.Context, in *patrolEventQuery) (*patrolEventList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	var among map[string][]string
	if l := strings.TrimSpace(in.Level); l != "" {
		among = map[string][]string{"level": {l}}
	}
	docs := o.seen(ctx, org, DTEvent, among, clamp(in.Limit, maxEvents), o.reachOf(ctx, org))
	out := make([]patrolEvent, 0, len(docs))
	for _, d := range docs {
		out = append(out, viewEvent(d))
	}
	return &patrolEventList{Data: out}, nil
}

// patrolEventIn raises one feed row by hand.
type patrolEventIn struct {
	// Level is red, amber, green or cyan.
	Level string `json:"level"`
	// Title is the one line the feed shows.
	Title string `json:"title"`
	// Detail is the longer account.
	Detail string `json:"detail"`
	// Site is the site it is about, by document name.
	Site string `json:"site"`
	// Unit is the unit it is about, by call sign.
	Unit string `json:"unit"`
	// Source is what raised it.
	Source string `json:"source"`
}

// patrolEventOut is one feed row.
type patrolEventOut struct {
	// Data is the row as recorded.
	Data patrolEvent `json:"data"`
}

// raiseEvent writes one feed row by hand and pushes it to every screen watching.
// It is how a controller puts something on the record that no automatic write
// would have produced.
func (o ops) raiseEvent(ctx context.Context, in *patrolEventIn) (*patrolEventOut, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	title := text.Trim(in.Title, maxTrigger)
	if title == "" {
		return nil, zip.ErrBadRequest("title is required")
	}
	level := strings.ToLower(strings.TrimSpace(in.Level))
	switch level {
	case LevelRed, LevelAmber, LevelGreen, LevelCyan:
	default:
		level = LevelCyan
	}
	row := map[string]any{"level": level, "title": title, "at": now()}
	set(row, "detail", text.Trim(in.Detail, maxDetail))
	set(row, "site", in.Site)
	set(row, "unit", in.Unit)
	set(row, "source", in.Source)
	made, err := framework.Ingest(ctx, org, DTEvent, row, "")
	if err != nil {
		return nil, o.fail(err, "raise event")
	}
	d, err := framework.Get(ctx, org, DTEvent, made.Name)
	if err != nil {
		return nil, o.fail(err, "read event")
	}
	v := viewEvent(d)
	o.publish(ctx, org, frame{Kind: "event", Event: &v})
	return &patrolEventOut{Data: v}, nil
}

// ---- incidents ----

// patrolAct is one act on an incident's trail.
type patrolAct struct {
	// Act is the act performed.
	Act string `json:"act"`
	// From is the state the incident left.
	From string `json:"from"`
	// To is the state it landed in.
	To string `json:"to"`
	// Actor is who performed it.
	Actor string `json:"actor"`
	// Detail is what they wrote with it.
	Detail string `json:"detail"`
	// At is when it happened.
	At string `json:"at"`
}

// patrolClock is one SLA clock, derived from the incident's stamps.
type patrolClock struct {
	// Label names the clock: Verify, Dispatch or On site.
	Label string `json:"label"`
	// Elapsed is the seconds run so far, or the seconds it took when settled.
	Elapsed int `json:"elapsed"`
	// Target is the contracted seconds.
	Target int `json:"target"`
	// Settled is the seconds it took, or -1 while the clock is still running.
	Settled int `json:"settled"`
}

// patrolIncident is one response.
type patrolIncident struct {
	// Name is the incident's document name.
	Name string `json:"name"`
	// Site is where it is happening.
	Site string `json:"site"`
	// Zone is the panel zone that reported.
	Zone string `json:"zone"`
	// Ref is the receiving centre's own reference.
	Ref string `json:"ref"`
	// Trigger is what the panel reported.
	Trigger string `json:"trigger"`
	// Tier is the site's contracted service tier.
	Tier string `json:"tier"`
	// SLA is the contracted response time in minutes.
	SLA int64 `json:"sla"`
	// State is one of received, verified, dispatched, enroute, onsite, checked,
	// secured, filed and closed.
	State string `json:"state"`
	// Unit is the call sign attending, or empty.
	Unit string `json:"unit"`
	// Opened is when the activation landed.
	Opened string `json:"opened"`
	// Verified is when the activation was confirmed real.
	Verified string `json:"verified"`
	// Dispatched is when a unit was assigned.
	Dispatched string `json:"dispatched"`
	// Enroute is when that unit started moving.
	Enroute string `json:"enroute"`
	// Onsite is when it arrived.
	Onsite string `json:"onsite"`
	// Checked is when the premises had been walked.
	Checked string `json:"checked"`
	// Secured is when the site was made safe.
	Secured string `json:"secured"`
	// Filed is when the report was written.
	Filed string `json:"filed"`
	// Closed is when the incident was closed.
	Closed string `json:"closed"`
	// Escalated is when it was escalated, or empty.
	Escalated string `json:"escalated"`
	// Outcome is what it came to.
	Outcome string `json:"outcome"`
	// False says the activation turned out to be nothing.
	False bool `json:"false"`
	// Steps is the act trail, present on a single-incident read.
	Steps []patrolAct `json:"steps,omitempty"`
	// Clocks is the SLA clocks, present on a single-incident read.
	Clocks []patrolClock `json:"clocks,omitempty"`
}

// patrolIncidentQuery selects which incidents to list.
type patrolIncidentQuery struct {
	// Open lists only the incidents that are not closed when it is 1.
	Open int `json:"open"`
	// Limit caps how many are returned. Anything that is not a positive integer
	// uses 50, and higher values are clamped to 50.
	Limit int `json:"limit"`
}

// patrolIncidentList is the incidents.
type patrolIncidentList struct {
	// Data is the matching incidents, without their trails.
	Data []patrolIncident `json:"data"`
}

// incidents lists responses. ?open=1 narrows it to the ones still running.
func (o ops) incidents(ctx context.Context, in *patrolIncidentQuery) (*patrolIncidentList, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	var among map[string][]string
	if in.Open == 1 {
		among = map[string][]string{"state": openStates}
	}
	docs := o.seen(ctx, org, DTIncident, among, clamp(in.Limit, maxIncidents), o.reachOf(ctx, org))
	out := make([]patrolIncident, 0, len(docs))
	for _, d := range docs {
		out = append(out, incidentRow(d))
	}
	return &patrolIncidentList{Data: out}, nil
}

// openStates are the eight an incident is in while the response is still
// running. Closed is the ninth and the only one that is not, so "still open" is
// a question about a set — one query, not one per state.
var openStates = []string{Received, Verified, Dispatched, Enroute, Onsite, Checked, Secured, Filed}

// patrolIncidentRef addresses one incident.
type patrolIncidentRef struct {
	// Name is the incident's document name, from the path.
	Name string `json:"name"`
}

// patrolIncidentOut is one incident.
type patrolIncidentOut struct {
	// Data is the incident with its act trail and SLA clocks.
	Data patrolIncident `json:"data"`
}

// incident returns one response whole: the incident, every act performed on it,
// and the SLA clocks those acts settled. The clocks are derived from the stamps
// and stored nowhere.
func (o ops) incident(ctx context.Context, in *patrolIncidentRef) (*patrolIncidentOut, error) {
	org, err := o.admit(ctx, everyone...)
	if err != nil {
		return nil, err
	}
	d, err := framework.Get(ctx, org, DTIncident, strings.TrimSpace(in.Name))
	if err != nil {
		return nil, zip.ErrNotFound("incident not found")
	}
	r := o.reachOf(ctx, org)
	if !r.sees(strField(d, "site")) {
		return nil, zip.ErrNotFound("incident not found")
	}
	return &patrolIncidentOut{Data: *o.viewIncident(ctx, org, d, r)}, nil
}

// patrolIncidentIn opens one incident by hand.
type patrolIncidentIn struct {
	// Site is where it is happening, by document name.
	Site string `json:"site"`
	// Zone is the panel zone that reported.
	Zone string `json:"zone"`
	// Ref is the receiving centre's own reference.
	Ref string `json:"ref"`
	// Trigger is what was reported.
	Trigger string `json:"trigger"`
}

// openIncident records a response by hand, in state received and carrying the
// site's tier and SLA. It is what a controller calls when an activation arrives
// by phone rather than over the intake.
func (o ops) openIncident(ctx context.Context, in *patrolIncidentIn) (*patrolIncidentOut, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	st, err := framework.Get(ctx, org, DTSite, strings.TrimSpace(in.Site))
	if err != nil {
		return nil, zip.ErrNotFound("site not found")
	}
	inc, err := o.open(ctx, org, st,
		text.Trim(in.Zone, maxRef), text.Trim(in.Ref, maxRef), text.Trim(in.Trigger, maxTrigger))
	if err != nil {
		return nil, o.fail(err, "open incident")
	}
	row := incidentRow(inc)
	o.publish(ctx, org, frame{Kind: "incident", Incident: &row})
	return &patrolIncidentOut{Data: *o.viewIncident(ctx, org, inc, o.reachOf(ctx, org))}, nil
}

// patrolIncidentAct performs one act on one incident.
type patrolIncidentAct struct {
	// Name is the incident's document name, from the path.
	Name string `json:"name" url:"name"`
	// Act is VERIFY, DISPATCH, ROLL, ARRIVE, CHECK, SECURE, FILE, CLOSE or
	// ESCALATE.
	Act string `json:"act"`
	// Unit is the call sign to send, read by DISPATCH.
	Unit string `json:"unit"`
	// Outcome is what the incident came to, read by CLOSE.
	Outcome string `json:"outcome"`
	// False says the activation turned out to be nothing, read by CLOSE.
	False bool `json:"false"`
	// Detail is what to write on the trail with this act.
	Detail string `json:"detail"`
}

// stepIncident performs one act on an incident: it stamps the act's time, moves
// the state, appends the act to the trail, and runs what the act sets in motion —
// a unit assigned, a site secured, a report filed, a controller told.
//
// The states run forward only. An act from a state it does not leave is 409
// naming the state the incident is in, and a caller without the act's roles is
// refused.
func (o ops) stepIncident(ctx context.Context, in *patrolIncidentAct) (*patrolIncidentOut, error) {
	inc, err := o.advance(ctx, strings.TrimSpace(in.Name), in)
	if err != nil {
		return nil, err
	}
	org, _ := principal.OrgFrom(ctx)
	return &patrolIncidentOut{Data: *o.viewIncident(ctx, org, inc, o.reachOf(ctx, org))}, nil
}

// ---- activations ----

// patrolAlarmIn is one activation as the receiving centre reports it.
type patrolAlarmIn struct {
	// Ref is the receiving centre's own reference for the activation.
	Ref string `json:"ref"`
	// Account is the alarm-panel account number that reported. It is what an
	// activation matches a site on when no site is named.
	Account string `json:"account"`
	// Site names the site directly, by document name, when the caller already
	// knows it.
	Site string `json:"site"`
	// Zone is the panel zone that fired.
	Zone string `json:"zone"`
	// Trigger is what the panel reported.
	Trigger string `json:"trigger"`
	// Source is which receiver or operator the activation came through.
	Source string `json:"source"`
	// Raw is the activation exactly as it arrived, kept verbatim up to 16 KiB.
	// A panel's own idea of the time rides in here; when the activation was
	// received is the server's stamp, which is the fact an insurer and a court
	// read off it and the one every SLA clock runs from.
	Raw json.RawMessage `json:"raw"`
}

// patrolActivation says what became of an activation.
type patrolActivation struct {
	// Alarm is the recorded activation's document name.
	Alarm string `json:"alarm"`
	// Matched says whether a site was found for it.
	Matched bool `json:"matched"`
	// Site is the site it matched, or empty.
	Site string `json:"site"`
	// Incident is the incident it opened, or null when it matched no site.
	Incident *patrolIncident `json:"incident"`
}

// patrolAlarmOut is the intake envelope. It answers 201 when the activation opened
// an incident and 202 when it matched no site and was only recorded.
type patrolAlarmOut struct {
	// Data is what became of the activation.
	Data patrolActivation `json:"data"`
}

// StatusCode says which of the intake's two answers this is: 201 when a site
// matched and an incident is open, 202 when the activation was recorded against
// no site.
func (a *patrolAlarmOut) StatusCode() int {
	if a.Data.Matched {
		return http.StatusCreated
	}
	return http.StatusAccepted
}

// alarm takes one activation. It records the activation whatever happens, then
// matches a site — the one the caller named, else the one whose account number
// reported — and on a match opens an incident in state received with that site's
// tier and SLA, puts the site into alarm, raises a red feed row and tells the
// controllers. An activation that matches no site is still recorded, raises an
// amber row, and answers 202: an activation is never dropped.
//
// It admits the receiving centre's own credential and a controller keying one in
// by hand. The centre holds the `centre` role its tenant granted it, which posts
// an activation and reads nothing, like every other identity here.
//
// The centre's own reference identifies the activation while the response it
// opened is still running, so a redelivery is answered with that response rather
// than dispatching a second unit. Centres recycle references, so once the
// response has closed the same one is the next activation.
func (o ops) alarm(ctx context.Context, in *patrolAlarmIn) (*patrolAlarmOut, error) {
	org, err := o.admitAlarm(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.installed(ctx, org); err != nil {
		return nil, err
	}
	if len(in.Raw) > maxRaw {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "activation too large")
	}
	if !o.afford(ctx, org) {
		return nil, zip.Errorf(http.StatusTooManyRequests, "too many activations")
	}
	return o.take(ctx, org, in)
}

// ---- notes ----

// patrolNote is one internal remark.
type patrolNote struct {
	// Name is the note's document name.
	Name string `json:"name"`
	// Subject is what the note is about.
	Subject string `json:"subject"`
	// Body is the remark.
	Body string `json:"body"`
	// Actor is who wrote it.
	Actor string `json:"actor"`
	// At is when it was written.
	At string `json:"at"`
}

// patrolNoteQuery selects which notes to list.
type patrolNoteQuery struct {
	// Subject narrows the list to one subject by exact match. Empty lists every
	// note.
	Subject string `json:"subject"`
}

// patrolNoteList is the notes.
type patrolNoteList struct {
	// Data is the matching notes in store order.
	Data []patrolNote `json:"data"`
}

// notes lists internal remarks. The client never reaches them: patrol.note is
// its own DocType and its permissions do not name the client, so there is no
// note row for a customer to read.
func (o ops) notes(ctx context.Context, in *patrolNoteQuery) (*patrolNoteList, error) {
	org, err := o.admit(ctx, staff...)
	if err != nil {
		return nil, err
	}
	filters := map[string]string{}
	if s := strings.TrimSpace(in.Subject); s != "" {
		filters["subject"] = s
	}
	docs, err := framework.Search(ctx, org, DTNote, filters, maxNotes)
	if err != nil {
		return nil, o.fail(err, "list notes")
	}
	out := make([]patrolNote, 0, len(docs))
	for _, d := range docs {
		out = append(out, patrolNote{
			Name: d.Name, Subject: strField(d, "subject"), Body: strField(d, "body"),
			Actor: strField(d, "actor"), At: strField(d, "at"),
		})
	}
	return &patrolNoteList{Data: out}, nil
}

// patrolNoteIn writes one internal remark.
type patrolNoteIn struct {
	// Subject is what the note is about.
	Subject string `json:"subject"`
	// Body is the remark.
	Body string `json:"body"`
}

// patrolNoteOut is one note.
type patrolNoteOut struct {
	// Data is the note as written.
	Data patrolNote `json:"data"`
}

// writeNote records an internal remark, stamped with who wrote it.
func (o ops) writeNote(ctx context.Context, in *patrolNoteIn) (*patrolNoteOut, error) {
	org, err := o.admit(ctx, staff...)
	if err != nil {
		return nil, err
	}
	subject := text.Trim(in.Subject, maxTrigger)
	if subject == "" {
		return nil, zip.ErrBadRequest("subject is required")
	}
	made, err := framework.Ingest(ctx, org, DTNote, map[string]any{
		"subject": subject,
		"body":    text.Trim(in.Body, maxDetail),
		"actor":   actor(ctx),
		"at":      now(),
	}, "")
	if err != nil {
		return nil, o.fail(err, "write note")
	}
	d, err := framework.Get(ctx, org, DTNote, made.Name)
	if err != nil {
		return nil, o.fail(err, "read note")
	}
	return &patrolNoteOut{Data: patrolNote{
		Name: d.Name, Subject: strField(d, "subject"), Body: strField(d, "body"),
		Actor: strField(d, "actor"), At: strField(d, "at"),
	}}, nil
}

// ---- reports ----

// patrolReport is one filed report.
type patrolReport struct {
	// Name is the report's document name and the segment
	// /v1/patrol/report/{name} addresses it by.
	Name string `json:"name"`
	// Kind is shift, incident or client.
	Kind string `json:"kind"`
	// From is the start of the window the report covers.
	From string `json:"from"`
	// To is the end of that window.
	To string `json:"to"`
	// Incident is the incident it is about, or empty.
	Incident string `json:"incident"`
	// Client is the customer it is about, or empty.
	Client string `json:"client"`
	// Body is the report as text.
	Body string `json:"body"`
	// At is when it was filed.
	At string `json:"at"`
}

// patrolReportIn asks for a report.
type patrolReportIn struct {
	// Kind is shift, incident or client.
	Kind string `json:"kind"`
	// From is the start of the window to cover.
	From string `json:"from"`
	// To is the end of that window.
	To string `json:"to"`
	// Incident is the incident to report on, required for kind incident.
	Incident string `json:"incident"`
	// Client is the customer to report on, read by kind client. Naming one files
	// an account that customer may download; leaving it empty covers every
	// customer, which is an account of the operation and stays with it.
	Client string `json:"client"`
}

// patrolReportOut is one report.
type patrolReportOut struct {
	// Data is the report as filed.
	Data patrolReport `json:"data"`
}

// makeReport composes and files a report from one estate read, so the same
// figures read the same a month later. The text is stored on the document; the
// PDF is produced on read at /v1/patrol/report/{name}.
//
// A client report naming a customer is an account of that customer's estate and
// is theirs to download; naming none covers every customer, which is an account
// of the operation.
func (o ops) makeReport(ctx context.Context, in *patrolReportIn) (*patrolReportOut, error) {
	org, err := o.admit(ctx, control...)
	if err != nil {
		return nil, err
	}
	ask := patrolReportIn{
		Kind:     strings.ToLower(strings.TrimSpace(in.Kind)),
		From:     strings.TrimSpace(in.From),
		To:       strings.TrimSpace(in.To),
		Incident: strings.TrimSpace(in.Incident),
		Client:   strings.TrimSpace(in.Client),
	}
	switch ask.Kind {
	case ReportShift, ReportIncident, ReportClient:
	default:
		return nil, zip.ErrBadRequest("kind is shift, incident or client")
	}
	if ask.Kind == ReportIncident && ask.Incident == "" {
		return nil, zip.ErrBadRequest("an incident report needs an incident")
	}
	if ask.Kind != ReportClient {
		ask.Client = ""
	}
	if ask.Client != "" {
		if _, err := framework.Get(ctx, org, DTClient, ask.Client); err != nil {
			return nil, zip.ErrNotFound("client not found")
		}
	}
	d, err := o.file(ctx, org, ask)
	if err != nil {
		return nil, err
	}
	return &patrolReportOut{Data: patrolReport{
		Name: d.Name, Kind: strField(d, "kind"), From: strField(d, "from"),
		To: strField(d, "to"), Incident: strField(d, "incident"),
		Client: strField(d, "client"), Body: strField(d, "body"), At: strField(d, "at"),
	}}, nil
}

// ---- projections ----

// viewSite projects one site. whole carries the zones and hazards that ride
// inside the document; a list leaves them out.
func viewSite(d framework.Document, whole bool) patrolSite {
	v := patrolSite{
		Name: d.Name, Ref: intField(d, "ref"), Account: strField(d, "account"),
		Category: strField(d, "category"), Town: strField(d, "town"),
		Eircode: strField(d, "eircode"), Lat: floatField(d, "lat"), Lon: floatField(d, "lon"),
		Risk: intField(d, "risk"), Tier: strField(d, "tier"), SLA: intField(d, "sla"),
		Sector: strField(d, "sector"), Unit: strField(d, "unit"),
		Keys: intField(d, "keys") == 1, Cameras: intField(d, "cameras"),
		Visits: intField(d, "visits"), Contact: strField(d, "contact"),
		Status: strField(d, "status"), Client: strField(d, "client"), Notes: strField(d, "notes"),
	}
	if !whole {
		return v
	}
	v.Zones, v.Hazards = []patrolZone{}, []string{}
	for _, z := range children(d, "zones") {
		v.Zones = append(v.Zones, patrolZone{
			Ref: str(z["ref"]), Location: str(z["location"]),
			Device: str(z["device"]), State: str(z["state"]),
		})
	}
	for _, h := range children(d, "hazards") {
		v.Hazards = append(v.Hazards, str(h["hazard"]))
	}
	return v
}

// viewUnit projects one unit. trail carries the tail of its recorded positions.
func (o ops) viewUnit(ctx context.Context, org string, d framework.Document, trail bool) patrolUnit {
	v := patrolUnit{
		Name: d.Name, Officer: strField(d, "officer"), Sector: strField(d, "sector"),
		State: strField(d, "state"), Lat: floatField(d, "lat"), Lon: floatField(d, "lon"),
		Seen: strField(d, "seen"), Due: strField(d, "due"),
	}
	if !trail {
		return v
	}
	fixes, err := framework.Search(ctx, org, DTFix, map[string]string{"unit": d.Name}, maxFixes)
	if err != nil {
		return v
	}
	if len(fixes) > trailFixes {
		fixes = fixes[:trailFixes]
	}
	v.Trail = make([]patrolFix, 0, len(fixes))
	for _, f := range oldest(fixes) {
		v.Trail = append(v.Trail, patrolFix{Lat: floatField(f, "lat"), Lon: floatField(f, "lon"), At: strField(f, "at")})
	}
	return v
}

// viewPoint projects one checkpoint.
func viewPoint(d framework.Document) patrolCheckpoint {
	return patrolCheckpoint{
		Name: d.Name, Tour: strField(d, "tour"), Site: strField(d, "site"),
		Tag: strField(d, "tag"), Method: strField(d, "method"),
		Open: strField(d, "open"), Close: strField(d, "close"), At: strField(d, "at"),
		Lat: floatField(d, "lat"), Lon: floatField(d, "lon"),
		Photo: strField(d, "photo"), Actor: strField(d, "actor"), State: strField(d, "state"),
	}
}

// viewKey projects one key set.
func viewKey(d framework.Document) patrolKey {
	return patrolKey{
		Name: d.Name, Site: strField(d, "site"), Where: strField(d, "where"),
		Holder: strField(d, "holder"), Moved: strField(d, "moved"),
	}
}

// viewEvent projects one feed row.
func viewEvent(d framework.Document) patrolEvent {
	return patrolEvent{
		Name: d.Name, Level: strField(d, "level"), Title: strField(d, "title"),
		Detail: strField(d, "detail"), Site: strField(d, "site"), Unit: strField(d, "unit"),
		Source: strField(d, "source"), At: strField(d, "at"),
	}
}

// incidentRow projects one incident without its trail — the list shape.
func incidentRow(d framework.Document) patrolIncident {
	return patrolIncident{
		Name: d.Name, Site: strField(d, "site"), Zone: strField(d, "zone"),
		Ref: strField(d, "ref"), Trigger: strField(d, "trigger"), Tier: strField(d, "tier"),
		SLA: intField(d, "sla"), State: strField(d, "state"), Unit: strField(d, "unit"),
		Opened: strField(d, "opened"), Verified: strField(d, "verified"),
		Dispatched: strField(d, "dispatched"), Enroute: strField(d, "enroute"),
		Onsite: strField(d, "onsite"), Checked: strField(d, "checked"),
		Secured: strField(d, "secured"), Filed: strField(d, "filed"),
		Closed: strField(d, "closed"), Escalated: strField(d, "escalated"),
		Outcome: strField(d, "outcome"), False: intField(d, "false") == 1,
	}
}

// viewIncident projects one incident with its act trail and its SLA clocks.
//
// reach decides WHICH incident a caller reads; this decides what comes back. A
// customer is handed the response — the acts, when each landed, what it moved
// the incident from and to — and not the operation's own half of it: who worked
// it, and the free text they typed on each act. That text is the same category
// of remark patrol.note is a separate DocType to keep from them.
func (o ops) viewIncident(ctx context.Context, org string, d framework.Document, r reach) *patrolIncident {
	v := incidentRow(d)
	v.Steps = o.steps(ctx, org, d.Name, r.all)
	v.Clocks = o.clocks(ctx, org, d)
	return &v
}

// steps is one incident's act trail. `whole` carries the actor and the detail,
// which are the operation's to read.
func (o ops) steps(ctx context.Context, org, incident string, whole bool) []patrolAct {
	docs, err := framework.Search(ctx, org, DTStep, map[string]string{"incident": incident}, maxSteps)
	if err != nil {
		return []patrolAct{}
	}
	out := make([]patrolAct, 0, len(docs))
	for _, d := range oldest(docs) {
		act := patrolAct{
			Act: strField(d, "act"), From: strField(d, "from"), To: strField(d, "to"),
			At: strField(d, "at"),
		}
		if whole {
			act.Actor, act.Detail = strField(d, "actor"), strField(d, "detail")
		}
		out = append(out, act)
	}
	performed(out)
	return out
}

// progress is the order an incident's states are reached in. No act moves an
// incident back, so the state an act lands in never comes before the one the act
// ahead of it landed in.
var progress = strings.Split(incidentStates, "\n")

// performed puts a trail in the order its acts were performed.
//
// The store reads newest first by (updated_at, name). updated_at is a whole
// second and a step's name is a hash, so two acts in one second come back in
// either order, and the trail's last act need not be the one just performed. The
// stamp says which second; within one, the state machine says which act came
// first: the one landing in an earlier state, and in one state the move into it
// before an act that stamps without moving (escalate).
func performed(trail []patrolAct) {
	rank := func(a patrolAct) int { return slices.Index(progress, a.To) }
	still := func(a patrolAct) int {
		if a.From == a.To {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(trail, func(a, b patrolAct) int {
		return cmp.Or(strings.Compare(a.At, b.At), cmp.Compare(rank(a), rank(b)), cmp.Compare(still(a), still(b)))
	})
}

// ---- values ----

// now is the moment, in the layout the engine stores a Datetime in.
func now() string { return time.Now().UTC().Format(stampLayout) }

// at reads a stored Datetime back.
func at(s string) (time.Time, bool) {
	t, err := time.Parse(stampLayout, strings.TrimSpace(s))
	return t, err == nil
}

// merge copies a document's data and overlays the change. The engine replaces a
// document's data on update, so every write sends the whole of it.
func merge(data map[string]any, change map[string]any) map[string]any {
	out := make(map[string]any, len(data)+len(change))
	maps.Copy(out, data)
	maps.Copy(out, change)
	return out
}

// set writes a value only when there is one, so an absent field is left as it
// stands.
func set(m map[string]any, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	}
}

// clamp bounds a caller's limit: not a positive integer takes the cap, and so
// does anything above it.
func clamp(n, cap int) int {
	if n <= 0 || n > cap {
		return cap
	}
	return n
}

// strField reads a string field, empty for a missing or non-string value.
func strField(d framework.Document, name string) string { return str(d.Data[name]) }

// str renders a stored value as a string.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// intField reads an Int or Check field. A stored number reaches Go as a float64
// through JSON, so both forms are read. A number written back goes as a float64:
// the engine coerces what JSON carries.
func intField(d framework.Document, name string) int64 {
	switch x := d.Data[name].(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// floatField reads a Float field.
func floatField(d framework.Document, name string) float64 {
	switch x := d.Data[name].(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

// jsonField reads a JSON field back as raw bytes, so a caller re-marshals it
// rather than re-shaping it. Absent is the JSON null.
func jsonField(d framework.Document, name string) json.RawMessage {
	v, ok := d.Data[name]
	if !ok || v == nil {
		return json.RawMessage("null")
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// children reads a child table's rows off a document.
func children(d framework.Document, name string) []map[string]any {
	rows, _ := d.Data[name].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
