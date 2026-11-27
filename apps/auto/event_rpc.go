package auto

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeProductEvents gives products the same durable trigger engine as hooks.
// It is an internal plane operation; it creates no public ingest endpoint.
func exposeProductEvents() {
	cloud.Plane().Post("/auto/events/deliver", planeProductEvent,
		zip.WithOperationID(client.AutoEventDeliver),
		zip.WithSummary("Deliver a committed product outcome to durable automation triggers"))
}

// planeProductEvent derives the org from the authenticated call, bounds the
// event, and preserves the engine's deduplication, depth and per-org run limits.
func planeProductEvent(ctx context.Context, in *client.ProductEvent) (*client.ProductEventDelivered, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("product event: no org on the call")
	}
	if strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Key) == "" || len(in.Source) > maxField || len(in.Name) > maxField || len(in.Key) > maxField || in.Depth < 0 {
		return nil, zip.ErrBadRequest("product event needs bounded source, name, stable key and non-negative depth")
	}
	if len(in.Payload) > maxTriggerBytes {
		return nil, zip.ErrBadRequest("product event payload is too large")
	}
	var payload map[string]any
	if err := json.Unmarshal(in.Payload, &payload); err != nil || payload == nil {
		return nil, zip.ErrBadRequest("product event payload must be an object")
	}
	n, err := Deliver(ctx, org, TriggerEvent{Source: in.Source, Name: in.Name, DedupeKey: in.Key, Depth: in.Depth, Payload: payload})
	if err != nil {
		return nil, err
	}
	return &client.ProductEventDelivered{Matched: n}, nil
}
