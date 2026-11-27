// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"testing"
	"time"

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

// SandboxForTest sets, for t, the variables commerce reads for a test-mode org's
// Square merchant to the sandbox merchant's values in KMS, as servePlane does.
func SandboxForTest(t *testing.T) {
	t.Helper()
	sandbox(t)
}

// FreshForTest exposes fresh for external package tests.
func FreshForTest(name string) string { return fresh(name) }

// SoldOnceForTest fails unless Square's sandbox holds the payment commerce recorded
// for invoice in org's books as completed for cents, and lists exactly one payment
// on that card's customer from began on.
func SoldOnceForTest(t *testing.T, org, invoice string, cents int64, began time.Time) {
	t.Helper()
	sandbox(t).soldOnce(t, org, invoice, cents, began)
}

// BootLinksForTest opens the plan-link store for a test that boots the embed itself,
// as Use does for a process.
func BootLinksForTest(t *testing.T) {
	t.Helper()
	bootLinks(t)
}
