package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud/clients/ads"
	"github.com/hanzoai/cloud/clients/campaign"
	"github.com/hanzoai/cloud/clients/experiments"
	"github.com/hanzoai/cloud/clients/principal"
)

// The cross-subsystem seams campaign owns, wired in ITS OWN composition root.
//
// These used to live in package apps (wire_seams.go), which the whole fleet
// linked. campaign never imports ads and ads never imports campaign; this main
// is the ONE place that imports both, so it adapts ads' connector-consuming
// execution funcs onto campaign's primitive-typed channel seam. Same injected-
// function decoupling the coding dispatcher uses. init() runs once at load.
func init() {
	// GTM PAID channel: the /v1/campaign orchestrator fans out to executors that
	// satisfy campaign.Channel; this adapts ads' provider.go funcs (each resolves
	// the org's ad token through integrations.TokenFor and fails closed) onto the
	// primitive-typed channel seam.
	campaign.RegisterChannel(campaign.NewChannel(campaign.KindPaid,
		func(ctx context.Context, org string, p campaign.Plan) (campaign.Ref, error) {
			r, err := ads.LaunchPaid(ctx, org, ads.PaidPlan{
				Platform: p.Platform, Account: p.Account, Name: p.Name,
				Objective: p.Objective, BudgetCents: p.BudgetCents, ScheduleAt: p.ScheduleAt,
			})
			return campaign.Ref{Platform: r.Platform, Account: r.Account, ExternalID: r.ExternalID, Status: r.Status, Detail: r.Detail}, err
		},
		func(ctx context.Context, org string, ref campaign.Ref) (int64, error) {
			return ads.PaidSpend(ctx, org, ads.PaidRef{Platform: ref.Platform, Account: ref.Account, ExternalID: ref.ExternalID})
		},
		func(ctx context.Context, org string, ref campaign.Ref) error {
			return ads.PausePaid(ctx, org, ads.PaidRef{Platform: ref.Platform, Account: ref.Account, ExternalID: ref.ExternalID})
		},
	))

	// GTM creative A/B composes the merged EXPERIMENT primitive (clients/experiments)
	// — campaign never reinvents bucketing or measurement. Assign resolves the
	// subject's variant (creative) from the experiment's flag; Analyze is pull-model
	// (it reads the metric from analytics itself), returned as opaque JSON so
	// campaign stays decoupled from the analysis type. Campaign-linked experiments
	// live in the org's default project. Both are nil-safe upstream: an org that
	// never created a "campaign:<id>" experiment runs a single creative (Assign
	// errors → "" → Content[0]).
	campaign.SetExperiment(
		func(ctx context.Context, org, experimentID, subject string) (string, error) {
			a, err := experiments.Assign(ctx, org, principal.DefaultProject, experimentID, subject, nil)
			if err != nil {
				return "", err
			}
			return a.Variant, nil
		},
		func(ctx context.Context, org, experimentID string, start, end time.Time) (json.RawMessage, error) {
			an, err := experiments.Analyze(ctx, org, principal.DefaultProject, experimentID, start, end, 0.05)
			if err != nil {
				return nil, err
			}
			return json.Marshal(an)
		},
	)
}
