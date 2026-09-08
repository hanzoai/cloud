// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"

	"github.com/hanzoai/cloud/client"
)

// PlaneTierForTest exposes planeTier for external package tests.
func PlaneTierForTest(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
	return planeTier(ctx, in)
}

// PlaneSubscribeForTest exposes planeSubscribe for external package tests.
func PlaneSubscribeForTest(ctx context.Context, in *client.SaleIn) (*client.Sold, error) {
	return planeSubscribe(ctx, in)
}

// PlaneSettingsForTest exposes planeSettings for external package tests.
func PlaneSettingsForTest(ctx context.Context) (*client.PaymentConfig, error) {
	return planeSettings(ctx, &struct{}{})
}
