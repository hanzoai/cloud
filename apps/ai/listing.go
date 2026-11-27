// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package ai

// listing.go — what a sync of a vendor's model list changed, on the bus.
//
// ai keeps OpenRouter's model list in its store and syncs it hourly (hanzoai/ai
// controllers/listing.go). Each change it reads — a model listed, gone, back,
// repriced, turned free or turned paid — is recorded there and handed to the host,
// which puts it on the SuperAdmin's pulse (bus.Announce): the fleet feed
// GET /v1/admin/feed streams. The history itself is ai's to answer
// (GET /v1/models/vendors/openrouter/events); the pulse is the notice.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/internal/cluster"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	aiobject "github.com/hanzoai/ai/object"
)

// listingTitle is what each kind of change reads as on the feed.
var listingTitle = map[string]string{
	aiobject.ListingNew:   "Model listed",
	aiobject.ListingBack:  "Model listed again",
	aiobject.ListingGone:  "Model gone",
	aiobject.ListingPrice: "Model repriced",
	aiobject.ListingFree:  "Model turned free",
	aiobject.ListingPaid:  "Model turned paid",
}

// announceListing puts each change on the SuperAdmin's pulse. Best effort: the
// change is already recorded in ai's store.
func announceListing(_ context.Context, evs []aiobject.ListingEvent) {
	for _, e := range evs {
		title, ok := listingTitle[e.Kind]
		if !ok {
			continue
		}
		_ = bus.Announce(bus.Pulse{
			Kind:   "model",
			Key:    e.Vendor + "/" + e.ID,
			At:     e.Time,
			Title:  title,
			Detail: listingDetail(e),
		})
	}
}

// listingDetail is a change's model and its per-token prompt and completion
// prices, before and after.
func listingDetail(e aiobject.ListingEvent) string {
	was, now := rates(e.Was), rates(e.Now)
	switch {
	case e.Was == "":
		return fmt.Sprintf("%s on %s at %s", e.ID, e.Vendor, now)
	case e.Now == "":
		return fmt.Sprintf("%s on %s, was %s", e.ID, e.Vendor, was)
	}
	return fmt.Sprintf("%s on %s: %s → %s", e.ID, e.Vendor, was, now)
}

// rates reads a canonical price list's prompt and completion rates (USD a token).
func rates(pricing string) string {
	var p struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	}
	if json.Unmarshal([]byte(pricing), &p) != nil {
		return "unpriced"
	}
	return fmt.Sprintf("prompt %s, completion %s", p.Prompt, p.Completion)
}

// feeWait bounds one read of a vendor's fee; past it the seed is the answer.
const feeWait = 5 * time.Second

// fee is a vendor's resale fee, a percent, as the usage policy holds it now
// (flags.FeeKey; admin.hanzo.ai edits it), read for the platform since one fee holds
// for every org. ai reads it each time it prices the vendor's catalog. A vendor with
// no seeded fee answers "": its models bill at the vendor's price.
func fee(vendor string) string {
	seed, ok := flags.SeedFees[vendor]
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), cluster.Platform), feeWait)
	defer cancel()
	return flagsplane.String(ctx, flags.FeeKey(vendor), seed)
}
