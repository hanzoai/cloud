package automations

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/destinations"
)

// connector_destinations.go registers the "destinations" connector — the ONE MCP tool
// (destinations_connect) the Business AI Guide runs to provision a marketing
// destination (GA4, Meta, TikTok, …) for the caller's org so canonical /v1/event
// conversions fan out to it.
//
// The tool provisions NON-SECRET ids only (measurement/pixel id). API secrets are
// NEVER a tool argument: they would be persisted in the guide's action ledger, so
// they flow only through the authenticated POST /v1/destinations/{platform} body →
// KMS. A destination whose secret is reusable from an existing OAuth connection (Meta
// CAPI ← the meta_ads token) goes fully live from the ids alone; otherwise the tool
// reports live=false and the console adds the secret. dispatchTool pins rc.Org to the
// VALIDATED principal, so the tool can only ever provision the caller's own org.

const destinationsConnector = "destinations"

func init() {
	register(&Connector{
		Name:        destinationsConnector,
		DisplayName: "Marketing Destinations",
		AuthType:    "none",
		AuthReq:     false,
		Actions: map[string]*Action{
			"connect": {
				Name:        "connect",
				DisplayName: "Connect a marketing destination",
				Description: "Provision an ad/analytics destination (ga4, meta, tiktok, linkedin, x, reddit) for your org so conversions fan out to it. Pass the non-secret ids (measurementId, pixelId, …); API secrets are added in the console.",
				Props: []PropSpec{
					{Name: "platform", Type: "string", Required: true, Description: "ga4 | meta | tiktok | linkedin | x | reddit"},
					{Name: "measurementId", Type: "string", Description: "GA4 Measurement ID (G-…)."},
					{Name: "pixelId", Type: "string", Description: "Meta pixel / dataset id, or X event-tag id."},
					{Name: "pixelCode", Type: "string", Description: "TikTok pixel code."},
					{Name: "accountId", Type: "string", Description: "Reddit ad-account id."},
					{Name: "conversionId", Type: "string", Description: "LinkedIn conversion-rule id."},
				},
				Run: runDestinationsConnect,
			},
		},
	})
}

// runDestinationsConnect drives the destinations in-process Connect seam AS THE
// CALLER'S ORG (rc.Org, pinned by dispatchTool). It forwards only the known
// non-secret id fields; the seam validates the platform's required ids and reports
// whether the destination is live.
func runDestinationsConnect(ctx context.Context, rc RunContext) (any, error) {
	platform := strings.TrimSpace(strInput(rc.Input, "platform"))
	if platform == "" {
		return nil, fmt.Errorf("destinations.connect: platform is required")
	}
	cfg := map[string]any{}
	for _, k := range []string{"measurementId", "pixelId", "pixelCode", "accountId", "conversionId", "datasetId", "testEventCode"} {
		if v := strInput(rc.Input, k); v != "" {
			cfg[k] = v
		}
	}
	status, err := destinations.Connect(ctx, rc.Org, platform, cfg)
	if err != nil {
		return nil, err
	}
	return status, nil
}
