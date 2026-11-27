package ai

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

func TestAITierReaderPreservesUnlimitedPlan(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	cloud.Plane().Post("/billing/tier", func(ctx context.Context, in *client.TierIn) (*client.Tier, error) {
		switch in.Subject {
		case "hanzo", "hanzo/alice":
			return &client.Tier{User: in.Subject, Plan: "unlimited", PlanName: "Unlimited", Tier: client.TierLimits{Name: "pro"}}, nil
		case "acme":
			return &client.Tier{User: in.Subject, Plan: "pro-3x", PlanName: "Pro 3x", Tier: client.TierLimits{Name: "pro"}}, nil
		case "ent":
			return &client.Tier{User: in.Subject, Plan: "dedicated", Tier: client.TierLimits{Name: "enterprise"}}, nil
		default:
			return &client.Tier{User: in.Subject, Tier: client.TierLimits{Name: "free"}}, nil
		}
	}, zip.WithOperationID(client.BillingTier))
	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("serve commerce: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	cases := []struct {
		subject, org, want string
	}{
		{"hanzo", "hanzo", "unlimited"},
		{"hanzo/alice", "hanzo/alice", "unlimited"},
		{"acme", "acme", "pro"},
		{"ent", "ent", "enterprise"},
		{"nobody", "nobody", "free"},
	}
	for _, tc := range cases {
		got, err := aiTierReader(context.Background(), tc.subject, tc.org)
		if err != nil {
			t.Fatalf("aiTierReader(%q, %q) err = %v", tc.subject, tc.org, err)
		}
		if got != tc.want {
			t.Fatalf("aiTierReader(%q, %q) = %q, want %q", tc.subject, tc.org, got, tc.want)
		}
	}
}
