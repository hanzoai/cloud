package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	trainpeer "github.com/hanzoai/cloud/client/train"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const (
	maxTitle = 200
	maxText  = 4096
	maxName  = 128
	maxURL   = 2048
)

type state struct {
	store *Store
	audit *audit.Recorder
	// chain is the Lux agent contracts, once they are deployed; nil until the
	// binding lands (chain.go).
	chain Chain
	// stop ends the sweep.
	stop context.CancelFunc
	// money is held on a job from asking whether it is paid to marking what
	// happened: a release and a refund of the same job never interleave, on one
	// replica or between two (marketOps.hold).
	money lock.Keyed
	// expertise scores and ranks experts and dresses their listings with what the
	// rewards program says of them.
	expertise *expertise
}

// errUnaudited refuses a money act on a process with no audit trail, before it is
// made.
var errUnaudited = errors.New("marketplace: no audit trail on this process, and a job or a payout is never changed unrecorded")

// The shop is the public window: read with no credential, one operation at a
// time, so a write that grows here later is never exempted by its neighbour.
func init() {
	spec.Open("/v1/marketplace/shop", "GET")
	spec.Open("/v1/marketplace/shop/:id", "GET")
	spec.Open("/v1/marketplace/shop/:id/weights", "GET")
}

var mounted *cloud.Service[state]

// Mount wires /v1/marketplace/* and closes the payment client both ways, so a
// published listing's price is challenged and settled at every call.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("marketplace: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("marketplace.Use:  empty DataDir")
	}
	store, err := Open(cloud.DataDir(), deps.MasterKey, deps.KMS)
	if err != nil {
		return fmt.Errorf("marketplace.Use:  open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "marketplace"), State: state{store: store, audit: deps.Audit}}
	s.State.expertise = newExpertise(store, s.Log)
	mounted = s

	// Close the payment client, both halves, from the one store that already holds
	// the price and the payee (payments.go). Publish FIRST: from the instant a
	// charger is installed a dispatch can ask what a tool costs, and it must find
	// the table already there rather than a moment of "nothing is priced".
	x402.Publish(&registry{store: store})
	tools.SetCharger(charger{})

	// The same table, published for the processes that do NOT have it: the rail
	// runs in its own binary and asks (rpc.go). Both paths read the one store, so
	// what a tool costs cannot differ by who is asking or over which transport.
	exposePrice(store)

	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the validated org and the request the project scope
	// and audit actor are read off still cross on the context, parked by the root
	// install.

	// The surface root is the group's own address: an empty leaf declares
	// /v1/marketplace, not /v1/marketplace/.
	o := marketOps{s: s}
	g := app.Group("/v1/marketplace")
	g.Get("", o.discover)
	g.Get("/shop", o.shop)
	g.Get("/shop/:id", o.shopItem)
	g.Get("/shop/:id/weights", o.shopWeights)
	g.Get("/listings", o.listListings)
	g.Post("/listings", o.publish, zip.WithStatus(http.StatusCreated))
	g.Patch("/listings/:id", o.update)
	g.Delete("/listings/:id", o.unpublish)
	g.Post("/listings/:id/verify", o.verify)
	g.Post("/install", o.install)
	g.Post("/uninstall", o.uninstall)
	g.Get("/jobs", o.listJobs)
	g.Post("/jobs", o.hire, zip.WithStatus(http.StatusCreated))
	g.Get("/jobs/:id", o.getJob)
	g.Post("/jobs/:id/accept", o.accept)
	g.Post("/jobs/:id/decline", o.decline)
	g.Post("/jobs/:id/cancel", o.cancel)
	g.Post("/jobs/:id/deliver", o.deliver)
	g.Post("/jobs/:id/release", o.release)
	g.Post("/jobs/:id/dispute", o.dispute)
	g.Post("/jobs/:id/refund", o.refund)
	g.Post("/jobs/:id/resolve", o.resolve)
	g.Post("/jobs/:id/feedback", o.feedback, zip.WithStatus(http.StatusCreated))
	g.Get("/seller", o.seller)
	g.Post("/seller/payout", o.challengePayout)
	g.Post("/seller/payout/verify", o.bindPayout)

	ctx, stop := context.WithCancel(context.Background())
	s.State.stop = stop
	s.Go("marketplace.sweep", nil, func() { o.sweep(ctx) })

	s.Log.Info("marketplace mounted", "prefix", "/v1/marketplace", "brand", cloud.Brand())
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// marketOps binds the service to marketplace's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
//
// sweepAt, when set, is the clock the sweep reads instead of the wall clock.
// Production never sets it. A test that runs two sweeps at one instant fixes it, so
// how long a sweep takes on a loaded machine cannot change what the next one finds due.
type marketOps struct {
	s       *cloud.Service[state]
	sweepAt func() time.Time
}

// projectOf is the caller's project sub-scope. It lives in a header rather than in
// the tenant key, so it needs the request; off the HTTP path there is none, and the
// caller lands in the org's default project exactly as an absent header does.
func projectOf(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return principal.DefaultProject
	}
	return principal.Project(c)
}

// Shutdown detaches the payment clients and closes the store, in that order.
// Idempotent.
//
// Detaching first is the whole point: both clients close over the store, so leaving
// them installed past Close would leave a price table answering from a closed
// database — and a price lookup that errors fails a dispatch closed, turning a
// clean shutdown into 402s on every tool in the process. Nothing priced, nothing
// charged, no dangling reader.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	x402.Publish(nil)
	tools.SetCharger(nil)
	if mounted.State.stop != nil {
		mounted.State.stop()
	}
	var err error
	if mounted.State.store != nil {
		err = mounted.State.store.Close()
	}
	mounted = nil
	return err
}

// ── discovery ───────────────────────────────────────────────────────────────────

// marketItem is a discoverable capability: the registry tool enriched with any
// public listing's shop metadata + whether the caller has it installed (activated).
type marketItem struct {
	tools.Tool
	// Title is the shop-window name, painted over the registry Name from the
	// CHEAPEST public listing of this very tool — the platform's, for a tool every
	// org reaches — the row a call would pay. Absent when the tool is not listed:
	// that row is a plain capability, not an offer.
	Title string `json:"title,omitempty"`
	// Category is that same listing's grouping. Free text chosen by the publisher,
	// absent when there is no public listing or the publisher left it blank.
	Category string `json:"category,omitempty"`
	// Installed is whether the tool is activated for THIS caller's (org, project):
	// the same bit as Activated, under the shop's name for it, which install and
	// uninstall are the writes for. It is per caller, so one listing reads
	// installed for one org and not for another.
	Installed bool `json:"installed"`
}

// marketCatalog is one discovery read.
type marketCatalog struct {
	// Items is every capability the caller can see in their own (org, project),
	// each carrying any public listing's shop metadata and whether it is installed.
	Items []marketItem `json:"items"`
}

// Discover lists every tool and agent the caller can reach in their own org and
// project, enriched with any public listing's title, category and price, and with
// installed=true on the ones already activated for that scope. It is the shop
// window: one read that answers what exists, what it costs and what is already on.
func (o marketOps) discover(ctx context.Context, _ *cloud.Unit) (*marketCatalog, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	set, err := toolpeer.ToolsList(ctx, &client.ToolsIn{Project: projectOf(ctx)})
	if err != nil {
		return nil, peerErr("the tool plane", err)
	}
	byTool, err := o.s.State.store.PublicByTool(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "listings: %v", err)
	}
	out := make([]marketItem, 0, len(set.Tools))
	for _, b := range set.Tools {
		t := tools.Tool{Name: b.Name, Source: tools.Source(b.Source), Description: b.Description, Schema: b.Schema,
			Dispatchable: b.Dispatchable, Activated: b.Activated}
		item := marketItem{Tool: t, Installed: t.Activated}
		if l, ok := byTool[client.ToolResource(b.Source, b.Name)]; ok {
			item.Title = l.Title
			item.Category = l.Category
			if l.Price.Sign() > 0 {
				item.Price = &tools.Price{Amount: l.Price, Currency: l.Currency, Recipient: l.Recipient}
			}
		}
		out = append(out, item)
	}
	return &marketCatalog{Items: out}, nil
}

// ── install / uninstall (== tool activation) ────────────────────────────────────

// installReq names the tool to install or uninstall.
type installReq struct {
	// Tool is the registry name of the capability to activate (or deactivate) for
	// the caller's own org and project. Required.
	Tool string `json:"tool"`
}

// installState reports a tool's activation for the caller's (org, project).
type installState struct {
	// Tool is the capability the write applied to.
	Tool string `json:"tool"`
	// Installed is its activation after the write.
	Installed bool `json:"installed"`
}

// Install activates one tool for the caller's own org and project. A marketplace
// install IS the tool plane's activation write — one store, one truth — so an
// installed capability is immediately dispatchable and a monetized one is priced
// from its listing at every call. The tool must resolve in the caller's scope, so
// installing something that does not exist is refused rather than recorded.
//
// Example: {"tool": "summarize"}
func (o marketOps) install(ctx context.Context, in *installReq) (*installState, error) {
	return o.activate(ctx, in, true)
}

// Uninstall deactivates one tool for the caller's own org and project, so it stops
// being dispatchable there. It is the exact inverse of install and touches the same
// activation record; deactivating something that was never active is not an error.
// The listing itself is untouched — this withdraws the caller's use of a capability,
// not anyone's offer of it.
//
// Example: {"tool": "summarize"}
func (o marketOps) uninstall(ctx context.Context, in *installReq) (*installState, error) {
	return o.activate(ctx, in, false)
}

// activate is install and uninstall: the tool plane's one activation write (or
// the train plane's capability mount for KindCapability), asked of the process
// that holds it, and the count the shop shows.
func (o marketOps) activate(ctx context.Context, in *installReq, on bool) (*installState, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	tool := strings.TrimSpace(in.Tool)
	if tool == "" || len(tool) > maxName {
		return nil, zip.ErrBadRequest("tool is required")
	}
	project := projectOf(ctx)
	if capListing, found, err := o.s.State.store.PublicCapability(ctx, tool); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "install: %v", err)
	} else if found {
		inst, err := trainpeer.TrainCapabilityInstall(ctx, &client.CapabilityInstallIn{
			Ref:  capListing.Ref,
			Name: capListing.Tool,
			On:   on,
		})
		if err != nil {
			return nil, peerErr("the train plane", err)
		}
		if err := o.s.State.store.installed(ctx, org, project, capListing.Tool, on); err != nil {
			o.s.Log.Warn("marketplace: install count", "tool", capListing.Tool, "err", err)
		}
		result := "uninstalled"
		if on {
			result = "installed"
		}
		record(ctx, o.s, org, "install:"+capListing.Tool, result, http.StatusOK)
		return &installState{Tool: inst.Name, Installed: inst.Installed}, nil
	}
	if _, err := toolpeer.ToolsActivate(ctx, &client.ActivateIn{Tool: tool, On: on, Project: project}); err != nil {
		return nil, peerErr("the tool plane", err)
	}
	if err := o.s.State.store.installed(ctx, org, project, tool, on); err != nil {
		o.s.Log.Warn("marketplace: install count", "tool", tool, "err", err)
	}
	result := "uninstalled"
	if on {
		result = "installed"
	}
	record(ctx, o.s, org, "install:"+tool, result, http.StatusOK)
	return &installState{Tool: tool, Installed: on}, nil
}

// noStore marks a response that carries an org's own standing or money: it must
// not sit in a browser's or an intermediary's cache.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// peerErr answers a peer's failure as this op's: a refusal the peer decided (a
// 4xx) is passed through as it was said, an absent peer is 503, and anything else
// is the peer failing.
func peerErr(what string, err error) error {
	if errors.Is(err, cloud.ErrNoPeer) {
		return zip.Errorf(http.StatusServiceUnavailable, "%s is not reachable", what)
	}
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		return he
	}
	return zip.Errorf(http.StatusBadGateway, "%s: %v", what, err)
}

// trail puts one change on the audit trail, for org, and fails when the trail
// does not take it: the job and payout acts call it inside the transaction that
// makes the change, so a change the trail refused is not made. by is the org or
// arbiter that acted, or the marketplace for a clock that ran out.
func (o marketOps) trail(ctx context.Context, org, by, action string, res audit.Resource, after map[string]any) error {
	rec := audit.Record{
		Actor:    audit.Actor{Org: org, Sub: "platform:marketplace"},
		Action:   action,
		Resource: res,
		Auth:     audit.AuthContext{Method: "platform"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
	}
	if c, ok := cloud.Request(ctx); ok {
		rec.Actor.Sub, rec.Actor.Email = c.User(), c.UserEmail()
		rec.Auth = audit.AuthContext{Method: "gateway", IsAdmin: principal.Super(c)}
		rec.Method, rec.Path, rec.SourceIP, rec.RequestID = c.Method(), c.Path(), geo.IP(c), c.RequestID()
	}
	after["by"] = by
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	rec.After = audit.Redact(body)
	if _, err := o.s.State.audit.Append(ctx, rec); err != nil {
		o.s.Log.Error("marketplace: audit append failed", "action", action, "id", res.ID, "err", err)
		return errors.New("marketplace: the audit trail did not take the record, so nothing was changed")
	}
	return nil
}

// record appends one audit row. It takes the CONTEXT rather than the request because
// its callers are typed ops, and it reaches the request through the same client they
// do; off the HTTP path there is no actor and no path to attribute, so it records
// nothing rather than an anonymous half-row.
func record(ctx context.Context, s *cloud.Service[state], org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    "marketplace." + result,
		Resource:  audit.Resource{Type: "marketplace", ID: resourceID},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: result, Status: status},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  geo.IP(c),
		RequestID: c.RequestID(),
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Warn("audit append failed", "err", err)
	}
}
