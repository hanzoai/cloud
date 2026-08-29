// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// alerts_rpc.go — the customer's own spend caps, and the verdict the metering
// edge reads against them, over the internal plane.
//
// FIVE OPS, ONE ROW SET, and the fifth is why the other four matter. A cap is a
// spend-alert row in commerce's store; the verdict is that row set evaluated for
// one proposed act. Both go through the module's own cores, so the budget a
// customer edits and the ceiling a request is measured against are the same rows
// read by the same query. Two derivations of a cap is how a spend cap and a rate
// limit come to disagree about which requests they bind.
//
// THE VERDICT IS ON THE HOT PATH. The metering edge asks it before every priced
// call and reads ANY non-2xx as fail-open, so a refusal here does not merely
// fail — it silently lifts the ceiling. That is the reason it is a plane op
// rather than an HTTP hop: a call by name cannot be misconfigured into the
// public edge, which is what once turned this read into a self-dispatch loop
// that 502'd until a depth guard refused.

import (
	"context"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeAlerts publishes the cap CRUD and the verdict. Mount calls it.
func exposeAlerts() {
	zip.Post[plane.SubjectIn, plane.Alerts](cloud.Plane(), "/billing/alerts", planeAlerts,
		zip.WithOperationID(plane.BillingAlerts),
		zip.WithSummary("This org's spend caps"))
	zip.Post[plane.AlertSpec, plane.Alert](cloud.Plane(), "/billing/alert/raise", planeAlertRaise,
		zip.WithOperationID(plane.BillingAlertRaise),
		zip.WithSummary("Open a spend cap"))
	zip.Post[plane.AlertPatch, plane.Alert](cloud.Plane(), "/billing/alert/amend", planeAlertAmend,
		zip.WithOperationID(plane.BillingAlertAmend),
		zip.WithSummary("Change one spend cap"))
	zip.Post[plane.AlertRef, plane.Dropped](cloud.Plane(), "/billing/alert/drop", planeAlertDrop,
		zip.WithOperationID(plane.BillingAlertDrop),
		zip.WithSummary("Remove one spend cap"))
	zip.Post[plane.CapIn, plane.CapVerdict](cloud.Plane(), "/billing/cap/authorize", planeCapAuthorize,
		zip.WithOperationID(plane.BillingCapAuthorize),
		zip.WithSummary("Whether one proposed spend fits inside this org's caps"))
}

// Lists this org's spend caps, each with the period spend derived for its scope.
//
// The derived figures are POINTERS on the wire and absent rather than zero when
// the aggregation could not be read, because "nothing spent" and "spend unknown"
// are different answers and a zero cannot tell them apart. The policy row is
// still reported either way — a cap whose spend cannot be read is still a cap.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAlerts(ctx context.Context, in *plane.SubjectIn) (*plane.Alerts, error) {
	org, err := orgOf(ctx, "alerts")
	if err != nil {
		return nil, err
	}
	rows, aerr := commercebilling.ListAlerts(ctx, org, in.Subject)
	if aerr != nil {
		return nil, zip.Errorf(502, "alerts: %v", aerr)
	}
	out := make([]plane.Alert, 0, len(rows))
	for _, a := range rows {
		out = append(out, alertRow(a))
	}
	return &plane.Alerts{Rows: out}, nil
}

// Opens a spend cap on the caller's own org.
//
// The row is keyed on the SUBJECT the endpoint resolved, never on a body value,
// and that is what makes enforcement bind: the gate looks the cap up under the
// same key, so a cap stored under anything else is a cap nothing reads.
//
// A refusal of the caller's own values — a threshold that bounds nothing, a soft
// percentage outside its range, one row too many — is a 400 and says which. Any
// other failure is the store's, and is a 502.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAlertRaise(ctx context.Context, in *plane.AlertSpec) (*plane.Alert, error) {
	org, err := orgOf(ctx, "raise cap")
	if err != nil {
		return nil, err
	}
	a, aerr := commercebilling.CreateAlert(ctx, org, in.Subject, commercebilling.AlertSpec{
		Title: in.Title, Threshold: in.Threshold, Currency: in.Currency,
		Project: in.Project, Service: in.Service, Enforce: in.Enforce,
		SoftPct: in.SoftPct, RateLimitRpm: in.RateLimitRpm,
	})
	if aerr != nil {
		return nil, capFault("raise cap", aerr)
	}
	row := alertRow(*a)
	return &row, nil
}

// Changes one spend cap. Only the fields the patch carries move; the rest are
// preserved, so flipping enforcement cannot silently wipe the threshold.
//
// A row belonging to anyone but the caller answers as a MISS rather than a
// refusal, so a guessed id never becomes an oracle for what the org holds.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAlertAmend(ctx context.Context, in *plane.AlertPatch) (*plane.Alert, error) {
	org, err := orgOf(ctx, "amend cap")
	if err != nil {
		return nil, err
	}
	a, aerr := commercebilling.UpdateAlert(ctx, org, in.Subject, in.ID, commercebilling.AlertPatch{
		Title: in.Title, Threshold: in.Threshold, Project: in.Project,
		Service: in.Service, Enforce: in.Enforce, SoftPct: in.SoftPct,
		RateLimitRpm: in.RateLimitRpm,
	})
	if aerr != nil {
		return nil, capFault("amend cap", aerr)
	}
	row := alertRow(*a)
	return &row, nil
}

// Removes one spend cap, lifting that ceiling entirely.
//
// A row belonging to anyone but the caller answers as a miss, for the same
// reason the amend does: deleting by guessed id must tell a caller nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAlertDrop(ctx context.Context, in *plane.AlertRef) (*plane.Dropped, error) {
	org, err := orgOf(ctx, "drop cap")
	if err != nil {
		return nil, err
	}
	if aerr := commercebilling.DeleteAlert(ctx, org, in.Subject, in.ID); aerr != nil {
		return nil, capFault("drop cap", aerr)
	}
	return &plane.Dropped{OK: true}, nil
}

// Answers whether one proposed spend fits inside this org's caps.
//
// It evaluates EVERY covering row, most-restrictive-wins, and denies when a
// hard-enforceable row is exceeded, reporting the tightest one. Soft rows — and
// a project-scoped enforce row whose project axis the caller could not establish
// — never block; they only raise the reported utilization.
//
// ProjectValidated travels because only the endpoint knows it. A project a caller
// merely claimed is not a project the cap may bind on, and a callee that assumed
// validation would turn an unproven claim into a refusal.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCapAuthorize(ctx context.Context, in *plane.CapIn) (*plane.CapVerdict, error) {
	org, err := orgOf(ctx, "cap authorize")
	if err != nil {
		return nil, err
	}
	v, verr := commercebilling.AuthorizeCap(ctx, org, in.Project, in.Service, in.Amount, in.ProjectValidated)
	if verr != nil {
		return nil, zip.Errorf(502, "cap authorize: %v", verr)
	}
	return &plane.CapVerdict{
		Allow: v.Allow, Reason: v.Reason, CapCents: v.CapCents,
		SpentCents: v.SpentCents, WarnPct: v.WarnPct,
	}, nil
}

// capFault maps a core refusal to the status the endpoint has always
// answered with.
//
// The three cases are the module's own and stay its own: a miss is 404 (and a
// row the caller does not own IS a miss, deliberately), a refusal of the
// caller's values is 400 with the reason, and anything else is the store
// failing. Reading them here rather than at the endpoint is what makes one mapping
// serve every projection of these ops.
func capFault(what string, err error) error {
	switch {
	case commercebilling.IsAlertNotFound(err):
		return zip.Errorf(404, "spend alert not found")
	case commercebilling.IsAlertRefusal(err):
		return zip.Errorf(400, "%v", err)
	default:
		return zip.Errorf(502, "%s: %v", what, err)
	}
}

// alertRow moves one cap onto the wire. The three derived figures stay pointers,
// so an unreadable aggregation is reported as absent rather than as zero.
func alertRow(a commercebilling.Alert) plane.Alert {
	return plane.Alert{
		ID: a.Id, UserID: a.UserId, Title: a.Title,
		Threshold: a.Threshold, Currency: a.Currency,
		Project: a.Project, Service: a.Service, Enforce: a.Enforce,
		SoftPct: a.SoftPct, RateLimitRpm: a.RateLimitRpm,
		TriggeredAt: a.TriggeredAt, Period: a.Period, ResetsAt: a.ResetsAt,
		CreatedAt: stamp(a.CreatedAt), UpdatedAt: stamp(a.UpdatedAt),
		PeriodSpentCents: a.PeriodSpentCents, Over: a.Over, Warn: a.Warn,
	}
}
