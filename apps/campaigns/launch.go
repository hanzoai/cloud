package campaigns

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// launch.go is the fan-out: turning a campaign VALUE into running executions
// across its channels. It is best-effort PER CHANNEL — each channel records its
// own outcome (live / failed / unavailable) on the campaign, so a paid launch can
// succeed while an email launch fails, and the operator sees exactly which
// connector was missing. The org is passed verbatim to every executor, which
// resolves ITS OWN org's connector token (integrations.TokenFor) — the fan-out
// itself never sees a credential.

// The prose for the two operations here that cannot be typed ops. Every other route
// in campaign is typed and zipdoc lifts its doc comment into zipdoc_gen.go; the
// launch and the pause stay raw handlers (routes says why — neither has ever read a
// request body, and typing them would turn today's 200 into a 400 for a caller that
// posts junk to a route that ignores it), so there is no comment for anything to
// lift and the published document would carry an operationId and nothing else — an
// SDK method and a CLI command that cannot explain themselves. These two are the
// ones that MOVE MONEY on a provider, so a caller reading only the document has to
// be told what a partial outcome means. Declared through the same registry Register
// uses, so it renders only while the router actually serves the route.
func init() {
	openapi.Describe("/v1/campaigns/:id/launch", http.MethodPost,
		"Launch a campaign across every channel it declares",
		"Pushes the campaign live on each of its channels through that channel's executor and "+
			"answers the whole campaign with the per-channel outcome written back onto it.\n\n"+
			"The fan-out is BEST-EFFORT PER CHANNEL, and the honest reading of the result is the "+
			"rule most callers get wrong: one channel failing never aborts the others, so each "+
			"channel row carries its own `live`, `failed` or `unavailable` status and detail, and "+
			"a paid launch can be live while an email launch failed. The campaign itself is `live` "+
			"when AT LEAST ONE channel launched and `failed` only when none did — `live` is not a "+
			"claim that every channel launched. Repeating the call is safe: a channel already live "+
			"is skipped, never re-launched. A campaign carrying more than one creative has its "+
			"variant assigned here by the experiment seam and tagged as `utm_content`.\n\n"+
			"Org-scoped and fails closed: a valid bearer is required (403 without one), the "+
			"campaign is read under the caller's OWN org so another tenant's id is a 404, and a "+
			"campaign with no channels is a 400 — there is nothing to launch. Each executor "+
			"resolves its own org's connector token from the org passed to it, so a launch can "+
			"never spend through another tenant's connector.")
	openapi.Describe("/v1/campaigns/:id/pause", http.MethodPost,
		"Pause every live channel on a campaign at its provider",
		"Pauses each live channel on its provider and answers the whole campaign, moved to "+
			"`paused`, with the per-channel outcome written back onto it.\n\n"+
			"Only channels that are live and carry a provider reference are touched; a channel "+
			"whose executor is no longer wired is marked `unavailable` and one whose pause errored "+
			"is marked `failed`, with the reason on the row. The campaign still reports `paused` "+
			"in both cases, and that is deliberate rather than sloppy: no live channel remains "+
			"that this process will meter, and the rows say exactly which provider was not "+
			"reached so it can be settled by hand.\n\n"+
			"Org-scoped and fails closed: a valid bearer is required (403 without one) and the "+
			"campaign is read under the caller's OWN org, so another tenant's id is a 404.")
}

// launchCampaign fans a campaign out to its channels. A campaign with no channels
// is a 400 (nothing to launch). After the fan-out the campaign is live when at
// least one channel launched, else failed. The channel rows carry the honest
// per-channel status. Idempotency: a channel already live is not re-launched.
func launchCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	if len(camp.Channels) == 0 {
		return zip.ErrBadRequest("campaign has no channels to launch")
	}
	camp = fanOut(c.Context(), org, camp)
	saved, err := s.State.store.Save(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// pauseCampaign pauses every live channel on the provider and moves the campaign
// to paused. A channel whose executor is gone, or whose pause errors, is recorded
// honestly; the campaign still reports paused (no live channel remains that this
// process will meter).
func pauseCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	camp = pauseAll(c.Context(), org, camp)
	saved, err := s.State.store.Save(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// fanOut is the pure orchestration core (no store, no HTTP): it launches every
// not-yet-live channel on the campaign through its registered executor, records
// each outcome (live / failed / unavailable) on the channel, and sets the
// campaign status (live if any channel launched, else failed). The org is passed
// verbatim to each executor — the ONLY tenant key — so a campaign can only ever
// resolve its OWN org's connector token. Best-effort per channel: one channel's
// failure never aborts the others. Returns the mutated campaign; the caller
// persists it.
func fanOut(ctx context.Context, org string, camp Campaign) Campaign {
	// A/B: if the campaign carries more than one creative, the experiment seam
	// assigns the variant this launch runs (utm_content the executor tags). With
	// no experiment wired, or a single creative, variant is "" (single-creative).
	variant := assignVariant(ctx, org, camp)

	launched := 0
	for i := range camp.Channels {
		spec := camp.Channels[i]
		if spec.Status == chanLive {
			launched++
			continue // idempotent: never re-launch a live channel
		}
		ch, ok := resolveChannel(spec.Kind)
		if !ok {
			camp.Channels[i].Status = chanUnavailable
			camp.Channels[i].Detail = "no executor wired for channel " + spec.Kind
			continue
		}
		ref, lerr := ch.Launch(ctx, org, planFor(camp, spec, variant))
		if lerr != nil {
			camp.Channels[i].Status = chanFailed
			camp.Channels[i].Detail = shortErr(lerr)
			continue
		}
		camp.Channels[i].ExternalID = ref.ExternalID
		if ref.Account != "" {
			camp.Channels[i].Account = ref.Account
		}
		camp.Channels[i].Status = statusOr(ref.Status, chanLive)
		camp.Channels[i].Detail = ref.Detail
		launched++
	}

	if launched > 0 {
		camp.Status = StatusLive
	} else {
		camp.Status = StatusFailed
	}
	camp.UpdatedAt = time.Now().Unix()
	return camp
}

// pauseAll is the pure pause core: it pauses every live channel on the provider
// (via its executor) and moves the campaign to paused. A channel whose executor
// is gone or whose pause errors is recorded honestly. Returns the mutated
// campaign; the caller persists it.
func pauseAll(ctx context.Context, org string, camp Campaign) Campaign {
	for i := range camp.Channels {
		spec := camp.Channels[i]
		if spec.Status != chanLive || spec.ExternalID == "" {
			continue
		}
		ch, ok := resolveChannel(spec.Kind)
		if !ok {
			camp.Channels[i].Status = chanUnavailable
			camp.Channels[i].Detail = "no executor wired for channel " + spec.Kind
			continue
		}
		if perr := ch.Pause(ctx, org, refOf(spec)); perr != nil {
			camp.Channels[i].Status = chanFailed
			camp.Channels[i].Detail = shortErr(perr)
			continue
		}
		camp.Channels[i].Status = chanPaused
		camp.Channels[i].Detail = ""
	}
	camp.Status = StatusPaused
	camp.UpdatedAt = time.Now().Unix()
	return camp
}

// planFor builds the org-scoped Plan a channel executor receives. Content[0] is
// the active creative; variant is the A/B assignment (utm_content), empty for a
// single-creative campaign.
func planFor(camp Campaign, spec ChannelSpec, variant string) Plan {
	return Plan{
		CampaignID:  camp.ID,
		Name:        camp.Name,
		Platform:    spec.Platform,
		Account:     spec.Account,
		Content:     camp.Content,
		Variant:     variant,
		BudgetCents: camp.Budget,
		ScheduleAt:  camp.ScheduleAt,
	}
}

// refOf reconstructs the executor Ref from a stored channel row for Spend/Pause.
func refOf(spec ChannelSpec) Ref {
	return Ref{
		Platform:   spec.Platform,
		Account:    spec.Account,
		ExternalID: spec.ExternalID,
		Status:     spec.Status,
	}
}

// statusOr returns the executor-reported status, or a fallback when it left it
// blank (the executor launched but did not name a status).
func statusOr(reported, fallback string) string {
	if reported == "" {
		return fallback
	}
	return reported
}
