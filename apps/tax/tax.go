package tax

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// state is tax's own data; the shared deps live in the embedded cloud.Base.
type state struct {
	// stores is one encrypted file per org, opened the first time that org uses tax.
	stores *cloud.OrgStore[*store]
	// audit is the tamper-evident trail every state change is written to.
	audit *audit.Recorder
}

var mounted *cloud.Service[state]

// Use mounts /v1/tax.
//
// The two things tax will not act without — the data-plane master a TIN is
// sealed under, and the audit trail every change is written to — are asked for
// at each act rather than at the mount, so a process that only describes the
// surface mounts, and one missing either refuses the act before it is made:
// a TIN is never written unsealed, and no change is made unrecorded.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("tax: nil app")
	}
	b := cloud.NewBase(deps, "tax")
	if b.DataDir == "" {
		return errors.New("tax.Use: empty DataDir")
	}
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStoreAt(b, subsystem, openStore(deps.MasterKey)),
		audit:  deps.Audit,
	}}
	mounted = s
	routes(app, s)
	expose(s)
	s.Log.Info("tax mounted")
	return nil
}

// Shutdown closes every org's store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}

// routes registers the surface. cloud.Bridge is the composer's, installed once at
// the root; the ops read what it parks.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/tax")

	g.Get("/profile", o.profile)
	g.Put("/profile", o.putProfile)
	g.Post("/profile/certify", o.certify)

	g.Post("/w9", o.requestW9, zip.WithStatus(http.StatusCreated))
	g.Get("/w9", o.listW9)
	g.Get("/w9/:id", o.w9)
	g.Get("/w9/:id/tin", o.tin)
	g.Post("/w9/:id/grant", o.grant)
	g.Post("/w9/:id/decline", o.decline)
	g.Post("/w9/:id/revoke", o.revoke)
	g.Post("/w9/:id/match", o.match)

	g.Get("/payments", o.payments)

	g.Post("/forms", o.prepare)
	g.Get("/forms", o.forms)
	g.Get("/forms/:id", o.form)
	g.Post("/forms/:id/review", o.review)
	g.Post("/forms/:id/furnish", o.furnish)
	g.Post("/forms/:id/mailed", o.mailed)
	g.Post("/forms/:id/correct", o.correct, zip.WithStatus(http.StatusCreated))
	g.Post("/forms/:id/void", o.void)
	g.Raw(http.MethodGet, "/forms/:id/pdf", cloud.Handle(s, formPDF))

	g.Get("/inbox", o.inbox)
	g.Get("/inbox/:id", o.statement)
	g.Raw(http.MethodGet, "/inbox/:id/pdf", cloud.Handle(s, statementPDF))

	g.Post("/filings", o.export, zip.WithStatus(http.StatusCreated))
	g.Get("/filings", o.filings)
	g.Get("/filings/:id", o.filing)
	g.Post("/filings/:id/receipt", o.receipt)
}

// ops binds the service to the typed ops; a method value is the only bound form
// zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// storeOf opens org's file. org is always a validated principal's; a counterparty's
// file is opened through other, never here.
func (o ops) storeOf(org string) (*store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, zip.ErrBadRequest("tax: " + err.Error())
	}
	st, err := o.s.State.stores.For(ns)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: store: %v", err)
	}
	return st, nil
}

// member resolves the caller's org and its store: any validated member.
func (o ops) member(ctx context.Context) (string, *store, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	st, err := o.storeOf(org)
	return org, st, err
}

// admin resolves the caller's org and store for an act only an org admin (or
// SuperAdmin) may take. write adds the anti-forgery check a state change owes.
//
// It runs in the op rather than on the route because a typed op is reached by
// REST, MCP and the call plane alike, and the op is the one place all of them
// pass through.
func (o ops) admin(ctx context.Context, act string, write bool) (string, *store, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return "", nil, zip.ErrForbidden("org admin required to " + act)
	}
	if write {
		if err := cloud.CSRF(ctx); err != nil {
			return "", nil, err
		}
		if o.s.State.audit == nil {
			return "", nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
		}
	}
	st, err := o.storeOf(org)
	return org, st, err
}

// own is admin for what only the company's OWN people may have or do: an answer
// that carries a payee's TIN in full (the audited TIN read, the IRS export and its
// re-read), and signing the company's W-9 certification under penalties of
// perjury. Platform authority — a SuperAdmin acting inside the org — is neither a
// reason to read a company's TIN nor a person who can certify for it, and every
// door to either asks this same question, so the export cannot be the way around
// the TIN read.
func (o ops) own(ctx context.Context, act string, write bool) (string, *store, error) {
	org, st, err := o.admin(ctx, act, write)
	if err != nil {
		return "", nil, err
	}
	if c, ok := cloud.Request(ctx); !ok || !principal.IsOrgAdmin(c) {
		return "", nil, zip.ErrForbidden("only the org's own admin may " + act + "; platform authority may not")
	}
	return org, st, nil
}

// other opens ANOTHER org's file — to read it or to write it — and only on the
// replica that is that org's elected writer. A request is routed to the replica
// that owns the CALLER's org; the counterparty's file may be owned elsewhere, and
// there it is either a read-only copy hydrated when this replica first opened it
// or a copy this replica held before a membership change moved the org away.
// Neither sees what the owner wrote since: a grant revoked there still reads as
// granted here, and the payee's TIN would be answered from it. A write would be a
// second copy of the payee's records that never ships. So both are refused here,
// before anything is read or changed, rather than served stale or forked. Without
// a durable plane every org is this process's own and this always opens.
func (o ops) other(org string) (*store, error) {
	st, err := o.storeOf(org)
	if err != nil {
		return nil, err
	}
	ns, _ := principal.OrgNamespace(org, "")
	if !o.s.State.stores.Owned(ns) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: this replica is not the elected writer for %s's tax records, so they are neither read nor changed here", org)
	}
	return st, nil
}

// commit makes a change durable in every org file it wrote — the caller's, and
// any counterparty's named in also — and then records it on the trail. A write a
// replica cannot ship is refused rather than acknowledged: two divergent copies
// of a company's tax records is the failure a fenced store exists to prevent.
func (o ops) commit(ctx context.Context, org, action string, res audit.Resource, after map[string]any, also ...string) error {
	if err := o.ship(append([]string{org}, also...)...); err != nil {
		return err
	}
	return o.audited(ctx, org, action, res, after)
}

// ship makes each org's written file durable, or says why it could not.
func (o ops) ship(orgs ...string) error {
	for _, who := range orgs {
		ns, err := principal.OrgNamespace(who, "")
		if err != nil {
			return zip.ErrBadRequest("tax: " + err.Error())
		}
		acked, err := o.s.State.stores.Sync(ns)
		if err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "tax: the change was written and could not be made durable: %v", err)
		}
		if !acked {
			return zip.Errorf(http.StatusServiceUnavailable, "tax: this replica is not the elected writer for %s, so the change is not acknowledged", who)
		}
	}
	return nil
}

// audited appends one state change to the trail. It is not best-effort: a change
// the trail did not take is reported to the caller as a failure, because a tax
// record that cannot say who moved it is the defect this whole surface exists to
// avoid. The After map carries ids, statuses and years — never a TIN, a name or an
// address.
func (o ops) audited(ctx context.Context, org, action string, res audit.Resource, after map[string]any) error {
	if o.s.State.audit == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   action,
		Resource: res,
		Auth:     audit.AuthContext{Method: "gateway"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
	}
	if c, ok := cloud.Request(ctx); ok {
		rec.Actor.Sub, rec.Actor.Email = c.User(), c.UserEmail()
		rec.Auth.IsAdmin = principal.Super(c)
		if home := principal.Owner(c); rec.Auth.IsAdmin && home != org {
			rec.Actor.Home = home // a SuperAdmin acting inside this org — the impersonation the trail must name
		}
		rec.Method, rec.Path = c.Method(), c.Path()
		rec.SourceIP, rec.RequestID = geo.IP(c), c.RequestID()
	}
	body, err := json.Marshal(after)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "tax: audit: %v", err)
	}
	rec.After = audit.Redact(body)
	if _, err := o.s.State.audit.Append(ctx, rec); err != nil {
		o.s.Log.Error("tax: audit append failed", "action", action, "id", res.ID, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "tax: the audit trail did not take the record")
	}
	return nil
}

// errUnaudited refuses an act on a process with no audit trail, before it is made.
var errUnaudited = errors.New("tax: no audit trail on this process, and a tax change is never made unrecorded")

// noStore marks a response that carries tax data: it must not sit in a cache.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// The two metered acts. Billing's catalog prices neither, so each is metered at
// zero: the meter is wired around both, and the price is the catalog's to set when
// it has one. There is no environment knob — a price that changed with a pod's
// environment would be a charge nobody can read off the product.
const (
	actPrepare = "prepare"
	actFurnish = "furnish"
)

// charge authorizes one metered act before it runs. The caller defers Release,
// and Debits the usage once the act has happened.
func (o ops) charge(ctx context.Context, act string) (*cloud.Charge, meter.Usage, error) {
	ch, err := o.s.Bill.Reserve(ctx, cloud.PayerOf(ctx), act, 0)
	if err != nil {
		return nil, meter.Usage{}, cloud.Denied(err)
	}
	return ch, meter.Usage{Model: act}, nil
}

func now() int64 { return time.Now().Unix() }

// decode reads a row's JSON column into v. A row this package wrote that will not
// decode is a store fault, never an empty value.
func decode(raw string, v any) error {
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "tax: stored record: %v", err)
	}
	return nil
}

// encode renders a row's JSON column. json.Marshal fails only on a channel, a
// func, a cycle or a non-finite float, and every value stored here is plain
// strings, integers and slices of them.
func encode(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
