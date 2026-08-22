package billing

// alerts.go serves the spend-cap family of /v1/billing — the customer's own
// budgets, and the verdict the metering edge measures a request against.
//
// TWO AUDIENCES, ONE ROW SET, and that is why the family is not one chain. The
// CRUD is a customer editing their own budget and is gated to an org admin: a
// cap is a financial safety control, so a compromised member key must not be
// able to delete the org's ceiling (unbounded spend) or set a one-cent enforcing
// one (an org-wide 402). The verdict is a SERVICE reading it on every priced
// call, presenting a service token and no user, and it reads any non-2xx as
// fail-open — so a refusal there does not fail, it silently lifts the ceiling.
//
// Both reach the same rows through the same core in the process that owns them,
// which is what keeps a budget and the ceiling it imposes from being two
// derivations of one number.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountAlerts registers the cap family. Called from routes.
func mountAlerts(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/alerts", o.alerts)
	zip.Get(zapp, "/v1/billing/alerts/authorize", o.capAuthorize)
	zip.Post(zapp, "/v1/billing/alerts", o.raiseAlert,
		zip.WithStatus(http.StatusCreated))
	zip.Patch(zapp, "/v1/billing/alerts/:id", o.amendAlert)
	// The delete stays RAW, and the reason is its body. This address answers 204
	// carrying commerce's `null`, and a typed op writes either a value or an
	// empty 204 — neither of which is that. A relay that tidied it would be
	// changing a wire while claiming to preserve one.
	zip.Delete(cloud.ZipApp(app), "/v1/billing/alerts/:id", o.dropAlert)
}

// The delete is the one route in this family the wire keeps untyped, so its
// prose is declared beside the route rather than lifted off a typed handler.
// Declared through the same registry Register uses, so it renders only while the
// router actually serves the route.
func init() {
	openapi.Describe("/v1/billing/alerts/:id", http.MethodDelete,
		"Remove one spend cap",
		"Deletes a budget the caller's org owns and answers 204.\n\n"+
			"Removing a cap REMOVES A CEILING, so it takes the same bar as setting "+
			"one: a validated org admin, the platform SuperAdmin, or the trusted "+
			"in-process service token. A member who could delete the org's cap would "+
			"have unbounded spend.\n\n"+
			"A cap this org does not own is NOT FOUND rather than refused — the same "+
			"answer whether the id is unknown or belongs to another customer — so an "+
			"id cannot be probed for existence by trying to delete it.")
}

// capAdmin refuses a cap WRITE to anyone but a validated org admin, the platform
// SuperAdmin, or the trusted in-process service token.
//
// A spend cap is a FINANCIAL SAFETY control, and both directions of getting it
// wrong are expensive: a member who can delete the org's cap has unbounded
// spend, and a member who can set a one-cent enforcing cap has an org-wide 402.
// The store's own gate admits any authenticated member, so this is the
// difference, and it has to be here — the reads beside it stay member-open, and
// only the mutations require admin.
//
// It runs in the HANDLER rather than on the route, for the reason the risk
// screen does: a typed op is reached by four projections and only the handler is
// the point all four pass through. It moved here with the routes it guards; the
// bits it reads are the unforgeable ones the identity boundary mints, never a
// client header.
func capAdmin(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrForbidden("org admin required to change spend caps")
	}
	if principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c) || account.IsServiceToken(c) {
		return nil
	}
	return zip.ErrForbidden("org admin required to change spend caps")
}

// caps is the org's spend caps — a bare array, which is the wire this address
// has always had.
type caps []plane.Alert

// Lists this org's spend caps: the ceiling, its scope, whether it enforces, and
// how much of it has been spent this period.
//
// `periodSpentCents`, `over` and `warn` are ABSENT rather than zero when the
// spend could not be read, because "nothing spent" and "spend unknown" are
// different answers and a customer acting on the first when the second is true
// would be reading a ceiling that is not there. The policy row is reported
// either way.
//
// The period is the UTC calendar month and `resetsAt` is when the count starts
// again, so a surface can say "resets on" without a second call.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) alerts(ctx context.Context, _ *noInput) (*caps, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	out, err := ask(ctx, org, "alerts", func(ctx context.Context) (*plane.Alerts, error) {
		return commercepeer.BillingAlerts(ctx, &plane.SubjectIn{Subject: subject})
	})
	if err != nil {
		return nil, err
	}
	rows := caps(out.Rows)
	if rows == nil {
		rows = caps{}
	}
	return &rows, nil
}

// Opens a spend cap on the caller's own org.
//
// At least one limit must mean something: a threshold above zero (a spend cap)
// or a requests-per-minute above zero (a rate limit). A row that bounds neither
// is refused rather than stored, because a ceiling nothing measures against is a
// ceiling a customer believes in and does not have.
//
// The cap is keyed on the caller's own billing subject, resolved server-side —
// the SAME key the verdict looks it up under, which is what makes enforcement
// bind rather than merely record.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) raiseAlert(ctx context.Context, in *plane.AlertSpec) (*plane.Alert, error) {
	if err := capAdmin(ctx); err != nil {
		return nil, err
	}
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	spec := *in
	spec.Subject = subject
	return ask(ctx, org, "raise cap", func(ctx context.Context) (*plane.Alert, error) {
		return commercepeer.BillingAlertRaise(ctx, &spec)
	})
}

// Changes one spend cap: raise or lower the ceiling, flip enforcement, retune
// the rate limit.
//
// Only the fields the body carries move. Every mutable field is optional, and an
// absent one is PRESERVED rather than reset — so a change that flips enforcement
// cannot silently wipe the threshold it enforces.
//
// A cap belonging to another org is a 404, not a 403: a guessed id must not
// become an oracle for what anyone else holds.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) amendAlert(ctx context.Context, in *plane.AlertPatch) (*plane.Alert, error) {
	if err := capAdmin(ctx); err != nil {
		return nil, err
	}
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	patch := *in
	patch.Subject = subject
	return ask(ctx, org, "amend cap", func(ctx context.Context) (*plane.Alert, error) {
		return commercepeer.BillingAlertAmend(ctx, &patch)
	})
}

// dropAlert removes one spend cap, lifting that ceiling entirely.
//
// Raw rather than typed, for its body alone: the address answers 204 carrying
// `null`, which is neither a value nor an empty 204, so a typed op could not
// reproduce it. A cap belonging to another org is a 404, for the reason the
// amend is.

// Answers whether one proposed spend fits inside this org's caps.
//
// It is the per-request verdict the metering edge consumes before every priced
// call, and its caller is a SERVICE rather than a person: a service token plus
// the gateway-pinned org, with no user behind it. So this admits that principal
// where the CRUD beside it does not.
//
// Every covering row is evaluated, most-restrictive-wins, and the tightest one
// is what `capCents`, `spentCents` and `reason` describe. Soft rows never deny;
// nor does a project-scoped enforcing row whose project axis the caller could
// not establish — `pv=1` is how a caller states that it did, and an unproven
// claim must not be able to refuse traffic.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) capAuthorize(ctx context.Context, in *capQuery) (*plane.CapVerdict, error) {
	org, err := serviceOrg(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "cap authorize", func(ctx context.Context) (*plane.CapVerdict, error) {
		return commercepeer.BillingCapAuthorize(ctx, &plane.CapIn{
			Project:          in.Project,
			Service:          in.Service,
			Amount:           atoiOr64(in.Amount, 0),
			ProjectValidated: in.PV == "1",
		})
	})
}

// capQuery is what the metering edge asks about: which scope, how much, and
// whether it could prove the project.
type capQuery struct {
	// Project narrows the verdict to one project's caps. Empty is the org-wide row.
	Project string `json:"project,omitempty"`
	// Service narrows it to one service's caps. Empty is every service.
	Service string `json:"service,omitempty"`
	// Amount is the proposed spend in cents.
	Amount string `json:"amount,omitempty"`
	// PV is "1" when the caller ESTABLISHED the project rather than merely
	// carrying a claim of one. An unproven project may not deny traffic.
	PV string `json:"pv,omitempty"`
}
