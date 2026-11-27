// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// routing_rpc.go — WHICH PROCESSOR TAKES A NEW CHECKOUT, what it costs in the buyer's
// currency, and the payment element's intent.
//
// Commerce owns all of it: the policy, the placement, the quote and the credit. These
// ops are the plane's address for commerce's cores and hold no rule of their own. A
// buyer's ops answer for the subject the billing endpoint resolved from the caller's
// credential; the policy ops answer a SuperAdmin and the platform only.
//
// The shapes commerce answers are vendor-neutral already (a processor is a VALUE, never
// a field name), so each op hands the plane's typed answer the same JSON commerce
// produced, and a test holds the two sets of field names equal so neither can drift.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercetypes "github.com/hanzoai/commerce/types"
	"github.com/hanzoai/commerce/payment/routing"
	"github.com/hanzoai/commerce/types"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/commerce/models/organization"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// exposeRouting publishes the routing, quote and local-intent ops. Mount calls it.
func exposeRouting() {
	cloud.Plane().Post("/billing/route", planeRoute,
		zip.WithOperationID(client.BillingRoute),
		zip.WithSummary("Which processor takes this buyer's new checkout"))
	cloud.Plane().Post("/billing/topup/intent", planeTopupIntent,
		zip.WithOperationID(client.BillingTopupIntent),
		zip.WithSummary("Open a payment-element checkout for a top-up"))
	cloud.Plane().Post("/billing/topup/intent/state", planeTopupIntentState,
		zip.WithOperationID(client.BillingTopupIntentState),
		zip.WithSummary("Where one of the buyer's own payments stands"))
	cloud.Plane().Post("/billing/topup/intent/cancel", planeTopupIntentCancel,
		zip.WithOperationID(client.BillingTopupIntentCancel),
		zip.WithSummary("Cancel one of the buyer's own payments that is not yet confirmed"))
	cloud.Plane().Post("/billing/quote", planeQuote,
		zip.WithOperationID(client.BillingQuote),
		zip.WithSummary("The price of what a buyer is about to buy, in their currency"))
	cloud.Plane().Post("/billing/routing", planeRouting,
		zip.WithOperationID(client.BillingRouting),
		zip.WithSummary("The routing policy and where recent checkouts went"))
	cloud.Plane().Post("/billing/routing/set", planeRoutingSet,
		zip.WithOperationID(client.BillingRoutingSet),
		zip.WithSummary("Set the routing policy"))
	cloud.Plane().Post("/billing/routing/preview", planeRoutingPreview,
		zip.WithOperationID(client.BillingRoutingPreview),
		zip.WithSummary("The route a sample checkout would take"))
	cloud.Plane().Post("/billing/routing/history", planeRoutingHistory,
		zip.WithOperationID(client.BillingRoutingHistory),
		zip.WithSummary("The routing policy's saved versions"))
	cloud.Plane().Post("/billing/routing/restore", planeRoutingRestore,
		zip.WithOperationID(client.BillingRoutingRestore),
		zip.WithSummary("Make an earlier routing policy version current"))
	cloud.Plane().Post("/billing/routing/reset", planeRoutingReset,
		zip.WithOperationID(client.BillingRoutingReset),
		zip.WithSummary("Drop payers' placements so their next checkout is placed afresh"))
}

// into is v in the plane's typed shape: the same JSON, decoded into T, with every list
// a list and never null. A plane answer that left a list null would cross as null.
func into[T any](v any) (*T, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, zip.Errorf(500, "routing: %v", err)
	}
	var t T
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, zip.Errorf(500, "routing: %v", err)
	}
	lists(reflect.ValueOf(&t).Elem())
	return &t, nil
}

// lists makes every nil slice under v an empty one.
func lists(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			lists(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				lists(v.Field(i))
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return
		}
		for i := 0; i < v.Len(); i++ {
			lists(v.Index(i))
		}
	}
}

// fail is a commerce refusal with its own status.
func fail(f *commercebilling.PaymentFault) error { return zip.Errorf(f.Status, "%s", f.Message) }

// Answers which processor takes this buyer's NEW card checkout, and the methods its
// card slot mounts, decided in the order: a saved card stays on its processor, the
// merchant accounts that can take the charge, the method chosen, the admin's rules,
// the buyer's placement, the weights, and a safe fallback before any payment starts.
// The answer carries the reason chain, the policy version, and, when an amount is
// asked, the quote the buyer authorizes.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoute(ctx context.Context, in *client.RouteIn) (*client.Route, error) {
	org, err := orgOf(ctx, "route")
	if err != nil {
		return nil, err
	}
	d, f := commercebilling.Place(ctx, org, commercebilling.RouteIn{
		Subject: in.Subject, Country: in.Country, Currency: in.Currency, QuoteCurrency: in.Currency,
		AmountCents: in.AmountCents, Method: in.Method,
	})
	if f != nil {
		return nil, fail(f)
	}
	return into[client.Route](d)
}

// Opens a payment-element intent for a top-up in the currency the buyer chose, else
// their country's default, priced by the policy's FX source with the quote disclosed
// in the answer. Nothing is credited here: the wallet gets the quoted dollars only when
// the processor reports the payment succeeded, once. A currency the merchant or the
// chosen method cannot take is refused (400).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopupIntent(ctx context.Context, in *client.TopupIntentIn) (*client.TopupIntentOut, error) {
	org, err := orgOf(ctx, "topup intent")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.OpenTopupIntent(ctx, org, commercebilling.TopupIntentIn{
		Subject: in.Subject, Country: in.Country, AmountCents: in.AmountCents, IdempotencyKey: in.IdempotencyKey,
		Currency: in.Currency, Method: in.Method,
		Payer: payerIn(in.Name, in.Email, in.Phone, in.Billing),
		PlanID: in.PlanID, Quantity: in.Quantity, Level: in.Level,
		Interval: commercetypes.Interval(in.Interval), Coupon: in.Coupon, Promo: promoNow(ctx),
	})
	if f != nil {
		cls := "processor_error"
		msg := strings.ToLower(f.Message)
		if strings.Contains(msg, "currency") || strings.Contains(msg, "method") {
			cls = "currency_method_unsupported"
		} else if strings.Contains(msg, "waiting on your bank") {
			cls = "open_intent_cap_exceeded"
		}
		RecordProcessorOutcome(in.Currency, in.Method, cls)
		return nil, fail(f)
	}
	res, err := into[client.TopupIntentOut](out)
	if err == nil && res != nil {
		RecordProcessorOutcome(res.Currency, in.Method, ClassifyProcessorOutcome("created", "", "", res.Currency, in.Method))
	}
	return res, err
}

// Answers where one of the subject's own payments stands: created, pending,
// succeeded or canceled, and whether the wallet holds the money. A payment that is
// not the subject's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopupIntentState(ctx context.Context, in *client.TopupIntentRef) (*client.TopupIntentState, error) {
	org, err := orgOf(ctx, "topup intent state")
	if err != nil {
		return nil, err
	}
	st, f := commercebilling.ReadTopupIntent(ctx, org, in.Subject, in.ID)
	if f != nil {
		return nil, fail(f)
	}
	res, err := into[client.TopupIntentState](st)
	if err == nil && res != nil {
		RecordProcessorOutcome("usd", "auto", ClassifyProcessorOutcome(res.State, "", "", "usd", "auto"))
	}
	return res, err
}

// Cancels one of the subject's own top-up payments that has not been confirmed, at the
// processor, and frees its place under the cap of open payments. A payment that has
// succeeded, is processing or is credited is 409; one that is not the subject's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopupIntentCancel(ctx context.Context, in *client.TopupIntentRef) (*client.TopupIntentCancelled, error) {
	org, err := orgOf(ctx, "topup intent cancel")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.CancelTopupIntent(ctx, org, in.Subject, in.ID)
	if f != nil {
		return nil, fail(f)
	}
	res, err := into[client.TopupIntentCancelled](out)
	if err == nil && res != nil {
		RecordProcessorOutcome("usd", "auto", ClassifyProcessorOutcome(res.State, "abandoned", "", "usd", "auto"))
	}
	return res, err
}

// Answers the price of what a buyer is about to buy, before any sale: a plan's first
// charge priced as the sale prices it, or an amount of credit, in the currency the
// buyer chose or their country's default. It is the displayed (cached) rate; the sale
// locks its own quote and refuses with the new price if it differs from what was shown.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeQuote(ctx context.Context, in *client.QuoteIn) (*client.PriceQuote, error) {
	org, err := orgOf(ctx, "quote")
	if err != nil {
		return nil, err
	}
	q, f := commercebilling.QuoteBeforeSale(ctx, org, commercebilling.QuoteIn{
		Subject: in.Subject, Plan: in.Plan, Level: in.Level, Interval: types.Interval(in.Interval),
		UsdCents: in.UsdCents, Country: in.Country, Currency: in.Currency, Method: in.Method,
	})
	if f != nil {
		return nil, fail(f)
	}
	return into[client.PriceQuote](q)
}

// platformOnly reports whether the caller is the platform's SuperAdmin.
func platformOnly(ctx context.Context, act string) error {
	if !cloud.Who(ctx).Admin {
		return zip.ErrForbidden(act + ": SuperAdmin required")
	}
	return nil
}

// Reads the routing policy for an org (the platform's when none is named), the
// platform default, how many of the latest checkouts each listed processor took, where
// the newest went, whether each can take a payment now, the merchant accounts the
// processors report, which processors can take which currency, and the names a rule may
// use.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRouting(ctx context.Context, in *client.RoutingScope) (*client.RoutingView, error) {
	if err := platformOnly(ctx, "routing"); err != nil {
		return nil, err
	}
	org, err := orgOf(ctx, "routing")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.ReadRoutingView(ctx, org, strings.TrimSpace(in.Org))
	if f != nil {
		return nil, fail(f)
	}
	return into[client.RoutingView](v)
}

// Sets the routing policy for an org, or for the platform when none is named. The
// statement replaces the stored policy whole and becomes a new version, with who made
// it and why. A rule that routes to a processor that cannot take the charge is refused
// (400) and changes nothing.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoutingSet(ctx context.Context, in *client.RoutingSetIn) (*client.RoutingPolicy, error) {
	if err := platformOnly(ctx, "routing set"); err != nil {
		return nil, err
	}
	pol, err := policyIn(in.Policy)
	if err != nil {
		return nil, err
	}
	p, f := commercebilling.WriteRouting(ctx, strings.TrimSpace(in.Org), *pol, in.By, in.Note)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.RoutingPolicy](p)
}

// Answers the route a sample checkout would take under the policy now, with the
// reason chain, changing nothing. A brand or issuing country decides only where it is
// already known, such as a saved card's.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoutingPreview(ctx context.Context, in *client.RoutingPreviewIn) (*client.Route, error) {
	if err := platformOnly(ctx, "routing preview"); err != nil {
		return nil, err
	}
	org, err := orgNamed(ctx, "routing preview", in.Org)
	if err != nil {
		return nil, err
	}
	d, f := commercebilling.PreviewRouting(ctx, org, commercebilling.PreviewIn{
		Subject: in.Subject, Country: in.Country, Currency: in.Currency, Method: in.Method,
		Brand: in.Brand, IssuerCountry: in.IssuerCountry, AmountCents: in.AmountCents,
	})
	if f != nil {
		return nil, fail(f)
	}
	return into[client.Route](d)
}

// Lists the saved versions of the routing policy, newest first, each with who saved
// it, when, why, and what changed.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoutingHistory(ctx context.Context, in *client.RoutingScope) (*client.RoutingVersions, error) {
	if err := platformOnly(ctx, "routing history"); err != nil {
		return nil, err
	}
	v, f := commercebilling.ListRoutingVersions(ctx, strings.TrimSpace(in.Org))
	if f != nil {
		return nil, fail(f)
	}
	return into[client.RoutingVersions](map[string]any{"versions": v})
}

// Makes an earlier version of the routing policy current as a NEW version, so the
// history is never rewritten. A version that no longer holds against today's merchant
// accounts is refused (400) with the reason.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoutingRestore(ctx context.Context, in *client.RoutingRestoreIn) (*client.RoutingPolicy, error) {
	if err := platformOnly(ctx, "routing restore"); err != nil {
		return nil, err
	}
	p, f := commercebilling.RestoreRouting(ctx, strings.TrimSpace(in.Org), in.Version, in.By, in.Note)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.RoutingPolicy](p)
}

// Drops payers' placements, an org's or one payer's, so the next checkout is placed
// afresh by the weights. Nothing in flight moves: a created payment keeps its processor.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoutingReset(ctx context.Context, in *client.RoutingResetIn) (*client.RoutingReset, error) {
	if err := platformOnly(ctx, "routing reset"); err != nil {
		return nil, err
	}
	n, f := commercebilling.ResetRouting(ctx, strings.TrimSpace(in.Org), strings.TrimSpace(in.Subject), in.By)
	if f != nil {
		return nil, fail(f)
	}
	return &client.RoutingReset{Placements: n}, nil
}

// policyIn is the plane's statement of a policy as commerce's, by the same JSON.
func policyIn(p client.RoutingPolicy) (*routing.Policy, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, zip.Errorf(400, "routing: %v", err)
	}
	var out routing.Policy
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, zip.Errorf(400, "routing: %v", err)
	}
	return &out, nil
}

// orgNamed is the org a SuperAdmin names, else the one they are acting in.
func orgNamed(ctx context.Context, op, name string) (*organization.Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return orgOf(ctx, op)
	}
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(503, "%s: commerce is not co-resident in this process", op)
	}
	org, err := commerceorg.Resolve(ctx, name)
	if err != nil {
		return nil, zip.Errorf(404, "%s: no org %q", op, name)
	}
	return org, nil
}
