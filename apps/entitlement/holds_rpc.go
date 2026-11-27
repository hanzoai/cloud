// Copyright © 2026 Hanzo AI. MIT License.

package entitlement

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// Whether an org has turned on one product, published on the internal plane.
//
// The caller is apps/framework's refusal, which runs in the binary serving a
// module while the enablements live in this app's store.

// exposeHolds publishes the enablement read and the beta grant. Mount calls it.
func exposeHolds() {
	cloud.Plane().Post("/entitlement/holds",
		holds,
		zip.WithOperationID(client.EntitlementHolds),
		zip.WithSummary("Whether the caller's org has one product turned on"))
	cloud.Plane().Post("/entitlement/grant",
		grant,
		zip.WithOperationID(client.EntitlementGrant),
		zip.WithSummary("Turns one product in beta on for the caller's org"))
}

// Beta is the products in beta. An org has one only by a grant: a person of the
// org applying to the beta (EntitlementGrant, from the public intake), a
// SuperAdmin turning it on for the org, or the org's live plan carrying early
// access (held). No org admin enables one for itself, whatever its plan says
// (post), so the enablement is the beta and the one predicate every op of the
// product asks (apps/framework elective.go).
var Beta = []string{"crm"}

// modules is the Base module each product in beta keeps its records in. An org that
// is given the product is given the module first (install): the CRM's people,
// candidates and activities are collections of the org's own Base (hanzoai/base
// module crm), so an org that holds the CRM has them before its first look.
var modules = map[string]string{"crm": "crm"}

// installBudget bounds one install, waking a cold base child included.
const installBudget = 20 * time.Second

// importFn is the one Base call, a variable so a test can see what leaves this
// process. Only a test reassigns it.
var importFn = basepeer.BaseImport

// install grows org's Base by the module product keeps its records in, before the
// product is turned on: a grant that cannot install is refused, so no org holds the
// CRM without its collections. Growing never drops or changes what is there, so a
// second install is a no-op and a field the org added survives it. A product with
// no module installs nothing.
//
// The call acts for org on a context of its own, bounded by ctx: a SuperAdmin's
// request outranks any org stated over it (zip.CallerOf), so on the request's own
// context the module would be grown in the SuperAdmin's Base.
func install(ctx context.Context, org, product string) error {
	module, ok := modules[product]
	if !ok {
		return nil
	}
	ictx, cancel := context.WithTimeout(plane.For(zip.WithCaller(context.Background(), zip.Caller{Org: org}), org), installBudget)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	if _, err := importFn(ictx, &client.BaseImportIn{Module: module}); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "install %q in %s's Base: %v", module, org, err)
	}
	return nil
}

// grant turns a product in beta on for the caller's own org, after recording it
// on the audit trail: no trail to take it, no grant. The caller names only the
// product, so "grant another org" is unrepresentable here as it is in holds; the
// org rides the call, stated by the first-party sibling that asks.
func grant(ctx context.Context, in *client.ProductIn) (*client.Held, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrUnauthorized("grant: no org on the call")
	}
	product := strings.TrimSpace(in.Product)
	if !slices.Contains(Beta, product) {
		return nil, zip.ErrForbidden("grant: only a product in beta is granted this way")
	}
	if mounted == nil || mounted.store == nil {
		return nil, zip.Errorf(503, "grant: the entitlement store is not mounted")
	}
	// The module first, whether or not the org holds the product already: an org
	// whose earlier install was lost gets it on its next application.
	if err := install(ctx, who.Org, product); err != nil {
		return nil, err
	}
	if on, err := mounted.store.Holds(ctx, who.Org, product); err != nil {
		return nil, err
	} else if on {
		return &client.Held{On: true}, nil
	}
	if err := mounted.give(ctx, who.Org, product, "a person of the org applied to the beta"); err != nil {
		return nil, err
	}
	return &client.Held{On: true}, nil
}

// give turns a product in beta on for org once its module is installed, after
// recording the grant and why on the audit trail: no trail to take it, no grant.
// The actor is whoever's call it was, the org the grant's own.
func (s *service) give(ctx context.Context, org, product, reason string) error {
	who := cloud.Who(ctx)
	if s.trail != nil {
		if _, err := s.trail.Append(ctx, audit.Record{
			Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
			Action:    "entitlement.grant",
			Resource:  audit.Resource{Type: "entitlement", ID: org + "/" + product},
			Auth:      audit.AuthContext{Method: "attested"},
			Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: reason},
			SourceIP:  who.IP,
			RequestID: who.RequestID,
		}); err != nil {
			return fmt.Errorf("grant: audit: %w", err)
		}
	}
	if err := s.store.Enable(ctx, org, product, who.User, time.Now().Unix()); err != nil {
		return err
	}
	s.log.Info("beta granted", "org", org, "product", product, "by", who.User, "reason", reason)
	return nil
}

// held is whether org has product: its enablement, the one predicate. A product in
// beta is given to an org without one here, on its first look, when a live plan of
// the org carries early access (@hanzo/plans licensing.early_access, every Max rung,
// read off commerce's plan row): its module installed and the grant on the audit
// trail, as an applicant's is, so a Max org opens the beta without applying and keeps
// it, as an applicant does, if it later leaves the plan.
//
// A shared or brand org (authz.IsHeldOrg) is never given a product so: a plan bought
// by one of its people is not the org's. A deployment with no commerce sells no plan,
// so nothing in it carries early access. A plan that cannot be read is an error, which
// every reader refuses on.
func (s *service) held(ctx context.Context, org, product string) (bool, error) {
	on, err := s.store.Holds(ctx, org, product)
	if err != nil || on || !slices.Contains(Beta, product) || authz.IsHeldOrg(org) || s.commerce == nil {
		return on, err
	}
	ent, err := s.commerce.CheckEntitlement(ctx, org, product)
	if err != nil {
		return false, zip.Errorf(http.StatusServiceUnavailable, "early access for %s: %v", org, err)
	}
	if ent == nil || !ent.EarlyAccess {
		return false, nil
	}
	if err := install(ctx, org, product); err != nil {
		return false, err
	}
	if err := s.give(ctx, org, product, "the org's plan carries early access"); err != nil {
		return false, err
	}
	return true, nil
}

// holds reads one product for the caller's own org. The caller names only the
// product, so "check another org's entitlement" is unrepresentable.
//
// An unmounted store is an error, never a false: a subsystem that failed to start
// must not read as a customer who has not subscribed.
func holds(ctx context.Context, in *client.ProductIn) (*client.Held, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("holds: no org on the call")
	}
	product := strings.TrimSpace(in.Product)
	if product == "" {
		return nil, zip.ErrBadRequest("holds: a product is required")
	}
	if mounted == nil || mounted.store == nil {
		return nil, zip.Errorf(503, "holds: the entitlement store is not mounted")
	}
	on, err := mounted.held(ctx, org, product)
	if err != nil {
		return nil, err
	}
	return &client.Held{On: on}, nil
}
