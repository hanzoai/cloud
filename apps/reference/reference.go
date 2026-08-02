// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package reference is the lookup data a risk decision needs but cannot derive:
// which email domains hand out throwaway inboxes, which addresses belong to a
// datacentre or a Tor exit, which card scheme an issuer prefix belongs to, which
// browsers the fleet sees everywhere, and how current the designation lists the
// screening engine holds actually are.
//
// EVERY SET IS A VERSION, AND EVERY ANSWER NAMES IT. A version is the content
// digest of what was taken, so the same data is the same version whoever fetched
// it, and a decision can record one string that an auditor resolves back to a
// publisher, a licence and a date. A set that has never loaded REFUSES rather
// than answering "not listed", because a jurisdiction list that answers "not
// listed" because it was never loaded is indistinguishable from a clean world —
// the discipline luxfi/aml pkg/reference states and this plane generalises.
//
// FRESHNESS IS ITSELF A RISK SIGNAL, so it is on the wire: every answer carries
// the version, when its oldest contributing publisher was current, how old that
// is, and whether it is past the bound. A stale set still answers — yesterday's
// list beats none — and says that it did.
//
// TWO PLANES, TWO STORES, ONE PRECEDENCE. The Hanzo-maintained BASELINE lives in
// the shared warehouse (store.go) and its tables have NO tenant column at all:
// there is nowhere in the shape for an organisation to go, so a cross-tenant
// write is unrepresentable rather than merely forbidden. A tenant's OWN allow
// and deny entries live in that organisation's own SQLite file (override.go),
// reached through the one door a validated org walks through. Resolution is
// override first, then baseline; first hit wins.
//
// WHAT MAY BE IN THE BASELINE. Data someone else published under terms we hold —
// every source in the catalog states its licence — and aggregates over fleet
// traffic that pass a k-anonymity floor no single organisation can reach alone.
// Nothing derived from one organisation's rows, ever. Where a source we would
// want needs a licence we do not have, the catalog declares it as a SEAM that
// refuses, because an unlicensed set and an absent one look identical from the
// outside and only one of them is a decision.
//
// Surface (/v1 only):
//
//	GET    /v1/ml/reference            every set, its version and its freshness
//	GET    /v1/ml/reference/{set}      one set, plus this org's overrides
//	PUT    /v1/ml/reference/{set}      write this org's overrides
//	DELETE /v1/ml/reference/{set}      clear one of this org's overrides
//	POST   /v1/ml/reference/resolve    resolve keys, naming the version consulted
//	POST   /v1/ml/reference/refresh    take a new version of a set (SuperAdmin)
package reference

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/reference openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// parentPrefix and leaf compose the ONE address this app answers on,
	// /v1/ml/reference. They are separate because the collection route has to be
	// declared on the parent to avoid a trailing slash, and a prefix written twice
	// is a prefix that can disagree with itself.
	parentPrefix = "/v1/ml"
	leaf         = "/reference"
	// subsystem names this app's per-org store file: {DataDir}/orgs/{org}/reference.db.
	subsystem = "reference"
	// maxKeys bounds one resolve call, so a lookup cannot be turned into a scan.
	maxKeys = 100
	// maxWrite bounds one override write.
	maxWrite = 1000
	// maxNote bounds an override's free-text note.
	maxNote = 512
	// pageSize and maxPage bound an override listing.
	pageSize = 200
	maxPage  = 1000
	// settle is how long the mount waits between attempts to hydrate from a
	// warehouse that is not up yet.
	settle = 30 * time.Second
	// beat is how often the plane re-hydrates and re-takes anything half-aged.
	beat = 6 * time.Hour
	// refreshTimeout bounds one whole set refresh.
	refreshTimeout = 10 * time.Minute
)

// state is this app's own data. The snapshots are process state and the
// overrides are per-org files; shared deps live in the embedded cloud.Base.
type state struct {
	plane *plane
	own   *cloud.OrgStore[*overrides]
	get   download
	now   func() time.Time
	work  *work
}

// work is the refresh worker's own coordination, held behind a pointer so the
// state value the Service carries has no lock in it to copy.
type work struct {
	// taking serialises refreshes. One at a time, process-wide: a refresh is a
	// handful of downloads and a bulk load into a single-pod warehouse, and
	// letting two run at once is how a housekeeping job becomes an outage.
	taking sync.Mutex
	stop   chan struct{}
	done   sync.WaitGroup
}

// mounted is the process-wide handle so Shutdown can stop the loop and close the
// stores.
var mounted *cloud.Service[state]

// Mount wires /v1/ml/reference/* and starts the hydrate-and-refresh loop.
//
// The loop is started rather than the sets being loaded inline because the
// warehouse connects ASYNCHRONOUSLY: at mount time it is usually not up, and a
// mount that failed on that would abort the subsystem for a condition that
// resolves itself seconds later. Until the first hydrate succeeds every set
// refuses, which is the correct answer for a plane that has not loaded — and the
// one that matters after a restart, because this deployment runs one replica
// with a recreate rollout, so every rollout starts from nothing.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("reference.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("reference.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("reference.Mount: empty DataDir")
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, subsystem),
		State: state{
			plane: newPlane(),
			own:   cloud.NewOrgStore[*overrides](deps.DataDir, subsystem, openOverrides),
			get:   wire,
			now:   func() time.Time { return time.Now().UTC() },
			work:  &work{stop: make(chan struct{})},
		},
	}
	mounted = s
	routes(app, s)
	s.State.work.done.Add(1)
	go tend(s)
	s.Log.Info("reference mounted", "sets", len(Catalog()), "brand", deps.Brand)
	return nil
}

// Shutdown stops the loop and closes every open per-org store.
func Shutdown() error {
	s := mounted
	if s == nil {
		return nil
	}
	close(s.State.work.stop)
	s.State.work.done.Wait()
	return s.State.own.CloseAll()
}

// tend hydrates from the warehouse until it succeeds, then re-hydrates and
// re-takes half-aged sets on a slow beat.
//
// It re-takes at HALF the freshness bound rather than at the bound: a set that
// is only refreshed once it is already stale is stale for the whole interval
// between the two, and the point of the bound is that crossing it is news.
func tend(s *cloud.Service[state]) {
	defer s.State.work.done.Done()
	warm := time.NewTicker(settle)
	defer warm.Stop()
	pulse := time.NewTicker(beat)
	defer pulse.Stop()

	loaded := false
	for {
		if !loaded {
			ctx, cancel := context.WithTimeout(context.Background(), ddlTimeout)
			err := hydrate(ctx, s)
			cancel()
			if err == nil {
				loaded = true
				s.Log.Info("reference hydrated", "sets", len(s.State.plane.all()))
			}
		}
		select {
		case <-s.State.work.stop:
			return
		case <-warm.C:
			continue
		case <-pulse.C:
			ctx, cancel := context.WithTimeout(context.Background(), refreshEvery())
			for _, set := range Catalog() {
				if got := s.State.plane.get(set.Name); got != nil && !halfAged(got, s.State.now()) {
					continue
				}
				if _, err := take(ctx, s, set, nil); err != nil {
					s.Log.Warn("reference refresh", "set", set.Name, "err", err)
				}
			}
			cancel()
		}
	}
}

// refreshEvery bounds one whole pass of the beat.
func refreshEvery() time.Duration { return time.Duration(len(Catalog())) * refreshTimeout }

// halfAged reports whether a set has used up half its freshness allowance.
func halfAged(s *snap, now time.Time) bool {
	if s.set.MaxAge <= 0 || s.asOf.IsZero() {
		return true
	}
	return now.Sub(s.asOf) > s.set.MaxAge/2
}

// hydrate rebuilds every snapshot from what is already durable, taking nothing
// from the network. It is what makes a restart recover: the versions are in the
// warehouse, so the process re-reads them rather than re-fetching the world.
func hydrate(ctx context.Context, s *cloud.Service[state]) error {
	held, err := current(ctx)
	if err != nil {
		return err
	}
	by := map[string][]version{}
	for _, v := range held {
		by[v.Set] = append(by[v.Set], v)
	}
	for _, set := range Catalog() {
		took := by[set.Name]
		var entries []Entry
		if set.Kind == KindFetch || set.Kind == KindLocal {
			for _, v := range took {
				got, err := read(ctx, set.Name, v.Source, v.Version)
				if err != nil {
					return err
				}
				entries = append(entries, got...)
			}
		}
		s.State.plane.put(set.Name, build(set, took, entries))
	}
	return nil
}

// take refreshes one set: fetch or compute each source, land it, then rebuild
// the snapshot. receipts, when present, are the loader's word for an attest set.
func take(ctx context.Context, s *cloud.Service[state], set Set, receipts []ReferenceReceipt) ([]ReferenceTaken, error) {
	s.State.work.taking.Lock()
	defer s.State.work.taking.Unlock()

	if err := ensure(ctx); err != nil {
		return nil, err
	}
	now := s.State.now()
	out := make([]ReferenceTaken, 0, len(set.Sources))

	switch set.Kind {
	case KindSeam:
		return nil, fmt.Errorf("%s", set.Refusal)
	case KindAttest:
		for _, r := range receipts {
			src, ok := set.source(r.Source)
			if !ok {
				return nil, fmt.Errorf("%q publishes no source named %q", set.Name, r.Source)
			}
			v := version{
				Set: set.Name, Source: src.Name, Version: r.Version,
				Origin: src.Origin, Terms: src.Terms,
				AsOf: stamp(r.AsOf, now), Fetched: now,
				Keys: uint64(r.Keys), Landed: uint64(r.Keys),
				Status: statusReady, Refusal: r.Refusal,
			}
			if err := mark(ctx, v, now); err != nil {
				return nil, err
			}
			out = append(out, ReferenceTaken{Source: src.Name, Version: r.Version, Keys: r.Keys})
		}
	default:
		for _, src := range set.Sources {
			entries, err := gather(ctx, s, src, now)
			if err != nil {
				// One publisher failing does not abandon the others, and it does not
				// shrink the set either: the previous version of THIS source stays
				// current and ages out visibly on its own row.
				s.Log.Warn("reference source", "set", set.Name, "source", src.Name, "err", err)
				out = append(out, ReferenceTaken{Source: src.Name, Refusal: err.Error()})
				continue
			}
			landed, err := ingest(ctx, set.Name, src, entries, now, now)
			if err != nil {
				return nil, err
			}
			out = append(out, ReferenceTaken{
				Source: src.Name, Version: landed.Version, Keys: landed.Keys,
				Wrote: landed.Wrote, Unchanged: landed.Unchanged, Resumed: landed.Resumed,
			})
		}
	}

	if err := hydrate(ctx, s); err != nil {
		return out, err
	}
	// Prune AFTER the snapshot is rebuilt from the new version, so nothing is
	// dropped that the live plane is still reading. Non-fatal: extra history
	// costs storage, and turning that into a refresh failure would trade a
	// housekeeping problem for a freshness one.
	if got := s.State.plane.get(set.Name); got != nil && set.Kind != KindAttest {
		for _, v := range got.took {
			if err := prune(ctx, set.Name, v.Source, v.Version, v.Version); err != nil {
				s.Log.Warn("reference prune", "set", set.Name, "source", v.Source, "err", err)
			}
		}
	}
	return out, nil
}

// gather produces one source's entries: downloaded for a published source,
// computed for a local one.
func gather(ctx context.Context, s *cloud.Service[state], src Source, now time.Time) ([]Entry, error) {
	switch {
	case src.parse != nil:
		return pull(ctx, s.State.get, src, pause)
	case src.produce != nil:
		p := producer{ctx: ctx, now: now}
		if datastore.Ready() {
			p.query = datastore.Query
		}
		return src.produce(p)
	default:
		return nil, fmt.Errorf("source %q states neither a parser nor a producer", src.Name)
	}
}

// stamp reads a caller-supplied instant, defaulting to now. A receipt with no
// date is dated on arrival, which is an upper bound on its age and therefore
// safe: it can only make a list look older than it is, never fresher.
func stamp(s string, now time.Time) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return now
	}
	return t.UTC()
}

// routes registers the surface. Every route is a TYPED op — one registry entry,
// from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow.
//
// The two static leaves are registered BEFORE the parameterised ones so a set
// can never be named "resolve": first match wins, and the addressing has to be
// decided here rather than by whatever a caller sends.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// The Bridge FIRST, on the subtree this app owns: a typed op receives only a
	// context, so the validated principal has to be parked there, and fiber runs
	// middleware in registration order — one installed after these leaves would
	// never run.
	ml := app.Group(parentPrefix)
	ml.Use(cloud.Bridge())
	g := ml.Group(leaf)

	o := ops{s: s}
	zip.Post(g, "/resolve", o.resolve,
		zip.WithOperationID("mlResolveReference"),
		zip.WithTags("ml"))
	zip.Post(g, "/refresh", o.refresh,
		zip.WithOperationID("mlRefreshReference"),
		zip.WithTags("ml"))
	// The collection route is declared on the PARENT with a non-empty leaf:
	// zip.Get(g, "") normalises to a trailing slash, and op.Path is the identity
	// every projection keys on — the document, the operationId, the MCP tool and
	// every generated SDK would carry an address this API has never served.
	zip.Get(ml, leaf, o.sets,
		zip.WithOperationID("mlReferenceSets"),
		zip.WithTags("ml"))
	zip.Get(g, "/:set", o.set,
		zip.WithOperationID("mlReference"),
		zip.WithTags("ml"))
	zip.Put(g, "/:set", o.write,
		zip.WithOperationID("mlSetReference"),
		zip.WithTags("ml"))
	zip.Delete(g, "/:set", o.clear,
		zip.WithOperationID("mlClearReference"),
		zip.WithTags("ml"))
}

// ops binds the mounted Service so each op is a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// ── tenancy ──────────────────────────────────────────────────────────────────

// nsOf resolves the caller's namespace from the VALIDATED principal, and from
// nothing else.
//
// It asks principal.OrgFrom and never the request, because the org is the whole
// of what a tenant-scoped read needs, and OrgFrom carries exactly that: the
// value the identity boundary minted from the validated owner claim and parked
// on the context. FAIL CLOSED off the HTTP path — a local in-process invoke
// parks nothing, so there is no organisation to act for and the answer is the
// same 403 a forged header gets, from the same line. No In struct in this
// package carries an org, a scope or a tenant field, so this is the only way an
// organisation is ever named.
func nsOf(ctx context.Context) (namespace.Namespace, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok || strings.TrimSpace(org) == "" {
		return namespace.Namespace{}, zip.ErrForbidden("no validated principal")
	}
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return namespace.Namespace{}, zip.ErrForbidden("this org does not name a store")
	}
	return ns, nil
}

// mine opens the caller's override store, creating it on first write.
func (o ops) mine(ctx context.Context) (*overrides, error) {
	ns, err := nsOf(ctx)
	if err != nil {
		return nil, err
	}
	own, err := o.s.State.own.For(ns)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reference store: %v", err)
	}
	return own, nil
}

// held opens the caller's override store ONLY if it already exists.
//
// A read must not mint a file. Every authenticated organisation reads this
// plane, most of them have never written an override, and creating a store to
// discover it is empty makes a directory per reader for no fact gained.
func (o ops) held(ctx context.Context) (*overrides, error) {
	ns, err := nsOf(ctx)
	if err != nil {
		return nil, err
	}
	if !o.s.State.own.Has(ns) {
		return nil, nil
	}
	own, err := o.s.State.own.For(ns)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reference store: %v", err)
	}
	return own, nil
}

// actor names who wrote an override, for the record.
//
// An override is an adverse-action input — it is why a signup was refused — so
// the row has to say who wrote it, and the validated user id (X-User-Id) is a
// fact only the request carries; principal.OrgFrom carries the tenant and not
// the person. Empty off the HTTP path, which cannot happen because nsOf already
// refused there.
func actor(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return strings.TrimSpace(c.User())
	}
	return ""
}

// ── GET /v1/ml/reference ─────────────────────────────────────────────────────

// ReferenceSet is one published set and how current it is.
//
// Age and Stale are first-class because a stale list answers "not listed" for
// everything and reads exactly like a clean world — the failure this whole plane
// exists to make visible.
type ReferenceSet struct {
	// Set is the name this set is addressed by.
	Set string `json:"set"`
	// Kind is how the baseline comes to exist: fetch (downloaded from a
	// publisher), local (computed here), attest (held by the component that
	// screens against it, freshness reported), or seam (declared and NOT held,
	// because the source needs a licence we do not have).
	Kind string `json:"kind"`
	// What the set holds, in one sentence.
	What string `json:"what"`
	// Match is how a key is tested: exact, domain, net, digits, pattern or range.
	Match string `json:"match"`
	// Version is the exact baseline consulted — every contributing publisher and
	// its content digest. A decision records this and an auditor resolves it back.
	Version string `json:"version,omitempty"`
	// AsOf is when the OLDEST contributing publisher was current, RFC 3339. The
	// oldest and not the newest: a set is exactly as fresh as its weakest source.
	AsOf string `json:"asOf,omitempty"`
	// Age is how long ago that was.
	Age string `json:"age,omitempty"`
	// MaxAge is how old this set may be before it is stale.
	MaxAge string `json:"maxAge"`
	// Stale is whether it is past that bound. A stale set still answers and says
	// so, because yesterday's list beats none.
	Stale bool `json:"stale"`
	// Keys is how many members the baseline carries.
	Keys int `json:"keys"`
	// Overrides is how many entries YOUR org has laid over this baseline.
	Overrides int `json:"overrides"`
	// Sources is each contributing publisher, its licence and its own freshness.
	Sources []ReferenceSource `json:"sources,omitempty"`
	// Refusal names why the set cannot be relied on, when it cannot: never
	// loaded, held elsewhere, or a licence we do not hold. Non-empty means a
	// lookup against this set will not answer, rather than answering clean.
	Refusal string `json:"refusal,omitempty"`
}

// ReferenceSource is one publisher's contribution to a set.
type ReferenceSource struct {
	// Source is the publisher.
	Source string `json:"source"`
	// Origin is exactly where it was taken from, so it can be taken again.
	Origin string `json:"origin"`
	// Terms is the licence or permission this data is redistributed under. A
	// source with no stated terms is not in the catalog.
	Terms string `json:"terms"`
	// Version is the content digest of what this publisher last supplied. Two
	// refreshes that agree on it took the same data.
	Version string `json:"version,omitempty"`
	// AsOf is when this publisher was current, RFC 3339.
	AsOf string `json:"asOf,omitempty"`
	// Keys is how many members this publisher contributed.
	Keys int `json:"keys"`
	// Refusal is why this publisher's last take failed, if it did. The set keeps
	// its previous version of this source and ages out visibly rather than
	// silently shrinking.
	Refusal string `json:"refusal,omitempty"`
}

// ReferenceSetsOut is every set the plane publishes.
type ReferenceSetsOut struct {
	// Sets is the whole catalog, in a stable order.
	Sets []ReferenceSet `json:"sets"`
	// Stale names the sets past their freshness bound — the list to alarm on.
	Stale []string `json:"stale,omitempty"`
	// Refused names the sets that cannot be consulted at all. A key checked
	// against one of these is UNKNOWN, not clean.
	Refused []string `json:"refused,omitempty"`
}

// ReferenceSets lists every set this plane publishes, with its version and how
// fresh it is.
//
// Read the Stale and Refused lists first: they are the two ways this plane can
// be quietly wrong, and they are reported rather than inferred. A set in
// Refused answers nothing — it has never loaded, it is held by another
// component, or it names a source we hold no licence for.
//
// Example: {}
func (o ops) sets(ctx context.Context, _ *struct{}) (*ReferenceSetsOut, error) {
	own, err := o.held(ctx)
	if err != nil {
		return nil, err
	}
	now := o.s.State.now()
	out := &ReferenceSetsOut{Sets: make([]ReferenceSet, 0, len(Catalog()))}
	for _, set := range Catalog() {
		view := project(set, o.s.State.plane.get(set.Name), now)
		if own != nil {
			if n, err := own.count(set.Name); err == nil {
				view.Overrides = n
			}
		}
		if view.Stale {
			out.Stale = append(out.Stale, set.Name)
		}
		if view.Refusal != "" {
			out.Refused = append(out.Refused, set.Name)
		}
		out.Sets = append(out.Sets, view)
	}
	return out, nil
}

// project renders a set and its snapshot for the wire.
func project(set Set, s *snap, now time.Time) ReferenceSet {
	view := ReferenceSet{
		Set: set.Name, Kind: string(set.Kind), What: set.What,
		Match: string(set.Match), MaxAge: set.MaxAge.String(),
		Stale: true, Refusal: set.Refusal,
	}
	for _, src := range set.Sources {
		view.Sources = append(view.Sources, ReferenceSource{Source: src.Name, Origin: src.Origin, Terms: src.Terms})
	}
	if s == nil {
		if view.Refusal == "" {
			view.Refusal = "this set has never loaded, so it cannot tell a clean key from an unknown one"
		}
		return view
	}
	view.Version, view.Refusal, view.Stale = s.version, s.refusal, s.stale(now)
	view.Keys = len(s.byKey) + len(s.scan)
	if !s.asOf.IsZero() {
		view.AsOf = s.asOf.UTC().Format(time.RFC3339)
		view.Age = s.age(now).Truncate(time.Minute).String()
	}
	held := map[string]version{}
	for _, v := range s.took {
		held[v.Source] = v
	}
	for i, src := range view.Sources {
		v, ok := held[src.Source]
		if !ok {
			view.Sources[i].Refusal = "this publisher has never loaded"
			continue
		}
		view.Sources[i].Version = v.Version
		view.Sources[i].Keys = int(v.Keys)
		view.Sources[i].Refusal = v.Refusal
		if !v.AsOf.IsZero() {
			view.Sources[i].AsOf = v.AsOf.UTC().Format(time.RFC3339)
		}
	}
	return view
}

// ── GET /v1/ml/reference/{set} ───────────────────────────────────────────────

// ReferenceIn addresses one set and pages this org's overrides in it.
type ReferenceIn struct {
	// Set is the set to describe, from the path.
	Set string `json:"-" url:"set"`
	// After pages the override listing: the last key of the previous page.
	After string `json:"after"`
	// Limit caps the override listing: default 200, maximum 1000.
	Limit int `json:"limit"`
}

// ReferenceOut is one set and this org's own entries in it.
type ReferenceOut struct {
	// Set is the published set: its version, its freshness and its sources.
	Set ReferenceSet `json:"set"`
	// Overrides is YOUR org's entries over that baseline, in key order. They are
	// held in your organisation's own store and are not visible to any other.
	Overrides []ReferenceOverride `json:"overrides"`
	// Next is the key to page from, empty when this is the last page.
	Next string `json:"next,omitempty"`
}

// Reference describes one set and lists your org's overrides in it.
//
// The set half is public data about a published list — its version, its
// publishers, their licences and how current each one is. The overrides half is
// yours alone: it is read from your organisation's own store, and no other
// organisation's entries can appear in it.
//
// Example: {"set": "domain", "limit": 50}
func (o ops) set(ctx context.Context, in *ReferenceIn) (*ReferenceOut, error) {
	set, ok := byName(strings.ToLower(strings.TrimSpace(in.Set)))
	if !ok {
		return nil, zip.ErrNotFound("this plane publishes no set by that name")
	}
	own, err := o.held(ctx)
	if err != nil {
		return nil, err
	}
	out := &ReferenceOut{
		Set:       project(set, o.s.State.plane.get(set.Name), o.s.State.now()),
		Overrides: []ReferenceOverride{},
	}
	if own == nil {
		return out, nil
	}
	if n, err := own.count(set.Name); err == nil {
		out.Set.Overrides = n
	}
	limit := in.Limit
	if limit <= 0 {
		limit = pageSize
	}
	limit = min(limit, maxPage)
	got, err := own.list(set.Name, in.After, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reference overrides: %v", err)
	}
	out.Overrides = got
	if len(got) == limit {
		out.Next = got[len(got)-1].Key
	}
	return out, nil
}

// ── PUT /v1/ml/reference/{set} ───────────────────────────────────────────────

// ReferenceOverrideIn is one entry your org is laying over the baseline.
type ReferenceOverrideIn struct {
	// Key is the member: a domain, a CIDR or address, an issuer prefix, a
	// device digest. It is matched the same way the baseline is, so a deny on
	// tempbox.example also covers mail.tempbox.example.
	Key string `json:"key"`
	// Verdict is allow or deny, and nothing else. An override is a decision —
	// unlike a baseline entry, which states facts and leaves the decision to your
	// policy — because your organisation is the only party entitled to say "for
	// us, this one is fine".
	Verdict string `json:"verdict"`
	// Note is why, in your own words. Optional, bounded to 512 bytes.
	Note string `json:"note,omitempty"`
}

// SetReferenceIn writes your org's overrides in one set.
//
// There is deliberately no organisation, scope or tenant field: the store is
// resolved from your validated principal, so an override cannot be aimed at
// anyone else's world. That is a property of the shape, not a check.
type SetReferenceIn struct {
	// Set is the set to write in, from the path.
	Set string `json:"-" url:"set"`
	// Entries are the overrides to write, up to 1000 per call.
	Entries []ReferenceOverrideIn `json:"entries" url:"-"`
}

// SetReferenceOut is the receipt for an override write.
type SetReferenceOut struct {
	// Set is the set written in.
	Set string `json:"set"`
	// Written is how many entries this call wrote.
	Written int `json:"written"`
	// Overrides is how many your org now holds in this set.
	Overrides int `json:"overrides"`
}

// SetReference writes your organisation's own allow and deny entries over a set.
//
// Idempotent on the key: writing the same entry twice is one entry, and writing
// it again replaces the verdict and the note. The whole batch is one
// transaction, so a batch that would cross the per-set bound writes nothing
// rather than half of itself — a half-applied deny list is worse than a refused
// one, because nobody can tell which half applied.
//
// Your entries are held in your organisation's own store and are never visible
// to another organisation, and they never change what any other organisation
// sees. The shared baseline is not writable from here at all.
//
// Example: {"set": "domain", "entries": [{"key": "partner.example", "verdict": "allow", "note": "our reseller"}]}
func (o ops) write(ctx context.Context, in *SetReferenceIn) (*SetReferenceOut, error) {
	set, ok := byName(strings.ToLower(strings.TrimSpace(in.Set)))
	if !ok {
		return nil, zip.ErrNotFound("this plane publishes no set by that name")
	}
	if len(in.Entries) == 0 {
		return nil, zip.ErrBadRequest("no entries")
	}
	if len(in.Entries) > maxWrite {
		return nil, zip.ErrBadRequest(fmt.Sprintf("at most %d entries per call", maxWrite))
	}
	batch := make([]ReferenceOverride, 0, len(in.Entries))
	for _, e := range in.Entries {
		key := strings.ToLower(strings.TrimSpace(e.Key))
		if key == "" {
			return nil, zip.ErrBadRequest("an override needs a key")
		}
		if !verdictOK(e.Verdict) {
			return nil, zip.ErrBadRequest("verdict is allow or deny")
		}
		note := e.Note
		if len(note) > maxNote {
			note = note[:maxNote]
		}
		batch = append(batch, ReferenceOverride{Key: normal(set, key), Verdict: e.Verdict, Note: note})
	}
	own, err := o.mine(ctx)
	if err != nil {
		return nil, err
	}
	n, err := own.put(set.Name, batch, actor(ctx), o.s.State.now())
	if err != nil {
		return nil, zip.ErrConflict(err.Error())
	}
	held, _ := own.count(set.Name)
	return &SetReferenceOut{Set: set.Name, Written: n, Overrides: held}, nil
}

// normal renders a key the way the baseline holds it, so an override written as
// "10.0.0.0/8 " and a baseline entry written as "10.0.0.0/8" are the same key.
// One normalisation, applied on the way in, so a lookup never has to guess.
func normal(set Set, key string) string {
	if set.Match != MatchNet {
		return key
	}
	if p, ok := prefix(key); ok {
		return p.String()
	}
	return key
}

// ── DELETE /v1/ml/reference/{set} ────────────────────────────────────────────

// ClearReferenceIn names one of your org's overrides to remove.
//
// The key is a query parameter rather than a path segment because a key can be
// a CIDR or a pattern, and those carry the characters a path segment cannot.
type ClearReferenceIn struct {
	// Set is the set to clear in, from the path.
	Set string `json:"-" url:"set"`
	// Key is the exact override key to remove.
	Key string `json:"-" url:"key"`
}

// ClearReferenceOut reports whether there was an entry to remove.
type ClearReferenceOut struct {
	// Set is the set cleared in.
	Set string `json:"set"`
	// Key is the entry named.
	Key string `json:"key"`
	// Cleared is false when your org held no such override — which is not an
	// error, it is the honest answer to a removal that had nothing to remove.
	Cleared bool `json:"cleared"`
	// Overrides is how many your org still holds in this set.
	Overrides int `json:"overrides"`
}

// ClearReference removes one of your organisation's overrides.
//
// It removes an entry your organisation wrote, never a baseline member: the
// published set is not writable from here, so a removal can only ever restore
// the baseline's own answer.
//
// Example: {"set": "domain", "key": "partner.example"}
func (o ops) clear(ctx context.Context, in *ClearReferenceIn) (*ClearReferenceOut, error) {
	set, ok := byName(strings.ToLower(strings.TrimSpace(in.Set)))
	if !ok {
		return nil, zip.ErrNotFound("this plane publishes no set by that name")
	}
	key := normal(set, strings.ToLower(strings.TrimSpace(in.Key)))
	if key == "" {
		return nil, zip.ErrBadRequest("which key")
	}
	own, err := o.held(ctx)
	if err != nil {
		return nil, err
	}
	if own == nil {
		return &ClearReferenceOut{Set: set.Name, Key: key}, nil
	}
	gone, err := own.clear(set.Name, key)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reference overrides: %v", err)
	}
	held, _ := own.count(set.Name)
	return &ClearReferenceOut{Set: set.Name, Key: key, Cleared: gone, Overrides: held}, nil
}

// ── POST /v1/ml/reference/resolve ────────────────────────────────────────────

// ResolveReferenceIn asks about keys.
type ResolveReferenceIn struct {
	// Sets narrows which sets to consult. Empty consults every set whose matcher
	// can read the keys given.
	Sets []string `json:"sets,omitempty" url:"-"`
	// Keys are the values to look up, at most 100 per call: email addresses or
	// domains, IP addresses, card prefixes, user-agent strings, autonomous system
	// numbers, device digests.
	Keys []string `json:"keys" url:"-"`
}

// ResolveReferenceOut is what the plane knows, and exactly which version knew it.
type ResolveReferenceOut struct {
	// Answers is one entry per (set, key) consulted.
	Answers []ReferenceAnswer `json:"answers"`
	// Consulted names the version of every set that took part, so a decision can
	// record precisely what it leaned on. Record this with the decision: it is
	// what makes the decision reproducible a year later.
	Consulted []ReferenceVersion `json:"consulted"`
	// Stale names the consulted sets past their freshness bound. Staleness is
	// itself a risk signal — a decision taken against a three-week-old list is a
	// weaker decision, and this is how it knows.
	Stale []string `json:"stale,omitempty"`
	// Refused names the consulted sets that could not answer at all. A key that
	// missed in one of these is UNKNOWN, not clean.
	Refused []string `json:"refused,omitempty"`
}

// ReferenceVersion is one set's identity at the moment it was consulted.
type ReferenceVersion struct {
	// Set is the set.
	Set string `json:"set"`
	// Version is every contributing publisher and its content digest.
	Version string `json:"version,omitempty"`
	// AsOf is when the oldest of them was current, RFC 3339.
	AsOf string `json:"asOf,omitempty"`
	// Stale is whether it is past its freshness bound.
	Stale bool `json:"stale"`
	// Refusal is why it could not be consulted, when it could not.
	Refusal string `json:"refusal,omitempty"`
}

// ResolveReference looks keys up against the reference plane.
//
// Your organisation's own overrides are consulted FIRST and win outright; the
// shared baseline answers everything they do not cover. Every answer names the
// version that produced it, when that version was current and whether it is
// stale, so a decision can record exactly what it consulted.
//
// Read Refusal before reading Hit. A set that has never loaded, one held by the
// component that screens against it, and one whose source needs a licence we do
// not hold all answer with a refusal — and a miss on a refusing set means
// nothing is known, not that the key is clean.
//
// Example: {"sets": ["domain", "net"], "keys": ["user@tempbox.example", "3.5.140.1"]}
func (o ops) resolve(ctx context.Context, in *ResolveReferenceIn) (*ResolveReferenceOut, error) {
	keys := make([]string, 0, len(in.Keys))
	for _, k := range in.Keys {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, zip.ErrBadRequest("no keys")
	}
	if len(keys) > maxKeys {
		return nil, zip.ErrBadRequest(fmt.Sprintf("at most %d keys per call", maxKeys))
	}
	wanted, err := chosen(in.Sets)
	if err != nil {
		return nil, err
	}
	own, err := o.held(ctx)
	if err != nil {
		return nil, err
	}
	now := o.s.State.now()

	out := &ResolveReferenceOut{
		Answers:   make([]ReferenceAnswer, 0, len(wanted)*len(keys)),
		Consulted: make([]ReferenceVersion, 0, len(wanted)),
	}
	for _, set := range wanted {
		s := o.s.State.plane.get(set.Name)
		seen := ReferenceVersion{Set: set.Name, Stale: true, Refusal: set.Refusal}
		if s != nil {
			seen.Version, seen.Stale = s.version, s.stale(now)
			if seen.Refusal == "" {
				seen.Refusal = s.refusal
			}
			if !s.asOf.IsZero() {
				seen.AsOf = s.asOf.UTC().Format(time.RFC3339)
			}
		} else if seen.Refusal == "" {
			seen.Refusal = "this set has never loaded, so it cannot tell a clean key from an unknown one"
		}
		out.Consulted = append(out.Consulted, seen)
		if seen.Stale {
			out.Stale = append(out.Stale, set.Name)
		}
		if seen.Refusal != "" {
			out.Refused = append(out.Refused, set.Name)
		}
		for _, k := range keys {
			a, err := answer(set, s, own, k, now)
			if err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "reference resolve: %v", err)
			}
			out.Answers = append(out.Answers, a)
		}
	}
	return out, nil
}

// chosen resolves the named sets, or the whole catalog when none are named. A
// name the catalog does not carry is refused rather than skipped: a caller who
// misspells a set and gets a silent pass has been told the key is clean by a set
// that was never consulted.
func chosen(names []string) ([]Set, error) {
	if len(names) == 0 {
		return Catalog(), nil
	}
	out := make([]Set, 0, len(names))
	for _, n := range names {
		set, ok := byName(strings.ToLower(strings.TrimSpace(n)))
		if !ok {
			return nil, zip.ErrNotFound(fmt.Sprintf("this plane publishes no set named %q", n))
		}
		out = append(out, set)
	}
	return out, nil
}

// ── POST /v1/ml/reference/refresh ────────────────────────────────────────────

// ReferenceReceipt is a load receipt from the component that HOLDS a set's
// membership — the screening engine, for the designation lists.
type ReferenceReceipt struct {
	// Source is the publisher this receipt is for.
	Source string `json:"source"`
	// Version is the digest of what that publisher supplied, so a refresh that
	// changed nothing can be told from a refresh that did not run.
	Version string `json:"version"`
	// AsOf is when the load happened, RFC 3339. Absent is dated on arrival, which
	// can only make the list look older than it is.
	AsOf string `json:"asOf,omitempty"`
	// Keys is how many designations that load carried. Zero from a publisher who
	// designates somebody is a failed load wearing a successful one's clothes,
	// and belongs in Refusal instead.
	Keys int `json:"keys"`
	// Refusal is why the load failed, when it did.
	Refusal string `json:"refusal,omitempty"`
}

// ReferenceTaken is what one publisher contributed to a refresh.
type ReferenceTaken struct {
	// Source is the publisher.
	Source string `json:"source"`
	// Version is the content digest that landed.
	Version string `json:"version,omitempty"`
	// Keys is how many members it carries.
	Keys int `json:"keys"`
	// Wrote is how many rows this run actually wrote. Zero with Unchanged means
	// the publisher served the same set again.
	Wrote int `json:"wrote"`
	// Unchanged is true when the publisher's data was byte-for-byte the set we
	// already held.
	Unchanged bool `json:"unchanged,omitempty"`
	// Resumed is true when this run continued a version a previous run left
	// half-landed.
	Resumed bool `json:"resumed,omitempty"`
	// Refusal is why this publisher contributed nothing, if it did not. The set
	// keeps its previous version of this source rather than shrinking.
	Refusal string `json:"refusal,omitempty"`
}

// RefreshReferenceIn asks the plane to take a new version of one set.
type RefreshReferenceIn struct {
	// Set is the set to refresh.
	Set string `json:"set"`
	// Receipts are supplied by the component that holds the membership, for a set
	// of kind attest. They are refused on any other kind, and a set of kind attest
	// is refused without them: this plane never invents a freshness it did not
	// observe.
	Receipts []ReferenceReceipt `json:"receipts,omitempty" url:"-"`
}

// RefreshReferenceOut is the outcome, per publisher.
type RefreshReferenceOut struct {
	// Set is the set refreshed.
	Set string `json:"set"`
	// Took is what each publisher contributed.
	Took []ReferenceTaken `json:"took"`
	// Version is the set's new composed version.
	Version string `json:"version,omitempty"`
	// Stale is whether it is STILL past its freshness bound after the refresh,
	// which is what a publisher that has stopped answering looks like.
	Stale bool `json:"stale"`
}

// RefreshReference takes a new version of one set. SuperAdmin only.
//
// It is platform work, not tenant work: it writes the shared baseline every
// organisation reads, so it is gated to the platform's own identity. Nothing
// here can write an organisation's overrides, and nothing an organisation sends
// can reach this route.
//
// Idempotent. A version is the content digest of what was taken, so refreshing
// an unchanged publisher writes no rows and reports unchanged. Resumable: a run
// that died half-way is continued from where it stopped rather than restarted.
//
// A set whose source needs a licence we do not hold is refused with the reason,
// rather than being quietly skipped.
//
// Example: {"set": "domain"}
func (o ops) refresh(ctx context.Context, in *RefreshReferenceIn) (*RefreshReferenceOut, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return nil, zip.ErrForbidden("SuperAdmin required: this writes the baseline every org reads")
	}
	set, ok2 := byName(strings.ToLower(strings.TrimSpace(in.Set)))
	if !ok2 {
		return nil, zip.ErrNotFound("this plane publishes no set by that name")
	}
	switch {
	case set.Kind == KindSeam:
		return nil, zip.Errorf(http.StatusNotImplemented, "%s", set.Refusal)
	case set.Kind == KindAttest && len(in.Receipts) == 0:
		return nil, zip.ErrBadRequest("this set's membership is held elsewhere; refresh it with the loader's receipts")
	case set.Kind != KindAttest && len(in.Receipts) > 0:
		return nil, zip.ErrBadRequest("this set is taken here; a receipt would be a freshness nobody observed")
	}
	if !datastore.Ready() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the warehouse is not connected, so a version cannot be recorded")
	}
	took, err := take(ctx, o.s, set, in.Receipts)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "reference refresh: %v", err)
	}
	out := &RefreshReferenceOut{Set: set.Name, Took: took, Stale: true}
	if s := o.s.State.plane.get(set.Name); s != nil {
		out.Version, out.Stale = s.version, s.stale(o.s.State.now())
	}
	return out, nil
}
