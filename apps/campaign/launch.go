package campaign

import (
	"context"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// launch.go is the fan-out: turning a campaign VALUE into running executions
// across its channels. It is best-effort PER CHANNEL — each channel records its
// own outcome (live / failed / unavailable) on the campaign, so a paid launch can
// succeed while an email launch fails, and the operator sees exactly which
// connector was missing. The org is passed verbatim to every executor, which
// resolves ITS OWN org's connector token (integrations.TokenFor) — the fan-out
// itself never sees a credential.

// launchCampaign fans a campaign out to every channel it carries, through that
// channel's own executor. A campaign with no channels is refused. The campaign ends
// live when at least one channel launched, else failed, and each channel row records
// its own outcome — live, failed, or unavailable when no executor is wired.
// Idempotent: a channel already live is not launched again.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) launchCampaign(ctx context.Context, in *CampaignRef) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	if len(camp.Channels) == 0 {
		return nil, zip.ErrBadRequest("campaign has no channels to launch")
	}
	camp = fanOut(ctx, org, camp)
	saved, err := s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
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

// pauseCampaign pauses every live channel at its provider and moves the campaign to
// paused. A channel whose executor is gone, or whose pause errors, is recorded
// honestly on the channel row; the campaign still reports paused, because no live
// channel remains that this process will meter.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) pauseCampaign(ctx context.Context, in *CampaignRef) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	camp = pauseAll(ctx, org, camp)
	saved, err := s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
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
