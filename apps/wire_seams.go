package apps

import (
	"context"

	"github.com/hanzoai/cloud/clients/ads"
	"github.com/hanzoai/cloud/clients/automations"
	"github.com/hanzoai/cloud/clients/campaign"
	"github.com/hanzoai/cloud/clients/coding"
	"github.com/hanzoai/cloud/clients/git"
	"github.com/hanzoai/cloud/clients/integrations"
)

// wire_seams.go wires cross-subsystem in-process seams that cannot be a MountSpec
// because they compose functions ACROSS packages that must not import each other.
//
// The coding orchestrator (clients/coding) needs git's CloneURL + VerifyRef, but
// clients/git imports clients/integrations (Slack-notify), and integrations calls
// coding — so coding -> git would cycle (integrations -> coding -> git ->
// integrations). The composition root is the ONE place that imports all three, so
// it assembles the coding Dispatcher here (git seams + agents/tracker/bot adapters)
// and injects it into the Slack trigger surface. init() runs once at load, before
// cloud.Serve; the git functions are plain reads that resolve their state at call
// time, so no mount ordering is required. The mirror-failure logger is nil (those
// failures are non-fatal and dropped).
func init() {
	integrations.SetCodingDispatcher(coding.NewDispatcher(git.CloneURL, git.VerifyRef, nil))

	// Inbound-event seam: a verified provider webhook (or chat channel) in
	// clients/integrations fires the automations engine's Deliver here — the ONE place
	// that imports both, so integrations never has to import automations (which imports
	// it, for credential custody). A primitive-typed adapter keeps the seam free of the
	// automations types.
	integrations.SetAutomationTrigger(func(ctx context.Context, org, source, name, dedupeKey string, depth int, payload map[string]any) (int, error) {
		return automations.Deliver(ctx, org, automations.TriggerEvent{
			Source: source, Name: name, DedupeKey: dedupeKey, Depth: depth, Payload: payload,
		})
	})

	// GTM PAID channel: the /v1/campaign orchestrator fans out to executors that
	// satisfy campaign.Channel; the composition root is the ONE place that imports
	// both campaign and the ad plane, so it adapts ads' connector-consuming
	// execution funcs (provider.go — each resolves the org's ad token through
	// integrations.TokenFor and fails closed) onto the primitive-typed channel
	// seam. campaign never imports ads and ads never imports campaign — the same
	// injected-function decoupling the coding dispatcher above uses.
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

	// GTM ORGANIC + EMAIL channels and the creative-A/B experiment seam wire HERE
	// the same way once their executors land (designed follow-ons):
	//
	//   campaign.RegisterChannel(campaign.NewChannel(campaign.KindOrganic,
	//       publish.Syndicate, publish.NoSpend, publish.Unpublish))     // social connectors
	//   campaign.RegisterChannel(campaign.NewChannel(campaign.KindEmail,
	//       marketing.Broadcast, marketing.NoSpend, marketing.Halt))    // email connectors
	//   campaign.SetExperiment(experiment.Assign, experiment.RecordEvidence) // flags-assignment + analytics evidence
	//
	// Until wired, those channels record "unavailable" on a fan-out (honest) and a
	// campaign runs a single creative — never a fabricated launch or variant.
}
