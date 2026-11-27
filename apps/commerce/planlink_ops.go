// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// planlink_ops.go — the plan-link ops, and the rules a link is made and ended by.
//
// apps/billing resolves WHO the caller is from their credential and states it here: the
// accounts they hold (Held) and whether they administer the org they act in
// (Privileged), as the subscription ops state Privileged. What is decided here is
// everything the books and the store can say: whether the org may borrow, whether it
// holds a plan of its own, whether the source's plan is live, and what is already lent.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeLink publishes the plan-link ops. Use calls it.
func exposeLink() {
	cloud.Plane().Post("/billing/link", planeLink,
		zip.WithOperationID(client.BillingLink),
		zip.WithSummary("The plan an org borrows, and the plans the caller could lend it"))
	cloud.Plane().Post("/billing/link/put", planeLinkPut,
		zip.WithOperationID(client.BillingLinkPut),
		zip.WithSummary("Lend a plan to an org"))
	cloud.Plane().Post("/billing/link/drop", planeLinkDrop,
		zip.WithOperationID(client.BillingLinkDrop),
		zip.WithSummary("End a plan link"))
}

// refuse is a refusal with the stable code a program reads beside its status.
func refuse(status int, code, msg string) error {
	return &zip.HTTPError{Status: status, Code: code, Msg: msg}
}

// accountOf reads the wire spelling of an account — "<org>/<name>" for a person's wallet,
// "<org>" for an org's — which is account.Account.Subject. A person always has a slash
// and an org slug never does, so the spelling names its kind. Zero when it names nothing.
func accountOf(s string) account.Account {
	org, name, person := strings.Cut(strings.TrimSpace(s), "/")
	if person {
		return account.Person(org, name)
	}
	return account.Org(org)
}

// lendable reports whether a is an account whose plan may be lent at all: a person's own
// wallet in the shared signup org, or an org account of an org that is nobody's platform.
func lendable(a account.Account) bool {
	switch {
	case a.Zero():
		return false
	case a.Kind() == account.Person(account.SignupOrg, "x").Kind():
		return a.Org() == account.SignupOrg
	case a.Kind() == account.Org("x").Kind():
		return !authz.IsHeldOrg(a.Org())
	}
	return false
}

// heldSet is the accounts the caller states they hold, as values.
func heldSet(held []string) []account.Account {
	out := make([]account.Account, 0, len(held))
	for _, s := range held {
		if a := accountOf(s); lendable(a) {
			out = append(out, a)
		}
	}
	return out
}

// mayBorrow is the refusal an org cannot borrow a plan for: the shared signup org, a held
// or reserved org. A fact about the org, so it is asked before who is asking.
func mayBorrow(org string) error {
	if strings.EqualFold(org, account.SignupOrg) || authz.IsHeldOrg(org) {
		return refuse(http.StatusBadRequest, client.LinkOrgRefused,
			"This organization cannot use a linked plan.")
	}
	return nil
}

// linkRefusal is why the caller may not link a plan to org right now, nil when they may:
// the ONE rule behind the read's canLink and the write's refusals, in the order a person
// should learn them — the org cannot borrow, the caller is not its admin, it holds a plan
// of its own, it already borrows one.
func linkRefusal(ctx context.Context, org string, privileged bool) error {
	if err := mayBorrow(org); err != nil {
		return err
	}
	if !privileged {
		return zip.ErrForbidden("Only an owner or admin of " + org + " can link a plan to it.")
	}
	if _, paid, err := ownPaidPlan(ctx, org); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "failed to read the organization's plan")
	} else if paid {
		return refuse(http.StatusConflict, client.LinkOrgHasPlan, "This organization already has a plan of its own.")
	}
	s, err := links()
	if err != nil {
		return err
	}
	_, ok, err := s.ByOrg(ctx, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "failed to read the plan link")
	}
	if ok {
		return refuse(http.StatusConflict, client.LinkOrgTaken, "This organization already uses a linked plan.")
	}
	return nil
}

// unlinkRefusal is why the caller may not end l, nil when they may: the ONE rule behind
// the read's canUnlink and the write's refusal. An owner or admin of the borrowing org
// may, and so may whoever holds the account the plan is lent from, from any org they act in.
func unlinkRefusal(l planLink, held []account.Account, privileged bool) error {
	if privileged || slices.Contains(held, l.From) {
		return nil
	}
	return zip.ErrForbidden("Only an owner or admin of " + l.Org + ", or the person whose plan it is, can unlink it.")
}

// declined reports whether err is a refusal the caller is told (4xx), as against a read
// that could not be made.
func declined(err error) bool {
	he, ok := errors.AsType[*zip.HTTPError](err)
	return ok && he.Status < http.StatusInternalServerError
}

// stateOf is the answer every link op gives: the plan org borrows, the live paid plans
// the caller holds, and what the caller may do from here.
func stateOf(ctx context.Context, org string, held []account.Account, privileged bool) (*client.PlanLinkState, error) {
	s, err := links()
	if err != nil {
		return nil, err
	}
	out := &client.PlanLinkState{Plans: []client.LendablePlan{}}
	l, ok, err := s.ByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "failed to read the plan link")
	}
	if ok {
		pl := client.PlanLink{Org: l.Org, From: l.From.Subject(), By: l.By, At: l.At.Format(time.RFC3339)}
		view, live, err := sourcePlan(ctx, l.From)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "failed to read the linked plan")
		}
		if live {
			pl.Live, pl.Plan, pl.PlanName = true, view.Plan, view.PlanName
		}
		out.Link = &pl
		out.CanUnlink = unlinkRefusal(l, held, privileged) == nil
	}
	for _, a := range held {
		view, live, err := sourcePlan(ctx, a)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "failed to read a plan you hold")
		}
		if !live {
			continue
		}
		lent := client.LendablePlan{From: a.Subject(), Plan: view.Plan, PlanName: view.PlanName}
		if to, ok, err := s.BySource(ctx, a); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "failed to read the plan link")
		} else if ok {
			lent.Org = to.Org
		}
		out.Plans = append(out.Plans, lent)
	}
	switch err := linkRefusal(ctx, org, privileged); {
	case err == nil:
		out.CanLink = true
	case !declined(err):
		return nil, err
	}
	return out, nil
}

// Answers the plan the org the caller acts in borrows, and the live paid plans the
// caller holds and could lend it.
//
// The link is the org's, whoever asks; the plans are the caller's own, from the Held
// list the route resolved from their credential. `live` is false when the source's plan
// has lapsed, and the link then confers nothing. canLink and canUnlink are the rules
// PUT and DELETE enforce, asked once and answered here.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLink(ctx context.Context, in *client.PlanLinkIn) (*client.PlanLinkState, error) {
	org, err := orgOf(ctx, "plan link")
	if err != nil {
		return nil, err
	}
	return stateOf(ctx, org.Name, heldSet(in.Held), in.Privileged)
}

// Lends one of the caller's live paid plans to the org the caller acts in.
//
// The org must be able to borrow (not the shared signup org, a held or reserved org, nor
// the plan's own), the caller its owner or admin, the org without a live paid plan of
// its own and without a link, and the plan one of the caller's Held accounts that holds a
// live paid plan now and is lent to no other org. The two uniques of the store decide a
// race: one link wins and the other is refused 409. The plan is READ from the source's
// own subscriptions on every use; nothing is copied, no money moves and no credit is
// minted.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLinkPut(ctx context.Context, in *client.PlanLinkPutIn) (*client.PlanLinkState, error) {
	org, err := orgOf(ctx, "link plan")
	if err != nil {
		return nil, err
	}
	from, held := accountOf(in.From), heldSet(in.Held)
	// A plan's own org is the one org it cannot be lent to, whatever the org holds.
	if !from.Zero() && strings.EqualFold(from.Org(), org.Name) {
		return nil, refuse(http.StatusBadRequest, client.LinkOrgRefused, "A plan cannot be linked to the organization it belongs to.")
	}
	if err := linkRefusal(ctx, org.Name, in.Privileged); err != nil {
		return nil, err
	}
	if !slices.Contains(held, from) {
		return nil, refuse(http.StatusBadRequest, client.LinkNoPlan, "That is not a plan you hold.")
	}
	if _, live, err := sourcePlan(ctx, from); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "failed to read the plan to link")
	} else if !live {
		return nil, refuse(http.StatusBadRequest, client.LinkNoPlan, "That account has no live paid plan to link.")
	}
	s, err := links()
	if err != nil {
		return nil, err
	}
	switch err := s.Put(ctx, planLink{Org: org.Name, From: from, By: in.By, At: time.Now().UTC()}); {
	case errors.Is(err, errOrgLinked):
		return nil, refuse(http.StatusConflict, client.LinkOrgTaken, "This organization already uses a linked plan.")
	case errors.Is(err, errSourceLinked):
		return nil, refuse(http.StatusConflict, client.LinkPlanTaken, "That plan is already linked to an organization.")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "failed to link the plan")
	}
	return stateOf(ctx, org.Name, held, in.Privileged)
}

// Ends a plan link: the acting org's when From is empty, else the one the plan of From
// is lent through.
//
// The acting org's link is ended by an owner or admin of the org, or by whoever holds the
// account it is lent from. With From, the account must be one the caller holds, and the
// link goes whatever org the caller acts in — so a person removed from the borrowing org
// can still take their plan back. Nothing linked is 404; the plan is free to be lent
// again once it is ended, and the borrowing org reads no plan once the sdk's tier answer
// ages out (about thirty seconds).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLinkDrop(ctx context.Context, in *client.PlanLinkDropIn) (*client.PlanLinkState, error) {
	org, err := orgOf(ctx, "unlink plan")
	if err != nil {
		return nil, err
	}
	s, err := links()
	if err != nil {
		return nil, err
	}
	held, privileged := heldSet(in.Held), in.Privileged
	var l planLink
	var ok bool
	if from := strings.TrimSpace(in.From); from == "" {
		l, ok, err = s.ByOrg(ctx, org.Name)
	} else {
		a := accountOf(from)
		if !slices.Contains(held, a) {
			return nil, zip.ErrForbidden("That is not a plan you hold.")
		}
		privileged = false // the other door: it opens on the account alone
		l, ok, err = s.BySource(ctx, a)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "failed to read the plan link")
	}
	if !ok {
		return nil, zip.Errorf(http.StatusNotFound, "no plan is linked")
	}
	if err := unlinkRefusal(l, held, privileged); err != nil {
		return nil, err
	}
	// Ended as read: a link another act replaced since is not this one, and is left.
	if gone, err := s.Drop(ctx, l); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "failed to unlink the plan")
	} else if !gone {
		return nil, zip.Errorf(http.StatusNotFound, "no plan is linked")
	}
	return stateOf(ctx, org.Name, held, in.Privileged)
}
