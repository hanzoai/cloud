// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// posture_rpc.go — the org's processor posture and its saved cards, over the
// internal plane: which processor a browser tokenizes against, which money the
// org is transacting in, the cards it has on file, and the catalog it buys from.
//
// TWO WIRES, ON PURPOSE. Settings and mode are TYPED, because each is a handful
// of fields this side decides the meaning of. Methods and plans are RENDERED —
// carried as the bytes the store produced — because their wire is a deep tree of
// the store's OWN models, where a mirror here would be sixty fields whose only
// job is to agree with something else. A value the door decides is typed; a
// document it forwards is bytes.

import (
	"context"
	"encoding/json"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/api/promo"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposePosture publishes the posture, methods and catalog ops. Mount calls it.
func exposePosture() {
	zip.Post[struct{}, plane.PaymentConfig](cloud.Plane(), "/billing/settings", planeSettings,
		zip.WithOperationID(plane.BillingSettings),
		zip.WithSummary("Public processor configuration for this org"))
	zip.Post[plane.ModeIn, plane.Mode](cloud.Plane(), "/billing/mode", planeMode,
		zip.WithOperationID(plane.BillingMode),
		zip.WithSummary("Move this org between test and live money"))
	zip.Post[plane.MethodsIn, plane.Rendered](cloud.Plane(), "/billing/methods", planeMethods,
		zip.WithOperationID(plane.BillingMethods),
		zip.WithSummary("Cards and accounts a subject has on file"))
	zip.Post[plane.MethodSaveIn, plane.Rendered](cloud.Plane(), "/billing/method/save", planeMethodSave,
		zip.WithOperationID(plane.BillingMethodSave),
		zip.WithSummary("Save a payment method for a subject"))
	zip.Post[plane.MethodRef, plane.Detachment](cloud.Plane(), "/billing/method/detach", planeMethodDetach,
		zip.WithOperationID(plane.BillingMethodDetach),
		zip.WithSummary("Remove one saved payment method"))
	zip.Post[plane.PlansIn, plane.Rendered](cloud.Plane(), "/billing/plans", planePlans,
		zip.WithOperationID(plane.BillingPlans),
		zip.WithSummary("The public plan catalog"))
}

// Answers the PUBLIC half of this org's processor configuration — the ids a
// browser needs to tokenize a card, and the environment it must tokenize
// against.
//
// It carries no secret: an application id is published to every checkout page by
// design. What matters is that it resolves sandbox-versus-production through the
// SAME authority the charge path uses, so the account a browser tokenizes
// against is always the account the charge will be made on. A mismatch there is
// a card that vaults and then cannot be charged.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSettings(ctx context.Context, _ *struct{}) (*plane.PaymentConfig, error) {
	org, err := payingOrg(ctx, "settings")
	if err != nil {
		return nil, err
	}
	cfg := commercebilling.ReadPaymentConfig(ctx, org)
	return &plane.PaymentConfig{
		Provider:      cfg.Provider,
		ApplicationID: cfg.ApplicationId,
		LocationID:    cfg.LocationId,
		Environment:   cfg.Environment,
		Live:          cfg.Live,
	}, nil
}

// Moves this org between sandbox money and real money.
//
// It flips whether a charge hits a real card, so it is the one posture change
// that is not self-service — the door holds it at the platform bar. Live and
// testMode come back as one fact stated twice, in the two vocabularies its
// readers use, from the single authority that decided it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMode(ctx context.Context, in *plane.ModeIn) (*plane.Mode, error) {
	org, err := payingOrg(ctx, "mode")
	if err != nil {
		return nil, err
	}
	m, merr := commercebilling.SetTestMode(ctx, org, in.TestMode)
	if merr != nil {
		return nil, zip.Errorf(500, "failed to set test mode")
	}
	return &plane.Mode{OrgID: m.OrgId, OrgName: m.OrgName, Live: m.Live, TestMode: m.TestMode}, nil
}

// Lists the cards and accounts one subject has on file, optionally of one kind.
//
// A store that cannot be read answers an EMPTY LIST rather than a failure, which
// is what this address has always done: the saved-cards panel renders empty
// instead of breaking the page around it. The subject is the door's, so a query
// cannot widen the list to another customer of the same org.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMethods(ctx context.Context, in *plane.MethodsIn) (*plane.Rendered, error) {
	org, err := payingOrg(ctx, "methods")
	if err != nil {
		return nil, err
	}
	rows, lerr := commercebilling.ListMethods(ctx, org, in.Subject, in.Kind, kmsFrom(ctx))
	if lerr != nil {
		// The honest empty list this address has always answered with, kept here
		// rather than at the door so both halves cannot disagree about it.
		rows = nil
	}
	if rows == nil {
		rows = []commercebilling.Method{}
	}
	return rendered(rows)
}

// Saves a payment method for a subject: vaults the instrument at the processor
// and persists the row.
//
// Saving a card that is ALREADY on file answers with the row that already holds
// it rather than stacking a duplicate, and the door needs to tell the two apart
// to answer 201 or 200 — so whether the row is new is part of the answer, not
// something a reader infers.
//
// The email names the processor's customer profile and is the CALLER'S own,
// resolved at the door from its credential: the store must never read an
// identity it was not handed.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMethodSave(ctx context.Context, in *plane.MethodSaveIn) (*plane.Rendered, error) {
	org, err := payingOrg(ctx, "save method")
	if err != nil {
		return nil, err
	}
	var body commercebilling.CreateMethodIn
	if uerr := json.Unmarshal(in.Body, &body); uerr != nil {
		return nil, zip.Errorf(400, "invalid request body")
	}
	// The subject the door resolved wins over anything the body carried: a
	// customer id a caller could choose is a card saved onto somebody else.
	body.CustomerId = in.Subject
	m, created, merr := commercebilling.CreateMethod(ctx, org, in.Email, kmsFrom(ctx), body)
	if merr != nil {
		switch {
		case commercebilling.IsMethodRefused(merr):
			return nil, zip.Errorf(400, "%v", merr)
		case commercebilling.IsCardDeclined(merr):
			return nil, zip.Errorf(402, "%v", merr)
		default:
			return nil, zip.Errorf(500, "failed to create payment method")
		}
	}
	out, rerr := rendered(m)
	if rerr != nil {
		return nil, rerr
	}
	out.Created = created
	return out, nil
}

// Removes one saved payment method.
//
// A method this subject does not own is NOT FOUND rather than refused — the same
// answer whether the id names nothing or names somebody else's card — so an id
// cannot be probed for existence. Privileged is the DOOR'S determination that
// this caller may act on any subject inside the org, and travels as one, because
// authority decided twice is authority that eventually disagrees with itself.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMethodDetach(ctx context.Context, in *plane.MethodRef) (*plane.Detachment, error) {
	org, err := payingOrg(ctx, "detach method")
	if err != nil {
		return nil, err
	}
	d, derr := commercebilling.DetachMethod(ctx, org, in.ID, in.Subject, in.Privileged, kmsFrom(ctx))
	if derr != nil {
		if commercebilling.IsMethodNotFound(derr) {
			return nil, zip.Errorf(404, "payment method not found")
		}
		return nil, zip.Errorf(500, "failed to detach payment method")
	}
	return &plane.Detachment{Deleted: d.Deleted, ID: d.Id}, nil
}

// Answers the public plan catalog, optionally narrowed to one category.
//
// It takes no org and needs none: the catalog is what anyone may buy, so there
// is nothing to scope and giving it a tenant would invent one.
//
// The active offer is resolved HERE, in the process that holds the platform row
// it lives in, and applied to the prices before they leave — so what the catalog
// quotes is what the checkout will charge. A door that priced this itself would
// need its own copy of the window rule, and a catalog priced without the offer
// is a different catalog.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planePlans(ctx context.Context, in *plane.PlansIn) (*plane.Rendered, error) {
	rows, err := commercebilling.ReadPlans(ctx, in.Category, promoNow(ctx))
	if err != nil {
		return nil, zip.Errorf(500, "failed to list plans")
	}
	if rows == nil {
		rows = []commercebilling.PlanView{}
	}
	return rendered(rows)
}

// promoNow is the offer in force, read in the process that holds the platform
// row it lives in. It is one function so the catalog and a sale cannot price the
// same plan two ways.
func promoNow(ctx context.Context) *promo.Promo { return promo.Current(ctx) }

// rendered marshals a store view into the bytes the door forwards.
//
// It is the ONE place this file turns a value into a document, so the two
// rendered families cannot come to encode differently, and a marshal that fails
// is a 500 rather than an empty body — an empty document is a lie a client will
// happily parse.
func rendered(v any) (*plane.Rendered, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, zip.Errorf(500, "failed to render answer")
	}
	return &plane.Rendered{Body: b}, nil
}
