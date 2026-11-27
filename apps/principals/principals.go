package principals

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// state is the app's own data; the shared deps live in the embedded cloud.Base.
type state struct {
	// store holds screening matches and clearances, one file for the deployment:
	// the review queue is the platform's, across orgs.
	store *store
	// lists are the published sanctions lists, loaded and kept fresh here.
	lists *lists
	// keys sign attestations; the key set lives in KMS.
	keys *keyring
	// audit is the tamper-evident trail every decision is written to.
	audit *audit.Recorder
	// claims puts statements on chain; nil until the chain binding lands.
	claims Issuer
	// stop ends the statements sweep.
	stop context.CancelFunc
}

var mounted *cloud.Service[state]

// Use mounts /v1/principal.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("principal: nil app")
	}
	b := cloud.NewBase(deps, "principal")
	if b.DataDir == "" {
		return errors.New("principals.Use: empty DataDir")
	}
	st, err := locus.Use(context.Background(), nil, namespace.System(), "principal", b.DataDir, func(db *sql.DB) (*store, error) {
		return openStore(db, deps.MasterKey)
	})
	if err != nil {
		return fmt.Errorf("principals.Use: %w", err)
	}
	s := &cloud.Service[state]{Base: b, State: state{
		store: st,
		lists: newLists(published, b.Log),
		keys:  &keyring{kms: deps.KMS, log: b.Log},
		audit: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	expose(s)
	ctx, stop := context.WithCancel(context.Background())
	s.State.stop = stop
	s.Go("principal.statements", nil, func() { ops{s: s}.sweep(ctx) })
	s.Log.Info("principal mounted", "audit", deps.Audit != nil, "kms", deps.KMS != nil)
	return nil
}

// Shutdown stops the list refresh and closes the store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	mounted.State.lists.close()
	mounted.State.stop()
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// routes registers the surface. cloud.Bridge is the composer's, installed once at
// the root; the ops read what it parks.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/principal")
	g.Get("", o.view)

	g.Post("/clearance", o.clear, zip.WithStatus(http.StatusCreated))
	g.Get("/clearance", o.clearances)
	g.Get("/clearance/:id", o.clearance)

	g.Get("/matches", o.matches)
	g.Post("/matches/:id", o.decide)

	g.Get("/agents/:ref", o.attest)
	g.Get("/statements/:id", o.statement)
	g.Get("/keys", o.published)
	g.Post("/keys", o.rotate, zip.WithStatus(http.StatusCreated))
}

// ops binds the service to the typed ops; a method value is the only bound form
// zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// member resolves the caller's org: any validated member.
func (o ops) member(ctx context.Context) (string, error) {
	return principal.Acting(ctx)
}

// full reports whether the caller sees the detail behind a verdict: an admin of
// its own org, or SuperAdmin.
func full(ctx context.Context) bool { return cloud.Admin.Admits(cloud.AuthorityIn(ctx)) }

// super admits SuperAdmin alone, for the acts on shared platform state: deciding
// a sanctions match, rotating the signing key. It runs in the op rather than on
// the route because a typed op is reached by REST, MCP and the call plane alike.
func (o ops) super(ctx context.Context, act string) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return "", zip.ErrForbidden("a Hanzo platform reviewer (SuperAdmin) is required to " + act + "; an org admin may not")
	}
	if err := cloud.CSRF(ctx); err != nil {
		return "", err
	}
	if o.s.State.audit == nil {
		return "", zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	return org, nil
}

// record builds the trail's record of one decision. The After map carries ids,
// verdicts and rule codes — never a name, a number or an address. A decision
// made by a person carries who made it; one the platform makes in the course of
// another org's request — screening a payee — carries no person, because the
// org it is about is not told who was about to pay it.
func (o ops) record(ctx context.Context, org, action string, res audit.Resource, after map[string]any, person bool) (audit.Record, error) {
	if o.s.State.audit == nil {
		return audit.Record{}, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   action,
		Resource: res,
		Auth:     audit.AuthContext{Method: "gateway"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
	}
	if c, ok := cloud.Request(ctx); ok {
		rec.RequestID = c.RequestID()
		if person {
			rec.Actor.Sub, rec.Actor.Email = c.User(), c.UserEmail()
			rec.Auth.IsAdmin = principal.Super(c)
			if home := principal.Owner(c); rec.Auth.IsAdmin && home != org {
				rec.Actor.Home = home // a SuperAdmin acting inside this org — the trail must name it
			}
			rec.Method, rec.Path = c.Method(), c.Path()
			rec.SourceIP = geo.IP(c)
		} else {
			rec.Actor.Sub, rec.Auth.Method = "platform:principal", "platform"
		}
	}
	body, err := json.Marshal(after)
	if err != nil {
		return audit.Record{}, zip.Errorf(http.StatusInternalServerError, "principal: audit: %v", err)
	}
	rec.After = audit.Redact(body)
	return rec, nil
}

// append puts a record on the trail. It is not best-effort: a decision the trail
// did not take is not made, because a sanctions decision or a clearance that
// cannot say who made it is the defect this surface exists to avoid. Callers
// append inside the transaction that writes the decision, as its last step.
func (o ops) append(ctx context.Context, rec audit.Record) error {
	if _, err := o.s.State.audit.Append(ctx, rec); err != nil {
		o.s.Log.Error("principal: audit append failed", "action", rec.Action, "id", rec.Resource.ID, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "principal: the audit trail did not take the record, so nothing was changed")
	}
	return nil
}

// unmade follows records the trail took for a change that did not commit with
// the same records marked as not made, so the trail never shows a decision in
// force that the store does not hold.
func (o ops) unmade(ctx context.Context, recs []audit.Record) {
	for _, r := range recs {
		r.Outcome = audit.Outcome{Result: "error", Status: http.StatusServiceUnavailable, Reason: "recorded and not committed; nothing was changed"}
		if _, err := o.s.State.audit.Append(ctx, r); err != nil {
			o.s.Log.Error("principal: the trail holds a change that did not commit, and the correction could not be recorded",
				"action", r.Action, "id", r.Resource.ID, "err", err)
		}
	}
}

// commit runs one decision's transaction and answers what the caller answers:
// nothing, or a refusal. write writes the rows and returns the records to put on
// the trail; they are appended last, inside the transaction.
func (o ops) commit(ctx context.Context, write func(*sql.Tx) ([]audit.Record, error)) error {
	var made []audit.Record
	err := o.s.State.store.within(ctx, func(tx *sql.Tx) error {
		recs, err := write(tx)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if err := o.append(ctx, r); err != nil {
				return err
			}
			made = append(made, r)
		}
		return nil
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errUncommitted):
		o.unmade(ctx, made)
		return zip.Errorf(http.StatusServiceUnavailable, "principal: the change could not be committed, so nothing was changed; retry")
	}
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
}

// errUnaudited refuses a decision on a process with no audit trail, before it is
// made.
var errUnaudited = errors.New("principal: no audit trail on this process, and a decision is never made unrecorded")

// user is the validated person making the request, for the record of who
// decided.
func user(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// noStore marks a response that carries an org's facts: it must not sit in a
// cache.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}
