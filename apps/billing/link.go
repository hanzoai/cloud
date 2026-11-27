package billing

// link.go serves the plan link: an org on no plan of its own uses one that a person it
// trusts already pays for on another account.
//
// What is decided here is WHO the caller is and what they hold, from their own
// credential and from nothing in the request: whether they are an owner or admin of the
// org they act in, the account that is their own wallet in the shared signup org, and
// the orgs they own. commerce decides the rest against the books (apps/commerce
// planlink_ops.go), and answers a refusal with a status and a stable code.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	iamsdk "github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
)

// mountLink registers the plan-link family. Called from routes.
func mountLink(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Get("/link", o.planLink,
		zip.WithOperationID("getPlanLink"),
		zip.WithSummary("The plan this org borrows, and the plans you could lend it"))
	g.Put("/link", o.linkPlan,
		zip.WithOperationID("linkPlan"),
		zip.WithSummary("Use a plan you hold on this org"))
	g.Delete("/link", o.unlinkPlan,
		zip.WithOperationID("unlinkPlan"),
		zip.WithSummary("Stop using a linked plan"))
}

// holdings is who the caller is and what they may lend: their IAM row, and every
// account they hold — their own wallet in the shared signup org, and each org they own.
// An org is theirs only when IAM says they hold an owner membership in it (cloud.Owns),
// never because it is in their token: a plain member of an org must never lend its plan.
// A held org and the signup org are nobody's to lend. An ownership nobody could read is
// not ownership.
type holdings struct {
	row  string
	held []string
}

func holdingsOf(ctx context.Context) (holdings, error) {
	p, err := iamsdk.SelfFrom(ctx)
	if err != nil {
		return holdings{}, err
	}
	h := holdings{row: p.Row, held: []string{}}
	owner, name, ok := strings.Cut(p.Row, "/")
	if !ok || owner == "" || name == "" {
		return h, nil
	}
	if owner == account.SignupOrg {
		h.held = append(h.held, account.Person(owner, name).Subject())
	}
	seen := map[string]bool{}
	for _, org := range principal.OrgsFrom(ctx) {
		org = strings.ToLower(strings.TrimSpace(org))
		if org == "" || seen[org] || authz.IsHeldOrg(org) {
			continue
		}
		seen[org] = true
		owns, err := cloud.Owns(ctx, p.Row, org)
		if err != nil {
			luxlog.Default().Warn("billing: an ownership could not be read; the org's plan is not lendable", "org", org, "err", err)
			continue
		}
		if owns {
			h.held = append(h.held, account.Org(org).Subject())
		}
	}
	return h, nil
}

// Answers the plan the org you act in borrows, and the plans you could lend it.
//
// `link` is the plan this org uses, omitted when it uses none; `live` is false once the
// plan it was lent from has lapsed, and the link then confers nothing. Any member of the
// org may read it. `plans` is every live paid plan YOU hold — your own wallet in the
// shared signup org and each org you own — whoever else is in the org, with `org` naming
// the org one is already lent to. `canLink` and `canUnlink` say what you may do from
// here, by the rules the writes enforce: link a plan if you are an owner or admin of an
// org that may borrow, holds no live paid plan of its own and borrows none; end the link
// if you are an owner or admin of the org or hold the plan it is lent from.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) planLink(ctx context.Context, _ *cloud.Unit) (*client.PlanLinkState, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	h, err := holdingsOf(ctx)
	if err != nil {
		// Reading what the org borrows needs nobody's identity: a caller IAM cannot name
		// holds nothing to lend, and the link is still theirs to see.
		luxlog.Default().Warn("billing: the caller's holdings could not be read", "org", org, "err", err)
		h = holdings{held: []string{}}
	}
	in := client.PlanLinkIn{Held: h.held, Privileged: administers(c)}
	return ask(ctx, org, "read the plan link", func(ctx context.Context) (*client.PlanLinkState, error) {
		return commercepeer.BillingLink(ctx, &in)
	})
}

// linkBody is the plan to link, from the body: one of the `plans` the read lists.
type linkBody struct {
	// From is the account to lend, as `plans` lists it: "<org>/<name>" for your own
	// wallet, "<org>" for an org you own.
	From string `json:"from"`
}

// Uses a plan you hold on the org you act in.
//
// The whole org is covered by it, and one plan is lent to at most one org. You must be
// an owner or admin of the org, and `from` must be one of your own `plans`: your own
// wallet in the shared signup org, or an org you own. The org keeps its own wallet and
// its own included allowance; a plan lent to it is never its to cancel. Refusals: 403
// when you are not an owner or admin; 400 `no_plan_to_link` when `from` is not a live
// paid plan you hold; 400 `org_cannot_link` for the shared signup org, a held or reserved
// org, or the plan's own org; 409 `org_has_plan` when the org already holds a live paid
// plan of its own; 409 `plan_already_linked` when the plan is lent to another org; 409
// `org_already_linked` when the org already uses one. It is on the audit trail.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) linkPlan(ctx context.Context, in *linkBody) (*client.PlanLinkState, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	h, err := holdingsOf(ctx)
	if err != nil {
		return nil, err
	}
	if h.row == "" {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "who you are cannot be read right now; try again")
	}
	from := ""
	if in != nil {
		from = strings.TrimSpace(in.From)
	}
	put := client.PlanLinkPutIn{From: from, Held: h.held, By: h.row, Privileged: administers(c)}
	out, err := ask(ctx, org, "link plan", func(ctx context.Context) (*client.PlanLinkState, error) {
		return commercepeer.BillingLinkPut(ctx, &put)
	})
	if err == nil {
		cloud.InvalidateTier(org)
	}
	linkAudit(c, o.s.State.trail, "billing.plan.link", org, from, put.By, err)
	return out, err
}

// Stops using a linked plan.
//
// Without a query it ends the link of the org you act in: an owner or admin of the org
// may, and so may the person who holds the account the plan is lent from. With
// `from=<account>` it ends the link that account's plan is lent through, from whatever
// org you act in — so someone removed from the borrowing org can still take their plan
// back — and `from` must be one of your own `plans`. Ending a link frees the plan to be
// lent again. Answers 404 when nothing is linked and 403 when it is not yours to end. It
// is on the audit trail.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) unlinkPlan(ctx context.Context, _ *cloud.Unit) (*client.PlanLinkState, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	h, err := holdingsOf(ctx)
	if err != nil {
		return nil, err
	}
	drop := client.PlanLinkDropIn{From: strings.TrimSpace(c.Query("from")), Held: h.held, Privileged: administers(c)}
	out, err := ask(ctx, org, "unlink plan", func(ctx context.Context) (*client.PlanLinkState, error) {
		return commercepeer.BillingLinkDrop(ctx, &drop)
	})
	if err == nil {
		cloud.InvalidateTier("")
	}
	linkAudit(c, o.s.State.trail, "billing.plan.unlink", org, drop.From, h.row, err)
	return out, err
}

// linkAudit puts one link or unlink on the audit trail under the org the request acted
// in, with the account concerned and who asked, and the refusal when there was one: a
// refused attempt to take or lend a plan is as much the trail's as one that landed. It
// is recorded after the fact and a row the trail does not take is logged, as saleAudit's.
func linkAudit(c *zip.Ctx, trail *audit.Recorder, action, org, from, by string, fault error) {
	if trail == nil {
		return
	}
	home := principal.Owner(c)
	if !principal.Super(c) || home == org {
		home = ""
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail(), Home: home},
		Action:    action,
		Resource:  audit.Resource{Type: "plan-link", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  geo.IP(c),
		RequestID: c.RequestID(),
	}
	if fault != nil {
		status := http.StatusInternalServerError
		if he, ok := errors.AsType[*zip.HTTPError](fault); ok {
			status = he.Status
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: fault.Error()}
	}
	b, _ := json.Marshal(map[string]any{"org": org, "from": from, "by": by})
	rec.After = audit.Redact(b)
	if _, err := trail.Append(c.Context(), rec); err != nil {
		luxlog.Default().Error("a plan link act was not put on the audit trail", "org", org, "action", action, "err", err)
	}
}
