package train

// policies.go — /v1/train/policies: the Enso router's policy, versioned, promoted
// and rolled back like a capability.
//
// A policy is an enso.policy/1 file (policy.go): what the tuner learned from
// per-question benchmark results about which model each Enso tier should map to,
// with its scores on questions it was not tuned on (held out) and on a sealed set
// no tuning reads (lockbox). Each file is an immutable version, pinned by the
// sha256 of its canonical bytes. Promoting a version puts each of its tiers' arms
// first on that tier's route in the Enso catalog, through the ai process's one
// write path onto it (hanzoai/ai routing.go, over the plane), which the engine
// takes at once (PUT /v1/admin/catalog) and the ai process keeps serving across
// the engine's restarts: live Enso reads the promoted version by hot reload, not
// by a roll. Rollback serves the version promoted before it, and past the first,
// the routes that served before any policy.
//
// One state machine, the capability's: a version is a draft until it is promoted;
// the promoted versions form a stack whose top serves; rollback is an action that
// pops it. Every act is a SuperAdmin's (owner == "admin"), is written to the audit
// trail before it is made, and is refused when it cannot be: no trail, no write.
//
//	GET  /v1/train/policies/:name                    the registry: serving, versions, history
//	POST /v1/train/policies/:name                    a draft from a policy file
//	GET  /v1/train/policies/:name/versions/:version  one version, its file and what promoting it serves
//	GET  /v1/train/policies/:name/diff               what changed between two versions
//	POST /v1/train/policies/:name/promote            serve a version
//	POST /v1/train/policies/:name/rollback           serve the one before
//
// The registry is the platform's, kept in the reserved platform namespace's
// train.db, never a tenant's: an Enso policy names the upstreams behind every tier.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	contract "github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// ensoPolicy is the one policy the registry holds: the Enso router's.
const ensoPolicy = "enso"

// router is the Enso family's routing as the ai process serves it: its catalog,
// the one write path onto it, and each tier's fixed price. A value, so a test
// holds a catalog of its own.
type router interface {
	routing(ctx context.Context) (*contract.Routing, error)
	apply(ctx context.Context, in *contract.RoutingApplyIn) (*contract.RoutingApplied, error)
	rates(ctx context.Context) (*contract.Rates, error)
}

// aiRouter is the router over the plane.
type aiRouter struct{}

func (aiRouter) routing(ctx context.Context) (*contract.Routing, error) {
	return aipeer.AIRouting(ctx, &contract.RoutingIn{Family: ensoPolicy})
}

func (aiRouter) apply(ctx context.Context, in *contract.RoutingApplyIn) (*contract.RoutingApplied, error) {
	return aipeer.AIRoutingApply(ctx, in)
}

func (aiRouter) rates(ctx context.Context) (*contract.Rates, error) {
	return aipeer.AIRates(ctx, &contract.RatesIn{Admin: true})
}

// policyMu serializes this process's promotions and rollbacks, so the check of
// what serves and the change are one step here; across processes the ai process's
// version check is the one that holds.
var policyMu sync.Mutex

func policyRoutes(app cloud.Router, o ops) {
	g := app.Group("/v1/train/policies")
	g.Get("/:name", o.getPolicy, zip.WithOperationID("policiesGet"))
	g.Post("/:name", o.createPolicy, zip.WithStatus(http.StatusCreated), zip.WithOperationID("policiesCreate"))
	g.Get("/:name/versions/:version", o.getPolicyVersion, zip.WithOperationID("policiesVersion"))
	g.Get("/:name/diff", o.diffPolicy, zip.WithOperationID("policiesDiff"))
	g.Post("/:name/promote", o.promotePolicy, zip.WithOperationID("policiesPromote"))
	g.Post("/:name/rollback", o.rollbackPolicy, zip.WithOperationID("policiesRollback"))
}

// super is the one gate on the registry: platform authority, owner == "admin". An
// org admin is refused like any member.
func super(ctx context.Context) error {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return cloud.Super.Refusal()
	}
	return nil
}

// change is the gate on a write: platform authority, then the anti-forgery
// control, which is a no-op for a caller presenting a credential.
func change(ctx context.Context) error {
	if err := super(ctx); err != nil {
		return err
	}
	return cloud.CSRF(ctx)
}

// platform is the registry's store: the reserved platform namespace's train.db.
func (o ops) platform() (*store, namespace.Namespace, error) {
	ns, err := principal.OrgNamespace(principal.Reserved, principal.Reserved)
	if err != nil {
		return nil, ns, err
	}
	st, err := o.s.State.stores.For(ns)
	if err != nil {
		o.s.Log.Error("train platform store open failed", "err", err)
		return nil, ns, zip.Errorf(http.StatusInternalServerError, "policy store unavailable")
	}
	return st, ns, nil
}

// shipped makes a registry write durable before it is acknowledged.
func (o ops) shipped(ns namespace.Namespace) error {
	acked, err := o.s.State.stores.Sync(ns)
	if err != nil || !acked {
		o.s.Log.Warn("train policy ship failed", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "policy store failover in progress; retry")
	}
	return nil
}

func (o ops) policyFault(what string, err error) error {
	o.s.Log.Error("train policy "+what+" failed", "err", err)
	return zip.Errorf(http.StatusInternalServerError, "policy %s failed", what)
}

// policyNamed refuses every name but the one policy the registry holds.
func policyNamed(name string) error {
	if name != ensoPolicy {
		return missing("policy")
	}
	return nil
}

// PolicyBoard is a policy registry as a SuperAdmin reads it.
type PolicyBoard struct {
	// Name is the policy's name: enso.
	Name string `json:"name"`
	// Promoted is the version whose tiers serve, 0 when none does.
	Promoted int `json:"promoted"`
	// Serving is what the Enso catalog serves now.
	Serving PolicyServing `json:"serving"`
	// Versions are every version, newest first, without their files.
	Versions []PolicyVersion `json:"versions"`
	// History is every promotion and rollback, newest first.
	History []PolicyEvent `json:"history"`
	// Rollbacks are what a rollback would serve, while a version is promoted: the
	// version promoted before it (or the catalog's own routes), and, when that is
	// a version, the catalog's own routes directly. Each says what it changes.
	Rollbacks []PolicyRollback `json:"rollbacks"`
}

// PolicyServing is the Enso catalog as the engine serves it now.
type PolicyServing struct {
	// Routing is the ai process's newest version of the catalog, 0 before the first.
	Routing int64 `json:"routing"`
	// Drift says the engine serves something other than that version; Redeployed,
	// a deployment it was not made over, which is left standing.
	Drift      bool `json:"drift"`
	Redeployed bool `json:"redeployed"`
	// Tiers are the paid Enso models, each with its route and price.
	Tiers []PolicyServingTier `json:"tiers"`
	// Arms are every upstream a paid model routes to: the models a policy's tiers
	// can lead with.
	Arms []PolicyServingArm `json:"arms"`
	// Error is why the catalog could not be read, "" when it was.
	Error string `json:"error,omitempty"`
}

// PolicyServingTier is one paid Enso model as it serves.
type PolicyServingTier struct {
	// Tier is the policy tier it serves (flash for enso-flash).
	Tier string `json:"tier"`
	// Model is the Enso model.
	Model string `json:"model"`
	// Route is its upstreams in the order they are asked.
	Route []string `json:"route"`
	// Price is what a million of its tokens bill, absent when the price could not
	// be read.
	Price *contract.Price `json:"price,omitempty"`
	// Refused is why the price guard refuses it, "" while it serves.
	Refused string `json:"refused,omitempty"`
	// Lead is the promoted version whose arm leads its route, 0 when its lead is
	// not the promoted policy's.
	Lead int `json:"lead"`
}

// PolicyServingArm is one upstream a paid Enso model routes to.
type PolicyServingArm struct {
	Upstream string `json:"upstream"`
	Provider string `json:"provider"`
	// Models are the paid models whose route names it.
	Models []string `json:"models"`
}

// PolicyVersion is one version of a policy.
type PolicyVersion struct {
	Version int `json:"version"`
	// SHA256 is the digest of its canonical file: the version's identity.
	SHA256 string `json:"sha256"`
	// Digest is the digest of the answers it was tuned on (source.digest).
	Digest string `json:"digest"`
	// Benches are the benchmarks it was tuned on.
	Benches []string `json:"benches"`
	// Tiers are the routes it sets: each tier's arm and the upstream it names.
	Tiers map[string]PolicyLead `json:"tiers"`
	// State is promoted (its tiers serve), previous (promoted, beneath the one that
	// serves: a rollback serves it again) or draft.
	State string `json:"state"`
	// Leads says, of a promoted version, that every tier it sets leads with its arm
	// in the catalog as it serves; a promoted version that does not (a new release
	// of the catalog stands over it) can be promoted again.
	Leads bool `json:"leads"`
	// Servable says it can be promoted on the catalog as it serves; Reasons, why
	// not, each a sentence.
	Servable bool     `json:"servable"`
	Reasons  []string `json:"reasons"`
	Note     string   `json:"note"`
	By       string   `json:"by"`
	At       int64    `json:"at"`
	// Chains are, on one version's read, each route promoting the version would
	// change, as it serves and as it would.
	Chains []PolicyChain `json:"chains,omitempty"`
	// Diff is, on one version's read, the catalog lines promoting it would change.
	Diff string `json:"diff,omitempty"`
	// Next is, on one version's read, the sha256 of the catalog promoting it would
	// send: a promotion names it, and is refused when the catalog has moved since.
	Next string `json:"next,omitempty"`
	// Document is, on one version's read, the policy file.
	Document json.RawMessage `json:"document,omitempty"`
}

// PolicyRollback is what one rollback would serve and change.
type PolicyRollback struct {
	// To is the version it serves, 0 for the catalog's own routes.
	To int `json:"to"`
	// Origin rolls straight back to the catalog's own routes, past every version.
	Origin bool `json:"origin"`
	// Chains are each route it changes, as it serves and as it would.
	Chains []PolicyChain `json:"chains"`
	// Next is the sha256 of the catalog it sends, which the rollback names.
	Next string `json:"next"`
	// Reasons are why it cannot be made, none when it can.
	Reasons []string `json:"reasons"`
}

// PolicyEvent is one promotion or rollback.
type PolicyEvent struct {
	// Action is promote or rollback.
	Action string `json:"action"`
	// Version is the version serving after it, 0 for the routes that served before
	// any policy; Prior, the one serving before it.
	Version int `json:"version"`
	Prior   int `json:"prior"`
	// Routing is the catalog version the ai process recorded for it.
	Routing int64 `json:"routing"`
	// Chains are the tiers' routes it changed, before and after.
	Chains []PolicyChain `json:"chains"`
	Note   string        `json:"note"`
	By     string        `json:"by"`
	At     int64         `json:"at"`
}

// live is the Enso catalog as it serves, read from the ai process.
type live struct {
	routing *contract.Routing
	cat     *ensoCatalog
	err     error
}

func (o ops) live(ctx context.Context) live {
	r, err := o.s.State.router.routing(ctx)
	if err != nil {
		return live{err: fmt.Errorf("the Enso catalog could not be read: %s", said(err))}
	}
	if r.Error != "" {
		return live{routing: r, err: fmt.Errorf("the Enso catalog could not be read: %s", r.Error)}
	}
	c, err := readEnso(r.Catalog)
	if err != nil {
		return live{routing: r, err: err}
	}
	return live{routing: r, cat: c}
}

// fresh is l's catalog read again from its bytes: what a change lays itself onto,
// leaving l as it serves.
func (l live) fresh() *ensoCatalog {
	c, err := readEnso(l.routing.Catalog)
	if err != nil {
		panic("a catalog that read once reads again: " + err.Error())
	}
	return c
}

// tiers are the catalog's paid models in name order: what a policy's tiers serve.
func (c *ensoCatalog) tiers() []string {
	var out []string
	for _, id := range sorted(c.models) {
		m := c.model(id)
		if m == nil || off(m) || !strings.HasPrefix(id, "enso-") || !paid(m) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// leads says every tier p sets leads with its arm on c.
func leads(p plan, c *ensoCatalog) bool {
	for _, t := range sorted(p.Tiers) {
		if up := c.upstreams(skuOf(t)); len(up) == 0 || up[0] != p.Tiers[t].Upstream {
			return false
		}
	}
	return true
}

// serving is the catalog as it serves, held against the promoted version.
func (o ops) serving(ctx context.Context, l live, promoted *policyRow) PolicyServing {
	s := PolicyServing{Tiers: []PolicyServingTier{}, Arms: []PolicyServingArm{}}
	if l.routing != nil {
		s.Routing, s.Drift, s.Redeployed = l.routing.Version, l.routing.Drift, l.routing.Redeployed
	}
	if l.err != nil {
		s.Error = l.err.Error()
		return s
	}
	quotes := map[string]contract.Quote{}
	if rs, err := o.s.State.router.rates(ctx); err == nil {
		for _, q := range rs.Quotes {
			quotes[q.Model] = q
		}
	}
	arms := map[string]*PolicyServingArm{}
	var order []string
	for _, id := range l.cat.tiers() {
		t := PolicyServingTier{Tier: strings.TrimPrefix(id, "enso-"), Model: id, Route: l.cat.upstreams(id)}
		if q, ok := quotes[id]; ok {
			p := q.Price
			t.Price, t.Refused = &p, q.Refused
		}
		if promoted != nil {
			if pt, ok := promoted.plan.Tiers[t.Tier]; ok && len(t.Route) > 0 && t.Route[0] == pt.Upstream {
				t.Lead = promoted.version
			}
		}
		s.Tiers = append(s.Tiers, t)
		for _, r := range l.cat.route(id) {
			up := upstreamOf(r)
			a, ok := arms[up]
			if !ok {
				p, _ := r["provider"].(string)
				a = &PolicyServingArm{Upstream: up, Provider: p}
				arms[up] = a
				order = append(order, up)
			}
			a.Models = append(a.Models, id)
		}
	}
	slices.Sort(order)
	for _, up := range order {
		s.Arms = append(s.Arms, *arms[up])
	}
	return s
}

// head is a version as the registry lists it, held against the catalog.
func head(v policyRow, r registry, l live) PolicyVersion {
	out := PolicyVersion{Version: v.version, SHA256: v.sha256, Digest: v.digest, Benches: v.benches,
		Tiers: v.plan.Tiers, State: "draft", Note: v.note, By: v.by, At: v.at}
	if out.Tiers == nil {
		out.Tiers = map[string]PolicyLead{}
	}
	if i := slices.Index(r.stack, v.version); i >= 0 {
		out.State = "previous"
		if i == len(r.stack)-1 {
			out.State = "promoted"
		}
	}
	if l.err != nil {
		out.Reasons = []string{l.err.Error()}
	} else {
		out.Reasons = servable(v.plan, l.cat)
		out.Leads = out.State == "promoted" && leads(v.plan, l.cat)
	}
	out.Servable = len(out.Reasons) == 0
	return out
}

type policyIn struct {
	// Name is the policy: enso.
	Name string `json:"name"`
}

// GetPolicy answers the Enso policy registry: what the Enso catalog serves now,
// each paid tier's route and price and whether the promoted version leads it; the
// upstreams the tiers route to; every version, newest first, with the tiers it
// sets and whether it can be promoted on the catalog as it serves; every
// promotion and rollback; and what each rollback would serve and change.
// SuperAdmin only. A catalog that cannot be read is said in serving.error, and
// every version reads unservable for it.
func (o ops) getPolicy(ctx context.Context, in *policyIn) (*PolicyBoard, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	st, _, err := o.platform()
	if err != nil {
		return nil, err
	}
	return o.board(ctx, st, in.Name)
}

func (o ops) board(ctx context.Context, st *store, name string) (*PolicyBoard, error) {
	r, err := st.registry(ctx, name)
	if err != nil {
		return nil, o.policyFault("registry read", err)
	}
	vs, err := st.policyVersions(ctx, name)
	if err != nil {
		return nil, o.policyFault("versions read", err)
	}
	es, err := st.policyEvents(ctx, name)
	if err != nil {
		return nil, o.policyFault("history read", err)
	}
	l := o.live(ctx)
	var promoted *policyRow
	out := &PolicyBoard{Name: name, Promoted: r.top(), Versions: []PolicyVersion{}, History: []PolicyEvent{}, Rollbacks: []PolicyRollback{}}
	for i, v := range vs {
		if v.version == r.top() {
			promoted = &vs[i]
		}
		out.Versions = append(out.Versions, head(v, r, l))
	}
	out.Serving = o.serving(ctx, l, promoted)
	for _, e := range es {
		out.History = append(out.History, PolicyEvent{Action: e.action, Version: e.version, Prior: e.prior,
			Routing: e.routing, Chains: e.chains, Note: e.note, By: e.by, At: e.at})
	}
	if r.top() > 0 && l.err == nil {
		for _, origin := range []bool{false, true} {
			rb, err := o.rollback(ctx, st, r, l, origin)
			if err != nil {
				return nil, err
			}
			if origin && len(out.Rollbacks) > 0 && out.Rollbacks[0].To == 0 {
				break // the one before is already the catalog's own routes
			}
			out.Rollbacks = append(out.Rollbacks, rb.view())
		}
	}
	return out, nil
}

type createPolicyIn struct {
	// Name is the policy: enso.
	Name string `json:"name"`
	// Document is the enso.policy/1 file the tuner wrote.
	Document json.RawMessage `json:"document"`
	// Note is why, kept with the version.
	Note string `json:"note"`
}

// CreatePolicy makes a draft version from an enso.policy/1 file the engine's tuner
// wrote: validated (its schema, the answers it names, every benchmark's tiers and
// scores), kept canonical and pinned by the sha256 of those bytes. Nothing serves
// until it is promoted. Refused 400 naming every problem with the file, 409 exists
// when a version already holds the same file, 503 when the act cannot be audited.
// SuperAdmin only.
func (o ops) createPolicy(ctx context.Context, in *createPolicyIn) (*PolicyVersion, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	canon, sha, doc, err := readPolicy(in.Document)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	st, ns, err := o.platform()
	if err != nil {
		return nil, err
	}
	policyMu.Lock()
	defer policyMu.Unlock()
	if n, err := st.policyVersionOf(ctx, in.Name, sha); err != nil {
		return nil, o.policyFault("versions read", err)
	} else if n > 0 {
		return nil, conflict("exists", "version "+strconv.Itoa(n)+" already holds this policy file")
	}
	last, err := st.lastPolicyVersion(ctx, in.Name)
	if err != nil {
		return nil, o.policyFault("versions read", err)
	}
	v := policyRow{version: last + 1, sha256: sha, document: canon, plan: planOf(doc), digest: doc.Source.Digest,
		benches: sorted(doc.Benches), note: strings.TrimSpace(in.Note), by: caller(ctx), at: unix()}
	if err := o.policyAudit(ctx, "create", in.Name, v.version, nil, map[string]any{
		"version": v.version, "sha256": sha, "digest": v.digest, "benches": v.benches, "tiers": v.plan.Tiers, "note": v.note,
	}); err != nil {
		return nil, err
	}
	if err := st.addPolicyVersion(ctx, in.Name, v); err != nil {
		return nil, o.policyFault("version write", err)
	}
	if err := o.shipped(ns); err != nil {
		return nil, err
	}
	r, err := st.registry(ctx, in.Name)
	if err != nil {
		return nil, o.policyFault("registry read", err)
	}
	out := head(v, r, o.live(ctx))
	return &out, nil
}

type policyVersionIn struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// GetPolicyVersion answers one version: its file, the tiers it sets, whether it can
// be promoted on the catalog as it serves and why not, each route promoting it
// would change, the catalog lines that would change, the digest of the catalog it
// would send (which a promotion names), and why the price guard would refuse the
// result, if it would. Nothing is applied. SuperAdmin only.
func (o ops) getPolicyVersion(ctx context.Context, in *policyVersionIn) (*PolicyVersion, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	st, _, err := o.platform()
	if err != nil {
		return nil, err
	}
	v, err := st.policyVersion(ctx, in.Name, in.Version)
	if err == errNotFound {
		return nil, missing("version")
	}
	if err != nil {
		return nil, o.policyFault("version read", err)
	}
	r, err := st.registry(ctx, in.Name)
	if err != nil {
		return nil, o.policyFault("registry read", err)
	}
	l := o.live(ctx)
	out := head(v, r, l)
	out.Document = v.document
	if !out.Servable || (out.State == "promoted" && out.Leads) {
		return &out, nil
	}
	ch, err := o.promotion(ctx, r, v, l)
	if err != nil {
		return nil, err
	}
	out.Chains, out.Next = ch.chains, ch.next
	switch {
	case ch.err != "":
		out.Servable, out.Reasons = false, []string{ch.err}
	default:
		out.Diff = ch.diff
	}
	return &out, nil
}

type policyDiffIn struct {
	Name string `json:"name"`
	// From is the version compared from: the promoted one when absent, else the
	// one before To; 0 when there is none, every part of To then new.
	From int `json:"from"`
	// To is the version compared to: the newest when absent.
	To int `json:"to"`
}

// DiffPolicy answers what changed from one version to another: the answers each
// was tuned on, the arms each knows, and per benchmark its pool, its best single
// arm, and each tier's cascade, budget and scores on new questions (held out) and
// the sealed set (lockbox). SuperAdmin only.
func (o ops) diffPolicy(ctx context.Context, in *policyDiffIn) (*PolicyDiff, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	st, _, err := o.platform()
	if err != nil {
		return nil, err
	}
	to := in.To
	if to == 0 {
		if to, err = st.lastPolicyVersion(ctx, in.Name); err != nil {
			return nil, o.policyFault("versions read", err)
		}
		if to == 0 {
			return nil, missing("version")
		}
	}
	from := in.From
	if from == 0 {
		r, err := st.registry(ctx, in.Name)
		if err != nil {
			return nil, o.policyFault("registry read", err)
		}
		from = r.top()
		if from == to {
			from = to - 1
		}
	}
	docs := map[int]policyDoc{0: {}}
	for _, n := range []int{from, to} {
		if n == 0 {
			continue
		}
		v, err := st.policyVersion(ctx, in.Name, n)
		if err == errNotFound {
			return nil, missing("version " + strconv.Itoa(n))
		}
		if err != nil {
			return nil, o.policyFault("version read", err)
		}
		var d policyDoc
		if err := json.Unmarshal(v.document, &d); err != nil {
			return nil, o.policyFault("version read", err)
		}
		docs[n] = d
	}
	d := diffPolicies(from, to, docs[from], docs[to])
	return &d, nil
}

// change is one promotion or rollback laid onto the catalog as it serves, before
// anything is sent: the registry it leaves, the routes it changes, the catalog it
// sends and its digest, and the catalog's diff and the price guard's refusal, from
// a dry run in the ai process.
type laid struct {
	r      registry
	chains []PolicyChain
	cat    []byte
	next   string
	diff   string
	// err is why it cannot be made: the guard's refusal, or a dry run that failed.
	err string
}

// lay realizes p onto a fresh read of l's catalog for r, and dry-runs the result.
func (o ops) lay(ctx context.Context, r registry, p plan, l live) (laid, error) {
	out := laid{r: registry{name: r.name, stack: append([]int{}, r.stack...), origin: map[string][]string{}}}
	for m, ord := range r.origin {
		out.r.origin[m] = append([]string{}, ord...)
	}
	c := l.fresh()
	out.chains = realize(&out.r, p, c)
	b, err := c.bytes()
	if err != nil {
		return out, o.policyFault("catalog encode", err)
	}
	out.cat, out.next = b, sum(b)
	d, err := o.s.State.router.apply(ctx, &contract.RoutingApplyIn{Family: ensoPolicy, Catalog: b, Dry: true})
	switch {
	case err != nil:
		out.err = "the change could not be checked: " + said(err)
	case d.Refused != "":
		out.err = "the price guard would refuse it: " + d.Refused
	default:
		out.diff = d.Diff
	}
	return out, nil
}

// promotion is v laid onto the catalog as it serves, v on top of r's stack. With
// nothing promoted the order before any policy is what serves now, whatever an
// earlier attempt that did not land wrote down.
func (o ops) promotion(ctx context.Context, r registry, v policyRow, l live) (laid, error) {
	if r.top() == 0 {
		r.origin = map[string][]string{}
	}
	ch, err := o.lay(ctx, r, v.plan, l)
	if err != nil {
		return ch, err
	}
	if ch.r.top() != v.version {
		ch.r.stack = append(ch.r.stack, v.version)
	}
	return ch, nil
}

// rolled is a rollback laid onto the catalog as it serves.
type rolled struct {
	laid
	to      int
	origin  bool
	sha     string
	reasons []string
}

func (b rolled) view() PolicyRollback {
	reasons := append([]string{}, b.reasons...)
	if b.err != "" {
		reasons = append(reasons, b.err)
	}
	return PolicyRollback{To: b.to, Origin: b.origin, Chains: b.chains, Next: b.next, Reasons: reasons}
}

// rollback is the rollback of r's top version laid onto l: to the version promoted
// before it, or with origin straight back to the catalog's own routes.
func (o ops) rollback(ctx context.Context, st *store, r registry, l live, origin bool) (rolled, error) {
	popped := registry{name: r.name, stack: append([]int{}, r.stack[:len(r.stack)-1]...), origin: r.origin}
	if origin {
		popped.stack = []int{}
	}
	out := rolled{to: popped.top(), origin: origin}
	var p plan
	if out.to > 0 {
		v, err := st.policyVersion(ctx, r.name, out.to)
		if err != nil {
			return out, o.policyFault("version read", err)
		}
		if out.reasons = servable(v.plan, l.cat); len(out.reasons) > 0 {
			return out, nil
		}
		p, out.sha = v.plan, v.sha256
	}
	ch, err := o.lay(ctx, popped, p, l)
	if err != nil {
		return out, err
	}
	ch.r.stack = popped.stack
	if out.to == 0 {
		ch.r.origin = map[string][]string{}
	}
	out.laid = ch
	return out, nil
}

type promotePolicyIn struct {
	Name string `json:"name"`
	// Version is the version to serve.
	Version int `json:"version"`
	// Base is the version serving when the promotion was reviewed, 0 for none: a
	// promotion made while another serves is refused.
	Base int `json:"base"`
	// Next is the sha256 of the catalog the promotion was reviewed against (the
	// version's read says it): refused when the catalog has moved since.
	Next string `json:"next"`
	// Note is why, kept with the promotion.
	Note string `json:"note"`
}

// PromotePolicy serves a version: each of its tiers' arms is put first on that
// tier's route in the Enso catalog, the rest of each route kept behind it in the
// order it had before any policy, and the engine serves the result at once. The
// version must be servable on the catalog as it serves (each tier asks one arm,
// the same on every benchmark, whose upstream is on that paid tier's route), the
// price guard must take the result, and the catalog it sends must be the one
// reviewed (next). A promoted version a new release of the catalog stands over can
// be promoted again. Refused 409: moved (another version serves than base),
// promoted (it serves already), not_servable (with every reason), changed (the
// catalog moved since it was reviewed); 404 for a version that is not there, 503
// when the catalog cannot be read or the act cannot be audited, 502 when the
// catalog would not take it, and 500 when it took it and ai or this registry did
// not record it. SuperAdmin only.
//
// Example: {"version": 2, "base": 1, "next": "4f1c…", "note": "held out 92.1% at $3.10 per 1k on flash"}
func (o ops) promotePolicy(ctx context.Context, in *promotePolicyIn) (*PolicyBoard, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	st, ns, err := o.platform()
	if err != nil {
		return nil, err
	}
	policyMu.Lock()
	defer policyMu.Unlock()
	r, err := st.registry(ctx, in.Name)
	if err != nil {
		return nil, o.policyFault("registry read", err)
	}
	if in.Base != r.top() {
		return nil, conflict("moved", "version "+strconv.Itoa(r.top())+" serves now, not "+strconv.Itoa(in.Base)+"; reload")
	}
	v, err := st.policyVersion(ctx, in.Name, in.Version)
	if err == errNotFound {
		return nil, missing("version")
	}
	if err != nil {
		return nil, o.policyFault("version read", err)
	}
	l := o.live(ctx)
	if l.err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", l.err)
	}
	if why := servable(v.plan, l.cat); len(why) > 0 {
		return nil, conflict("not_servable", strings.Join(why, "; "))
	}
	if v.version == r.top() && leads(v.plan, l.cat) {
		return nil, conflict("promoted", "version "+strconv.Itoa(v.version)+" already serves")
	}
	ch, err := o.promotion(ctx, r, v, l)
	if err != nil {
		return nil, err
	}
	if ch.err != "" {
		return nil, conflict("not_servable", ch.err)
	}
	if in.Next != ch.next {
		return nil, conflict("changed", "the Enso catalog changed since this promotion was reviewed; review it again")
	}
	note := fmt.Sprintf("Enso policy version %d (%s)", v.version, short(v.sha256))
	if n := strings.TrimSpace(in.Note); n != "" {
		note += ": " + n
	}
	return o.serve(ctx, st, ns, r, ch, l, policyEvent{action: "promote", version: v.version, prior: in.Base, chains: ch.chains, note: strings.TrimSpace(in.Note)}, note, v.sha256)
}

type rollbackPolicyIn struct {
	Name string `json:"name"`
	// Base is the version serving when the rollback was reviewed: a rollback made
	// while another serves is refused.
	Base int `json:"base"`
	// Origin rolls straight back to the catalog's own routes, past every version.
	Origin bool `json:"origin"`
	// Next is the sha256 of the catalog the rollback was reviewed against (the
	// registry's read says it): refused when the catalog has moved since.
	Next string `json:"next"`
	// Note is why, kept with the rollback.
	Note string `json:"note"`
}

// RollbackPolicy serves the version promoted before the one that serves, compiled
// again onto the catalog as it serves; a tier only the one rolled back set goes
// back to the order its route had before any policy. Past the first promotion, or
// with origin, every tier a policy set goes back to that order. Only order moves:
// every rung is the catalog's own as it serves. Refused 409: moved,
// nothing_promoted, not_servable (the version before can no longer be served: roll
// back with origin), changed; 503, 502 and 500 as a promotion is. SuperAdmin only.
func (o ops) rollbackPolicy(ctx context.Context, in *rollbackPolicyIn) (*PolicyBoard, error) {
	if err := change(ctx); err != nil {
		return nil, err
	}
	if err := policyNamed(in.Name); err != nil {
		return nil, err
	}
	st, ns, err := o.platform()
	if err != nil {
		return nil, err
	}
	policyMu.Lock()
	defer policyMu.Unlock()
	r, err := st.registry(ctx, in.Name)
	if err != nil {
		return nil, o.policyFault("registry read", err)
	}
	if in.Base != r.top() {
		return nil, conflict("moved", "version "+strconv.Itoa(r.top())+" serves now, not "+strconv.Itoa(in.Base)+"; reload")
	}
	if r.top() == 0 {
		return nil, conflict("nothing_promoted", "no version serves; the tiers serve the catalog's own routes")
	}
	l := o.live(ctx)
	if l.err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", l.err)
	}
	rb, err := o.rollback(ctx, st, r, l, in.Origin)
	if err != nil {
		return nil, err
	}
	if len(rb.reasons) > 0 {
		return nil, conflict("not_servable", "version "+strconv.Itoa(rb.to)+" cannot serve now: "+strings.Join(rb.reasons, "; ")+"; roll back to the catalog's own routes instead")
	}
	if rb.err != "" {
		return nil, conflict("not_servable", rb.err)
	}
	if in.Next != rb.next {
		return nil, conflict("changed", "the Enso catalog changed since this rollback was reviewed; review it again")
	}
	note := "Enso policy rolled back to the catalog's own routes"
	if rb.to > 0 {
		note = fmt.Sprintf("Enso policy rolled back to version %d (%s)", rb.to, short(rb.sha))
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		note += ": " + n
	}
	return o.serve(ctx, st, ns, r, rb.laid, l, policyEvent{action: "rollback", version: rb.to, prior: in.Base, chains: rb.chains, note: strings.TrimSpace(in.Note)}, note, rb.sha)
}

// serve sends a laid change through the ai process and records the registry's new
// state. The order before any policy is written first, so a change that lands and
// is not recorded still leaves what a rollback puts back; the act is audited
// before it is sent and refused without a trail; once sent, nothing cancels it, so
// a caller that leaves does not leave the catalog changed and the registry not.
func (o ops) serve(ctx context.Context, st *store, ns namespace.Namespace, was registry, ch laid, l live, e policyEvent, note, sha string) (*PolicyBoard, error) {
	facts := map[string]any{"version": e.version, "prior": e.prior, "sha256": sha, "chains": e.chains, "routing_base": l.routing.Version, "next": ch.next, "note": e.note}
	if err := o.policyAudit(ctx, e.action, was.name, e.version, map[string]any{"version": e.prior}, facts); err != nil {
		return nil, err
	}
	sent := context.WithoutCancel(ctx)
	if len(ch.r.origin) > 0 {
		if err := st.keepOrigin(sent, registry{name: was.name, origin: ch.r.origin}); err != nil {
			return nil, o.policyFault("registry write", err)
		}
	}
	applied, err := o.s.State.router.apply(sent, &contract.RoutingApplyIn{Family: ensoPolicy, Catalog: ch.cat, Base: l.routing.Version, Note: note})
	if err != nil {
		o.policyRefused(sent, e.action, was.name, e.version, err)
		var he *zip.HTTPError
		switch {
		case errors.As(err, &he) && he.Status == http.StatusConflict:
			return nil, conflict("changed", "the Enso catalog changed since this was read; review it again: "+said(err))
		case errors.As(err, &he) && he.Status == http.StatusBadRequest:
			return nil, conflict("not_servable", said(err))
		case errors.As(err, &he) && he.Status == http.StatusInternalServerError:
			return nil, zip.Errorf(http.StatusInternalServerError, "the Enso catalog took it and ai did not record its version: %s; serving shows what the engine serves", said(err))
		}
		return nil, zip.Errorf(http.StatusBadGateway, "the Enso catalog did not take it: %s", said(err))
	}
	e.routing, e.by, e.at = applied.Version, caller(ctx), unix()
	if err := st.enact(sent, ch.r, was.stack, e); err != nil {
		o.policyRefused(sent, e.action, was.name, e.version, fmt.Errorf("the catalog took it and the registry did not record it: %v", err))
		if err == errMoved {
			return nil, conflict("moved", "the Enso catalog took it and another act changed the registry meanwhile; reload")
		}
		return nil, o.policyFault("registry write", err)
	}
	if err := o.shipped(ns); err != nil {
		return nil, err
	}
	return o.board(ctx, st, was.name)
}

func short(sha string) string {
	if len(sha) > 12 {
		return "sha256:" + sha[:12]
	}
	return "sha256:" + sha
}

// said is an error's sentence, without the transport's wrapping.
func said(err error) string {
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Msg != "" {
		return he.Msg
	}
	return err.Error()
}

// policyAudit writes a registry act to the trail before it is made, as the
// SuperAdmin who makes it; without a trail the act is refused.
func (o ops) policyAudit(ctx context.Context, action, name string, version int, before, after any) error {
	if o.s.State.audit == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "train: this policy change could not be audited")
	}
	who := cloud.Who(ctx)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	rec := audit.Record{
		Actor:     audit.Actor{Org: who.Org, Sub: caller(ctx), Email: who.Email},
		Action:    "train.policy." + action,
		Resource:  audit.Resource{Type: "train.policy", ID: name + "@" + strconv.Itoa(version)},
		Auth:      audit.AuthContext{Method: "session", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(a),
	}
	if before != nil {
		rec.Before = audit.Redact(b)
	}
	if _, err := o.s.State.audit.Append(ctx, rec); err != nil {
		o.s.Log.Error("train: a policy change could not be audited", "action", action, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "train: this policy change could not be audited")
	}
	return nil
}

// policyRefused records that an audited act did not take, under the same request.
func (o ops) policyRefused(ctx context.Context, action, name string, version int, cause error) {
	if o.s.State.audit == nil {
		return
	}
	who := cloud.Who(ctx)
	if _, err := o.s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: who.Org, Sub: caller(ctx), Email: who.Email},
		Action:    "train.policy." + action,
		Resource:  audit.Resource{Type: "train.policy", ID: name + "@" + strconv.Itoa(version)},
		Auth:      audit.AuthContext{Method: "session", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "error", Status: http.StatusBadGateway, Reason: said(cause)},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
	}); err != nil {
		o.s.Log.Error("train: a refused policy change could not be audited", "action", action, "err", err)
	}
}
